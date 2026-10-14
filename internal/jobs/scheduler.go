package jobs

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"
)

// Errors of the scheduler. (jobs imports nothing internal, so these are
// its own; callers that map errors to the UI or HTTP treat ErrNotFound as
// core.ErrNotFound, ErrInvalid as core.ErrInvalid, ErrClosed as
// core.ErrCancelled.)
var (
	ErrNotFound = errors.New("jobs: no such job")
	ErrInvalid  = errors.New("jobs: invalid job")
	ErrClosed   = errors.New("jobs: the scheduler is closed")
)

// Error codes the scheduler sets on its own (executors add theirs, e.g.
// the protocol's).
const (
	CodeCancelled   = "cancelled"         // Cancel
	CodeInterrupted = "interrupted"       // the worker died mid-run, or the app quit (see CodeShutdown)
	CodeShutdown    = "shutdown"          // Close interrupted it; Idempotent jobs re-run at the next Start
	CodeDependency  = "dependency_failed" // a job it depends on failed
	CodeNoExecutor  = "no_executor"       // nothing registered for the kind
	CodeInternal    = "internal"          // an executor returned a plain error, or panicked
)

// Options configure New.
type Options struct {
	Store Store // required; NewMemStore() in tests
	// Publish receives every Updated, in order, on one goroutine of the
	// scheduler that holds no lock: func(u jobs.Updated) { bus.Publish(u) }.
	Publish func(Updated)
	// Slots overrides LaneSlots per lane; a lane in neither has 1 slot.
	Slots map[Lane]int
	// NewID makes job ids (default: a ULID, like core.NewID).
	NewID func() string
	Now   func() time.Time
	// ProgressEvery is the shortest interval between two progress updates
	// of a job (default 100 ms). A change of stage is always published.
	ProgressEvery time.Duration
	// RetryDelay is the wait before automatic attempt n+1 after attempt n
	// failed retryably (default n seconds, at most 30 s).
	RetryDelay func(attempt int) time.Duration
	// KeepFinished bounds the finished jobs kept in memory for Get/List
	// (default 200); older ones are read back from the Store.
	KeepFinished int
	Logf         func(format string, args ...any)
}

// Sched implements Scheduler. Make it with New.
//
// How it works: one mutex guards the jobs in memory. Every change is
// appended, as a snapshot, to an ordered outbox that one goroutine drains:
// it saves state changes to the Store and then publishes Updated, holding
// no lock, so subscribers may call back into the scheduler. Submit saves
// new jobs itself before they become visible, so a job the caller got an
// id for is durable. A dispatcher goroutine starts queued jobs when their
// lane has a free slot: higher Priority first, then older.
type Sched struct {
	opts      Options
	execs     map[string]Executor
	root      context.Context
	stopRoot  context.CancelFunc
	kick      chan struct{}
	loopDone  chan struct{}
	executors sync.WaitGroup

	mu         sync.Mutex
	started    bool
	closed     bool
	jobs       map[string]*entry   // unfinished jobs, and recently finished ones
	done       map[string]State    // every job known to be terminal, by id
	dependents map[string][]string // job id → ids of jobs that wait on it
	running    map[Lane]int
	finished   []string // ids of terminal entries in s.jobs, oldest first
	timer      *time.Timer

	outMu   sync.Mutex
	outCond *sync.Cond
	outQ    []outItem
	outStop bool
	outDone chan struct{}
}

var _ Scheduler = (*Sched)(nil)

type entry struct {
	job       Job
	cancel    context.CancelCauseFunc // set while running
	run       int                     // bumps on every start: stale reports are dropped
	notBefore time.Time               // automatic retry waits until then
	base      int                     // Attempt when last (re)queued by hand: auto-retries count from it
	lastPub   time.Time
	lastStage string
}

type outItem struct {
	job   Job
	chunk json.RawMessage
	save  bool
	flush chan struct{} // closed when the items before it are out
}

var (
	errUserCancel = errors.New("cancelled by the user")
	errShutdown   = errors.New("the scheduler closed")
)

