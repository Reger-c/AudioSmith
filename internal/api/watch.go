package api

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/jobs"
)

// watcher hands the queue's job events to the requests waiting for them,
// by job owner ("take:<id>") or by job id. One bus subscription serves
// every request; the handler never blocks (bus rule): each watch queues
// its events without bound and the request reads them at its pace.
type watcher struct {
	mu      sync.Mutex
	byKey   map[string]map[*watch]struct{}
	unsub   func()
	allMu   sync.Mutex
	all     map[*watch]struct{} // watches of every job (SSE of all jobs)
	stopped bool
}

func newWatcher(bus *core.Bus) *watcher {
	w := &watcher{byKey: map[string]map[*watch]struct{}{}, all: map[*watch]struct{}{}}
	if bus != nil {
		w.unsub = core.On(bus, w.publish)
	}
	return w
}

func (w *watcher) close() {
	if w.unsub != nil {
		w.unsub()
	}
	w.mu.Lock()
	w.stopped = true
	for _, set := range w.byKey {
		for x := range set {
			x.close()
		}
	}
	w.byKey = map[string]map[*watch]struct{}{}
	w.mu.Unlock()
	w.allMu.Lock()
	for x := range w.all {
		x.close()
	}
	w.all = map[*watch]struct{}{}
	w.allMu.Unlock()
}

// publish is the bus handler: it must not block.
func (w *watcher) publish(u jobs.Updated) {
	var targets []*watch
	w.mu.Lock()
	for _, key := range []string{"owner:" + u.Job.Owner, "job:" + u.Job.ID} {
		for x := range w.byKey[key] {
			targets = append(targets, x)
		}
	}
	w.mu.Unlock()
	w.allMu.Lock()
	for x := range w.all {
		if u.Chunk == nil { // the all-jobs stream carries no audio
			targets = append(targets, x)
		}
	}
	w.allMu.Unlock()
	for _, x := range targets {
		x.push(u)
	}
}

// owner watches the jobs of an owner. Call stop when done.
func (w *watcher) owner(owner string) *watch { return w.add("owner:" + owner) }

// job watches one job by id.
func (w *watcher) job(id string) *watch { return w.add("job:" + id) }

// every watches every job's state (no chunks).
func (w *watcher) every() *watch {
	x := newWatch()
	w.allMu.Lock()
	w.all[x] = struct{}{}
	w.allMu.Unlock()
	x.stopFn = func() {
		w.allMu.Lock()
		delete(w.all, x)
		w.allMu.Unlock()
	}
	return x
}

func (w *watcher) add(key string) *watch {
	x := newWatch()
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		x.close()
		return x
	}
	set := w.byKey[key]
	if set == nil {
		set = map[*watch]struct{}{}
		w.byKey[key] = set
	}
	set[x] = struct{}{}
	w.mu.Unlock()
	x.stopFn = func() {
		w.mu.Lock()
		if s := w.byKey[key]; s != nil {
			delete(s, x)
			if len(s) == 0 {
				delete(w.byKey, key)
			}
		}
		w.mu.Unlock()
	}
	return x
}

// watch is one request's queue of job events.
type watch struct {
	mu     sync.Mutex
	q      []jobs.Updated
	signal chan struct{}
	closed bool
	stopFn func()
	once   sync.Once
}

func newWatch() *watch { return &watch{signal: make(chan struct{}, 1)} }

func (x *watch) push(u jobs.Updated) {
	x.mu.Lock()
	if !x.closed {
		x.q = append(x.q, u)
	}
	x.mu.Unlock()
	select {
	case x.signal <- struct{}{}:
	default:
	}
}

func (x *watch) close() {
	x.mu.Lock()
	x.closed = true
	x.mu.Unlock()
	select {
	case x.signal <- struct{}{}:
	default:
	}
}

// stop unregisters the watch.
func (x *watch) stop() {
	x.once.Do(func() {
		if x.stopFn != nil {
			x.stopFn()
		}
		x.close()
	})
}

var errWatchClosed = errors.New("api: server stopping")

