package services

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/dub/dubtest"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/store/sqlitestore"
	"github.com/hehehai/AudioSmith/internal/translate"
	"github.com/hehehai/AudioSmith/internal/workflow"
)

type wfEnv struct {
	dir   string
	bus   *core.Bus
	sched *jobs.Sched
	svc   *Services
	wf    *Workflows
	eng   *dubtest.Engine
	tr    *fakeTranslator
	out   string
	clip  string
	// gate, when set, holds each tts.synthesize call until it is closed.
	gate    chan struct{}
	started chan struct{}
	missing string // a model "not downloaded"
	mu      sync.Mutex
	st      *sqlitestore.Store
	ms      *jobs.MemStore
	cfg     WorkflowsConfig
}

func newWorkflowEnv(t *testing.T) *wfEnv {
	t.Helper()
	dir := t.TempDir()
	st, err := sqlitestore.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	e := &wfEnv{dir: dir, bus: core.NewBus(), eng: dubtest.New(), tr: &fakeTranslator{fail: map[string]bool{}}, out: t.TempDir()}
	e.eng.Script = []string{"Hello there.", "How are you today?"}
	e.st, e.ms = st, jobs.NewMemStore()
	e.sched = jobs.New(jobs.Options{Store: e.ms, Publish: func(u jobs.Updated) { e.bus.Publish(u) },
		RetryDelay: func(int) time.Duration { return time.Millisecond }})
	modelPath := func(_ context.Context, m string) (string, error) {
		if m == e.missing {
			return "", core.ErrNotDownloaded
		}
		return "/models/" + m, nil
	}
	synth := e.eng.Executor(proto.MethodTTSSynthesize)
	gated := jobs.ExecutorFunc(func(c context.Context, j jobs.Job, report func(jobs.Progress), chunk func(json.RawMessage)) (json.RawMessage, error) {
		e.mu.Lock()
		gate, started := e.gate, e.started
		e.mu.Unlock()
		if gate != nil {
			select {
			case started <- struct{}{}:
			default:
			}
			select {
			case <-gate:
			case <-c.Done():
				return nil, c.Err()
			}
		}
		return synth.Run(c, j, report, chunk)
	})
	e.svc = New(Config{Store: st, Bus: e.bus, Scheduler: e.sched, CacheDir: filepath.Join(dir, "cache"),
		Synth:     func() jobs.Executor { return gated },
		ModelPath: modelPath,
		Models: func(string) ModelInfo {
			return ModelInfo{Traits: TraitsFor("qwen3_tts", []string{"speed", "clone"}), Revision: "r1"}
		},
	})
	e.cfg = WorkflowsConfig{
		Store: st.Workflows(), Bus: e.bus, Scheduler: e.sched, Render: e.svc.Render,
		Engine:    func(m string) jobs.Executor { return e.eng.Executor(m) },
		ModelPath: modelPath,
		STT:       func() WorkflowSTT { return WorkflowSTT{Model: "stt", Words: true} },
		Caps:      func(string) DubModelCaps { return DubModelCaps{Clone: true, Speed: true} },
		Voice: func(_ context.Context, ref, model, lang string) (dub.VoiceSpec, error) {
			if ref == "gone" {
				return dub.VoiceSpec{}, core.ErrNotFound
			}
			return dub.VoiceSpec{Voice: "speaker-" + ref}, nil
		},
		Translator: func(context.Context, string) (translate.Translator, error) { return e.tr, nil },
	}
	e.wf = NewWorkflows(e.cfg)
	if err := e.sched.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		e.sched.Close()
		e.svc.Close(ctx)
		st.Close()
	})
	e.clip = filepath.Join(dir, "interview.wav")
	if _, err := dubtest.Speech(e.clip, 2); err != nil {
		t.Fatal(err)
	}
	return e
}