// New returns a scheduler. Register executors, then Start it. Jobs may be
// submitted before Start; they run once it is called.
func New(opts Options) *Sched {
	if opts.Store == nil {
		panic("jobs.New: no Store")
	}
	if opts.NewID == nil {
		opts.NewID = func() string { return newULID(time.Now()) }
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.ProgressEvery == 0 {
		opts.ProgressEvery = 100 * time.Millisecond
	}
	if opts.RetryDelay == nil {
		opts.RetryDelay = func(n int) time.Duration { return min(time.Duration(n)*time.Second, 30*time.Second) }
	}
	if opts.KeepFinished == 0 {
		opts.KeepFinished = 200
	}
	if opts.Logf == nil {
		opts.Logf = log.Printf
	}
	root, stop := context.WithCancel(context.Background())
	s := &Sched{
		opts: opts, execs: map[string]Executor{}, root: root, stopRoot: stop,
		kick: make(chan struct{}, 1), loopDone: make(chan struct{}),
		jobs: map[string]*entry{}, done: map[string]State{}, dependents: map[string][]string{},
		running: map[Lane]int{}, outDone: make(chan struct{}),
	}
	s.outCond = sync.NewCond(&s.outMu)
	go s.outbox()
	return s
}

func (s *Sched) Register(kind string, ex Executor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.execs[kind] = ex
}

func (s *Sched) slots(l Lane) int {
	if n, ok := s.opts.Slots[l]; ok {
		return n
	}
	if n, ok := LaneSlots[l]; ok {
		return n
	}
	return 1
}

// Start loads the persisted unfinished jobs and starts dispatching. Jobs
// found running were interrupted by a crash: Idempotent ones are
// re-queued, the others become Interrupted. Jobs interrupted by Close
// (CodeShutdown) that are Idempotent are re-queued too.
func (s *Sched) Start(ctx context.Context) error {
	loaded, err := s.opts.Store.LoadJobs(ctx, Filter{})
	if err != nil {
		return fmt.Errorf("jobs: loading: %w", err)
	}
	now := s.opts.Now()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	if s.started {
		s.mu.Unlock()
		return nil
	}
	var waiting []*entry
	for _, j := range loaded {
		if _, ok := s.jobs[j.ID]; ok {
			continue
		}
		e := &entry{job: j, base: j.Attempt}
		changed := false
		switch {
		case j.State == StateRunning && j.Idempotent,
			j.State == StateInterrupted && j.Idempotent && j.Err != nil && j.Err.Code == CodeShutdown:
			e.job.State, e.job.Err, e.job.Finished, changed = StateQueued, nil, time.Time{}, true
			if len(j.After) > 0 {
				e.job.State = StateWaiting
			}
		case j.State == StateRunning:
			e.job.State, e.job.Finished, changed = StateInterrupted, now, true
			e.job.Err = &Error{Code: CodeInterrupted, Message: "AudioSmith stopped while the job ran"}
		}
		s.jobs[j.ID] = e
		for _, d := range j.After {
			s.dependents[d] = append(s.dependents[d], j.ID)
		}
		if e.job.State == StateWaiting {
			waiting = append(waiting, e)
		}
		s.out(e, nil, changed)
	}
	unknown := s.unknownDeps(waiting)
	s.mu.Unlock()

	if len(unknown) > 0 {
		if err := s.lookupDone(ctx); err != nil {
			s.opts.Logf("jobs: dependencies: %v", err)
		}
	}

	s.mu.Lock()
	for _, e := range waiting {
		s.evaluate(e, true)
	}
	s.started = true
	s.mu.Unlock()
	s.poke()
	go s.loop()
	return nil
}

// unknownDeps lists the dependencies of es that are neither in memory nor
// known to be finished. Caller holds mu.
func (s *Sched) unknownDeps(es []*entry) []string {
	var out []string
	for _, e := range es {
		for _, d := range e.job.After {
			if _, ok := s.jobs[d]; ok {
				continue
			}
			if _, ok := s.done[d]; ok {
				continue
			}
			out = append(out, d)
		}
	}
	return out
}

// lookupDone reads the states of every finished job from the store into
// s.done (the dependencies of jobs from an earlier run). Slow path.
func (s *Sched) lookupDone(ctx context.Context) error {
	old, err := s.opts.Store.LoadJobs(ctx, Filter{States: []State{StateSucceeded, StateFailed, StateCancelled}})
	if err != nil {
		return err
	}
	s.mu.Lock()
	for _, j := range old {
		if _, ok := s.jobs[j.ID]; !ok {
			s.done[j.ID] = j.State
		}
	}
	s.mu.Unlock()
	return nil
}

func (s *Sched) Submit(ctx context.Context, spec Spec) (string, error) {
	ids, err := s.SubmitGraph(ctx, []Node{{Key: "job", Spec: spec}})
	if err != nil {
		return "", err
	}
	return ids["job"], nil
}

// SubmitGraph validates the graph (unique keys, known references, no
// cycle), saves every job, then makes them all visible at once.
func (s *Sched) SubmitGraph(ctx context.Context, nodes []Node) (map[string]string, error) {
	if len(nodes) == 0 {
		return nil, fmt.Errorf("%w: empty graph", ErrInvalid)
	}
	keys := map[string]int{}
	for i, n := range nodes {
		if n.Key == "" {
			return nil, fmt.Errorf("%w: node %d has no key", ErrInvalid, i)
		}
		if _, dup := keys[n.Key]; dup {
			return nil, fmt.Errorf("%w: duplicate key %q", ErrInvalid, n.Key)
		}
		if n.Spec.Kind == "" || n.Spec.Lane == "" {
			return nil, fmt.Errorf("%w: node %q needs a kind and a lane", ErrInvalid, n.Key)
		}
		keys[n.Key] = i
	}
	order, err := topoOrder(nodes, keys)
	if err != nil {
		return nil, err
	}

	// External references: jobs in memory, or finished ones in the store.
	var external []string
	for _, n := range nodes {
		for _, a := range n.Spec.After {
			if _, ok := keys[a]; !ok {
				external = append(external, a)
			}
		}
	}
	s.mu.Lock()
	closed := s.closed
	var unknown []string
	for _, id := range external {
		if _, ok := s.jobs[id]; !ok {
			if _, ok := s.done[id]; !ok {
				unknown = append(unknown, id)
			}
		}
	}
	s.mu.Unlock()
	if closed {
		return nil, ErrClosed
	}
	if len(unknown) > 0 {
		if err := s.lookupDone(ctx); err != nil {
			return nil, err
		}
		s.mu.Lock()
		for _, id := range unknown {
			_, inMem := s.jobs[id]
			_, isDone := s.done[id]
			if !inMem && !isDone {
				s.mu.Unlock()
				return nil, fmt.Errorf("%w: dependency %s", ErrNotFound, id)
			}
		}
		s.mu.Unlock()
	}

	now := s.opts.Now()
	ids := make(map[string]string, len(nodes))
	for _, n := range nodes {
		ids[n.Key] = s.opts.NewID()
	}
	built := make([]Job, 0, len(nodes))
	for _, i := range order {
		n := nodes[i]
		j := Job{ID: ids[n.Key], Spec: n.Spec, State: StateQueued, Created: now, Progress: Progress{Fraction: -1}}
		j.After = nil
		for _, a := range n.Spec.After {
			if id, ok := ids[a]; ok {
				a = id
			}
			if !slices.Contains(j.After, a) {
				j.After = append(j.After, a)
			}
		}
		if len(j.After) > 0 {
			j.State = StateWaiting
		}
		j.Params = slices.Clone(j.Params)
		built = append(built, j)
	}
	for k, j := range built {
		if err := s.opts.Store.SaveJob(ctx, j); err != nil {
			// Undo what was saved: those jobs never become visible.
			for _, prev := range built[:k] {
				prev.State, prev.Finished = StateCancelled, now
				prev.Err = &Error{Code: CodeCancelled, Message: "submission failed"}
				if err := s.opts.Store.SaveJob(context.WithoutCancel(ctx), prev); err != nil {
					s.opts.Logf("jobs: %s: %v", prev.ID, err)
				}
			}
			return nil, fmt.Errorf("jobs: saving: %w", err)
		}
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	var es []*entry
	for _, j := range built {
		e := &entry{job: j}
		s.jobs[j.ID] = e
		for _, d := range j.After {
			s.dependents[d] = append(s.dependents[d], j.ID)
		}
		s.out(e, nil, false) // saved above
		es = append(es, e)
	}
	// Dependencies may have finished since the states were chosen.
	for _, e := range es {
		if e.job.State == StateWaiting {
			s.evaluate(e, true)
		}
	}
	s.mu.Unlock()
	s.poke()
	return ids, nil
}

// topoOrder returns node indexes with in-graph dependencies first.
func topoOrder(nodes []Node, keys map[string]int) ([]int, error) {
	state := make([]int, len(nodes)) // 0 new, 1 visiting, 2 done
	var order []int
	var visit func(i int) error
	visit = func(i int) error {
		switch state[i] {
		case 1:
			return fmt.Errorf("%w: cycle through %q", ErrInvalid, nodes[i].Key)
		case 2:
			return nil
		}
		state[i] = 1
		for _, a := range nodes[i].Spec.After {
			if j, ok := keys[a]; ok {
				if err := visit(j); err != nil {
					return err
				}
			}
		}
		state[i] = 2
		order = append(order, i)
		return nil
	}
	for i := range nodes {
		if err := visit(i); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// depState is the state of job id as far as dependents care. Caller holds mu.
func (s *Sched) depState(id string) (State, bool) {
	if e, ok := s.jobs[id]; ok {
		return e.job.State, true
	}
	st, ok := s.done[id]
	return st, ok
}

// evaluate settles a waiting job against its dependencies: queued when
// all succeeded, failed or cancelled (and so its dependents) when one
// failed or was cancelled. Caller holds mu.
func (s *Sched) evaluate(e *entry, publish bool) {
	if e.job.State != StateWaiting {
		return
	}
	ready := true
	for _, d := range e.job.After {
		st, ok := s.depState(d)
		switch {
		case !ok:
			s.finishLocked(e, StateFailed, &Error{Code: CodeDependency, Message: "A job it depends on is gone: " + d})
			return
		case st == StateFailed:
			s.finishLocked(e, StateFailed, &Error{Code: CodeDependency, Message: "A job it depends on failed"})
			return
		case st == StateCancelled:
			s.finishLocked(e, StateCancelled, &Error{Code: CodeCancelled, Message: "A job it depends on was cancelled"})
			return
		case st != StateSucceeded:
			ready = false
		}
	}
	if ready {
		e.job.State = StateQueued
		if publish {
			s.out(e, nil, true)
		}
		s.poke()
	}
}

// finishLocked makes a non-running job terminal and settles its
// dependents. Caller holds mu.
func (s *Sched) finishLocked(e *entry, st State, jerr *Error) {
	e.job.State, e.job.Err, e.job.Finished = st, jerr, s.opts.Now()
	s.retire(e)
	s.out(e, nil, true)
	s.settleDependents(e.job.ID)
}

// retire records a terminal entry and drops the oldest finished ones
// from memory. Caller holds mu.
func (s *Sched) retire(e *entry) {
	s.done[e.job.ID] = e.job.State
	s.finished = append(s.finished, e.job.ID)
	for len(s.finished) > s.opts.KeepFinished {
		id := s.finished[0]
		s.finished = s.finished[1:]
		if old, ok := s.jobs[id]; ok && old.job.State.Terminal() {
			delete(s.jobs, id)
		}
	}
}

// settleDependents re-evaluates the waiting jobs that depend on id. Caller holds mu.
func (s *Sched) settleDependents(id string) {
	for _, d := range s.dependents[id] {
		if e, ok := s.jobs[d]; ok {
			s.evaluate(e, true)
		}
	}
	if st, ok := s.depState(id); ok && st.Terminal() {
		delete(s.dependents, id)
	}
}

// poke wakes the dispatcher.
func (s *Sched) poke() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

func (s *Sched) loop() {
	defer close(s.loopDone)
	for {
		select {
		case <-s.kick:
		case <-s.root.Done():
			return
		}
		s.dispatch()
	}
}

// dispatch starts every queued job that has a free slot in its lane.
func (s *Sched) dispatch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started || s.closed {
		return
	}
	now := s.opts.Now()
	var queued []*entry
	var wake time.Time
	for _, e := range s.jobs {
		if e.job.State != StateQueued {
			continue
		}
		if e.notBefore.After(now) {
			if wake.IsZero() || e.notBefore.Before(wake) {
				wake = e.notBefore
			}
			continue
		}
		queued = append(queued, e)
	}
	slices.SortFunc(queued, func(a, b *entry) int {
		if a.job.Priority != b.job.Priority {
			return int(b.job.Priority - a.job.Priority)
		}
		if c := a.job.Created.Compare(b.job.Created); c != 0 {
			return c
		}
		return strings.Compare(a.job.ID, b.job.ID)
	})
	for _, e := range queued {
		if s.running[e.job.Lane] >= s.slots(e.job.Lane) {
			continue
		}
		s.startLocked(e)
	}
	if !wake.IsZero() {
		if s.timer != nil {
			s.timer.Stop()
		}
		s.timer = time.AfterFunc(wake.Sub(now), s.poke)
	}
}

func (s *Sched) startLocked(e *entry) {
	ex, ok := s.execs[e.job.Kind]
	if !ok {
		s.finishLocked(e, StateFailed, &Error{Code: CodeNoExecutor, Message: "Nothing can run jobs of kind " + e.job.Kind})
		return
	}
	ctx, cancel := context.WithCancelCause(s.root)
	e.cancel = cancel
	e.run++
	e.job.State = StateRunning
	e.job.Attempt++
	e.job.Started, e.job.Finished = s.opts.Now(), time.Time{}
	e.job.Err, e.job.Result = nil, nil
	e.job.Progress = Progress{Fraction: -1}
	e.lastPub, e.lastStage = time.Time{}, ""
	s.running[e.job.Lane]++
	s.out(e, nil, true)
	job := cloneJob(e.job)
	run := e.run
	s.executors.Add(1)
	go s.execute(ctx, e, run, ex, job)
}

func (s *Sched) execute(ctx context.Context, e *entry, run int, ex Executor, job Job) {
	defer s.executors.Done()
	report := func(p Progress) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if e.run != run || e.job.State != StateRunning {
			return
		}
		e.job.Progress = p
		now := s.opts.Now()
		if p.Stage != e.lastStage || now.Sub(e.lastPub) >= s.opts.ProgressEvery {
			e.lastPub, e.lastStage = now, p.Stage
			s.out(e, nil, false)
		}
	}
	var chunk func(json.RawMessage)
	if wantsStream(job.Params) {
		chunk = func(c json.RawMessage) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if e.run == run && e.job.State == StateRunning {
				s.out(e, slices.Clone(c), false)
			}
		}
	}
	res, err := runSafely(ctx, ex, job, report, chunk)
	s.finishRun(e, run, res, err, context.Cause(ctx))
}

// wantsStream peeks at params for "stream": true.
func wantsStream(params json.RawMessage) bool {
	var p struct {
		Stream bool `json:"stream"`
	}
	return json.Unmarshal(params, &p) == nil && p.Stream
}

func runSafely(ctx context.Context, ex Executor, job Job, report func(Progress), chunk func(json.RawMessage)) (res json.RawMessage, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &Error{Code: CodeInternal, Message: fmt.Sprintf("panic: %v\n%s", r, debug.Stack())}
		}
	}()
	return ex.Run(ctx, job, report, chunk)
}

