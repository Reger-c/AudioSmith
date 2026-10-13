package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/store/sqlitestore"
)

var ctx = context.Background()

// fakeScheduler records submissions; tests finish jobs by publishing
// jobs.Updated themselves, as thread B's scheduler will.
type fakeScheduler struct {
	mu   sync.Mutex
	bus  *core.Bus
	jobs map[string]jobs.Job
	n    int
	err  error
}

func (f *fakeScheduler) Register(string, jobs.Executor) {}
func (f *fakeScheduler) Start(context.Context) error    { return nil }
func (f *fakeScheduler) Submit(_ context.Context, spec jobs.Spec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	f.n++
	id := fmt.Sprintf("job-%d", f.n)
	f.jobs[id] = jobs.Job{ID: id, Spec: spec, State: jobs.StateQueued, Created: time.Now()}
	return id, nil
}
func (f *fakeScheduler) SubmitGraph(context.Context, []jobs.Node) (map[string]string, error) {
	return nil, core.ErrUnsupported
}
func (f *fakeScheduler) Cancel(context.Context, string) error { return nil }
func (f *fakeScheduler) Retry(context.Context, string) error  { return nil }
func (f *fakeScheduler) Get(_ context.Context, id string) (jobs.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.jobs[id], nil
}
func (f *fakeScheduler) List(context.Context, jobs.Filter) ([]jobs.Job, error) { return nil, nil }
func (f *fakeScheduler) Idle() bool                                            { return true }
func (f *fakeScheduler) Close() error                                          { return nil }

// finish ends a job as the engine would: on success the WAV exists first.
func (f *fakeScheduler) finish(t *testing.T, id string, state jobs.State, res *proto.SynthesizeResult) jobs.Job {
	t.Helper()
	f.mu.Lock()
	j := f.jobs[id]
	j.State, j.Finished = state, time.Now()
	if res != nil && j.Kind == KindRender {
		// A render: its result carries the engine's figures.
		var p RenderParams
		json.Unmarshal(j.Params, &p)
		os.WriteFile(p.Output, []byte("RIFF"), 0o644)
		rr := RenderResult{Output: p.Output, SampleRate: res.SampleRate, AudioSeconds: res.AudioSeconds, Seconds: res.Seconds}
		if res.Seed != nil {
			rr.Seed = *res.Seed
		}
		if p.Take.Render != nil {
			rr.Render = *p.Take.Render
		}
		j.Result, _ = json.Marshal(rr)
	} else if res != nil {
		var p proto.SynthesizeParams
		json.Unmarshal(j.Params, &p)
		os.WriteFile(p.Output.Path, []byte("RIFF"), 0o644)
		res.Output = p.Output.Path
		j.Result, _ = json.Marshal(res)
	}
	f.jobs[id] = j
	f.mu.Unlock()
	f.bus.Publish(jobs.Updated{Job: j})
	return j
}

type env struct {
	st    *sqlitestore.Store
	bus   *core.Bus
	sched *fakeScheduler
	svc   *Services
	mu    sync.Mutex
	got   []core.Event
	logs  []string
}

func newEnv(t *testing.T, mod func(*Config)) *env {
	t.Helper()
	st, err := sqlitestore.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &env{st: st, bus: core.NewBus()}
	e.sched = &fakeScheduler{bus: e.bus, jobs: map[string]jobs.Job{}}
	e.bus.Subscribe(func(ev core.Event) {
		if _, ok := ev.(jobs.Updated); ok {
			return
		}
		e.mu.Lock()
		e.got = append(e.got, ev)
		e.mu.Unlock()
	})
	cfg := Config{Store: st, Bus: e.bus, Scheduler: e.sched, DraftDelay: 20 * time.Millisecond,
		Logf: func(f string, a ...any) { e.mu.Lock(); e.logs = append(e.logs, fmt.Sprintf(f, a...)); e.mu.Unlock() }}
	if mod != nil {
		mod(&cfg)
	}
	e.svc = New(cfg)
	t.Cleanup(func() {
		if err := e.svc.Close(ctx); err != nil {
			t.Error(err)
		}
		st.Close()
	})
	return e
}

