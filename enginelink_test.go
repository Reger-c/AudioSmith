package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/engine/enginetest"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/jobs"
)

// TestMain lets the test binary be a scripted engine (enginetest) for
// TestStudioOverTheSupervisor.
func TestMain(m *testing.M) {
	if enginetest.Enabled() {
		enginetest.Main()
	}
	os.Exit(m.Run())
}

func TestErrorsAndCancel(t *testing.T) {
	a, eng, tt := newTestApp(t)
	tt.Frame()
	tt.Click("Generate")
	req := eng.last(t)
	if req.Spec.Owner != takeOwner+a.job.takeID || req.Spec.Lane != jobs.LaneGPU || req.Spec.Priority != jobs.PriorityInteractive {
		t.Fatalf("job spec %+v", req.Spec)
	}
	eng.progress(req.ID, "loading", -1, 0)
	tt.Frame()
	if !tt.HasText("Loading model…") {
		t.Errorf("loading not shown: %q", tt.Texts())
	}

	eng.fail(req.ID, proto.CodeModelLoadFailed, "Unsupported model type: nil")
	tt.Frame()
	if a.job != nil || !tt.HasText("Unsupported model type: nil") {
		t.Fatalf("error not shown: %q", tt.Texts())
	}

	// Known codes are explained in the user's words.
	tt.Click("Generate")
	eng.fail(eng.last(t).ID, proto.CodeModelNotDownloaded, "Kokoro is not downloaded")
	tt.Frame()
	if !tt.HasText("The model is not downloaded. Download it on the Models page.") {
		t.Errorf("not downloaded: %q", tt.Texts())
	}

	tt.Click("Generate")
	id := eng.last(t).ID
	tt.Frame()
	if err := tt.Click("Cancel"); err != nil {
		t.Fatal(err)
	}
	if r := eng.last(t); r.Cmd != "cancel" || r.ID != id {
		t.Fatalf("Cancel sent %+v", r)
	}
	tt.Frame()
	if !tt.HasText("Cancelling…") {
		t.Errorf("cancelling not shown: %q", tt.Texts())
	}
	eng.progress(id, "generating", 0.5, 1) // late progress does not undo "Cancelling…"
	if a.job.status != i18n.T("Cancelling…") {
		t.Errorf("status %q", a.job.status)
	}
	eng.cancelled(id)
	if a.job != nil {
		t.Fatal("job survived cancellation")
	}
	// Updates of other owners' jobs are not the Studio's.
	tt.Frame()
	if err := tt.Click("Generate"); err != nil || a.job == nil {
		t.Fatalf("generate: %v", err)
	}
	other, _ := eng.host.sched.Submit(context.Background(), jobs.Spec{Kind: proto.MethodTTSSynthesize, Lane: jobs.LaneGPU, Owner: "take:x"})
	eng.cancelled(other)
	if a.job == nil {
		t.Error("another job's update ended the Studio's")
	}
}

func TestEngineCrash(t *testing.T) {
	a, eng, tt := newTestApp(t)
	tt.Frame()
	tt.Click("Generate")
	id := eng.last(t).ID
	eng.progress(id, "generating", -1, 0)
	eng.status(engine.WorkerStatus{Role: proto.RoleGPU, State: engine.WorkerFailed, Reason: engine.FailCrashed,
		Err: "the engine exited: signal: abort trap", StderrTail: "fatal: Failed to load the default metallib",
		RestartAt: time.Now().Add(time.Second)})
	eng.update(id, func(j *jobs.Job) {
		j.State, j.Err = jobs.StateInterrupted, &jobs.Error{Code: jobs.CodeInterrupted, Message: "engine worker exited"}
	})
	a.showPage("engine")
	tt.Frame()
	if a.job != nil || a.engState != engineFailed {
		t.Fatalf("state %v job %v", a.engState, a.job)
	}
	if !tt.HasText("Engine stopped") || !tt.HasText("fatal: Failed to load the default metallib") ||
		!tt.HasText("The engine stopped: the engine exited: signal: abort trap Restarting…") {
		t.Errorf("crash not shown: %q", tt.Texts())
	}
	if a.jobError != i18n.T("The engine stopped while it worked. Try again.") {
		t.Errorf("job error %q", a.jobError)
	}
	if err := tt.Click("Restart engine"); err != nil {
		t.Fatal(err)
	}
	if eng.restarts != 1 || a.engState != engineStarting {
		t.Errorf("restarts %d state %v", eng.restarts, a.engState)
	}
}

func TestWorkerFailures(t *testing.T) {
	a, eng, tt := newTestApp(t)
	for reason, want := range map[engine.FailReason]string{
		engine.FailMissing:   "audiosmith-engine was not found",
		engine.FailProtocol:  "Engine version mismatch: rebuild audiosmith-engine. (it speaks protocol 1)",
		engine.FailHandshake: "The engine did not answer in time.",
		engine.FailStuck:     "A cancelled job did not stop",
	} {
		eng.status(engine.WorkerStatus{Role: proto.RoleGPU, State: engine.WorkerFailed, Reason: reason, Err: "it speaks protocol 1"})
		if !strings.Contains(a.engError, want) || a.engineSummary().State != engineFailed {
			t.Errorf("%s: %q", reason, a.engError)
		}
	}
	a.showPage("engine")
	tt.Frame()
	if !tt.HasText("Speech engine") {
		t.Errorf("engine page: %q", tt.Texts())
	}
}

