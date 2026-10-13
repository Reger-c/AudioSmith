package main

// Fakes of the engine side for the view tests: a scheduler that records
// submissions and a supervisor that records calls. The test plays the
// engine by publishing jobs.Updated and engine.StatusChanged on the app's
// bus (synchronously, so views see the result at once).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/jobs"
)

// sentReq is what the Studio asked of the engine side.
type sentReq struct {
	ID  string
	Cmd string // "synthesize", "load", "cancel", "unload", or another method
	proto.SynthesizeParams
	Spec jobs.Spec
}

type fakeEngine struct {
	host     *engineHost
	a        *app
	sent     []sentReq
	jobs      map[string]jobs.Job
	executors map[string]jobs.Executor
	restarts  int
	// What the settings told the supervisor.
	budget   int64
	idle     time.Duration
	cacheDir string
}

func newFakeEngine() *fakeEngine {
	f := &fakeEngine{jobs: map[string]jobs.Job{}, executors: map[string]jobs.Executor{}}
	f.host = &engineHost{bin: "/fake/audiosmith-engine", sup: &fakeSup{f}, sched: fakeSched{f}}
	return f
}

func (f *fakeEngine) last(t *testing.T) sentReq {
	t.Helper()
	if len(f.sent) == 0 {
		t.Fatal("nothing was sent to the engine")
	}
	return f.sent[len(f.sent)-1]
}

// ready publishes the gpu worker as ready.
func (f *fakeEngine) ready() {
	f.status(engine.WorkerStatus{Role: proto.RoleGPU, State: engine.WorkerReady, PID: 42,
		Hello: &proto.HelloResult{Protocol: 2, Version: "test", Role: proto.RoleGPU, Concurrency: 1}})
}

func (f *fakeEngine) status(st engine.WorkerStatus) {
	if st.Since.IsZero() {
		st.Since = time.Now()
	}
	f.a.bus.Publish(engine.StatusChanged{Status: st})
}

func (f *fakeEngine) update(id string, mod func(*jobs.Job)) {
	j, ok := f.jobs[id]
	if !ok {
		panic("unknown job " + id)
	}
	mod(&j)
	f.jobs[id] = j
	f.a.bus.Publish(jobs.Updated{Job: j})
}

// progress reports a running job's stage; fraction -1 is unknown.
func (f *fakeEngine) progress(id, stage string, fraction, audioSeconds float64) {
	f.update(id, func(j *jobs.Job) {
		j.State = jobs.StateRunning
		j.Progress = jobs.Progress{Stage: stage, Fraction: fraction, AudioSeconds: audioSeconds}
	})
}

func (f *fakeEngine) succeed(id string, result any) {
	raw, _ := json.Marshal(result)
	f.update(id, func(j *jobs.Job) { j.State, j.Result = jobs.StateSucceeded, raw })
}

func (f *fakeEngine) fail(id, code, message string) {
	f.update(id, func(j *jobs.Job) { j.State, j.Err = jobs.StateFailed, &jobs.Error{Code: code, Message: message} })
}

func (f *fakeEngine) cancelled(id string) {
	f.update(id, func(j *jobs.Job) { j.State, j.Err = jobs.StateCancelled, &jobs.Error{Code: jobs.CodeCancelled} })
}

type fakeSched struct{ f *fakeEngine }

// Register keeps the executors of Go-side kinds (downloads), which the
// fake runs at once, inside Submit.
func (s fakeSched) Register(kind string, ex jobs.Executor) {
	if kind == hub.KindDownload {
		s.f.executors[kind] = ex
	}
}
func (s fakeSched) Start(context.Context) error         { return nil }
func (s fakeSched) Idle() bool                          { return true }
func (s fakeSched) Close() error                        { return nil }
func (s fakeSched) Retry(context.Context, string) error { return nil }