func (e *env) events() []core.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]core.Event(nil), e.got...)
}

// wait waits for an event matching ok (saving a take is asynchronous).
func (e *env) wait(t *testing.T, ok func(core.Event) bool) core.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, ev := range e.events() {
			if ok(ev) {
				return ev
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("event not published; got %v, logs %v", e.events(), e.logs)
	return nil
}

func isCreated(ev core.Event) bool { _, ok := ev.(core.TakeCreated); return ok }

func TestGenerate(t *testing.T) {
	e := newEnv(t, nil)
	takes := e.svc.Takes
	recipe := core.Take{Text: "  Hello there.  ", Model: "m/kokoro", Voice: "af_heart", Speed: 1.25, VoiceID: "",
		ID: "ignored", File: "ignored.wav", AudioSeconds: 99, Starred: true}
	id, err := takes.Generate(ctx, core.TakeRequest{Recipe: recipe, Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	job, _ := e.sched.Get(ctx, id)
	if job.Kind != KindRender || job.Lane != jobs.LaneGPU || job.Priority != jobs.PriorityInteractive ||
		!strings.HasPrefix(job.Owner, "take:") || job.Title == "" || !job.Idempotent {
		t.Fatalf("spec %+v", job.Spec)
	}
	var p RenderParams
	if err := json.Unmarshal(job.Params, &p); err != nil {
		t.Fatal(err)
	}
	takeID := strings.TrimPrefix(job.Owner, "take:")
	if p.Take.Text != "Hello there." || p.Take.Model != "m/kokoro" || p.Take.Voice != "af_heart" || p.Take.Speed != 1.25 ||
		p.Take.ID != takeID || p.Take.AudioSeconds != 0 || p.Take.Starred || p.Take.Render == nil || p.Take.Render.Master != MasterDefault ||
		!p.Stream || p.Output != filepath.Join(takes.AudioDir(), takeID+".wav") {
		t.Fatalf("params %+v", p)
	}
	if len(e.events()) != 0 {
		t.Fatal("events before the job ends")
	}

	// A progress update is not the end.
	e.bus.Publish(jobs.Updated{Job: jobs.Job{ID: id, Spec: job.Spec, State: jobs.StateRunning}})
	seed := int64(7)
	e.sched.finish(t, id, jobs.StateSucceeded, &proto.SynthesizeResult{SampleRate: 24000, AudioSeconds: 2, Seconds: 0.5, Seed: &seed})
	created := e.wait(t, isCreated).(core.TakeCreated).Take
	if created.ID != takeID || created.JobID != id || created.Text != "Hello there." || created.RTF() != 4 ||
		created.Seed != 7 || created.Starred || created.File != takeID+".wav" {
		t.Fatalf("take %+v", created)
	}
	stored, err := takes.Get(ctx, takeID)
	if err != nil || stored.Speed != 1.25 || stored.SampleRate != 24000 {
		t.Fatalf("stored %+v %v", stored, err)
	}

	// The same success again (a duplicate event) saves nothing new.
	e.sched.finish(t, id, jobs.StateSucceeded, &proto.SynthesizeResult{SampleRate: 24000})
	e.svc.takes.wg.Wait()
	n := 0
	for _, ev := range e.events() {
		if isCreated(ev) {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d TakeCreated", n)
	}
}

func TestGenerateFailsOrIsCancelled(t *testing.T) {
	e := newEnv(t, nil)
	for _, st := range []jobs.State{jobs.StateFailed, jobs.StateCancelled} {
		id, err := e.svc.Takes.Generate(ctx, core.TakeRequest{Recipe: core.Take{Text: "x", Model: "m"}, Background: true})
		if err != nil {
			t.Fatal(err)
		}
		if j, _ := e.sched.Get(ctx, id); j.Priority != jobs.PriorityBackground {
			t.Errorf("priority %v", j.Priority)
		}
		e.sched.finish(t, id, st, nil)
	}
	e.svc.takes.wg.Wait()
	if l, _ := e.svc.Takes.List(ctx, core.TakeQuery{}); len(l) != 0 || len(e.events()) != 0 || len(e.svc.takes.pending) != 0 {
		t.Errorf("takes %v events %v pending %v", l, e.events(), e.svc.takes.pending)
	}
}

// A job that succeeds after a restart (not pending in this process) is
// rebuilt from its params.
func TestGenerateAfterRestart(t *testing.T) {
	e := newEnv(t, nil)
	speed := 0.9
	path := filepath.Join(e.svc.Takes.AudioDir(), "T1.wav")
	params, _ := json.Marshal(proto.SynthesizeParams{Model: "m", Text: "Recovered", Voice: "v", Speed: &speed, Output: proto.Output{Path: path}})
	e.sched.jobs["old"] = jobs.Job{ID: "old", Spec: jobs.Spec{Kind: "tts.synthesize", Owner: "take:T1", Params: params}, Created: time.Unix(100, 0)}
	e.sched.finish(t, "old", jobs.StateSucceeded, &proto.SynthesizeResult{SampleRate: 24000, AudioSeconds: 1, Seconds: 1})
	tk := e.wait(t, isCreated).(core.TakeCreated).Take
	if tk.ID != "T1" || tk.Text != "Recovered" || tk.Speed != 0.9 || tk.File != "T1.wav" || !tk.CreatedAt.Equal(time.Unix(100, 0)) {
		t.Fatalf("%+v", tk)
	}
	// Other kinds and owners are not ours.
	e.bus.Publish(jobs.Updated{Job: jobs.Job{ID: "x", Spec: jobs.Spec{Kind: "model.download", Owner: "take:T2"}, State: jobs.StateSucceeded}})
	e.bus.Publish(jobs.Updated{Job: jobs.Job{ID: "y", Spec: jobs.Spec{Kind: "tts.synthesize", Owner: "project:P"}, State: jobs.StateSucceeded}})
	e.svc.takes.wg.Wait()
	if len(e.logs) != 0 {
		t.Error(e.logs)
	}
}

type gate struct{ model string }

func (g gate) Check(_ context.Context, model string) error {
	if model == g.model {
		return &core.LicenceRequiredError{Model: model, Licence: "cc-by-nc-4.0", Name: "CC BY-NC 4.0"}
	}
	return nil
}
func (gate) Accept(context.Context, core.LicenceAcceptance) error   { return nil }
func (gate) Revoke(context.Context, string, string) error           { return nil }
func (gate) List(context.Context) ([]core.LicenceAcceptance, error) { return nil, nil }

func TestGenerateChecks(t *testing.T) {
	e := newEnv(t, func(c *Config) {
		c.Licences = gate{"m/omni"}
		c.Ready = func(_ context.Context, model string) error {
			if model == "m/missing" {
				return fmt.Errorf("%s: %w", model, core.ErrNotDownloaded)
			}
			return nil
		}
	})
	g := func(text, model string) error {
		_, err := e.svc.Takes.Generate(ctx, core.TakeRequest{Recipe: core.Take{Text: text, Model: model}})
		return err
	}
	if err := g("  ", "m"); !errors.Is(err, core.ErrInvalid) {
		t.Error(err)
	}
	if err := g("x", ""); !errors.Is(err, core.ErrInvalid) {
		t.Error(err)
	}
	var lre *core.LicenceRequiredError
	if err := g("x", "m/omni"); !errors.As(err, &lre) || lre.Licence != "cc-by-nc-4.0" {
		t.Error(err)
	}
	if err := g("x", "m/missing"); !errors.Is(err, core.ErrNotDownloaded) {
		t.Error(err)
	}
	e.sched.err = core.ErrBusy
	if err := g("x", "m"); !errors.Is(err, core.ErrBusy) || len(e.svc.takes.pending) != 0 {
		t.Error(err)
	}
	if len(e.sched.jobs) != 0 {
		t.Error("a job was submitted")
	}

	noSched := newEnv(t, func(c *Config) { c.Scheduler = nil })
	if _, err := noSched.svc.Takes.Generate(ctx, core.TakeRequest{Recipe: core.Take{Text: "x", Model: "m"}}); !errors.Is(err, core.ErrUnsupported) {
		t.Error(err)
	}
}

func TestAddStarDeleteExport(t *testing.T) {
	e := newEnv(t, nil)
	takes := e.svc.Takes
	if _, err := takes.Add(ctx, core.Take{Text: "x"}); !errors.Is(err, core.ErrInvalid) {
		t.Error(err)
	}
	at := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	tk := core.Take{ID: "20261013-120000.000", CreatedAt: at, File: "20261013-120000.000.wav", Text: "Made by the v1 engine", Model: "m"}
	audio := strings.Repeat("RIFF-data", 100_000)
	os.WriteFile(takes.AudioPath(tk), []byte(audio), 0o644)
	added, err := takes.Add(ctx, tk)
	if err != nil || added.ID != tk.ID {
		t.Fatal(added, err)
	}
	if ev := e.events(); len(ev) != 1 || ev[0].(core.TakeCreated).Take.Text != tk.Text {
		t.Fatalf("events %v", ev)
	}
	auto, _ := takes.Add(ctx, core.Take{File: "a.wav", Text: "auto id", Model: "m"})
	if len(auto.ID) != 26 || auto.CreatedAt.IsZero() {
		t.Errorf("auto %+v", auto)
	}

	if err := takes.SetStarred(ctx, tk.ID, true); err != nil {
		t.Fatal(err)
	}
	takes.SetStarred(ctx, tk.ID, true) // unchanged: no second event
	if l, _ := takes.List(ctx, core.TakeQuery{Starred: true}); len(l) != 1 {
		t.Error("not starred")
	}
	updates := 0
	for _, ev := range e.events() {
		if u, ok := ev.(core.TakeUpdated); ok && u.Take.Starred {
			updates++
		}
	}
	if updates != 1 {
		t.Errorf("%d TakeUpdated", updates)
	}
	if err := takes.SetStarred(ctx, "nope", true); !errors.Is(err, core.ErrNotFound) {
		t.Error(err)
	}

	dst := filepath.Join(t.TempDir(), "out.wav")
	if err := takes.Export(ctx, tk.ID, dst); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(dst); string(data) != audio {
		t.Error("export differs")
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(dst), ".*part")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := takes.Export(cancelled, tk.ID, filepath.Join(filepath.Dir(dst), "c.wav")); err == nil {
		t.Error("cancelled export succeeded")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dst), "c.wav")); !os.IsNotExist(err) {
		t.Error("cancelled export left a file")
	}

	if err := takes.Delete(ctx, tk.ID); err != nil {
		t.Fatal(err)
	}
	if ev := e.events(); ev[len(ev)-1] != (core.TakeDeleted{ID: tk.ID}) {
		t.Errorf("last event %v", ev[len(ev)-1])
	}
	if err := takes.Delete(ctx, tk.ID); !errors.Is(err, core.ErrNotFound) {
		t.Error(err)
	}
}

func TestVoicesAndProjects(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	e := newEnv(t, func(c *Config) { c.Now = func() time.Time { return now } })
	vs := e.svc.Voices
	if _, err := vs.Save(ctx, core.Voice{Name: " ", Kind: core.VoiceCloned}); !errors.Is(err, core.ErrInvalid) {
		t.Error(err)
	}
	if _, err := vs.Save(ctx, core.Voice{Name: "x", Kind: "?"}); !errors.Is(err, core.ErrInvalid) {
		t.Error(err)
	}
	v, err := vs.Save(ctx, core.Voice{Name: " Narrator ", Kind: core.VoiceCloned})
	if err != nil || v.ID == "" || v.Name != "Narrator" || !v.CreatedAt.Equal(now) {
		t.Fatal(v, err)
	}
	now = now.Add(time.Hour)
	v.Name = "Narrator 2"
	v.CreatedAt = time.Time{} // ignored: kept from the stored profile
	v, err = vs.Save(ctx, v)
	if err != nil || !v.CreatedAt.Equal(now.Add(-time.Hour)) || !v.UpdatedAt.Equal(now) {
		t.Fatal(v, err)
	}
	if _, err := vs.Save(ctx, core.Voice{ID: "unknown", Name: "x", Kind: core.VoiceCloned}); !errors.Is(err, core.ErrNotFound) {
		t.Error(err)
	}
	if l, _ := vs.List(ctx, core.VoiceQuery{Search: "narr"}); len(l) != 1 {
		t.Error(l)
	}
	if err := vs.Delete(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := vs.Import(ctx, "x.asvoice"); !errors.Is(err, core.ErrUnsupported) {
		t.Error(err)
	}

	ps := e.svc.Projects
	p, err := ps.Save(ctx, core.Project{Kind: core.ProjectStory, Title: "Night"})
	if err != nil || string(p.Body) != "{}" {
		t.Fatal(p, err)
	}
	if _, err := ps.Save(ctx, core.Project{Kind: "nope", Title: "x"}); !errors.Is(err, core.ErrInvalid) {
		t.Error(err)
	}
	p.Body = json.RawMessage(`{"version":1,"lines":[]}`)
	if _, err := ps.Save(ctx, p); err != nil {
		t.Fatal(err)
	}
	if got, _ := ps.Get(ctx, p.ID); string(got.Body) != `{"version":1,"lines":[]}` {
		t.Error(string(got.Body))
	}
	ps.Delete(ctx, p.ID)

	var kinds []string
	for _, ev := range e.events() {
		switch ev := ev.(type) {
		case core.VoiceChanged:
			kinds = append(kinds, fmt.Sprintf("voice:%v", ev.Deleted))
		case core.ProjectChanged:
			kinds = append(kinds, fmt.Sprintf("project:%v", ev.Deleted))
		}
	}
	if strings.Join(kinds, " ") != "voice:false voice:false voice:true project:false project:false project:true" {
		t.Errorf("events %v", kinds)
	}
}

func TestDrafts(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.DraftDelay = time.Hour }) // only Flush writes
	d := e.svc.Drafts
	type form struct{ Text string }
	var got form
	if ok, err := d.Load(ctx, "studio", &got); ok || err != nil {
		t.Fatal(ok, err)
	}
	f := form{"one"}
	d.Save("studio", f)
	f.Text = "mutated after Save" // Save copied the value
	d.Save("studio", form{"two"})
	// Pending drafts are visible to Load before they are written.
	if ok, _ := d.Load(ctx, "studio", &got); !ok || got.Text != "two" {
		t.Fatal(got)
	}
	if _, ok, _ := e.st.Documents().Get(ctx, "draft:studio"); ok {
		t.Fatal("written before the delay")
	}
	if err := d.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	doc, ok, _ := e.st.Documents().Get(ctx, "draft:studio")
	if !ok || string(doc) != `{"Text":"two"}` {
		t.Fatalf("doc %s", doc)
	}
	saves := func() int {
		n := 0
		for _, ev := range e.events() {
			if ev == (core.DraftSaved{Key: "studio"}) {
				n++
			}
		}
		return n
	}
	if saves() != 1 {
		t.Errorf("%d DraftSaved", saves())
	}
	// Saving what is stored writes nothing.
	d.Save("studio", form{"two"})
	d.Flush(ctx)
	if saves() != 1 {
		t.Error("no-op save written")
	}
	if err := d.Delete(ctx, "studio"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := d.Load(ctx, "studio", &got); ok {
		t.Error("deleted draft loads")
	}
	d.Save("x", make(chan int)) // not JSON: logged, dropped
	if len(e.logs) != 1 {
		t.Errorf("logs %v", e.logs)
	}
}

func TestDraftDebounce(t *testing.T) {
	e := newEnv(t, nil) // 20 ms
	d := e.svc.Drafts
	for i := range 10 {
		d.Save("k", i)
		time.Sleep(time.Millisecond)
	}
	e.wait(t, func(ev core.Event) bool { return ev == core.DraftSaved{Key: "k"} })
	time.Sleep(60 * time.Millisecond)
	n := 0
	for _, ev := range e.events() {
		if _, ok := ev.(core.DraftSaved); ok {
			n++
		}
	}
	if doc, _, _ := e.st.Documents().Get(ctx, "draft:k"); string(doc) != "9" || n != 1 {
		t.Errorf("doc %s after %d writes", doc, n)
	}
	// Close writes what is pending, and Saves after it are dropped.
	d.Save("k", 10)
	if err := e.svc.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if doc, _, _ := e.st.Documents().Get(ctx, "draft:k"); string(doc) != "10" {
		t.Errorf("after close %s", doc)
	}
	d.Save("k", 11)
}

// TestConcurrentServices runs generation, starring and drafts from several
// goroutines (run with -race).
func TestConcurrentServices(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.DraftDelay = time.Millisecond })
	var wg sync.WaitGroup
	for g := range 4 {
		wg.Go(func() {
			for i := range 10 {
				id, err := e.svc.Takes.Generate(ctx, core.TakeRequest{Recipe: core.Take{Text: "t", Model: "m"}})
				if err != nil {
					t.Error(err)
					return
				}
				e.sched.finish(t, id, jobs.StateSucceeded, &proto.SynthesizeResult{SampleRate: 1})
				e.svc.Drafts.Save(fmt.Sprint("g", g), i)
			}
		})
	}
	wg.Wait()
	e.svc.takes.wg.Wait()
	l, _ := e.svc.Takes.List(ctx, core.TakeQuery{})
	if len(l) != 40 {
		t.Fatalf("%d takes", len(l))
	}
	for _, tk := range l[:5] {
		wg.Go(func() { e.svc.Takes.SetStarred(ctx, tk.ID, true) })
	}
	wg.Wait()
	if s, _ := e.svc.Takes.List(ctx, core.TakeQuery{Starred: true}); len(s) != 5 {
		t.Errorf("%d starred", len(s))
	}
}

