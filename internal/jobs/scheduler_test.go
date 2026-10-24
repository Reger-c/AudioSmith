package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"
)

// gate is an executor whose runs block until the test lets them end.
type gate struct {
	mu      sync.Mutex
	started chan Job
	finish  map[string]chan result // by job id
	order   []string               // ids, in the order they started
	running int
	maxRun  int
}

type result struct {
	res json.RawMessage
	err error
}

func newGate() *gate { return &gate{started: make(chan Job, 100), finish: map[string]chan result{}} }

func (g *gate) ch(id string) chan result {
	g.mu.Lock()
	defer g.mu.Unlock()
	c, ok := g.finish[id]
	if !ok {
		c = make(chan result, 1)
		g.finish[id] = c
	}
	return c
}

func (g *gate) Run(ctx context.Context, j Job, report func(Progress), chunk func(json.RawMessage)) (json.RawMessage, error) {
	g.mu.Lock()
	g.order = append(g.order, j.ID)
	g.running++
	g.maxRun = max(g.maxRun, g.running)
	g.mu.Unlock()
	defer func() { g.mu.Lock(); g.running--; g.mu.Unlock() }()
	g.started <- j
	select {
	case r := <-g.ch(j.ID):
		return r.res, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// next waits for the next run to start.
func (g *gate) next(t *testing.T) Job {
	t.Helper()
	select {
	case j := <-g.started:
		return j
	case <-time.After(5 * time.Second):
		t.Fatal("no job started")
	}
	return Job{}
}

func (g *gate) none(t *testing.T) {
	t.Helper()
	select {
	case j := <-g.started:
		t.Fatalf("job %s (%s) started", j.ID, j.Title)
	case <-time.After(50 * time.Millisecond):
	}
}

func (g *gate) end(id string, res string, err error) {
	var raw json.RawMessage
	if res != "" {
		raw = json.RawMessage(res)
	}
	g.ch(id) <- result{raw, err}
}

// recorder collects published updates.
type recorder struct {
	mu      sync.Mutex
	updates []Updated
	cond    *sync.Cond
}

func newRecorder() *recorder {
	r := &recorder{}
	r.cond = sync.NewCond(&r.mu)
	return r
}

func (r *recorder) publish(u Updated) {
	r.mu.Lock()
	r.updates = append(r.updates, u)
	r.cond.Broadcast()
	r.mu.Unlock()
}

func (r *recorder) states(id string) []State {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []State
	for _, u := range r.updates {
		if u.Job.ID == id && u.Chunk == nil && (len(out) == 0 || out[len(out)-1] != u.Job.State) {
			out = append(out, u.Job.State)
		}
	}
	return out
}

// waitState waits until the latest update of job id has state st. The
// outbox publishes updates after the scheduler acts, so the latest update
// may still be one from before (a failed attempt before a Retry): use
// waitAttempt to wait for a state of a given attempt.
func (r *recorder) waitState(t *testing.T, id string, st State) Job {
	t.Helper()
	return r.waitAttempt(t, id, st, 0)
}

// waitAttempt waits until the latest update of job id has state st and,
// unless attempt is 0, that attempt.
func (r *recorder) waitAttempt(t *testing.T, id string, st State, attempt int) Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		for i := len(r.updates) - 1; i >= 0; i-- {
			if u := r.updates[i]; u.Job.ID == id {
				if u.Job.State == st && (attempt == 0 || u.Job.Attempt == attempt) {
					return u.Job
				}
				break
			}
		}
		if time.Now().After(deadline) {
			var got []string
			for _, u := range r.updates {
				if u.Job.ID == id {
					got = append(got, string(u.Job.State))
				}
			}
			t.Fatalf("job %s never %s; got %v", id, st, got)
		}
		r.mu.Unlock()
		time.Sleep(2 * time.Millisecond)
		r.mu.Lock()
	}
}

func newTestSched(t *testing.T, store Store) (*Sched, *gate, *recorder) {
	t.Helper()
	if store == nil {
		store = NewMemStore()
	}
	rec := newRecorder()
	s := New(Options{Store: store, Publish: rec.publish, ProgressEvery: time.Hour,
		RetryDelay: func(int) time.Duration { return time.Millisecond }, Logf: t.Logf})
	g := newGate()
	s.Register("work", g)
	t.Cleanup(func() { s.Close() })
	return s, g, rec
}