// finishRun records how a run ended.
func (s *Sched) finishRun(e *entry, run int, res json.RawMessage, err, cause error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.run != run {
		return
	}
	s.running[e.job.Lane]--
	e.cancel(nil)
	e.cancel = nil
	now := s.opts.Now()
	switch {
	case err == nil:
		e.job.Result = slices.Clone(res)
		if e.job.Result == nil {
			e.job.Result = json.RawMessage(`{}`)
		}
		s.finishLocked(e, StateSucceeded, nil)
	case errors.Is(cause, errShutdown):
		e.job.State, e.job.Finished = StateInterrupted, now
		e.job.Err = &Error{Code: CodeShutdown, Message: "AudioSmith quit while the job ran"}
		s.out(e, nil, true)
	case errors.Is(cause, errUserCancel):
		s.finishLocked(e, StateCancelled, &Error{Code: CodeCancelled, Message: "Cancelled"})
	default:
		var jerr *Error
		if !errors.As(err, &jerr) {
			jerr = &Error{Code: CodeInternal, Message: err.Error()}
		}
		jerr = &Error{Code: jerr.Code, Message: jerr.Message, Data: slices.Clone(jerr.Data), Retryable: jerr.Retryable}
		if jerr.Retryable && e.job.Attempt-e.base < max(e.job.MaxAttempts, 1) {
			e.job.State, e.job.Err = StateQueued, jerr
			e.notBefore = now.Add(s.opts.RetryDelay(e.job.Attempt))
			s.out(e, nil, true)
			s.poke()
			return
		}
		if jerr.Code == CodeInterrupted {
			e.job.State, e.job.Err, e.job.Finished = StateInterrupted, jerr, now
			s.out(e, nil, true)
		} else {
			s.finishLocked(e, StateFailed, jerr)
		}
	}
	s.poke()
}

