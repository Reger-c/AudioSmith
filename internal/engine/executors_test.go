package engine

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

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
