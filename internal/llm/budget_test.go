package llm

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestBudgetExpires(t *testing.T) {
	ctx, cancel := WithBudget(context.Background(), 20*time.Millisecond)
	defer cancel()
	child, ccancel := context.WithCancel(ctx)
	defer ccancel()
	select {
	case <-child.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the budget never ran out")
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) || !errors.Is(child.Err(), context.DeadlineExceeded) {
		t.Errorf("errors %v / %v", ctx.Err(), child.Err())
	}
	if e := ctxErr(child, "local", child.Err(), time.Second); e.Code != CodeTimeout || !e.Retryable {
		t.Errorf("mapped to %+v", e)
	}

	// The parent ending ends it with the parent's error; cancel ends it as Canceled.
	parent, pcancel := context.WithCancel(context.Background())
	ctx, cancel = WithBudget(parent, time.Hour)
	pcancel()
	<-ctx.Done()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Errorf("after the parent: %v", ctx.Err())
	}
	cancel()
	ctx, cancel = WithBudget(context.Background(), time.Hour)
	cancel()
	<-ctx.Done()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Errorf("cancelled: %v", ctx.Err())
	}
}

// Time spent waiting for a turn does not count, in every enclosing budget.
func TestBudgetPausesWhileWaiting(t *testing.T) {
	var mu sync.Mutex
	var seen []bool
	base := OnWait(context.Background(), func(w bool) { mu.Lock(); seen = append(seen, w); mu.Unlock() })
	outer, cancel := WithBudget(base, 60*time.Millisecond)
	defer cancel()
	inner, icancel := WithBudget(outer, 60*time.Millisecond)
	defer icancel()
	resume := Waiting(inner)
	time.Sleep(200 * time.Millisecond)
	if outer.Err() != nil || inner.Err() != nil {
		t.Fatalf("the wait counted: %v / %v", outer.Err(), inner.Err())
	}
	start := time.Now()
	resume()
	resume() // once only
	select {
	case <-inner.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the budget never ran out after the wait")
	}
	if !errors.Is(inner.Err(), context.DeadlineExceeded) {
		t.Errorf("inner: %v", inner.Err())
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("ran out %v after the wait", d)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || !seen[0] || seen[1] {
		t.Errorf("observer saw %v", seen)
	}
}

// The engine backend's Queue wait does not count against the Client's
// timeout (a request queued behind a render) and tokens still stream.
func TestEngineQueueDoesNotCountAgainstTimeout(t *testing.T) {
	queued := 0
	released := 0
	eb := &EngineBackend{DefaultModel: "m",
		Queue: func(ctx context.Context) (context.Context, func(), error) {
			queued++
			select {
			case <-time.After(300 * time.Millisecond): // the render ends
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
			return ctx, func() { released++ }, nil
		},
		Call: func(ctx context.Context, _ string, params any, notify func(string, json.RawMessage)) (json.RawMessage, error) {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if params.(GenerateParams).Stream {
				for _, w := range []string{"hel", "lo"} {
					p, _ := json.Marshal(TokenNotification{Text: w})
					notify(NotifyToken, p)
				}
			}
			return json.RawMessage(`{"text":"hello","finishReason":"stop"}`), nil
		}}
	c := &Client{Backend: eb, Timeout: 100 * time.Millisecond, Retry: RetryPolicy{MaxAttempts: 1}}
	var got string
	resp, err := c.Chat(context.Background(), Request{Messages: []Message{{Role: User, Content: "x"}}, OnDelta: func(d string) { got += d }})
	if err != nil || resp.Text != "hello" || got != "hello" {
		t.Fatalf("%v %+v %q", err, resp, got)
	}
	if queued != 1 || released != 1 {
		t.Errorf("queued %d, released %d", queued, released)
	}

	// Cancelling while queued ends the request as cancelled.
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if _, err := c.Chat(ctx, Request{Messages: []Message{{Role: User, Content: "x"}}}); Code(err) != CodeCancelled {
		t.Errorf("cancelled while queued: %v", err)
	}
	if released != 1 {
		t.Errorf("released %d", released)
	}
}
