package main

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/jobs"
)

// #101 H1: quitting does not wait without a bound for a job whose
// executor ignores its cancel (beta.6's yt-dlp), and the pending drafts
// are written before anything waits on jobs.
func TestQuitBoundedAndFlushesFirst(t *testing.T) {
	saved := quitStopLimit
	quitStopLimit = 300 * time.Millisecond
	t.Cleanup(func() { quitStopLimit = saved })
	a, _, _ := newTestApp(t)
	a.lib.svc.Drafts.Save("quit-test", map[string]string{"edit": "kept"})

	// A scheduler that would wait forever for its stuck executor.
	sched := jobs.New(jobs.Options{Store: jobs.NewMemStore(), CloseTimeout: -1, Logf: t.Logf})
	stuck, started := make(chan struct{}), make(chan struct{})
	defer close(stuck)
	sched.Register("stuck", jobs.ExecutorFunc(func(context.Context, jobs.Job, func(jobs.Progress), func(json.RawMessage)) (json.RawMessage, error) {
		close(started)
		<-stuck
		return nil, nil
	}))
	ctx := context.Background()
	sched.Start(ctx)
	sched.Submit(ctx, jobs.Spec{Kind: "stuck", Lane: jobs.LaneNet})
	<-started

	done := make(chan struct{})
	go func() {
		a.willQuit(&engineHost{sched: sched})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("quitting waits for the stuck job")
	}
	var got map[string]string
	if ok, err := a.lib.svc.Drafts.Load(ctx, "quit-test", &got); !ok || err != nil || got["edit"] != "kept" {
		t.Fatalf("draft %v %v %v", got, ok, err)
	}
	if b, ok, _ := a.lib.store.Documents().Get(ctx, "draft:quit-test"); !ok || len(b) == 0 {
		t.Error("the draft was not written at quit")
	}
}

// The dub editor's save queue never blocks the main thread, runs saves in
// order, and drains (bounded) at quit.
func TestSaveQueue(t *testing.T) {
	q := newSaveQueue()
	gate := make(chan struct{})
	var order []int
	q.push(func() { <-gate })
	start := time.Now()
	for i := range 1000 { // over the old channel's 256
		q.push(func() { order = append(order, i) })
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("pushing took %s", d)
	}
	if q.drain(50 * time.Millisecond) {
		t.Fatal("drained while a save hangs")
	}
	close(gate)
	if !q.drain(5 * time.Second) {
		t.Fatal("not drained")
	}
	for i, n := range order {
		if i != n {
			t.Fatalf("save %d ran as %d", n, i)
		}
	}
	if len(order) != 1000 {
		t.Fatalf("%d saves ran", len(order))
	}
	// A panicking save does not stop the ones after it.
	var ran atomic.Bool
	q.push(func() { panic("boom") })
	q.push(func() { ran.Store(true) })
	if !q.drain(5*time.Second) || !ran.Load() {
		t.Fatal("the queue stopped at a panic")
	}
}