func spec(title string, lane Lane, p Priority) Spec {
	return Spec{Kind: "work", Lane: lane, Priority: p, Title: title, Params: json.RawMessage(`{}`)}
}

func TestLanePriorityAndOrder(t *testing.T) {
	s, g, rec := newTestSched(t, nil)
	ctx := context.Background()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	first, _ := s.Submit(ctx, spec("first", LaneGPU, PriorityNormal))
	if j := g.next(t); j.ID != first || j.Attempt != 1 || j.State != StateRunning {
		t.Fatalf("first run %+v", j)
	}
	bg, _ := s.Submit(ctx, spec("background", LaneGPU, PriorityBackground))
	n1, _ := s.Submit(ctx, spec("normal 1", LaneGPU, PriorityNormal))
	inter, _ := s.Submit(ctx, spec("interactive", LaneGPU, PriorityInteractive))
	n2, _ := s.Submit(ctx, spec("normal 2", LaneGPU, PriorityNormal))
	g.none(t) // one GPU slot
	if s.Idle() {
		t.Fatal("idle while jobs run")
	}

	want := []string{inter, n1, n2, bg}
	g.end(first, `{"ok":1}`, nil)
	for _, id := range want {
		j := g.next(t)
		if j.ID != id {
			t.Fatalf("ran %s (%s), want %s", j.ID, j.Title, id)
		}
		g.none(t)
		g.end(id, "", nil)
	}
	got := rec.waitState(t, bg, StateSucceeded)
	if string(got.Result) != `{}` || got.Finished.IsZero() || got.Started.IsZero() {
		t.Errorf("finished job %+v", got)
	}
	s.Flush()
	if !s.Idle() {
		t.Error("not idle at the end")
	}
	if st := rec.states(first); !slices.Equal(st, []State{StateQueued, StateRunning, StateSucceeded}) {
		t.Errorf("states of first: %v", st)
	}
	j, err := s.Get(ctx, first)
	if err != nil || string(j.Result) != `{"ok":1}` {
		t.Errorf("Get: %+v %v", j, err)
	}
}

func TestLaneSlots(t *testing.T) {
	s, g, _ := newTestSched(t, nil)
	ctx := context.Background()
	s.Start(ctx)
	var ids []string
	for i := range 3 {
		id, _ := s.Submit(ctx, spec(fmt.Sprint("media ", i), LaneMedia, PriorityNormal))
		ids = append(ids, id)
	}
	gpu, _ := s.Submit(ctx, spec("gpu", LaneGPU, PriorityNormal))
	started := map[string]bool{}
	for range 3 { // 2 media + 1 gpu
		started[g.next(t).ID] = true
	}
	g.none(t)
	if !started[gpu] || started[ids[2]] {
		t.Fatalf("started %v", started)
	}
	g.end(ids[0], "", nil)
	if j := g.next(t); j.ID != ids[2] {
		t.Fatalf("then %s", j.Title)
	}
	g.end(ids[1], "", nil)
	g.end(ids[2], "", nil)
	g.end(gpu, "", nil)
	s.Flush()
	if g.maxRun != 3 {
		t.Errorf("max concurrent %d", g.maxRun)
	}
}