// Cancel cancels a job and the jobs waiting on it. A running job's context
// is cancelled; it becomes Cancelled when its executor returns.
func (s *Sched) Cancel(ctx context.Context, id string) error {
	s.mu.Lock()
	e, ok := s.jobs[id]
	if !ok {
		_, isDone := s.done[id]
		s.mu.Unlock()
		if isDone {
			return nil
		}
		if _, err := s.load(ctx, id); err != nil {
			return err
		}
		return nil // finished in an earlier run
	}
	defer s.mu.Unlock()
	s.cancelLocked(e)
	return nil
}

func (s *Sched) cancelLocked(e *entry) {
	switch e.job.State {
	case StateRunning:
		if e.cancel != nil {
			e.cancel(errUserCancel)
		}
		// Its dependents are cancelled now, not when it returns.
		for _, d := range s.dependents[e.job.ID] {
			if de, ok := s.jobs[d]; ok && !de.job.State.Terminal() {
				s.cancelLocked(de)
			}
		}
	case StateWaiting, StateQueued, StateInterrupted:
		s.finishLocked(e, StateCancelled, &Error{Code: CodeCancelled, Message: "Cancelled"})
		for _, d := range s.dependents[e.job.ID] {
			if de, ok := s.jobs[d]; ok && !de.job.State.Terminal() {
				s.cancelLocked(de)
			}
		}
	}
}

