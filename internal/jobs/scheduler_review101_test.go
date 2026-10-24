package jobs

// Regression tests of code review #101: bounded Close, jobs after an
// interrupted one, interrupted jobs leaving memory, lookups by id, a
// panicking Publish.

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

// #101 H1: Close does not wait forever for an executor that ignores its
// context: within CloseTimeout it records the job Interrupted (shutdown),
// and the executor's late result is dropped.
func TestCloseBoundedWithStuckExecutor(t *testing.T) {
	store := NewMemStore()
	rec := newRecorder()
	s := New(Options{Store: store, Publish: rec.publish, CloseTimeout: 200 * time.Millisecond, Logf: t.Logf})
	release := make(chan struct{})
	started := make(chan struct{})
	s.Register("stuck", ExecutorFunc(func(context.Context, Job, func(Progress), func(json.RawMessage)) (json.RawMessage, error) {
		close(started)
		<-release // ignores ctx
		return json.RawMessage(`{}`), nil
	}))
	ctx := context.Background()
	s.Start(ctx)
	id, _ := s.Submit(ctx, Spec{Kind: "stuck", Lane: LaneNet, Idempotent: true})
	<-started
	done := make(chan error, 1)
	begin := time.Now()
	go func() { done <- s.Close() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close still waits for the stuck executor")
	}
	if d := time.Since(begin); d < 150*time.Millisecond {
		t.Errorf("Close returned after %s, before giving the executor its time", d)
	}
	j, _, _ := store.LoadJob(ctx, id)
	if j.State != StateInterrupted || j.Err == nil || j.Err.Code != CodeShutdown {
		t.Fatalf("stored job %s %+v, want interrupted (shutdown)", j.State, j.Err)
	}
	close(release)
	time.Sleep(20 * time.Millisecond)
	if j, _, _ := store.LoadJob(ctx, id); j.State != StateInterrupted {
		t.Errorf("the late result changed the job to %s", j.State)
	}
	// It re-runs at the next start (Idempotent, CodeShutdown).
	s2 := New(Options{Store: store, Logf: t.Logf})
	ran := make(chan struct{}, 1)
	s2.Register("stuck", ExecutorFunc(func(context.Context, Job, func(Progress), func(json.RawMessage)) (json.RawMessage, error) {
		ran <- struct{}{}
		return nil, nil
	}))
	s2.Start(ctx)
	defer s2.Close()
	select {
	case <-ran:
	case <-time.After(3 * time.Second):
		t.Fatal("the abandoned job did not re-run")
	}
}

// #101 H2: a job that ends Interrupted (retries used up) does not leave
// the jobs after it Waiting forever: they fail, saying why, and the
// scheduler is idle again.
func TestDependentOfInterruptedFails(t *testing.T) {
	s, _, rec := newTestSched(t, nil)
	s.Register("dies", ExecutorFunc(func(context.Context, Job, func(Progress), func(json.RawMessage)) (json.RawMessage, error) {
		return nil, &Error{Code: CodeInterrupted, Message: "worker died", Retryable: true}
	}))
	ctx := context.Background()
	s.Start(ctx)
	ids, err := s.SubmitGraph(ctx, []Node{{Key: "a", Spec: Spec{Kind: "dies", Lane: LaneGPU, MaxAttempts: 2}},
		{Key: "b", Spec: Spec{Kind: "work", Lane: LaneCPU, After: []string{"a"}}},
		{Key: "c", Spec: Spec{Kind: "work", Lane: LaneCPU, After: []string{"b"}}}})
	if err != nil {
		t.Fatal(err)
	}
	rec.waitState(t, ids["a"], StateInterrupted)
	for _, k := range []string{"b", "c"} {
		j := rec.waitState(t, ids[k], StateFailed)
		if j.Err == nil || j.Err.Code != CodeDependency {
			t.Errorf("%s: %+v", k, j.Err)
		}
	}
	if !s.Idle() {
		t.Error("not idle")
	}
	s.mu.Lock()
	if n := len(s.dependents); n != 0 {
		t.Errorf("%d dependency edges left", n)
	}
	s.mu.Unlock()
}