func TestSubmitBeforeStartAndPersistence(t *testing.T) {
	store := NewMemStore()
	var savedState State
	var s *Sched
	rec := newRecorder()
	s = New(Options{Store: store, Publish: func(u Updated) {
		// Every update is saved before it is published.
		js, _ := store.LoadJobs(context.Background(), Filter{States: []State{u.Job.State}})
		for _, j := range js {
			if j.ID == u.Job.ID {
				savedState = j.State
			}
		}
		if savedState != u.Job.State {
			panic(fmt.Sprintf("published %s before saving it (store has %s)", u.Job.State, savedState))
		}
		rec.publish(u)
	}})
	defer s.Close()
	g := newGate()
	s.Register("work", g)
	ctx := context.Background()
	id, err := s.Submit(ctx, Spec{Kind: "work", Lane: LaneCPU, Title: "early", Owner: "take:1"})
	if err != nil {
		t.Fatal(err)
	}
	// Durable as soon as Submit returns.
	js, _ := store.LoadJobs(ctx, Filter{})
	if len(js) != 1 || js[0].ID != id || js[0].State != StateQueued || js[0].Owner != "take:1" {
		t.Fatalf("stored %+v", js)
	}
	g.none(t)
	s.Start(ctx)
	g.next(t)
	g.end(id, `{"x":true}`, nil)
	rec.waitState(t, id, StateSucceeded)
	js, _ = store.LoadJobs(ctx, Filter{States: []State{StateSucceeded}})
	if len(js) != 1 || string(js[0].Result) != `{"x":true}` || js[0].Attempt != 1 {
		t.Fatalf("stored %+v", js)
	}
}

