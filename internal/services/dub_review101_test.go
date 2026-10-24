package services

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/translate"
)

// #101 H2: a stage that ends interrupted (the engine died twice) does not
// leave the stages after it waiting: they fail, the project is not busy,
// it shows as interrupted, and Resume runs it to the end.
func TestDubInterruptedStageDoesNotStick(t *testing.T) {
	e := newDubEnv(t, 3)
	p := e.create(t, func(p *dub.Project) { p.Settings.KeepBackground = false })
	e.eng.Fail[proto.MethodSTTTranscribe] = jobs.CodeInterrupted
	ids, err := e.dubs.Run(ctx, p.ID, RunRequest{Through: dub.StageMix})
	if err != nil {
		t.Fatal(err)
	}
	if j := e.wait(t, ids["transcribe"]); j.State != jobs.StateInterrupted {
		t.Fatalf("transcribe %s", j.State)
	}
	for k, id := range ids {
		if k == "transcribe" || k == "ingest" {
			continue
		}
		if j := e.wait(t, id); j.State != jobs.StateFailed || j.Err == nil || j.Err.Code != jobs.CodeDependency {
			t.Errorf("%s: %s %+v", k, j.State, j.Err)
		}
	}
	active, _ := e.dubs.Active(ctx, p.ID)
	if len(active) != 0 {
		t.Fatalf("still active: %d jobs", len(active))
	}
	p, _ = e.dubs.Get(ctx, p.ID)
	if !Interrupted(p, active) {
		t.Fatal("the project does not show as interrupted")
	}
	delete(e.eng.Fail, proto.MethodSTTTranscribe)
	ids, err = e.dubs.Resume(ctx, p.ID)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	for _, id := range ids {
		if j := e.wait(t, id); j.State != jobs.StateSucceeded {
			t.Errorf("%s: %s %+v", j.Title, j.State, j.Err)
		}
	}
	if p, _ = e.dubs.Get(ctx, p.ID); p.Tracks["es"].File == "" || p.Run != nil {
		t.Errorf("after Resume: track %+v run %+v", p.Tracks["es"], p.Run)
	}
}

// #101 M4: runs started at once (the UI and the API) make one pipeline;
// the others are refused as busy.
func TestDubRunIsAtomic(t *testing.T) {
	e := newDubEnv(t, 3)
	p := e.create(t, nil)
	block := make(chan struct{})
	e.dubs.cfg.Translator = func(ctx context.Context, _ dub.Settings) (translate.Translator, error) {
		select {
		case <-block:
		case <-ctx.Done():
		}
		return nil, ctx.Err()
	}
	defer close(block)
	var ok, busy atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			_, err := e.dubs.Run(ctx, p.ID, RunRequest{Through: dub.StageMix})
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, core.ErrBusy):
				busy.Add(1)
			default:
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if ok.Load() != 1 || busy.Load() != 7 {
		t.Fatalf("%d runs started, %d refused", ok.Load(), busy.Load())
	}
	e.dubs.Cancel(ctx, p.ID)
}

// submitSpy calls check before each graph is submitted.
type submitSpy struct {
	jobs.Scheduler
	check func()
}

func (s submitSpy) SubmitGraph(ctx context.Context, nodes []jobs.Node) (map[string]string, error) {
	s.check()
	return s.Scheduler.SubmitGraph(ctx, nodes)
}

// #101 L12: the run is recorded before its jobs can start, so a run whose
// stages all end at once still ends (Run cleared); a refused submission
// leaves the project as it was.
func TestDubRunRecordedBeforeJobs(t *testing.T) {
	e := newDubEnv(t, 3)
	p := e.create(t, func(p *dub.Project) { p.Settings.KeepBackground = false })
	var seen *dub.Run
	e.dubs.cfg.Scheduler = submitSpy{Scheduler: e.sched, check: func() {
		q, _ := e.dubs.Get(ctx, p.ID)
		seen = q.Run
	}}
	ids, err := e.dubs.Run(ctx, p.ID, RunRequest{Through: dub.StageReferences})
	if err != nil {
		t.Fatal(err)
	}
	if seen == nil || seen.Through != dub.StageReferences {
		t.Fatalf("run at submission %+v", seen)
	}
	for _, id := range ids {
		e.wait(t, id)
	}
	if p, _ = e.dubs.Get(ctx, p.ID); p.Run != nil {
		t.Errorf("run left set: %+v", p.Run)
	}

	e.sched.Close() // submissions fail now
	if _, err := e.dubs.Run(ctx, p.ID, RunRequest{Through: dub.StageMix}); err == nil {
		t.Fatal("ran on a closed scheduler")
	}
	if p, _ = e.dubs.Get(ctx, p.ID); p.Run != nil {
		t.Errorf("a refused run was recorded: %+v", p.Run)
	}
}

// #101 M3: deleting a dub waits for its running stage to stop before its
// folder goes; a stage that does not stop makes it busy, and keeps it.
func TestDubDeleteWaitsForStage(t *testing.T) {
	e := newDubEnv(t, 3)
	p := e.create(t, nil)
	var stopped atomic.Bool
	release := make(chan struct{})
	ignore := atomic.Bool{}
	e.dubs.cfg.Translator = func(ctx context.Context, _ dub.Settings) (translate.Translator, error) {
		if ignore.Load() {
			<-release
		} else {
			<-ctx.Done()
			time.Sleep(150 * time.Millisecond) // winding down
		}
		stopped.Store(true)
		return nil, context.Canceled
	}
	running := func(ids map[string]string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if j, _ := e.sched.Get(ctx, ids["translate:es"]); j.State == jobs.StateRunning {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("translation did not start")
	}

	// A stage that does not stop: busy, nothing removed.
	ignore.Store(true)
	e.dubs.deleteWait = 200 * time.Millisecond
	ids, err := e.dubs.Run(ctx, p.ID, RunRequest{Through: dub.StageMix})
	if err != nil {
		t.Fatal(err)
	}
	running(ids)
	if err := e.dubs.Delete(ctx, p.ID); !errors.Is(err, core.ErrBusy) {
		t.Fatalf("delete under a stuck stage: %v", err)
	}
	if _, err := os.Stat(e.dubs.Dir(p.ID)); err != nil {
		t.Errorf("the folder went: %v", err)
	}
	close(release)
	e.wait(t, ids["translate:es"])

	// A stage that stops: Delete returns after it did.
	ignore.Store(false)
	stopped.Store(false)
	e.dubs.deleteWait = 0
	ids, err = e.dubs.Run(ctx, p.ID, RunRequest{Through: dub.StageMix})
	if err != nil {
		t.Fatal(err)
	}
	running(ids)
	if err := e.dubs.Delete(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if !stopped.Load() {
		t.Error("deleted while the stage still ran")
	}
	if _, err := os.Stat(e.dubs.Dir(p.ID)); !os.IsNotExist(err) {
		t.Errorf("folder: %v", err)
	}
}