func TestPreloadUnloadAndStats(t *testing.T) {
	a, eng, tt := newTestApp(t)
	tt.Frame()
	if err := tt.Click("Preload"); err != nil {
		t.Fatal(err)
	}
	req := eng.last(t)
	if req.Cmd != "load" || req.Model != kokoro {
		t.Fatalf("Preload sent %+v", req)
	}
	eng.succeed(req.ID, proto.LoadResult{Model: kokoro, Task: "tts", SampleRate: 24000, Seconds: 1.5})
	tt.Frame()
	if a.job != nil || !tt.HasText("Loaded") || a.loadedModel != kokoro {
		t.Fatalf("after load: job %v texts %q", a.job, tt.Texts())
	}

	// The worker's stats list what is resident, most recent first.
	other := "mlx-community/Soprano-80M-bf16"
	eng.status(engine.WorkerStatus{Role: proto.RoleGPU, State: engine.WorkerReady, PID: 42,
		Hello: &proto.HelloResult{Version: "0.2.0", Device: proto.Device{Name: "Apple M2", Memory: 24 << 30, OS: "macOS 26.7"}},
		Stats: &proto.StatsResult{Memory: proto.Memory{Active: 500 << 20, Peak: 900 << 20},
			Models: []proto.LoadedModel{{Model: kokoro, Bytes: 390 << 20}, {Model: other, Bytes: 160 << 20}}},
		Running: []string{"01JOB"}})
	if s := a.engineSummary(); len(s.Loaded) != 2 || s.Loaded[0] != kokoro || s.Version != "0.2.0" {
		t.Errorf("summary %+v", s)
	}
	a.showPage("engine")
	tt.Frame()
	for _, want := range []string{"Apple M2 · " + catalog.HumanSize(24<<30) + " · macOS 26.7", "01JOB",
		catalog.HumanSize(500<<20) + " in use · " + catalog.HumanSize(900<<20) + " at most"} {
		if !tt.HasText(want) {
			t.Errorf("engine page lacks %q: %q", want, tt.Texts())
		}
	}

	a.showPage("studio")
	tt.Frame()
	tt.Click("Unload")
	if eng.last(t).Cmd != "unload" {
		t.Fatalf("Unload sent %+v", eng.last(t))
	}
	if a.loadedModel != "" || len(a.engineSummary().Loaded) != 0 {
		t.Error("model still loaded")
	}
}

// TestStudioOverTheSupervisor runs the Studio against the real scheduler
// (on the library's SQLite jobs table) and supervisor, with the test
// binary as a scripted engine process.
func TestStudioOverTheSupervisor(t *testing.T) {
	lib := openTestLibrary(t)
	useLocalModels(t, lib)
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	h := &engineHost{bin: bin, logDir: t.TempDir()}
	a := newApp(h, lib)
	a.autoplay = false
	events := make(chan func(), 4096)
	a.enginePost = func(fn func()) { events <- fn }
	a.postQueue = a.enginePost
	h.sup = engine.NewSupervisor(engine.Config{Bin: bin, Bus: a.bus, LogDir: h.logDir, Env: enginetest.Env(""),
		Log: func(e core.LogEntry) { a.logs.Append(e) }, Logf: t.Logf})
	a.connectEngine(h)
	defer h.Stop()
	waitFor := func(what string, done func() bool) {
		t.Helper()
		deadline := time.After(10 * time.Second)
		for !done() {
			select {
			case fn := <-events:
				fn()
			case <-deadline:
				t.Fatalf("timed out waiting for %s (job %+v, error %q, engine %q)", what, a.job, a.jobError, a.engError)
			}
		}
	}

	a.startEngine()
	waitFor("ready", func() bool { return a.engState == engineReady })
	if a.engVersion != "fake" {
		t.Errorf("version %q", a.engVersion)
	}
	a.text = "Over the real scheduler."
	a.generate()
	waitFor("take", func() bool { return a.job == nil })
	if a.jobError != "" {
		t.Fatal(a.jobError)
	}
	c := a.lastClip()
	if c == nil || c.SampleRate != 24000 || c.JobID == "" || c.Seed != 0 {
		t.Fatalf("take %+v", c)
	}
	if _, err := os.Stat(filepath.Join(a.takeStore.AudioDir(), c.File)); err != nil {
		t.Errorf("audio: %v", err)
	}
	j, err := h.sched.Get(context.Background(), c.JobID)
	if err != nil || j.State != jobs.StateSucceeded || j.Owner != takeOwner+c.ID {
		t.Errorf("job %+v %v", j, err)
	}
	waitFor("stats", func() bool { return len(a.loaded) == 1 })

	// Cancel a long job by id.
	a.text = "slow"
	a.generate()
	waitFor("running", func() bool { return a.job.id != "" && a.job.status == i18n.T("Generating…") })
	a.cancel()
	waitFor("cancelled", func() bool { return a.job == nil })
	if a.jobError != "" {
		t.Errorf("cancel: %q", a.jobError)
	}

	// The engine's stderr and log lines reach the logs.
	found := false
	for _, e := range a.logs.Tail(1000, "engine:gpu") {
		found = found || (e.Source == "engine:gpu" && e.Message == "fake engine ready")
	}
	if !found {
		t.Error("engine log lines missing from the logs")
	}
}