// restart quits the job queue (running jobs are interrupted) and opens
// a new one over the same jobs, as AudioSmith does at its next start.
func (e *wfEnv) restart(t *testing.T) {
	t.Helper()
	e.sched.Close()
	e.sched = jobs.New(jobs.Options{Store: e.ms, Publish: func(u jobs.Updated) { e.bus.Publish(u) },
		RetryDelay: func(int) time.Duration { return time.Millisecond }})
	cfg := e.cfg
	cfg.Scheduler = e.sched
	e.wf = NewWorkflows(cfg)
	if err := e.sched.Start(ctx); err != nil {
		t.Fatal(err)
	}
}

func (e *wfEnv) save(t *testing.T, w *workflow.Workflow) *workflow.Workflow {
	t.Helper()
	w, err := e.wf.Save(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// wait waits for the workflow's run to end.
func (e *wfEnv) wait(t *testing.T, id string) *workflow.Run {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		active, _ := e.wf.Active(ctx, id)
		_, run, err := e.wf.Get(ctx, id)
		if err == nil && len(active) == 0 && run != nil && run.State != workflow.RunRunning {
			return run
		}
		time.Sleep(2 * time.Millisecond)
	}
	_, run, _ := e.wf.Get(ctx, id)
	t.Fatalf("run did not end: %+v", run)
	return nil
}

func (e *wfEnv) run(t *testing.T, id string, o RunOptions) *workflow.Run {
	t.Helper()
	if _, err := e.wf.Run(ctx, id, o); err != nil {
		t.Fatal(err)
	}
	return e.wait(t, id)
}

func narration(e *wfEnv, scripts ...string) *workflow.Workflow {
	tp, _ := workflow.TemplateByID("narration-normalize")
	w := tp.Make("", "Lessons", time.Now(), workflow.TemplateOptions{Model: "tts", Voice: "v1"})
	w.Normalize()
	for i := range w.Nodes {
		switch w.Nodes[i].Kind {
		case workflow.KindStart:
			w.Nodes[i].P.Scripts = nil
			for _, s := range scripts {
				w.Nodes[i].P.Scripts = append(w.Nodes[i].P.Scripts, workflow.Script{Text: s})
			}
		case workflow.KindExport:
			w.Nodes[i].P.Folder = e.out
		}
	}
	return w
}

func nodeOf(w *workflow.Workflow, k workflow.Kind) *workflow.Node {
	for i := range w.Nodes {
		if w.Nodes[i].Kind == k {
			return &w.Nodes[i]
		}
	}
	return nil
}

func TestWorkflowNarration(t *testing.T) {
	e := newWorkflowEnv(t)
	w := e.save(t, narration(e, "Welcome to the course.\n---\nLesson one: sound.", "Lesson two: light."))
	run := e.run(t, w.ID, RunOptions{})
	done, failed, total := run.Counts()
	if run.State != workflow.RunDone || done != 3 || failed != 0 || total != 3 {
		t.Fatalf("run %s: %d/%d failed %d (%s %s)", run.State, done, total, failed, run.Code, run.Err)
	}
	files, _ := filepath.Glob(filepath.Join(e.out, "*.wav"))
	if len(files) != 3 || !strings.Contains(filepath.Base(files[0]), "Lessons-01-Welcome to the course.") {
		t.Fatalf("exports %v", files)
	}
	b, _, err := wav.ReadFile(files[0])
	if err != nil || b.Seconds() < 0.5 {
		t.Fatalf("export %v %v", b.Seconds(), err)
	}
	// The voice and speed reached the render.
	var sp proto.SynthesizeParams
	json.Unmarshal(e.eng.Last(proto.MethodTTSSynthesize), &sp)
	if sp.Voice != "speaker-v1" {
		t.Errorf("voice %q", sp.Voice)
	}
	outs := Outputs(w, run)
	if len(outs) != 3 || outs[0].Value.Type != workflow.TypeAudio || !filepath.IsAbs(outs[0].Value.File) {
		t.Errorf("outputs %+v", outs)
	}
	calls := e.eng.Count(proto.MethodTTSSynthesize)

	// Nothing changed: nothing to do.
	run2, err := e.wf.Run(ctx, w.ID, RunOptions{})
	if err != nil || run2.State != workflow.RunDone || len(run2.Steps) != 0 {
		t.Fatalf("again: %+v %v", run2, err)
	}
	// One script edited: one item made again.
	nodeOf(w, workflow.KindStart).P.Scripts[1].Text = "Lesson two: colour."
	w = e.save(t, w)
	run3 := e.run(t, w.ID, RunOptions{})
	if run3.State != workflow.RunDone || e.eng.Count(proto.MethodTTSSynthesize) != calls+1 {
		t.Errorf("edited: %s, %d calls (was %d)", run3.State, e.eng.Count(proto.MethodTTSSynthesize), calls)
	}
	// The normalize level: every item is normalized again, none spoken again.
	nodeOf(w, workflow.KindNormalize).P.Target = -20
	w = e.save(t, w)
	run4 := e.run(t, w.ID, RunOptions{})
	if run4.State != workflow.RunDone || e.eng.Count(proto.MethodTTSSynthesize) != calls+1 || len(run4.Steps) != 2 {
		t.Errorf("level: %s, steps %v", run4.State, run4.Steps)
	}
	// Start a new run: all again (the chunk cache answers the engine).
	run5 := e.run(t, w.ID, RunOptions{Again: true})
	if run5.State != workflow.RunDone || len(run5.Steps) != 3 {
		t.Errorf("again: %+v", run5.Steps)
	}
	// Export all.
	all := t.TempDir()
	written, err := e.wf.ExportAll(ctx, w.ID, all, "")
	if err != nil || len(written) != 3 {
		t.Fatalf("export all %v %v", written, err)
	}
	if recs, _ := e.wf.List(ctx); len(recs) != 1 || recs[0].State != "done" || recs[0].Done != 3 {
		t.Errorf("list %+v", recs)
	}
}

func TestWorkflowTranslateAndSubtitles(t *testing.T) {
	e := newWorkflowEnv(t)
	tp, _ := workflow.TemplateByID("translate-narrate")
	w := tp.Make("", "Interview", time.Now(), workflow.TemplateOptions{Model: "tts"})
	w.Normalize()
	nodeOf(w, workflow.KindSource).P.Clips = []workflow.Clip{{Name: "interview.wav", Path: e.clip, Size: 1000}}
	ex := nodeOf(w, workflow.KindExport)
	ex.P.Folder = e.out
	// A second export: the transcript as subtitles.
	tr := nodeOf(w, workflow.KindTranscribe)
	srt := w.AddNode(workflow.KindExport, 0, 0)
	w.Node(srt).P.Folder, w.Node(srt).P.Format = e.out, workflow.FormatSRT
	w.Connect(tr.ID, workflow.PortOut, srt, workflow.PortIn)
	w = e.save(t, w)
	run := e.run(t, w.ID, RunOptions{})
	if run.State != workflow.RunDone {
		t.Fatalf("run %s %s %+v", run.State, run.Err, run.Items[0].Steps)
	}
	subs, _ := filepath.Glob(filepath.Join(e.out, "*.srt"))
	if len(subs) != 1 {
		t.Fatalf("subtitles %v", subs)
	}
	data, _ := os.ReadFile(subs[0])
	if !strings.Contains(string(data), "Hello there.") || !strings.Contains(string(data), "-->") {
		t.Errorf("srt %s", data)
	}
	step := run.Items[0].Steps[nodeOf(w, workflow.KindTranslate).ID]
	if step.Out == nil || !strings.HasPrefix(step.Out.Text, "[es] Hello there.") || step.Out.Type != workflow.TypeTranscript || step.Out.Lang != "es" {
		t.Errorf("translation %+v", step.Out)
	}
	var sp proto.SynthesizeParams
	json.Unmarshal(e.eng.Last(proto.MethodTTSSynthesize), &sp)
	// ("[es]" reads as markup and is not spoken.)
	if !strings.Contains(sp.Text, "Hello there.") || sp.Language != "es" {
		t.Errorf("spoke %q in %q", sp.Text, sp.Language)
	}
}

func TestWorkflowConditionBranches(t *testing.T) {
	e := newWorkflowEnv(t)
	w := &workflow.Workflow{Name: "Sort"}
	s := w.AddNode(workflow.KindStart, 0, 0)
	w.Node(s).P.Scripts = []workflow.Script{{Text: "Hello world"}, {Text: "Goodbye"}}
	c := w.AddNode(workflow.KindCondition, 0, 0)
	w.Node(c).P = workflow.Params{Match: workflow.MatchPrefix, Text: "HELLO"}
	sp := w.AddNode(workflow.KindSpeak, 0, 0)
	w.Node(sp).P.Model = "tts"
	yes := w.AddNode(workflow.KindExport, 0, 0)
	no := w.AddNode(workflow.KindExport, 0, 0)
	w.Node(yes).P.Folder, w.Node(no).P.Folder = e.out, e.out
	w.Connect(s, workflow.PortOut, c, workflow.PortIn)
	w.Connect(c, workflow.PortYes, sp, workflow.PortIn)
	w.Connect(sp, workflow.PortOut, yes, workflow.PortIn)
	w.Connect(c, workflow.PortNo, no, workflow.PortIn)
	w = e.save(t, w)
	run := e.run(t, w.ID, RunOptions{})
	if run.State != workflow.RunDone {
		t.Fatalf("%s %+v", run.State, run.Items)
	}
	a, b := run.Items[0].Steps, run.Items[1].Steps
	if a[c].Branch != workflow.PortYes || b[c].Branch != workflow.PortNo {
		t.Errorf("branches %q %q", a[c].Branch, b[c].Branch)
	}
	if a[no].Status != workflow.StepSkipped || b[sp].Status != workflow.StepSkipped || b[yes].Code != workflow.SkipBranch {
		t.Errorf("skips %+v %+v", a[no], b[sp])
	}
	wavs, _ := filepath.Glob(filepath.Join(e.out, "*.wav"))
	txts, _ := filepath.Glob(filepath.Join(e.out, "*.txt"))
	if len(wavs) != 1 || len(txts) != 1 {
		t.Errorf("files %v %v", wavs, txts)
	}
	if data, _ := os.ReadFile(txts[0]); string(data) != "Goodbye\n" {
		t.Errorf("text %q", data)
	}
}

func TestWorkflowFailuresAndRetry(t *testing.T) {
	e := newWorkflowEnv(t)
	tp, _ := workflow.TemplateByID("translate-narrate")
	w := tp.Make("", "Interview", time.Now(), workflow.TemplateOptions{Model: "tts"})
	w.Normalize()
	clip2 := filepath.Join(e.dir, "second.wav")
	dubtest.Speech(clip2, 1)
	nodeOf(w, workflow.KindSource).P.Clips = []workflow.Clip{{Name: "interview.wav", Path: e.clip, Size: 1}, {Name: "second.wav", Path: clip2, Size: 1}}
	w = e.save(t, w)
	// The first clip's second line can't be translated: that clip fails.
	e.tr.fail["How are you today?"] = true
	run := e.run(t, w.ID, RunOptions{})
	done, failed, _ := run.Counts()
	if run.State != workflow.RunFailed || done != 1 || failed != 1 {
		t.Fatalf("run %s done %d failed %d", run.State, done, failed)
	}
	trID := nodeOf(w, workflow.KindTranslate).ID
	spID := nodeOf(w, workflow.KindSpeak).ID
	if s := run.Items[0].Steps[trID]; s.Status != workflow.StepFailed || s.Code != translate.CodeLineFailed {
		t.Errorf("translate %+v", s)
	}
	if s := run.Items[0].Steps[spID]; s.Status != workflow.StepSkipped || s.Code != workflow.SkipUpstream {
		t.Errorf("speak %+v", s)
	}
	stt := e.eng.Count(proto.MethodSTTTranscribe)
	// Retry: only the failed item, from the failed step.
	delete(e.tr.fail, "How are you today?")
	run = e.run(t, w.ID, RunOptions{})
	if run.State != workflow.RunDone || e.eng.Count(proto.MethodSTTTranscribe) != stt {
		t.Errorf("retry %s, transcribed %d more", run.State, e.eng.Count(proto.MethodSTTTranscribe)-stt)
	}

	// A model that is not downloaded stops the run.
	e.missing = "tts"
	nodeOf(w, workflow.KindSpeak).P.Speed = 1.5
	w = e.save(t, w)
	run = e.run(t, w.ID, RunOptions{})
	if run.State != workflow.RunFailed || run.Code != proto.CodeModelNotDownloaded {
		t.Errorf("missing model: %s %q", run.State, run.Code)
	}

	// An invalid workflow does not run.
	nodeOf(w, workflow.KindTranslate).P.To = ""
	w = e.save(t, w)
	_, err := e.wf.Run(ctx, w.ID, RunOptions{})
	var inv *WorkflowInvalidError
	if !errors.As(err, &inv) || !errors.Is(err, core.ErrInvalid) {
		t.Errorf("invalid: %v", err)
	}
}

func TestWorkflowCancelResume(t *testing.T) {
	e := newWorkflowEnv(t)
	w := e.save(t, narration(e, "One.", "Two.", "Three."))
	e.mu.Lock()
	e.gate, e.started = make(chan struct{}), make(chan struct{}, 8)
	gate := e.gate
	e.mu.Unlock()
	if _, err := e.wf.Run(ctx, w.ID, RunOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.wf.Run(ctx, w.ID, RunOptions{}); !errors.Is(err, core.ErrBusy) {
		t.Errorf("second run: %v", err)
	}
	// The first item through, then cancel during the second.
	<-e.started
	gate <- struct{}{}
	<-e.started
	if err := e.wf.Cancel(ctx, w.ID); err != nil {
		t.Fatal(err)
	}
	run := e.wait(t, w.ID)
	if run.State != workflow.RunCancelled {
		t.Fatalf("state %s", run.State)
	}
	spID := nodeOf(w, workflow.KindSpeak).ID
	if run.Items[0].Steps[spID].Status != workflow.StepDone || run.Items[1].Steps[spID].Status != workflow.StepPending {
		t.Fatalf("checkpoints %+v %+v", run.Items[0].Steps[spID], run.Items[1].Steps[spID])
	}
	e.mu.Lock()
	e.gate = nil
	e.mu.Unlock()
	calls := e.eng.Count(proto.MethodTTSSynthesize)
	run = e.run(t, w.ID, RunOptions{})
	if run.State != workflow.RunDone || e.eng.Count(proto.MethodTTSSynthesize) != calls+2 {
		t.Errorf("resumed: %s, %d calls", run.State, e.eng.Count(proto.MethodTTSSynthesize)-calls)
	}
	// Delete removes the files.
	if err := e.wf.Delete(ctx, w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.wf.Dir(w.ID)); !os.IsNotExist(err) {
		t.Error("files left")
	}
}

func TestWorkflowConvertVoice(t *testing.T) {
	e := newWorkflowEnv(t)
	tp, _ := workflow.TemplateByID("change-voice")
	w := tp.Make("", "Redub", time.Now(), workflow.TemplateOptions{Model: "tts", Voice: "narrator"})
	w.Normalize()
	nodeOf(w, workflow.KindSource).P.Clips = []workflow.Clip{{Name: "interview.wav", Path: e.clip, Size: 1}}
	cv := nodeOf(w, workflow.KindConvert)
	cv.P.MatchDuration = true
	w = e.save(t, w)
	run := e.run(t, w.ID, RunOptions{})
	if run.State != workflow.RunDone {
		t.Fatalf("%s %+v", run.State, run.Items[0].Steps)
	}
	src := run.Items[0].Steps[nodeOf(w, workflow.KindSource).ID].Out
	got := run.Items[0].Steps[cv.ID].Out
	if got == nil || src == nil || got.Seconds < src.Seconds-0.1 || got.Seconds > src.Seconds+0.1 {
		t.Errorf("lengths %+v %+v", src, got)
	}
	var sp proto.SynthesizeParams
	json.Unmarshal(e.eng.Last(proto.MethodTTSSynthesize), &sp)
	if sp.Voice != "speaker-narrator" || !strings.Contains(sp.Text, "Hello there.") {
		t.Errorf("spoke %+v", sp)
	}
	// No folder: the result stays in the app.
	outs := Outputs(w, run)
	if len(outs) != 1 || filepath.IsAbs(outs[0].Value.File) {
		t.Errorf("outputs %+v", outs)
	}
}

func TestWorkflowResumesAfterQuit(t *testing.T) {
	e := newWorkflowEnv(t)
	w := e.save(t, narration(e, "One.", "Two.", "Three."))
	e.mu.Lock()
	e.gate, e.started = make(chan struct{}), make(chan struct{}, 8)
	gate := e.gate
	e.mu.Unlock()
	if _, err := e.wf.Run(ctx, w.ID, RunOptions{}); err != nil {
		t.Fatal(err)
	}
	<-e.started
	gate <- struct{}{}
	<-e.started
	calls := e.eng.Count(proto.MethodTTSSynthesize)
	// Quit in the middle of the second item.
	e.mu.Lock()
	e.gate = nil
	e.mu.Unlock()
	e.restart(t)
	run := e.wait(t, w.ID)
	if run.State != workflow.RunDone {
		t.Fatalf("after restart: %s %s", run.State, run.Err)
	}
	if n := e.eng.Count(proto.MethodTTSSynthesize) - calls; n != 2 {
		t.Errorf("spoke %d items again, want the 2 left", n)
	}
	if files, _ := filepath.Glob(filepath.Join(e.out, "*.wav")); len(files) != 3 {
		t.Errorf("exports %v", files)
	}
}

func TestWorkflowExportM4AAndText(t *testing.T) {
	e := newWorkflowEnv(t)
	w := narration(e, "One.")
	nodeOf(w, workflow.KindExport).P.Format = workflow.FormatM4A
	// The script itself, as text, beside it.
	tx := w.AddNode(workflow.KindExport, 0, 0)
	w.Node(tx).P.Folder, w.Node(tx).P.Template = e.out, "{item}-script"
	w.Connect(nodeOf(w, workflow.KindStart).ID, workflow.PortOut, tx, workflow.PortIn)
	w = e.save(t, w)
	run := e.run(t, w.ID, RunOptions{})
	if run.State != workflow.RunDone {
		t.Fatalf("%s %+v", run.State, run.Items[0].Steps)
	}
	if e.eng.Count(proto.MethodMediaEncode) != 1 {
		t.Error("no media.encode")
	}
	if m4a, _ := filepath.Glob(filepath.Join(e.out, "*.m4a")); len(m4a) != 1 {
		t.Errorf("m4a %v", m4a)
	}
	if data, err := os.ReadFile(filepath.Join(e.out, "One.-script.txt")); err != nil || string(data) != "One.\n" {
		t.Errorf("text %q %v", data, err)
	}
	// Without media.encode the step fails and the run stops.
	e.eng.Missing[proto.MethodMediaEncode] = true
	run = e.run(t, w.ID, RunOptions{Again: true})
	if run.State != workflow.RunFailed || run.Code != WorkflowCodeEncode {
		t.Errorf("no encoder: %s %q", run.State, run.Code)
	}
}