// A caller may choose the take's id, to follow its job by owner before
// Generate returns; OmniVoice's duration and steps reach the engine. With
// Async inline the take is saved before finish returns.
func TestGenerateWithChosenID(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Async = func(fn func()) { fn() } })
	id := core.NewID(time.Now())
	jobID, err := e.svc.Takes.Generate(ctx, core.TakeRequest{Recipe: core.Take{ID: id, Text: "x", Model: "m", Duration: 3.5, Steps: 16}})
	if err != nil {
		t.Fatal(err)
	}
	j, _ := e.sched.Get(ctx, jobID)
	var p RenderParams
	json.Unmarshal(j.Params, &p)
	if j.Owner != "take:"+id || p.Take.Duration != 3.5 || p.Take.Steps != 16 || !strings.Contains(p.Output, id) {
		t.Fatalf("job %+v params %+v", j.Spec, p)
	}
	e.sched.finish(t, jobID, jobs.StateSucceeded, &proto.SynthesizeResult{SampleRate: 24000, AudioSeconds: 3.5, Seconds: 1})
	got, err := e.svc.Takes.Get(ctx, id)
	if err != nil || got.Duration != 3.5 || got.Steps != 16 || got.JobID != jobID {
		t.Fatalf("take %+v %v", got, err)
	}
	if _, err := e.svc.Takes.Generate(ctx, core.TakeRequest{Recipe: core.Take{ID: "../x", Text: "x", Model: "m"}}); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("bad id: %v", err)
	}
}