func (s fakeSched) Submit(_ context.Context, spec jobs.Spec) (string, error) {
	id := fmt.Sprintf("job%d", len(s.f.jobs)+1)
	r := sentReq{ID: id, Spec: spec}
	switch spec.Kind {
	case proto.MethodTTSSynthesize:
		r.Cmd = "synthesize"
		json.Unmarshal(spec.Params, &r.SynthesizeParams)
	case hub.KindDownload:
		r.Cmd = "download"
		var p hub.DownloadParams
		json.Unmarshal(spec.Params, &p)
		r.Model = p.Repo
	case proto.MethodModelsLoad:
		r.Cmd = "load"
		var p proto.LoadParams
		json.Unmarshal(spec.Params, &p)
		r.Model = p.Model
	}
	s.f.sent = append(s.f.sent, r)
	s.f.jobs[id] = jobs.Job{ID: id, Spec: spec, State: jobs.StateQueued, Created: time.Now()}
	if ex := s.f.executors[spec.Kind]; ex != nil {
		s.f.run(id, ex)
	}
	return id, nil
}

// run executes a job at once, publishing its updates as the scheduler would.
func (f *fakeEngine) run(id string, ex jobs.Executor) {
	f.update(id, func(j *jobs.Job) { j.State, j.Attempt, j.Started = jobs.StateRunning, 1, time.Now() })
	res, err := ex.Run(context.Background(), f.jobs[id], func(p jobs.Progress) {
		f.update(id, func(j *jobs.Job) { j.Progress = p })
	}, nil)
	f.update(id, func(j *jobs.Job) {
		j.Finished = time.Now()
		var je *jobs.Error
		switch {
		case err == nil:
			j.State, j.Result = jobs.StateSucceeded, res
		case errors.As(err, &je):
			j.State, j.Err = jobs.StateFailed, je
		case errors.Is(err, context.Canceled):
			j.State, j.Err = jobs.StateCancelled, &jobs.Error{Code: jobs.CodeCancelled}
		default:
			j.State, j.Err = jobs.StateFailed, &jobs.Error{Code: "internal", Message: err.Error()}
		}
	})
}

func (s fakeSched) SubmitGraph(ctx context.Context, nodes []jobs.Node) (map[string]string, error) {
	ids := map[string]string{}
	for _, n := range nodes {
		ids[n.Key], _ = s.Submit(ctx, n.Spec)
	}
	return ids, nil
}

func (s fakeSched) Cancel(_ context.Context, id string) error {
	s.f.sent = append(s.f.sent, sentReq{ID: id, Cmd: "cancel"})
	return nil
}

func (s fakeSched) Get(_ context.Context, id string) (jobs.Job, error) {
	j, ok := s.f.jobs[id]
	if !ok {
		return j, jobs.ErrNotFound
	}
	return j, nil
}

func (s fakeSched) List(context.Context, jobs.Filter) ([]jobs.Job, error) { return nil, nil }

type fakeSup struct{ f *fakeEngine }

var _ engine.Supervisor = (*fakeSup)(nil)

func (s fakeSup) Start(context.Context) error   { return nil }
func (s fakeSup) Status() []engine.WorkerStatus { return nil }
func (s fakeSup) Stop()                         {}

func (s fakeSup) SetMemoryBudget(n int64)       { s.f.budget = n }
func (s fakeSup) SetIdleUnload(d time.Duration) { s.f.idle = d }
func (s fakeSup) SetCacheDir(dir string)        { s.f.cacheDir = dir }

func (s fakeSup) Restart(proto.Role) error {
	s.f.restarts++
	return nil
}

func (s fakeSup) Call(_ context.Context, _ proto.Role, id, method string, params any, _ func(proto.Message)) (json.RawMessage, error) {
	cmd := method
	switch method {
	case proto.MethodModelsUnload:
		cmd = "unload"
	}
	s.f.sent = append(s.f.sent, sentReq{ID: id, Cmd: cmd})
	if method == proto.MethodModelsUnload {
		return json.RawMessage(`{"unloaded":[]}`), nil
	}
	return json.RawMessage(`{}`), nil
}