// #101 H2: a chain stuck behind an interrupted job in an earlier run (a
// job that was running at a crash, not Idempotent) recovers at Start.
func TestStuckChainRecoversAtStart(t *testing.T) {
	store := NewMemStore()
	ctx := context.Background()
	now := time.Now()
	store.SaveJob(ctx, Job{ID: "a", Spec: Spec{Kind: "work", Lane: LaneGPU}, State: StateInterrupted, Created: now,
		Err: &Error{Code: CodeInterrupted, Message: "worker died"}, Finished: now})
	store.SaveJob(ctx, Job{ID: "b", Spec: Spec{Kind: "work", Lane: LaneCPU, After: []string{"a"}}, State: StateWaiting, Created: now})
	store.SaveJob(ctx, Job{ID: "c", Spec: Spec{Kind: "work", Lane: LaneGPU}, State: StateRunning, Created: now})
	store.SaveJob(ctx, Job{ID: "d", Spec: Spec{Kind: "work", Lane: LaneCPU, After: []string{"c"}}, State: StateWaiting, Created: now})
	s, _, rec := newTestSched(t, store)
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	rec.waitState(t, "b", StateFailed)
	rec.waitState(t, "d", StateFailed)
	if !s.Idle() {
		t.Error("not idle after the start")
	}
	// The interrupted job can still be retried, or cancelled.
	if err := s.Retry(ctx, "a"); err != nil {
		t.Errorf("Retry: %v", err)
	}
	if err := s.Cancel(ctx, "c"); err != nil {
		t.Errorf("Cancel: %v", err)
	}
	rec.waitState(t, "c", StateCancelled)
}

// #101 L1: interrupted jobs leave memory like finished ones (KeepFinished),
// and stay readable from the store.
func TestInterruptedRetire(t *testing.T) {
	rec := newRecorder()
	s := New(Options{Store: NewMemStore(), Publish: rec.publish, KeepFinished: 2, Logf: t.Logf})
	s.Register("dies", ExecutorFunc(func(context.Context, Job, func(Progress), func(json.RawMessage)) (json.RawMessage, error) {
		return nil, &Error{Code: CodeInterrupted, Message: "worker died"}
	}))
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	s.Start(ctx)
	var ids []string
	for range 20 {
		id, _ := s.Submit(ctx, Spec{Kind: "dies", Lane: LaneCPU})
		ids = append(ids, id)
	}
	for _, id := range ids {
		rec.waitState(t, id, StateInterrupted)
	}
	s.mu.Lock()
	n := len(s.jobs)
	s.mu.Unlock()
	if n > 2 {
		t.Errorf("%d jobs in memory, want at most 2", n)
	}
	if j, err := s.Get(ctx, ids[0]); err != nil || j.State != StateInterrupted {
		t.Errorf("Get of a retired job: %v %v", j.State, err)
	}
	if err := s.Cancel(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	rec.waitState(t, ids[0], StateCancelled)
}

// loadCounter counts LoadJobs calls.
type loadCounter struct {
	*MemStore
	mu    sync.Mutex
	loads int
}

func (l *loadCounter) LoadJobs(ctx context.Context, f Filter) ([]Job, error) {
	l.mu.Lock()
	l.loads++
	l.mu.Unlock()
	return l.MemStore.LoadJobs(ctx, f)
}

// #101 L2: looking up a job no longer in memory reads it by id, not the
// whole table.
func TestGetOldJobByID(t *testing.T) {
	store := &loadCounter{MemStore: NewMemStore()}
	ctx := context.Background()
	store.SaveJob(ctx, Job{ID: "old", Spec: Spec{Kind: "work", Lane: LaneCPU}, State: StateSucceeded, Finished: time.Now()})
	s, _, _ := newTestSched(t, store)
	s.Start(ctx)
	store.mu.Lock()
	before := store.loads
	store.mu.Unlock()
	if j, err := s.Get(ctx, "old"); err != nil || j.State != StateSucceeded {
		t.Fatalf("Get: %v %v", j.State, err)
	}
	if _, err := s.Get(ctx, "none"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get of an unknown id: %v", err)
	}
	if _, err := s.SubmitGraph(ctx, []Node{{Key: "n", Spec: Spec{Kind: "work", Lane: LaneCPU, After: []string{"old"}}}}); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.loads != before {
		t.Errorf("%d LoadJobs calls for lookups by id", store.loads-before)
	}
}

// #101 L11: a Publish that panics does not stop the outbox: later
// updates are still saved and published.
func TestOutboxSurvivesPanickingPublish(t *testing.T) {
	store := NewMemStore()
	var mu sync.Mutex
	var seen []State
	first := true
	s := New(Options{Store: store, Logf: t.Logf, Publish: func(u Updated) {
		mu.Lock()
		defer mu.Unlock()
		if first {
			first = false
			panic("boom")
		}
		seen = append(seen, u.Job.State)
	}})
	s.Register("ok", ExecutorFunc(func(context.Context, Job, func(Progress), func(json.RawMessage)) (json.RawMessage, error) {
		return nil, nil
	}))
	ctx := context.Background()
	s.Start(ctx)
	defer s.Close()
	id, _ := s.Submit(ctx, Spec{Kind: "ok", Lane: LaneCPU})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if j, _, _ := store.LoadJob(ctx, id); j.State == StateSucceeded {
			s.Flush()
			mu.Lock()
			n := len(seen)
			mu.Unlock()
			if n == 0 {
				t.Fatal("nothing published after the panic")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the job's end was never saved")
}