// Retry re-queues a failed, cancelled or interrupted job for a new attempt.
func (s *Sched) Retry(ctx context.Context, id string) error {
	s.mu.Lock()
	e, ok := s.jobs[id]
	s.mu.Unlock()
	if !ok {
		j, err := s.load(ctx, id)
		if err != nil {
			return err
		}
		s.mu.Lock()
		if e, ok = s.jobs[id]; !ok {
			e = &entry{job: j}
			s.jobs[id] = e
			for _, d := range j.After {
				s.dependents[d] = append(s.dependents[d], id)
			}
		}
		s.mu.Unlock()
	}
	s.mu.Lock()
	unknown := s.unknownDeps([]*entry{e})
	s.mu.Unlock()
	if len(unknown) > 0 {
		if err := s.lookupDone(ctx); err != nil {
			return err
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	switch e.job.State {
	case StateFailed, StateCancelled, StateInterrupted:
	default:
		return fmt.Errorf("%w: job %s is %s", ErrInvalid, id, e.job.State)
	}
	delete(s.done, id)
	if i := slices.Index(s.finished, id); i >= 0 {
		s.finished = slices.Delete(s.finished, i, i+1)
	}
	e.base = e.job.Attempt
	e.notBefore = time.Time{}
	e.job.Err, e.job.Result, e.job.Finished = nil, nil, time.Time{}
	e.job.Progress = Progress{Fraction: -1}
	e.job.State = StateQueued
	if len(e.job.After) > 0 {
		e.job.State = StateWaiting
	}
	s.out(e, nil, true)
	s.evaluate(e, true)
	s.poke()
	return nil
}

// load reads one job from the store (one not in memory).
func (s *Sched) load(ctx context.Context, id string) (Job, error) {
	all, err := s.opts.Store.LoadJobs(ctx, Filter{States: []State{
		StateWaiting, StateQueued, StateRunning, StateSucceeded, StateFailed, StateCancelled, StateInterrupted}})
	if err != nil {
		return Job{}, err
	}
	for _, j := range all {
		if j.ID == id {
			return j, nil
		}
	}
	return Job{}, fmt.Errorf("%w: %s", ErrNotFound, id)
}

func (s *Sched) Get(ctx context.Context, id string) (Job, error) {
	s.mu.Lock()
	if e, ok := s.jobs[id]; ok {
		j := cloneJob(e.job)
		s.mu.Unlock()
		return j, nil
	}
	s.mu.Unlock()
	return s.load(ctx, id)
}

// List returns the jobs f selects, oldest first: the store's, overlaid
// with the (newer) states in memory.
func (s *Sched) List(ctx context.Context, f Filter) ([]Job, error) {
	stored, err := s.opts.Store.LoadJobs(ctx, Filter{States: f.States, Lane: f.Lane, Owner: f.Owner, Kind: f.Kind, Since: f.Since})
	if err != nil {
		return nil, err
	}
	byID := make(map[string]int, len(stored))
	for i, j := range stored {
		byID[j.ID] = i
	}
	var out []Job
	s.mu.Lock()
	for _, j := range stored {
		if e, ok := s.jobs[j.ID]; ok {
			j = cloneJob(e.job)
		}
		if f.Match(j) {
			out = append(out, j)
		}
	}
	for id, e := range s.jobs {
		if _, ok := byID[id]; !ok && f.Match(e.job) {
			out = append(out, cloneJob(e.job))
		}
	}
	s.mu.Unlock()
	sortJobs(out)
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

func (s *Sched) Idle() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.jobs {
		switch e.job.State {
		case StateWaiting, StateQueued, StateRunning:
			return false
		}
	}
	return true
}

// Close stops dispatching, cancels the running jobs (they become
// Interrupted with CodeShutdown), waits for their executors, and returns
// once every update is saved and published.
func (s *Sched) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	started := s.started
	for _, e := range s.jobs {
		if e.job.State == StateRunning && e.cancel != nil {
			e.cancel(errShutdown)
		}
	}
	if s.timer != nil {
		s.timer.Stop()
	}
	s.mu.Unlock()
	s.executors.Wait()
	s.stopRoot()
	if started {
		<-s.loopDone
	}
	s.outMu.Lock()
	s.outStop = true
	s.outCond.Broadcast()
	s.outMu.Unlock()
	<-s.outDone
	return nil
}

// Flush waits until every change so far is saved and published.
func (s *Sched) Flush() {
	ch := make(chan struct{})
	s.outMu.Lock()
	if s.outStop {
		s.outMu.Unlock()
		return
	}
	s.outQ = append(s.outQ, outItem{flush: ch})
	s.outCond.Signal()
	s.outMu.Unlock()
	<-ch
}

// out queues e's current state for saving (save) and publishing. Caller holds mu.
func (s *Sched) out(e *entry, chunk json.RawMessage, save bool) {
	it := outItem{job: cloneJob(e.job), chunk: chunk, save: save}
	s.outMu.Lock()
	s.outQ = append(s.outQ, it)
	s.outCond.Signal()
	s.outMu.Unlock()
}

// outbox saves and publishes queued updates in order, holding no lock of
// the scheduler while it does.
func (s *Sched) outbox() {
	defer close(s.outDone)
	for {
		s.outMu.Lock()
		for len(s.outQ) == 0 && !s.outStop {
			s.outCond.Wait()
		}
		if len(s.outQ) == 0 {
			s.outMu.Unlock()
			return
		}
		it := s.outQ[0]
		s.outQ[0] = outItem{}
		s.outQ = s.outQ[1:]
		s.outMu.Unlock()

		if it.flush != nil {
			close(it.flush)
			continue
		}
		if it.save {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if err := s.opts.Store.SaveJob(ctx, it.job); err != nil {
				s.opts.Logf("jobs: saving %s: %v", it.job.ID, err)
			}
			cancel()
		}
		if s.opts.Publish != nil {
			s.opts.Publish(Updated{Job: it.job, Chunk: it.chunk})
		}
	}
}

// The last ULID newULID made.
var (
	ulidMu   sync.Mutex
	ulidMS   uint64
	ulidLast [16]byte
	ulidUsed bool
)

// newULID makes a ULID (as core.NewID, which jobs cannot import),
// monotonic within a millisecond so the jobs of a graph sort, and so run,
// in the order they were made.
func newULID(t time.Time) string {
	const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	ms := uint64(t.UnixMilli())
	ulidMu.Lock()
	var b [16]byte
	if ulidUsed && ms == ulidMS {
		b = ulidLast
		for i := 15; i >= 6; i-- {
			b[i]++
			if b[i] != 0 {
				break
			}
		}
	} else {
		for i := 5; i >= 0; i-- {
			b[i] = byte(ms >> (8 * (5 - i)))
		}
		rand.Read(b[6:])
		b[6] &= 0x7f
	}
	ulidMS, ulidLast, ulidUsed = ms, b, true
	ulidMu.Unlock()
	var out [26]byte
	var acc uint64
	bits, j := 0, 25
	for i := 15; i >= 0; i-- {
		acc |= uint64(b[i]) << bits
		bits += 8
		for bits >= 5 {
			out[j] = crockford[acc&31]
			j--
			acc >>= 5
			bits -= 5
		}
	}
	out[0] = crockford[acc&31]
	return string(out[:])
}