func TestGraph(t *testing.T) {
	s, g, rec := newTestSched(t, nil)
	ctx := context.Background()
	s.Start(ctx)
	ids, err := s.SubmitGraph(ctx, []Node{
		{Key: "mix", Spec: Spec{Kind: "work", Lane: LaneCPU, Title: "mix", After: []string{"a", "b"}}},
		{Key: "a", Spec: Spec{Kind: "work", Lane: LaneGPU, Title: "a"}},
		{Key: "b", Spec: Spec{Kind: "work", Lane: LaneGPU, Title: "b", After: []string{"a"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if j, _ := s.Get(ctx, ids["mix"]); j.State != StateWaiting || !slices.Equal(j.After, []string{ids["a"], ids["b"]}) {
		t.Fatalf("mix %+v", j)
	}
	if j := g.next(t); j.ID != ids["a"] {
		t.Fatalf("first %s", j.Title)
	}
	g.none(t)
	g.end(ids["a"], "", nil)
	if j := g.next(t); j.ID != ids["b"] {
		t.Fatalf("second %s", j.Title)
	}
	g.end(ids["b"], "", nil)
	if j := g.next(t); j.ID != ids["mix"] {
		t.Fatalf("third %s", j.Title)
	}
	g.end(ids["mix"], "", nil)
	rec.waitState(t, ids["mix"], StateSucceeded)
	if st := rec.states(ids["mix"]); !slices.Equal(st, []State{StateWaiting, StateQueued, StateRunning, StateSucceeded}) {
		t.Errorf("mix states %v", st)
	}

	// A job may depend on a finished job by id.
	later, err := s.Submit(ctx, Spec{Kind: "work", Lane: LaneCPU, After: []string{ids["mix"]}})
	if err != nil {
		t.Fatal(err)
	}
	g.next(t)
	g.end(later, "", nil)

	// Invalid graphs.
	for name, nodes := range map[string][]Node{
		"cycle":   {{Key: "x", Spec: Spec{Kind: "work", Lane: LaneCPU, After: []string{"y"}}}, {Key: "y", Spec: Spec{Kind: "work", Lane: LaneCPU, After: []string{"x"}}}},
		"dup key": {{Key: "x", Spec: Spec{Kind: "work", Lane: LaneCPU}}, {Key: "x", Spec: Spec{Kind: "work", Lane: LaneCPU}}},
		"no kind": {{Key: "x", Spec: Spec{Lane: LaneCPU}}},
	} {
		if _, err := s.SubmitGraph(ctx, nodes); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := s.Submit(ctx, Spec{Kind: "work", Lane: LaneCPU, After: []string{"nope"}}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown dependency: %v", err)
	}
}

func TestFailureAndCancelCascade(t *testing.T) {
	s, g, rec := newTestSched(t, nil)
	ctx := context.Background()
	s.Start(ctx)
	ids, _ := s.SubmitGraph(ctx, []Node{
		{Key: "a", Spec: Spec{Kind: "work", Lane: LaneGPU}},
		{Key: "b", Spec: Spec{Kind: "work", Lane: LaneGPU, After: []string{"a"}}},
		{Key: "c", Spec: Spec{Kind: "work", Lane: LaneGPU, After: []string{"b"}}},
	})
	g.next(t)
	g.end(ids["a"], "", &Error{Code: "model_load_failed", Message: "bad weights"})
	j := rec.waitState(t, ids["a"], StateFailed)
	if j.Err == nil || j.Err.Code != "model_load_failed" || j.Attempt != 1 {
		t.Fatalf("a %+v", j)
	}
	for _, k := range []string{"b", "c"} {
		if j := rec.waitState(t, ids[k], StateFailed); j.Err.Code != CodeDependency {
			t.Errorf("%s: %+v", k, j.Err)
		}
	}
	g.none(t)

	// Cancelling a running job cancels its context and its dependents.
	ids, _ = s.SubmitGraph(ctx, []Node{
		{Key: "a", Spec: Spec{Kind: "work", Lane: LaneGPU}},
		{Key: "b", Spec: Spec{Kind: "work", Lane: LaneGPU, After: []string{"a"}}},
	})
	other, _ := s.Submit(ctx, spec("queued", LaneGPU, PriorityBackground))
	g.next(t)
	if err := s.Cancel(ctx, ids["a"]); err != nil {
		t.Fatal(err)
	}
	if j := rec.waitState(t, ids["a"], StateCancelled); j.Err.Code != CodeCancelled {
		t.Errorf("a %+v", j)
	}
	rec.waitState(t, ids["b"], StateCancelled)
	// A queued job cancels at once; cancelling it again is a no-op.
	if j := g.next(t); j.ID != other {
		t.Fatalf("ran %s", j.ID)
	}
	s.Cancel(ctx, other)
	rec.waitState(t, other, StateCancelled)
	if err := s.Cancel(ctx, other); err != nil {
		t.Error(err)
	}
	if err := s.Cancel(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("cancel unknown: %v", err)
	}
	q, _ := s.Submit(ctx, spec("blocked", LaneGPU, 0))
	blocker, _ := s.Submit(ctx, spec("blocker", LaneCPU, 0))
	_ = blocker
	g.next(t) // either; the GPU one runs, CPU one runs
	g.next(t)
	s.Cancel(ctx, q)
	s.Cancel(ctx, blocker)
	rec.waitState(t, q, StateCancelled)
	rec.waitState(t, blocker, StateCancelled)
}

func TestRetry(t *testing.T) {
	s, g, rec := newTestSched(t, nil)
	ctx := context.Background()
	s.Start(ctx)
	sp := spec("flaky", LaneGPU, 0)
	sp.MaxAttempts = 2
	id, _ := s.Submit(ctx, sp)
	g.next(t)
	g.end(id, "", &Error{Code: "oom", Message: "out of memory", Retryable: true})
	if j := g.next(t); j.ID != id || j.Attempt != 2 {
		t.Fatalf("retry %+v", j)
	}
	g.end(id, "", &Error{Code: "oom", Message: "again", Retryable: true})
	j := rec.waitState(t, id, StateFailed)
	if j.Attempt != 2 || j.Err.Message != "again" || !j.Err.Retryable {
		t.Fatalf("after 2 attempts %+v", j)
	}
	if st := rec.states(id); !slices.Equal(st, []State{StateQueued, StateRunning, StateQueued, StateRunning, StateFailed}) {
		t.Errorf("states %v", st)
	}

	// By hand: a new attempt, with its own automatic retries.
	if err := s.Retry(ctx, id); err != nil {
		t.Fatal(err)
	}
	if j := g.next(t); j.Attempt != 3 || j.Err != nil {
		t.Fatalf("manual retry %+v", j)
	}
	if err := s.Retry(ctx, id); !errors.Is(err, ErrInvalid) {
		t.Errorf("retry of a running job: %v", err)
	}
	g.end(id, "", errors.New("plain"))
	// Attempt 3's failure, not attempt 2's (whose update may still be the
	// latest published: the outbox runs behind the executor).
	j = rec.waitAttempt(t, id, StateFailed, 3)
	if j.Err.Code != CodeInternal || j.Err.Message != "plain" || j.Attempt != 3 {
		t.Errorf("plain error %+v", j.Err)
	}

	// The worker died: interrupted unless retries remain.
	sp.MaxAttempts = 1
	id2, _ := s.Submit(ctx, sp)
	g.next(t)
	g.end(id2, "", &Error{Code: CodeInterrupted, Message: "engine worker exited", Retryable: true})
	rec.waitState(t, id2, StateInterrupted)
	if s.Idle() != true {
		t.Error("an interrupted job keeps the scheduler busy")
	}
	s.Retry(ctx, id2)
	g.next(t)
	g.end(id2, "", nil)
	rec.waitAttempt(t, id2, StateSucceeded, 2)
}

func TestRestartInterrupts(t *testing.T) {
	store := NewMemStore()
	ctx := context.Background()
	at := time.Now().Add(-time.Minute)
	mk := func(id string, st State, idem bool, after ...string) Job {
		return Job{ID: id, Spec: Spec{Kind: "work", Lane: LaneGPU, Idempotent: idem, After: after}, State: st, Attempt: 1, Created: at}
	}
	for _, j := range []Job{
		mk("idem", StateRunning, true),
		mk("once", StateRunning, false),
		mk("queued", StateQueued, false),
		mk("after", StateWaiting, false, "done-before"),
		mk("after-failed", StateWaiting, false, "failed-before"),
		mk("done-before", StateSucceeded, false),
		mk("failed-before", StateFailed, false),
	} {
		store.SaveJob(ctx, j)
	}
	s, g, rec := newTestSched(t, store)
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if j := rec.waitState(t, "once", StateInterrupted); j.Err.Code != CodeInterrupted {
		t.Errorf("once %+v", j)
	}
	rec.waitState(t, "after-failed", StateFailed)
	ran := map[string]bool{}
	for range 3 {
		j := g.next(t)
		ran[j.ID] = true
		if j.ID == "idem" && j.Attempt != 2 {
			t.Errorf("idem attempt %d", j.Attempt)
		}
		g.end(j.ID, "", nil)
	}
	if !ran["idem"] || !ran["queued"] || !ran["after"] {
		t.Fatalf("ran %v", ran)
	}
	g.none(t)

	// Close interrupts what runs; Idempotent ones run again at the next start.
	sp := spec("long", LaneGPU, 0)
	sp.Idempotent = true
	long, _ := s.Submit(ctx, sp)
	g.next(t)
	s.Close()
	js, _ := store.LoadJobs(ctx, Filter{States: []State{StateInterrupted}})
	var found bool
	for _, j := range js {
		if j.ID == long {
			found = j.Err.Code == CodeShutdown
		}
	}
	if !found {
		t.Fatalf("after Close: %+v", js)
	}
	if _, err := s.Submit(ctx, sp); !errors.Is(err, ErrClosed) {
		t.Errorf("Submit after Close: %v", err)
	}

	s2, g2, _ := newTestSched(t, store)
	s2.Start(ctx)
	if j := g2.next(t); j.ID != long {
		t.Fatalf("after restart ran %s", j.ID)
	}
	g2.end(long, "", nil)
	s2.Flush()

	// Get and Retry reach jobs of earlier runs through the store.
	if j, err := s2.Get(ctx, "once"); err != nil || j.State != StateInterrupted {
		t.Fatalf("Get once: %+v %v", j, err)
	}
	if err := s2.Retry(ctx, "once"); err != nil {
		t.Fatal(err)
	}
	g2.next(t)
	g2.end("once", "", nil)
}

func TestProgressAndChunks(t *testing.T) {
	store := NewMemStore()
	rec := newRecorder()
	s := New(Options{Store: store, Publish: rec.publish, ProgressEvery: time.Hour})
	defer s.Close()
	ctx := context.Background()
	s.Register("tts", ExecutorFunc(func(ctx context.Context, j Job, report func(Progress), chunk func(json.RawMessage)) (json.RawMessage, error) {
		report(Progress{Stage: "loading", Fraction: -1})
		report(Progress{Stage: "generating", Fraction: 0.1})
		report(Progress{Stage: "generating", Fraction: 0.2}) // throttled
		if chunk != nil {
			for i := range 3 {
				chunk(json.RawMessage(fmt.Sprintf(`{"seq":%d}`, i)))
			}
		}
		report(Progress{Stage: "writing", Fraction: 1})
		return json.RawMessage(`{"audioSeconds":1}`), nil
	}))
	s.Start(ctx)
	streamed, _ := s.Submit(ctx, Spec{Kind: "tts", Lane: LaneGPU, Params: json.RawMessage(`{"stream":true}`)})
	plain, _ := s.Submit(ctx, Spec{Kind: "tts", Lane: LaneGPU, Params: json.RawMessage(`{"text":"x"}`)})
	rec.waitState(t, plain, StateSucceeded)
	s.Flush()
	saves := store.Saves()

	rec.mu.Lock()
	defer rec.mu.Unlock()
	var stages, chunks []string
	for _, u := range rec.updates {
		if u.Job.ID != streamed || u.Job.State != StateRunning {
			continue
		}
		if u.Chunk != nil {
			chunks = append(chunks, string(u.Chunk))
		} else {
			stages = append(stages, fmt.Sprintf("%s %.1f", u.Job.Progress.Stage, u.Job.Progress.Fraction))
		}
	}
	if !slices.Equal(stages, []string{" -1.0", "loading -1.0", "generating 0.1", "writing 1.0"}) {
		t.Errorf("progress %q", stages)
	}
	if !slices.Equal(chunks, []string{`{"seq":0}`, `{"seq":1}`, `{"seq":2}`}) {
		t.Errorf("chunks %q", chunks)
	}
	for _, u := range rec.updates {
		if u.Job.ID == plain && u.Chunk != nil {
			t.Errorf("chunk for a job that did not ask to stream")
		}
	}
	// Progress is not saved on its own: submit + start + finish per job.
	if saves != 6 {
		t.Errorf("%d saves", saves)
	}
}

func TestExecutorPanicsAndMissingKinds(t *testing.T) {
	s, _, rec := newTestSched(t, nil)
	ctx := context.Background()
	s.Register("boom", ExecutorFunc(func(context.Context, Job, func(Progress), func(json.RawMessage)) (json.RawMessage, error) {
		panic("kaboom")
	}))
	s.Start(ctx)
	id, _ := s.Submit(ctx, Spec{Kind: "boom", Lane: LaneCPU})
	if j := rec.waitState(t, id, StateFailed); j.Err.Code != CodeInternal {
		t.Errorf("panic: %+v", j.Err)
	}
	id, _ = s.Submit(ctx, Spec{Kind: "unknown", Lane: LaneCPU})
	if j := rec.waitState(t, id, StateFailed); j.Err.Code != CodeNoExecutor {
		t.Errorf("unknown kind: %+v", j.Err)
	}
}

// Subscribers may call back into the scheduler: updates are published
// without its locks held.
func TestReentrantPublish(t *testing.T) {
	store := NewMemStore()
	var s *Sched
	done := make(chan string, 4)
	s = New(Options{Store: store, Publish: func(u Updated) {
		if u.Job.State == StateSucceeded {
			if u.Job.Title == "first" {
				s.Submit(context.Background(), Spec{Kind: "ok", Lane: LaneCPU, Title: "second"})
			}
			s.List(context.Background(), Filter{})
			done <- u.Job.Title
		}
	}})
	defer s.Close()
	s.Register("ok", ExecutorFunc(func(context.Context, Job, func(Progress), func(json.RawMessage)) (json.RawMessage, error) {
		return nil, nil
	}))
	s.Start(context.Background())
	s.Submit(context.Background(), Spec{Kind: "ok", Lane: LaneCPU, Title: "first"})
	for _, want := range []string{"first", "second"} {
		select {
		case got := <-done:
			if got != want {
				t.Fatalf("got %s want %s", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("deadlock")
		}
	}
}

func TestListAndFilter(t *testing.T) {
	s, g, rec := newTestSched(t, nil)
	ctx := context.Background()
	s.Start(ctx)
	a, _ := s.Submit(ctx, Spec{Kind: "work", Lane: LaneGPU, Owner: "take:1"})
	g.next(t)
	b, _ := s.Submit(ctx, Spec{Kind: "work", Lane: LaneGPU, Owner: "project:9"})
	c, _ := s.Submit(ctx, Spec{Kind: "work", Lane: LaneCPU, Owner: "take:2"})
	g.next(t)
	g.end(c, "", nil)
	rec.waitState(t, c, StateSucceeded)

	ids := func(js []Job) []string {
		var out []string
		for _, j := range js {
			out = append(out, j.ID)
		}
		return out
	}
	for name, tc := range map[string]struct {
		f    Filter
		want []string
	}{
		"unfinished":  {Filter{}, []string{a, b}},
		"take owners": {Filter{Owner: "take:", States: []State{StateRunning, StateSucceeded}}, []string{a, c}},
		"exact owner": {Filter{Owner: "project:9"}, []string{b}},
		"lane":        {Filter{Lane: LaneCPU, States: []State{StateSucceeded}}, []string{c}},
		"limit":       {Filter{States: []State{StateQueued, StateRunning, StateSucceeded}, Limit: 2}, []string{a, b}},
	} {
		js, err := s.List(ctx, tc.f)
		if err != nil || !slices.Equal(ids(js), tc.want) {
			t.Errorf("%s: %v %v, want %v", name, ids(js), err, tc.want)
		}
	}
	g.end(a, "", nil)
}

// The scheduler stays consistent under concurrent submits and cancels (go test -race).
func TestConcurrentUse(t *testing.T) {
	store := NewMemStore()
	s := New(Options{Store: store})
	defer s.Close()
	s.Register("ok", ExecutorFunc(func(ctx context.Context, j Job, report func(Progress), _ func(json.RawMessage)) (json.RawMessage, error) {
		for i := range 5 {
			report(Progress{Stage: "x", Fraction: float64(i) / 5})
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Millisecond):
			}
		}
		return nil, nil
	}))
	s.Start(context.Background())
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Go(func() {
			for i := range 20 {
				lane := []Lane{LaneGPU, LaneCPU, LaneNet, LaneMedia}[(w+i)%4]
				id, err := s.Submit(context.Background(), Spec{Kind: "ok", Lane: lane, Priority: Priority(i % 3)})
				if err != nil {
					t.Error(err)
					return
				}
				if i%3 == 0 {
					s.Cancel(context.Background(), id)
				}
				s.Get(context.Background(), id)
			}
		})
	}
	wg.Wait()
	deadline := time.Now().Add(20 * time.Second)
	for !s.Idle() {
		if time.Now().After(deadline) {
			t.Fatal("never idle")
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.Flush()
	left, _ := store.LoadJobs(context.Background(), Filter{})
	if len(left) != 0 {
		t.Errorf("%d unfinished jobs in the store", len(left))
	}
}

// TestCancelledBy (#108): a cancel says who asked (WithActor), for a
// running job, its dependents and a queued one.
func TestCancelledBy(t *testing.T) {
	s, g, rec := newTestSched(t, nil)
	ctx := context.Background()
	s.Start(ctx)
	ids, _ := s.SubmitGraph(ctx, []Node{
		{Key: "a", Spec: Spec{Kind: "work", Lane: LaneGPU}},
		{Key: "b", Spec: Spec{Kind: "work", Lane: LaneGPU, After: []string{"a"}}},
	})
	queued, _ := s.Submit(ctx, spec("queued", LaneGPU, PriorityBackground))
	g.next(t)
	by := func(j Job) string {
		var d struct{ By string }
		json.Unmarshal(j.Err.Data, &d)
		return d.By
	}
	s.Cancel(WithActor(ctx, "api"), ids["a"])
	for _, id := range []string{ids["a"], ids["b"]} {
		if j := rec.waitState(t, id, StateCancelled); by(j) != "api" {
			t.Errorf("%s: by %q", id, by(j))
		}
	}
	g.next(t)
	s.Cancel(ctx, queued) // no actor: no Data
	if j := rec.waitState(t, queued, StateCancelled); len(j.Err.Data) != 0 || ActorOf(ctx) != "" {
		t.Errorf("queued: %s", j.Err.Data)
	}
}