// next waits for the next event, until ctx ends or timeout passes (0 =
// none): then it returns ok false and the context's error, or nil for a
// timeout.
func (x *watch) next(ctx context.Context, timeout time.Duration) (jobs.Updated, bool, error) {
	var timer <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timer = t.C
	}
	for {
		x.mu.Lock()
		if len(x.q) > 0 {
			u := x.q[0]
			x.q[0] = jobs.Updated{}
			x.q = x.q[1:]
			x.mu.Unlock()
			return u, true, nil
		}
		closed := x.closed
		x.mu.Unlock()
		if closed {
			return jobs.Updated{}, false, errWatchClosed
		}
		select {
		case <-ctx.Done():
			return jobs.Updated{}, false, ctx.Err()
		case <-timer:
			return jobs.Updated{}, false, nil
		case <-x.signal:
		}
	}
}

// waitJob follows a job until it ends. Events go to fn as they come
// (chunks included); fn's error stops waiting. startBy bounds how long
// the job may wait in the queue before it runs (0 = no bound): past it,
// the job is cancelled and the request answered 503 (queue_timeout).
// When ctx ends (the client left), the job is cancelled.
func waitJob(ctx context.Context, sched jobs.Scheduler, x *watch, jobID string, startBy time.Duration, fn func(jobs.Updated) error) (jobs.Job, error) {
	started := false
	deadline := time.Time{}
	if startBy > 0 {
		deadline = time.Now().Add(startBy)
	}
	cancel := func() {
		c, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		sched.Cancel(c, jobID)
	}
	// The job may have moved on before the watch saw it (it was made
	// first): look at it once.
	if j, err := sched.Get(ctx, jobID); err == nil {
		if j.State == jobs.StateRunning || j.State.Terminal() {
			started = true
		}
		if j.State.Terminal() || j.State == jobs.StateInterrupted {
			// Events may still be queued for it (chunks): drain them.
			for {
				u, ok, _ := x.next(ctx, 50*time.Millisecond)
				if !ok {
					break
				}
				if u.Job.ID == jobID && fn != nil {
					if err := fn(u); err != nil {
						return j, err
					}
				}
				if u.Job.ID == jobID && u.Chunk == nil && (u.Job.State.Terminal() || u.Job.State == jobs.StateInterrupted) {
					return u.Job, nil
				}
			}
			return j, nil
		}
	}
	for {
		var wait time.Duration
		if !started && !deadline.IsZero() {
			wait = time.Until(deadline)
			if wait <= 0 {
				cancel()
				return jobs.Job{}, errUnavailable(CodeQueueTimeout, 30, "The engine is busy with other work; try again later.")
			}
		}
		u, ok, err := x.next(ctx, wait)
		if err != nil {
			if ctx.Err() != nil {
				cancel()
			}
			return jobs.Job{}, err
		}
		if !ok {
			continue // the deadline check above answers
		}
		if u.Job.ID != jobID {
			continue
		}
		if u.Job.State == jobs.StateRunning || u.Job.State.Terminal() {
			started = true
		}
		if fn != nil {
			if err := fn(u); err != nil {
				cancel()
				return u.Job, err
			}
		}
		if u.Chunk == nil && (u.Job.State.Terminal() || u.Job.State == jobs.StateInterrupted) {
			return u.Job, nil
		}
	}
}

// jobOutcome turns a finished job into its result or error.
func jobOutcome(j jobs.Job, out any) error {
	switch j.State {
	case jobs.StateSucceeded:
		if out != nil && len(j.Result) > 0 {
			if err := json.Unmarshal(j.Result, out); err != nil {
				return err
			}
		}
		return nil
	case jobs.StateInterrupted:
		return errUnavailable(CodeEngineUnavailable, 10, "The engine stopped while it worked on this request; try again.")
	}
	if j.Err != nil {
		return j.Err
	}
	if j.State == jobs.StateCancelled {
		return &jobs.Error{Code: "cancelled", Message: "The job was cancelled."}
	}
	return &jobs.Error{Code: "internal", Message: "The job ended " + string(j.State) + "."}
}
