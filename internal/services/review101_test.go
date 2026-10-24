package services

import (
	"context"
	"errors"
	"fmt"
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
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	ignore := atomic.Bool{}
	e.dubs.cfg.Translator = func(ctx context.Context, _ dub.Settings) (translate.Translator, error) {
		if ignore.Load() {
			entered <- struct{}{}
			<-release
		} else {
			<-ctx.Done()
			time.Sleep(150 * time.Millisecond) // winding down
		}
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
	// The stage must be inside the call that ignores cancel: a job shown
	// running may not have reached it yet, and would stop on the cancel.
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the translator was not called")
	}
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
	e.dubs.deleteWait = 0
	ids, err = e.dubs.Run(ctx, p.ID, RunRequest{Through: dub.StageMix})
	if err != nil {
		t.Fatal(err)
	}
	running(ids)
	if err := e.dubs.Delete(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	// (The stage may end before it asks for the translator: its job's
	// end is what Delete waits for.)
	if j, _ := e.sched.Get(ctx, ids["translate:es"]); j.State == jobs.StateRunning {
		t.Error("deleted while the stage still ran")
	}
	if _, err := os.Stat(e.dubs.Dir(p.ID)); !os.IsNotExist(err) {
		t.Errorf("folder: %v", err)
	}
}

// #101 M7: read-modify-writes of a document never lose each other's
// change (UpdateDoc), and a save based on an older read is refused
// (SaveDocIf with BodyIs).
func TestLibraryDocConflicts(t *testing.T) {
	e := newLongformEnv(t, t.TempDir(), false)
	defer e.close()
	lib := e.svc.Library
	d := NewLongformDoc(core.ProjectStory, "m/kokoro")
	d.Title = "The Lighthouse"
	p, err := lib.SaveDoc(ctx, core.Project{}, d)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			if _, _, err := lib.UpdateDoc(ctx, p.ID, func(_ *core.Project, d *LongformDoc) error {
				d.Lines = append(d.Lines, StoryLine{ID: fmt.Sprint(i), Text: "line"})
				return nil
			}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	read, got, _ := lib.LoadDoc(ctx, p.ID)
	if len(got.Lines) != 20 {
		t.Fatalf("%d lines kept of 20", len(got.Lines))
	}
	if _, err := lib.SaveDocIf(ctx, read, got, BodyIs(read)); err != nil {
		t.Fatalf("a save on the latest read: %v", err)
	}
	lib.Rename(ctx, p.ID, "Night Light") // saved elsewhere meanwhile
	got.Speed = 1.2
	if _, err := lib.SaveDocIf(ctx, read, got, BodyIs(read)); !errors.Is(err, ErrChanged) || !errors.Is(err, core.ErrBusy) {
		t.Fatalf("a save on an old read: %v", err)
	}
	if cur, _, _ := lib.LoadDoc(ctx, p.ID); cur.Title != "Night Light" {
		t.Errorf("title %q", cur.Title)
	}
}

// flakyDocs fails Put while fail is set.
type flakyDocs struct {
	mu   sync.Mutex
	fail bool
	docs map[string][]byte
}

func (f *flakyDocs) Get(_ context.Context, key string) ([]byte, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.docs[key]
	return d, ok, nil
}

func (f *flakyDocs) Put(_ context.Context, key string, doc []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("disk full")
	}
	f.docs[key] = doc
	return nil
}

func (f *flakyDocs) Delete(_ context.Context, key string) error { return nil }

// #101 L9: a draft whose write failed is not dropped: it stays pending,
// the next Flush (or the retry after the delay) writes it.
func TestDraftWriteFailureKeepsIt(t *testing.T) {
	docs := &flakyDocs{fail: true, docs: map[string][]byte{}}
	d := &Drafts{docs: docs, bus: core.NewBus(), delay: 30 * time.Millisecond, logf: t.Logf,
		pending: map[string]*draft{}, saved: map[string][]byte{}}
	d.Save("k", "edit")
	if err := d.Flush(ctx); err == nil {
		t.Fatal("the failed write was not reported")
	}
	var got string
	if ok, _ := d.Load(ctx, "k", &got); !ok || got != "edit" {
		t.Fatalf("lost after a failed write: %q %v", got, ok)
	}
	docs.mu.Lock()
	docs.fail = false
	docs.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, ok, _ := docs.Get(ctx, DraftPrefix+"k"); ok && string(b) == `"edit"` {
			return // the retry wrote it
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("never written after the store recovered")
}
