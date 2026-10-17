package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
)

// The scheduler runs engine jobs through the supervisor and the fake engine.
func TestExecutorsOverTheSupervisor(t *testing.T) {
	sup, w := newTestSup(t, "")
	w.until(t, "ready", ready)

	var mu sync.Mutex
	var updates []jobs.Updated
	sched := jobs.New(jobs.Options{Store: jobs.NewMemStore(), ProgressEvery: time.Nanosecond,
		RetryDelay: func(int) time.Duration { return 10 * time.Millisecond }, Logf: t.Logf,
		Publish: func(u jobs.Updated) { mu.Lock(); updates = append(updates, u); mu.Unlock() }})
	defer sched.Close()
	RegisterExecutors(sched, sup)
	ctx := context.Background()
	sched.Start(ctx)

	wait := func(id string, st jobs.State) jobs.Job {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			j, err := sched.Get(ctx, id)
			if err == nil && j.State == st {
				sched.Flush()
				return j
			}
			if time.Now().After(deadline) {
				t.Fatalf("job %s: %+v, want %s", id, j, st)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	submit := func(method string, params any, mod func(*jobs.Spec)) string {
		t.Helper()
		raw, _ := json.Marshal(params)
		sp := jobs.Spec{Kind: method, Lane: jobs.LaneGPU, Params: raw}
		if mod != nil {
			mod(&sp)
		}
		id, err := sched.Submit(ctx, sp)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	// Load, then a streamed synthesis: progress and chunks reach the bus.
	load := submit(proto.MethodModelsLoad, proto.LoadParams{Model: "m/one"}, nil)
	tts := submit(proto.MethodTTSSynthesize, synth("hi", true), nil)
	j := wait(load, jobs.StateSucceeded)
	var lr proto.LoadResult
	if json.Unmarshal(j.Result, &lr); lr.Model != "m/one" {
		t.Errorf("load result %s", j.Result)
	}
	j = wait(tts, jobs.StateSucceeded)
	var sr proto.SynthesizeResult
	if json.Unmarshal(j.Result, &sr); sr.SampleRate != 24000 || j.Progress.Fraction != 1 {
		t.Errorf("tts %+v result %s", j.Progress, j.Result)
	}
	mu.Lock()
	var chunks []int
	var fractions []float64
	for _, u := range updates {
		if u.Job.ID != tts {
			continue
		}
		if u.Chunk != nil {
			var c proto.ChunkParams
			json.Unmarshal(u.Chunk, &c)
			chunks = append(chunks, c.Seq)
		} else if u.Job.State == jobs.StateRunning {
			fractions = append(fractions, u.Job.Progress.Fraction)
		}
	}
	mu.Unlock()
	if len(chunks) != 3 || chunks[2] != 2 {
		t.Errorf("chunks %v", chunks)
	}
	if len(fractions) < 2 || fractions[0] != -1 {
		t.Errorf("fractions %v", fractions)
	}

	// Typed errors keep their code; oom is retried.
	broken := submit(proto.MethodModelsLoad, proto.LoadParams{Model: "x/broken"}, nil)
	if j := wait(broken, jobs.StateFailed); j.Err.Code != proto.CodeModelLoadFailed || j.Err.Retryable {
		t.Errorf("broken %+v", j.Err)
	}
	oom := submit(proto.MethodTTSSynthesize, synth("oom", false), func(s *jobs.Spec) { s.MaxAttempts = 2 })
	if j := wait(oom, jobs.StateFailed); j.Err.Code != proto.CodeOOM || j.Attempt != 2 {
		t.Errorf("oom %+v attempt %d", j.Err, j.Attempt)
	}

	// Cancel by id.
	slow := submit(proto.MethodTTSSynthesize, synth("slow", false), nil)
	wait(slow, jobs.StateRunning)
	time.Sleep(30 * time.Millisecond)
	sched.Cancel(ctx, slow)
	if j := wait(slow, jobs.StateCancelled); j.Err.Code != jobs.CodeCancelled {
		t.Errorf("cancel %+v", j.Err)
	}

	// The worker crashing interrupts the job; the next one waits for the
	// restarted worker.
	crash := submit(proto.MethodTTSSynthesize, synth("crash", false), nil)
	if j := wait(crash, jobs.StateInterrupted); j.Err.Code != jobs.CodeInterrupted {
		t.Errorf("crash %+v", j.Err)
	}
	after := submit(proto.MethodTTSSynthesize, synth("after", false), nil)
	wait(after, jobs.StateSucceeded)
}

// recordSup answers every call with {} and records its params.
type recordSup struct {
	Supervisor
	mu     sync.Mutex
	params []json.RawMessage
}

func (r *recordSup) Call(_ context.Context, _ proto.Role, _, _ string, params any, _ func(proto.Message)) (json.RawMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.params = append(r.params, params.(json.RawMessage))
	return json.RawMessage(`{}`), nil
}

type gate map[string]bool

func (g gate) Check(_ context.Context, model string) error {
	if g[model] {
		return &core.LicenceRequiredError{Model: model, Name: "CC BY-NC 4.0"}
	}
	return nil
}
func (gate) Accept(context.Context, core.LicenceAcceptance) error   { return nil }
func (gate) Revoke(context.Context, string, string) error           { return nil }
func (gate) List(context.Context) ([]core.LicenceAcceptance, error) { return nil, nil }

// Jobs load models by the path of their local snapshot, and refuse models
// that are missing or whose licence is not accepted.
func TestExecutorsFillModelPaths(t *testing.T) {
	sup := &recordSup{}
	sched := jobs.New(jobs.Options{Store: jobs.NewMemStore(), Logf: t.Logf})
	defer sched.Close()
	RegisterExecutors(sched, sup, ExecutorOptions{
		Licences: gate{"nc/model": true},
		ModelPath: func(_ context.Context, model string) (string, error) {
			if model == "missing/model" {
				return "", fmt.Errorf("%s: %w", model, core.ErrNotDownloaded)
			}
			return "/cache/" + model, nil
		},
		AuxPaths: func(_ context.Context, model string) map[string]string {
			if model == "mlx-community/Kokoro-82M-bf16" {
				return map[string]string{"beshkenadze/kitten-tts-g2p": "/cache/g2p"}
			}
			return nil
		},
	})
	ctx := context.Background()
	sched.Start(ctx)
	run := func(method string, params any) jobs.Job {
		t.Helper()
		raw, _ := json.Marshal(params)
		id, err := sched.Submit(ctx, jobs.Spec{Kind: method, Lane: jobs.LaneGPU, Params: raw})
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			j, _ := sched.Get(ctx, id)
			if j.State.Terminal() {
				return j
			}
			if time.Now().After(deadline) {
				t.Fatalf("job %+v", j)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	if j := run(proto.MethodTTSSynthesize, synth("hi", false)); j.State != jobs.StateSucceeded {
		t.Fatalf("synthesize: %+v", j)
	}
	if j := run(proto.MethodModelsLoad, proto.LoadParams{Model: "m/one"}); j.State != jobs.StateSucceeded {
		t.Fatalf("load: %+v", j)
	}
	var sp proto.SynthesizeParams
	var lp proto.LoadParams
	json.Unmarshal(sup.params[0], &sp)
	json.Unmarshal(sup.params[1], &lp)
	if sp.ModelPath != "/cache/mlx-community/Kokoro-82M-bf16" || sp.Text != "hi" || lp.Path != "/cache/m/one" {
		t.Errorf("params %s %s", sup.params[0], sup.params[1])
	}
	// Kokoro's auxiliary repo goes with it; a model without one gets none.
	var aux struct {
		Aux map[string]string `json:"auxPaths"`
	}
	json.Unmarshal(sup.params[0], &aux)
	if aux.Aux["beshkenadze/kitten-tts-g2p"] != "/cache/g2p" || strings.Contains(string(sup.params[1]), proto.ParamAuxPaths) {
		t.Errorf("auxPaths %s / %s", sup.params[0], sup.params[1])
	}

	j := run(proto.MethodTTSSynthesize, proto.SynthesizeParams{Model: "missing/model", Text: "x"})
	if j.State != jobs.StateFailed || j.Err.Code != proto.CodeModelNotDownloaded {
		t.Errorf("missing model: %+v %+v", j, j.Err)
	}
	j = run(proto.MethodModelsLoad, proto.LoadParams{Model: "nc/model"})
	if j.State != jobs.StateFailed || j.Err.Code != proto.CodeLicenceRequired {
		t.Errorf("licence: %+v %+v", j, j.Err)
	}
	if len(sup.params) != 2 {
		t.Errorf("refused jobs reached the engine: %d calls", len(sup.params))
	}
}
