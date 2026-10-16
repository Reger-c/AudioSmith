package llm

// Budgets that do not count queueing (thread #53). The engine's language
// model waits for its turn on the gpu lane (a render may be running), so
// a timeout counted from the call would run out behind every render.
// WithBudget is context.WithTimeout whose clock stops while a backend
// waits for its turn (Waiting); OnWait lets a caller see the wait (to say
// "waiting for the current job…" or to give up and fall back).

import (
	"context"
	"sync"
	"time"
)

type budgetKey struct{}
type watchKey struct{}

// budget is the clock of one WithBudget context.
type budget struct {
	parent *budget // the enclosing budget, paused with this one

	mu        sync.Mutex
	done      chan struct{}
	err       error
	remaining time.Duration
	since     time.Time // when the clock last (re)started; zero while paused
	paused    int
	timer     *time.Timer
	stop      func() bool // stops following the parent context
}

// budgetCtx is a context that ends when its parent does or its budget is
// spent: Err is then the parent's error or context.DeadlineExceeded.
type budgetCtx struct {
	context.Context
	b *budget
}

func (c *budgetCtx) Done() <-chan struct{} { return c.b.done }

func (c *budgetCtx) Err() error {
	c.b.mu.Lock()
	defer c.b.mu.Unlock()
	return c.b.err
}

func (c *budgetCtx) Value(key any) any {
	if key == (budgetKey{}) {
		return c.b
	}
	return c.Context.Value(key)
}

func (b *budget) end(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return
	}
	b.err = err
	if b.timer != nil {
		b.timer.Stop()
	}
	close(b.done)
}

func (b *budget) pause(now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.paused++
	if b.paused > 1 || b.err != nil {
		return
	}
	b.remaining -= now.Sub(b.since)
	b.since = time.Time{}
	if b.timer != nil {
		b.timer.Stop()
	}
}

func (b *budget) resume(now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.paused--
	if b.paused > 0 || b.err != nil {
		return
	}
	b.since = now
	b.arm()
}

// arm starts the timer for what is left. Caller holds mu.
func (b *budget) arm() {
	if b.remaining <= 0 {
		b.timer = time.AfterFunc(0, func() { b.end(context.DeadlineExceeded) })
		return
	}
	b.timer = time.AfterFunc(b.remaining, func() { b.end(context.DeadlineExceeded) })
}

// WithBudget is context.WithTimeout(ctx, d) except that time a backend
// spends waiting for its turn (between Waiting and its resume) does not
// count. When d is spent, the context's Err is context.DeadlineExceeded
// (context.Cause of it may be nil: it is not a standard context). It has
// no Deadline of its own. A budget made while a wait goes on counts from
// when it is made.
func WithBudget(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	b := &budget{done: make(chan struct{}), remaining: d, since: time.Now()}
	b.parent, _ = ctx.Value(budgetKey{}).(*budget)
	c := &budgetCtx{Context: ctx, b: b}
	if err := ctx.Err(); err != nil {
		b.end(err)
		return c, func() {}
	}
	b.stop = context.AfterFunc(ctx, func() { b.end(ctx.Err()) })
	b.mu.Lock()
	b.arm()
	b.mu.Unlock()
	return c, func() {
		b.stop()
		b.end(context.Canceled)
	}
}

// watch is an OnWait observer.
type watch struct {
	fn     func(waiting bool)
	parent *watch
}

// OnWait returns a context under which fn is told when a backend starts
// (true) and stops (false) waiting for its turn. fn runs on the caller's
// goroutine and must not block.
func OnWait(ctx context.Context, fn func(waiting bool)) context.Context {
	w := &watch{fn: fn}
	w.parent, _ = ctx.Value(watchKey{}).(*watch)
	return context.WithValue(ctx, watchKey{}, w)
}

// Waiting marks the start of a wait for a turn (in a queue, for a lane)
// under ctx: the budgets of ctx stop counting and OnWait observers hear
// it. Call the returned func once when the turn comes (or the wait ends
// otherwise). Backends call it; any goroutine.
func Waiting(ctx context.Context) (resume func()) {
	now := time.Now()
	var bs []*budget
	for b, _ := ctx.Value(budgetKey{}).(*budget); b != nil; b = b.parent {
		b.pause(now)
		bs = append(bs, b)
	}
	var ws []*watch
	for w, _ := ctx.Value(watchKey{}).(*watch); w != nil; w = w.parent {
		w.fn(true)
		ws = append(ws, w)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			now := time.Now()
			for _, b := range bs {
				b.resume(now)
			}
			for _, w := range ws {
				w.fn(false)
			}
		})
	}
}

// Remaining returns what is left of the innermost budget of ctx
// (WithBudget), or false when ctx has none.
func Remaining(ctx context.Context) (time.Duration, bool) {
	b, _ := ctx.Value(budgetKey{}).(*budget)
	if b == nil {
		return 0, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	left := b.remaining
	if !b.since.IsZero() {
		left -= time.Since(b.since)
	}
	return max(left, 0), true
}
