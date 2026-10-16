package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type holdResult struct {
	ctx     context.Context
	release func()
	err     error
}

// holdAsync starts a Hold and returns where its outcome arrives.
func holdAsync(ctx context.Context, s *Sched, lane Lane, prio Priority) chan holdResult {
	ch := make(chan holdResult, 1)
	go func() {
		c, r, err := s.Hold(ctx, lane, prio)
		ch <- holdResult{c, r, err}
	}()
	return ch
}

func waitHold(t *testing.T, ch chan holdResult) holdResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("the hold never returned")
	}
	return holdResult{}
}

func noHold(t *testing.T, ch chan holdResult) {
	t.Helper()
	select {
	case r := <-ch:
		t.Fatalf("the hold returned early: %v", r.err)
	case <-time.After(50 * time.Millisecond):
	}
}

// waitQueued waits until n holds are queued (not running).
func waitQueued(t *testing.T, s *Sched, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		q := 0
		for h := range s.holds {
			if !h.running {
				q++
			}
		}
		s.mu.Unlock()
		if q == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d holds queued, want %d", q, n)
		}
		time.Sleep(time.Millisecond)
	}
}

// A hold waits behind a running job (no preemption), goes before queued
// jobs of lower priority, keeps the slot until released, and is never
// listed or published.
func TestHoldQueuesByPriority(t *testing.T) {
	s, g, rec := newTestSched(t, nil)
	ctx := context.Background()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	render, _ := s.Submit(ctx, spec("render", LaneGPU, PriorityNormal))
	g.next(t)
	batch, _ := s.Submit(ctx, spec("batch", LaneGPU, PriorityBackground))
	h := holdAsync(ctx, s, LaneGPU, PriorityInteractive)
	noHold(t, h) // the running render is not preempted
	other := holdAsync(ctx, s, LaneMedia, PriorityNormal)
	if r := waitHold(t, other); r.err != nil || !Holds(r.ctx, LaneMedia) || Holds(r.ctx, LaneGPU) {
		t.Fatalf("media hold: %+v", r)
	} else {
		r.release()
	}

	g.end(render, "", nil)
	r := waitHold(t, h)
	if r.err != nil || !Holds(r.ctx, LaneGPU) {
		t.Fatalf("hold: %+v", r)
	}
	g.none(t) // the batch job waits for the hold
	if s.Idle() {
		t.Error("idle while a hold runs")
	}
	js, _ := s.List(ctx, Filter{})
	if len(js) != 1 || js[0].ID != batch {
		t.Errorf("listed %+v", js)
	}
	r.release()
	r.release() // idempotent
	if j := g.next(t); j.ID != batch {
		t.Fatalf("ran %s", j.Title)
	}
	g.end(batch, "", nil)
	rec.waitState(t, batch, StateSucceeded)
	s.Flush()
	if !s.Idle() {
		t.Error("not idle at the end")
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, u := range rec.updates {
		if u.Job.ID != render && u.Job.ID != batch {
			t.Errorf("published %+v", u.Job)
		}
	}
}

// Holds of equal priority are served in order, between jobs of that
// priority by age.
func TestHoldOrderAmongInteractive(t *testing.T) {
	s, g, _ := newTestSched(t, nil)
	ctx := context.Background()
	s.Start(ctx)
	first, _ := s.Submit(ctx, spec("first", LaneGPU, PriorityNormal))
	g.next(t)
	h1 := holdAsync(ctx, s, LaneGPU, PriorityInteractive)
	waitQueued(t, s, 1)
	take, _ := s.Submit(ctx, spec("take", LaneGPU, PriorityInteractive))
	time.Sleep(2 * time.Millisecond)
	h2 := holdAsync(ctx, s, LaneGPU, PriorityInteractive)
	waitQueued(t, s, 2)
	g.end(first, "", nil)
	r1 := waitHold(t, h1)
	g.none(t)
	noHold(t, h2)
	r1.release()
	if j := g.next(t); j.ID != take {
		t.Fatalf("ran %s", j.Title)
	}
	noHold(t, h2)
	g.end(take, "", nil)
	waitHold(t, h2).release()
}

// Cancelling a waiting hold leaves the queue as it was; a hold whose
// context ends frees its slot.
func TestHoldCancel(t *testing.T) {
	s, g, _ := newTestSched(t, nil)
	ctx := context.Background()
	s.Start(ctx)
	render, _ := s.Submit(ctx, spec("render", LaneGPU, PriorityNormal))
	g.next(t)
	cctx, cancel := context.WithCancel(ctx)
	h := holdAsync(cctx, s, LaneGPU, PriorityInteractive)
	waitQueued(t, s, 1)
	cancel()
	if r := waitHold(t, h); !errors.Is(r.err, context.Canceled) {
		t.Fatalf("cancelled hold: %v", r.err)
	}
	waitQueued(t, s, 0)
	next, _ := s.Submit(ctx, spec("next", LaneGPU, PriorityNormal))
	g.end(render, "", nil)
	if j := g.next(t); j.ID != next {
		t.Fatalf("ran %s", j.Title)
	}
	g.end(next, "", nil)

	// The held context ending (the caller's) frees the slot without release.
	cctx, cancel = context.WithCancel(ctx)
	r := waitHold(t, holdAsync(cctx, s, LaneGPU, PriorityInteractive))
	if r.err != nil {
		t.Fatal(r.err)
	}
	after, _ := s.Submit(ctx, spec("after", LaneGPU, PriorityNormal))
	g.none(t)
	cancel()
	if j := g.next(t); j.ID != after {
		t.Fatalf("ran %s", j.Title)
	}
	if r.ctx.Err() == nil {
		t.Error("held context still alive")
	}
	g.end(after, "", nil)
	r.release()

	// Already cancelled: no wait.
	if _, _, err := s.Hold(cctx, LaneGPU, PriorityInteractive); !errors.Is(err, context.Canceled) {
		t.Errorf("hold with a dead context: %v", err)
	}
}

// Work inside a job holds its lane: holding it again returns at once (a
// dub stage on the gpu lane that calls the language model), while
// another lane still queues.
func TestHoldReentrantInsideAJob(t *testing.T) {
	s, _, _ := newTestSched(t, nil)
	ctx := context.Background()
	s.Start(ctx)
	inner := make(chan error, 1)
	s.Register("stage", ExecutorFunc(func(ctx context.Context, job Job, _ func(Progress), _ func(json.RawMessage)) (json.RawMessage, error) {
		hctx, release, err := s.Hold(ctx, LaneGPU, PriorityInteractive)
		if err != nil {
			inner <- err
			return nil, err
		}
		release() // a no-op: the job keeps its slot
		if hctx != ctx {
			inner <- errors.New("the context changed")
			return nil, nil
		}
		// A nested hold from a held context is free too.
		if _, r2, err := s.Hold(context.WithoutCancel(ctx), LaneGPU, PriorityNormal); err != nil {
			inner <- err
			return nil, err
		} else {
			r2()
		}
		inner <- nil
		return nil, nil
	}))
	id, _ := s.Submit(ctx, Spec{Kind: "stage", Lane: LaneGPU, Title: "stage", Params: json.RawMessage(`{}`)})
	select {
	case err := <-inner:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stage deadlocked on its own lane")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		j, _ := s.Get(ctx, id)
		if j.State == StateSucceeded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stage %s", j.State)
		}
		time.Sleep(time.Millisecond)
	}
	// The slot is free again.
	r := waitHold(t, holdAsync(ctx, s, LaneGPU, PriorityInteractive))
	if r.err != nil {
		t.Fatal(r.err)
	}
	r.release()
}

// Close ends waiting and running holds.
func TestHoldClose(t *testing.T) {
	s, g, _ := newTestSched(t, nil)
	ctx := context.Background()
	s.Start(ctx)
	running := waitHold(t, holdAsync(ctx, s, LaneGPU, PriorityInteractive))
	if running.err != nil {
		t.Fatal(running.err)
	}
	waiting := holdAsync(ctx, s, LaneGPU, PriorityInteractive)
	waitQueued(t, s, 1)
	go s.Close()
	if r := waitHold(t, waiting); !errors.Is(r.err, ErrClosed) {
		t.Errorf("waiting hold at close: %v", r.err)
	}
	select {
	case <-running.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the running hold's context did not end")
	}
	running.release()
	if _, _, err := s.Hold(ctx, LaneGPU, PriorityInteractive); !errors.Is(err, ErrClosed) {
		t.Errorf("hold after close: %v", err)
	}
	g.none(t)
}
