package main

// Quitting without hanging (#101 H1, L6). The quit runs on the main
// thread (MyGo's OnWillQuit, then OnQuit), after the windows closed, so
// every wait there is bounded:
//
//  1. OnWillQuit first writes what the user edited (pending drafts, the
//     dub editor's queued saves): a quit forced after that loses nothing.
//  2. Then it stops the engines and the job scheduler side by side: the
//     scheduler cancels the jobs (their children's process groups are
//     killed with their contexts) and waits for them at most
//     jobs.Options.CloseTimeout; the supervisor shuts the engines down,
//     killing those that do not exit. Both together at most quitStopLimit.
//  3. OnQuit closes the rest — the Local API, calls, the family, the
//     notifications and logs, the library — each step bounded, the
//     independent ones side by side.
//
// A step that runs over its bound is left running: the process exits.
// MyGo has no window left to show progress in by then, so there is none;
// the bounds keep the wait short instead.

import (
	"context"
	"log"
	"runtime/debug"
	"sync"
	"time"
)

// The bounds (variables: tests shorten them).
var (
	quitFlushLimit = 2 * time.Second // drafts and queued saves, first
	quitStopLimit  = 5 * time.Second // the engines and the job scheduler
	quitStepLimit  = 3 * time.Second // each group of OnQuit's steps
)

// willQuit runs in OnWillQuit, on the main thread.
func (a *app) willQuit(h *engineHost) {
	// family_coord.go: the family's runtime file goes first, the engines'
	// last updates are not written after it.
	a.quitFamilyRuntime()
	if a.dubSvc != nil {
		a.dubSvc.CancelProbes() // yt-dlp asked about a link (#106): its process group goes with it
	}
	a.flushForQuit()
	h.Stop()
}

// flushForQuit writes the pending drafts and the dub editor's queued
// saves, before anything waits on jobs. Main thread.
func (a *app) flushForQuit() {
	a.flushLongform() // longform_compile.go: hands a pending draft to Drafts, flushed below
	var steps []func()
	if a.lib != nil && a.lib.svc != nil && a.lib.svc.Drafts != nil {
		drafts := a.lib.svc.Drafts
		steps = append(steps, func() {
			ctx, cancel := context.WithTimeout(context.Background(), quitFlushLimit)
			defer cancel()
			if err := drafts.Flush(ctx); err != nil {
				log.Printf("quit: drafts: %v", err)
			}
		})
	}
	if q := a.dubSaveQ; q != nil {
		steps = append(steps, func() {
			if !q.drain(quitFlushLimit) {
				log.Printf("quit: dub saves still pending after %s", quitFlushLimit)
			}
		})
	}
	if !runBounded(quitFlushLimit, steps...) {
		log.Printf("quit: writing pending edits took over %s; going on", quitFlushLimit)
	}
}

// quitSteps closes what the app holds besides the engines (OnQuit, via
// quit): the I/O steps side by side, bounded. Main thread.
func (a *app) quitSteps() {
	var steps []func()
	if stop := a.apiStopper(); stop != nil {
		steps = append(steps, stop)
	}
	if svc, gw := a.callSvc, a.callGW; svc != nil {
		steps = append(steps, func() {
			ctx, cancel := context.WithTimeout(context.Background(), quitStepLimit)
			defer cancel()
			svc.Close(ctx)
			gw.Close(ctx)
		})
	}
	if fam := a.fam; fam != nil {
		steps = append(steps, fam.Close)
	}
	if h := a.eng; h != nil {
		steps = append(steps, h.Stop) // done in OnWillQuit already, unless the quit skipped it
	}
	if !runBounded(quitStepLimit, steps...) {
		log.Printf("quit: closing took over %s; going on", quitStepLimit)
	}
	// After the steps above, which may still notify and log.
	var last []func()
	if a.notes != nil {
		last = append(last, a.notes.Close)
	}
	if a.logs != nil {
		last = append(last, a.logs.Close)
	}
	if !runBounded(quitStepLimit, last...) {
		log.Printf("quit: saving the notifications and logs took over %s", quitStepLimit)
	}
}

// runBounded runs steps side by side and reports whether they all ended
// within d. Those that did not keep running; a panic in one is logged.
func runBounded(d time.Duration, steps ...func()) bool {
	var wg sync.WaitGroup
	for _, step := range steps {
		wg.Go(func() {
			defer recoverLog("quit")
			step()
		})
	}
	return waitFor(wg.Wait, d)
}

// waitFor runs wait and reports whether it returned within d. A wait that
// does not return keeps its goroutine.
func waitFor(wait func(), d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wait()
		close(done)
	}()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}

// recoverLog, deferred at the top of a goroutine, logs a panic with its
// stack instead of crashing the app (#101 L11): a crash skips the quit's
// flush. what names the goroutine's work.
func recoverLog(what string) {
	if r := recover(); r != nil {
		log.Printf("panic in %s: %v\n%s", what, r, debug.Stack())
	}
}

// saveQueue runs saves in order on one goroutine (the dub editor's,
// dubSave). Push never blocks the main thread (#101: a full channel
// would), and drain waits, bounded, for what is queued (quit).
type saveQueue struct {
	mu      sync.Mutex
	fns     []func()
	running bool
	idle    *sync.Cond
}

func newSaveQueue() *saveQueue {
	q := &saveQueue{}
	q.idle = sync.NewCond(&q.mu)
	return q
}

// push queues fn after the saves queued before it. Any goroutine.
func (q *saveQueue) push(fn func()) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.fns = append(q.fns, fn)
	if !q.running {
		q.running = true
		go q.run()
	}
}

func (q *saveQueue) run() {
	for {
		q.mu.Lock()
		if len(q.fns) == 0 {
			q.running = false
			q.idle.Broadcast()
			q.mu.Unlock()
			return
		}
		fn := q.fns[0]
		q.fns[0] = nil
		q.fns = q.fns[1:]
		q.mu.Unlock()
		func() {
			defer recoverLog("saving a dub")
			fn()
		}()
	}
}

// drain waits until every queued save ran, at most d.
func (q *saveQueue) drain(d time.Duration) bool {
	return waitFor(func() {
		q.mu.Lock()
		for q.running {
			q.idle.Wait()
		}
		q.mu.Unlock()
	}, d)
}
