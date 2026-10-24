package jobs

// Lane holds (thread #53): work that runs outside the job list but must
// take its turn on a lane, such as the engine's language model, whose
// llm.generate requests would otherwise collide with a running render on
// the gpu worker and fail with busy. A hold waits in the lane's queue
// like a job (by Priority, then age; running jobs are not preempted),
// then keeps one slot until it is released. Holds are never persisted,
// listed or published: they are short, interactive and owned by a
// caller that waits for them, so the jobs list does not show them.
//
// Re-entrancy: a job's executor runs with a context that holds its lane
// (Holds), and so does the context Hold returns. Holding a lane the
// context already holds returns at once, so work that runs inside a gpu
// job (a dub's translation on the gpu lane) does not wait for itself.

import (
	"context"
	"fmt"
	"time"
)

// Holder is implemented by schedulers that can hold a lane slot (Sched).
type Holder interface {
	// Hold waits for a slot of lane, queued at prio among the lane's jobs,
	// and keeps it until release is called, or until Options.HoldGrace
	// after the returned context ended (it ends with ctx, and when the
	// scheduler closes) without release. The returned
	// context holds lane (Holds). If ctx already holds lane, Hold returns
	// ctx and a no-op release at once. While it waits, ctx ending returns
	// ctx's error; the scheduler closing returns ErrClosed.
	Hold(ctx context.Context, lane Lane, prio Priority) (held context.Context, release func(), err error)
}

var _ Holder = (*Sched)(nil)

type laneKey struct{ lane Lane }

// Holds reports whether ctx holds a slot of lane: it is a job's context
// on that lane (or derived from one), or a Hold's.
func Holds(ctx context.Context, lane Lane) bool {
	return ctx.Value(laneKey{lane}) != nil
}

// withLane marks ctx as holding lane.
func withLane(ctx context.Context, lane Lane) context.Context {
	return context.WithValue(ctx, laneKey{lane}, true)
}

// hold is a waiting or running lane hold. Guarded by Sched.mu.
type hold struct {
	id      string
	lane    Lane
	prio    Priority
	created time.Time
	running bool
	done    bool
	started chan struct{} // closed when it gets its slot
	cancel  context.CancelCauseFunc
}

func (s *Sched) Hold(ctx context.Context, lane Lane, prio Priority) (context.Context, func(), error) {
	if Holds(ctx, lane) {
		return ctx, func() {}, nil
	}
	if lane == "" {
		return nil, nil, fmt.Errorf("%w: a hold needs a lane", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	hctx, cancel := context.WithCancelCause(withLane(ctx, lane))
	h := &hold{id: s.opts.NewID(), lane: lane, prio: prio, created: s.opts.Now(), started: make(chan struct{}), cancel: cancel}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel(errShutdown)
		return nil, nil, ErrClosed
	}
	if s.holds == nil {
		s.holds = map[*hold]struct{}{}
	}
	s.holds[h] = struct{}{}
	s.mu.Unlock()
	s.poke()

	release := func() { s.releaseHold(h) }
	select {
	case <-h.started:
	case <-ctx.Done():
		release()
		cancel(nil)
		return nil, nil, ctx.Err()
	case <-hctx.Done(): // ctx ended, or the scheduler closed
		release()
		cancel(nil)
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		return nil, nil, ErrClosed
	}
	// A context that ends frees the slot too, but only after HoldGrace:
	// the work under it (an engine request asked to cancel) may run on
	// until it confirms, and the caller's release frees it then.
	var late *time.Timer
	stop := context.AfterFunc(hctx, func() {
		s.mu.Lock()
		if !h.done {
			late = time.AfterFunc(s.opts.HoldGrace, release)
		}
		s.mu.Unlock()
	})
	return hctx, func() {
		stop()
		release()
		s.mu.Lock()
		if late != nil {
			late.Stop()
		}
		s.mu.Unlock()
		cancel(nil)
	}, nil
}

// releaseHold frees h's slot (or drops it from the queue). Idempotent.
func (s *Sched) releaseHold(h *hold) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h.done {
		return
	}
	h.done = true
	delete(s.holds, h)
	if h.running {
		s.running[h.lane]--
		s.poke()
	}
}

// Started reports whether Start has run (and Close has not): until then
// jobs and holds wait.
func (s *Sched) Started() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started && !s.closed
}

// startHoldLocked gives h its slot. Caller holds mu.
func (s *Sched) startHoldLocked(h *hold) {
	h.running = true
	s.running[h.lane]++
	close(h.started)
}
