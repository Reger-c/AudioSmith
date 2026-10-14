package services

// Dubbing (M4): the dub service keeps dub projects (internal/dub.Project,
// migration 005's table, files in <data>/dubs/<id>/) and runs their
// pipeline as a graph of jobs owned by "dub:<id>":
//
//	fetch (net: yt-dlp, URL sources)
//	 └ ingest (media: probe, decode, 16 kHz copy, peaks, onsets, poster, preview proxy)
//	    └ separate (gpu: voices / background; skipped when the engine cannot)
//	       └ transcribe (gpu: diarize, transcribe, align; or the captions)
//	          └ references (gpu: each speaker's clone reference, cleaned)
//	             ├ translate:es (net, or gpu for the engine's LLM) ─ synthesize:es (gpu) ─ mix:es (cpu)
//	             └ translate:fr …
//	qc:<lang> (gpu, "Verify")    export (cpu/media)
//
// Every stage is idempotent: it reads the project, skips what is done
// (files are named by their inputs' fingerprints), writes its results
// into the project (Update) and marks its StageState. A run after a crash
// or a cancel goes on where it stopped; a re-dub renders only the lines
// whose fingerprint changed. No job waits for another inside its
// executor: the scheduler's dependencies order them, so a stage never
// holds the gpu lane's one slot while waiting.
//
// With review mode on, a run stops after the transcript (Through
// references) and again after the translation; the UI's checkpoint
// banners start the next run.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/store"
	"github.com/hehehai/AudioSmith/internal/translate"
)

// DubOwner prefixes the owners of a dub's jobs: "dub:<id>".
const DubOwner = "dub:"

// Job kinds of the dub pipeline.
const (
	KindDubFetch      = "dub.fetch"
	KindDubIngest     = "dub.ingest"
	KindDubSeparate   = "dub.separate"
	KindDubTranscribe = "dub.transcribe"
	KindDubReferences = "dub.references"
	KindDubTranslate  = "dub.translate"
	KindDubSynthesize = "dub.synthesize"
	KindDubMix        = "dub.mix"
	KindDubQC         = "dub.qc"
	KindDubExport     = "dub.export"
)

// Warning and error codes of the dub stages (StageState.Code/Warnings).
const (
	DubWarnNoSeparation  = "dub.no_separation"     // the engine cannot separate (or no model): the original is ducked under the dub
	DubWarnNoDiarization = "dub.no_diarization"    // the engine cannot tell speakers apart: one speaker
	DubWarnTooManySpk    = "dub.too_many_speakers" // more speakers than the model separates
	DubWarnNoPoster      = "dub.no_poster"         // no media.thumbnail
	DubWarnNoProxy       = "dub.no_proxy"          // the video cannot play in the preview (no media.transcode)
	DubWarnNoEnhance     = "dub.no_enhance"        // references kept as they are
	DubWarnCaptions      = "dub.captions"          // the downloaded captions were used instead of transcription
	DubWarnNoReference   = "dub.no_reference"      // a speaker with too little speech: the default voice
	DubWarnVoice         = "dub.voice"             // a cast voice could not be resolved: the default voice
	DubWarnNoBackground  = "dub.no_background"     // keep background, but nothing to keep it from
	DubCodeNoSource      = "dub.no_source"
	DubCodeNoSpeech      = "dub.no_speech"     // transcription found no speech
	DubCodeNoTranslator  = "dub.no_translator" // no translation engine is set up
	DubCodeNoLines       = "dub.no_lines"      // nothing to say in the language
	DubCodeNotMixed      = "dub.not_mixed"     // export of a track not made yet
	DubCodeNeedsEngine   = "dub.needs_engine"  // the export needs an engine method it lacks (data: method)
	DubCodeNeedsFFmpeg   = "dub.needs_ffmpeg"  // MP3 and burnt-in subtitles
	DubCodeFailed        = "dub.failed"
)

// DubChanged is published after a dub project is saved or deleted.
// Topic "dub.changed".
type DubChanged struct {
	ID      string
	Project *dub.Project // a copy; nil when Deleted
	Deleted bool
}

func (DubChanged) Topic() string { return "dub.changed" }

// DubModels are the models the pipeline uses besides the TTS model (repo
// ids; "" = that step is skipped).
type DubModels struct {
	STT, Align, Diarize, Separate, Enhance string
	// WordTimes: STT times words itself (no aligner needed).
	WordTimes bool
}

// DubModelCaps are what a TTS model can do, as the pipeline needs it.
type DubModelCaps struct {
	Clone, Duration, Instruct, Speed, Steps, Guidance bool
	// Languages maps BCP-47 codes to the model's language values
	// ("zh" → "Chinese"); nil = it takes codes, or has no language.
	Languages map[string]string
}

// DubsConfig wires the dub service. Store, Bus and Scheduler are required.
type DubsConfig struct {
	Store     store.Dubs
	Bus       *core.Bus
	Scheduler jobs.Scheduler
	// Engine returns the executor of an engine method (engine.Executor
	// in the method's role, dub.RoleOf); nil or a nil executor fails the
	// stage with engine_unavailable (retryable).
	Engine func(method string) jobs.Executor
	// Render renders lines through the take pipeline (svc.Render).
	Render    *Takes
	ModelPath func(ctx context.Context, repo string) (string, error)
	// Models are the pipeline's models now (from the catalogue/settings).
	Models func() DubModels
	// Caps describes a TTS model.
	Caps func(repo string) DubModelCaps
	// Voice resolves a VoiceRef for a model (saved voices, designs,
	// gallery voices). Goroutine-safe. nil: every cast voice is the
	// default one.
	Voice func(ctx context.Context, ref, model, lang string) (dub.VoiceSpec, error)
	// Translator returns the translator for a project's settings, or an
	// error with translate.CodeNoLLM / translate.CodeNoKey when none is set
	// up; Extractor the glossary extractor (nil: no Auto glossary).
	Translator func(ctx context.Context, s dub.Settings) (translate.Translator, error)
	Extractor  func() *translate.Extractor
	// TranslateLane is the lane of translation jobs (net; gpu when the
	// route is the engine's own LLM). nil = net.
	TranslateLane func() jobs.Lane
	// YtDlp is the URL importer ("" path = URL import is off).
	YtDlp func() dub.YtDlp
	// Documents keep the batch queue's settings and watch folder.
	Documents store.Documents
	// WatchEvery is the watch folder's scan interval (0 = 5 s).
	WatchEvery time.Duration
	Now        func() time.Time
	Logf       func(string, ...any)
}

// Dubs is the dub service. Goroutine-safe.
type Dubs struct {
	cfg   DubsConfig
	mu    sync.Mutex // serializes read-modify-write of projects
	batch batch
}

// NewDubs builds the service and registers its executors.
func NewDubs(cfg DubsConfig) *Dubs {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logf == nil {
		cfg.Logf = log.Printf
	}
	if cfg.Models == nil {
		cfg.Models = func() DubModels { return DubModels{} }
	}
	if cfg.Caps == nil {
		cfg.Caps = func(string) DubModelCaps { return DubModelCaps{Clone: true} }
	}
	s := &Dubs{cfg: cfg}
	s.batch.every = cfg.WatchEvery
	if cfg.Scheduler != nil {
		for kind, ex := range s.Executors() {
			cfg.Scheduler.Register(kind, ex)
		}
	}
	return s
}

// Executors are the service's executors by job kind (registered by
// NewDubs; tests with a fake scheduler run them themselves).
func (s *Dubs) Executors() map[string]jobs.Executor {
	return map[string]jobs.Executor{
		KindDubFetch:      s.stage(dub.StageFetch, s.fetch),
		KindDubIngest:     s.stage(dub.StageIngest, s.ingest),
		KindDubSeparate:   s.stage(dub.StageSeparate, s.separate),
		KindDubTranscribe: s.stage(dub.StageTranscribe, s.transcribe),
		KindDubReferences: s.stage(dub.StageReferences, s.references),
		KindDubTranslate:  s.stage(dub.StageTranslate, s.translate),
		KindDubSynthesize: s.stage(dub.StageSynthesize, s.synthesize),
		KindDubMix:        s.stage(dub.StageMix, s.mix),
		KindDubQC:         s.stage(dub.StageQC, s.qc),
		KindDubExport:     jobs.ExecutorFunc(s.export),
	}
}

// --- the library ------------------------------------------------------------------

// Dir is the directory of project id's files.
func (s *Dubs) Dir(id string) string { return filepath.Join(s.cfg.Store.Dir(), id) }

// Path is a project file's absolute path ("" for "").
func (s *Dubs) Path(id, name string) string {
	if name == "" {
		return ""
	}
	if filepath.IsAbs(name) {
		return name
	}
	return filepath.Join(s.Dir(id), name)
}

// Create saves a new project (its ID chosen when empty).
func (s *Dubs) Create(ctx context.Context, p *dub.Project) (*dub.Project, error) {
	if p.ID == "" {
		p.ID = core.NewID(s.cfg.Now())
	}
	now := s.cfg.Now()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	p.Normalize()
	if err := os.MkdirAll(s.Dir(p.ID), 0o755); err != nil {
		return nil, err
	}
	s.mu.Lock()
	err := s.put(ctx, p)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	s.publish(p)
	return p.Clone(), nil
}

func (s *Dubs) put(ctx context.Context, p *dub.Project) error {
	doc, err := json.Marshal(p)
	if err != nil {
		return err
	}
	src := p.Source.Path
	if p.Source.Kind == dub.SourceURL && p.Source.URL != "" {
		src = p.Source.URL
	}
	return s.cfg.Store.Put(ctx, store.DubRecord{ID: p.ID, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, Batch: p.Batch,
		Title: p.Title, Source: src, Languages: p.Targets, Phase: string(p.Phase()), Duration: p.Source.Duration, Doc: doc})
}

func (s *Dubs) publish(p *dub.Project) {
	if s.cfg.Bus != nil {
		s.cfg.Bus.Publish(DubChanged{ID: p.ID, Project: p.Clone()})
	}
}

// Get reads a project.
func (s *Dubs) Get(ctx context.Context, id string) (*dub.Project, error) {
	r, err := s.cfg.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	p := &dub.Project{}
	if err := json.Unmarshal(r.Doc, p); err != nil {
		return nil, fmt.Errorf("dub %s: %w", id, err)
	}
	p.Normalize()
	return p, nil
}

// List lists projects (without their documents).
func (s *Dubs) List(ctx context.Context, q store.DubQuery) ([]store.DubRecord, error) {
	return s.cfg.Store.List(ctx, q)
}

// Update applies fn to the saved project, saves it and publishes it. fn
// may return an error to change nothing.
func (s *Dubs) Update(ctx context.Context, id string, fn func(*dub.Project) error) (*dub.Project, error) {
	s.mu.Lock()
	p, err := s.Get(ctx, id)
	if err == nil {
		err = fn(p)
	}
	if err == nil {
		p.ID = id
		p.UpdatedAt = s.cfg.Now()
		err = s.put(ctx, p)
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	s.publish(p)
	return p.Clone(), nil
}

// Delete cancels a project's jobs and removes it with its files.
func (s *Dubs) Delete(ctx context.Context, id string) error {
	s.Cancel(ctx, id)
	s.mu.Lock()
	err := s.cfg.Store.Delete(ctx, id)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if dir := s.Dir(id); filepath.Base(dir) == id && id != "" {
		os.RemoveAll(dir)
	}
	if s.cfg.Bus != nil {
		s.cfg.Bus.Publish(DubChanged{ID: id, Deleted: true})
	}
	return nil
}

// Active lists a project's unfinished jobs.
func (s *Dubs) Active(ctx context.Context, id string) ([]jobs.Job, error) {
	if s.cfg.Scheduler == nil {
		return nil, nil
	}
	return s.cfg.Scheduler.List(ctx, jobs.Filter{Owner: DubOwner + id})
}

// Cancel cancels a project's unfinished jobs and forgets its run.
func (s *Dubs) Cancel(ctx context.Context, id string) error {
	active, err := s.Active(ctx, id)
	if err != nil {
		return err
	}
	var errs []error
	for _, j := range active {
		if err := s.cfg.Scheduler.Cancel(ctx, j.ID); err != nil && !errors.Is(err, jobs.ErrNotFound) {
			errs = append(errs, err)
		}
	}
	s.Update(ctx, id, func(p *dub.Project) error {
		p.Run = nil
		for k, st := range p.Stages {
			if st.Status == dub.StatusRunning {
				st.Status, st.Code = dub.StatusNone, proto.CodeCancelled
				p.Stages[k] = st
			}
		}
		return nil
	})
	return errors.Join(errs...)
}

// --- runs ---------------------------------------------------------------------------

// RunRequest asks for the pipeline up to a stage.
type RunRequest struct {
	// Through is the last stage: dub.StageReferences (the transcript),
	// StageTranslate, StageMix (the dub) or StageQC (Verify).
	Through dub.Stage
	// Langs are the languages (nil = every language the project speaks).
	Langs []string
	// Force runs these stages again although they are done (a new
	// transcript, every line translated again).
	Force []dub.Stage
	// Only re-renders these segments ("Regen"): nil = the stale ones.
	Only     []string
	Priority jobs.Priority
	// Batch: the batch queue's run (background priority, no review).
	Batch bool
}

// StageParams are a stage job's params.
type StageParams struct {
	Dub   string   `json:"dub"`
	Lang  string   `json:"lang,omitempty"`
	Force bool     `json:"force,omitempty"`
	Only  []string `json:"only,omitempty"`
}

var stageKinds = map[dub.Stage]string{
	dub.StageFetch: KindDubFetch, dub.StageIngest: KindDubIngest, dub.StageSeparate: KindDubSeparate,
	dub.StageTranscribe: KindDubTranscribe, dub.StageReferences: KindDubReferences, dub.StageTranslate: KindDubTranslate,
	dub.StageSynthesize: KindDubSynthesize, dub.StageMix: KindDubMix, dub.StageQC: KindDubQC,
}

var stageLanes = map[dub.Stage]jobs.Lane{
	dub.StageFetch: jobs.LaneNet, dub.StageIngest: jobs.LaneMedia, dub.StageSeparate: jobs.LaneGPU,
	dub.StageTranscribe: jobs.LaneGPU, dub.StageReferences: jobs.LaneGPU, dub.StageTranslate: jobs.LaneNet,
	dub.StageSynthesize: jobs.LaneGPU, dub.StageMix: jobs.LaneCPU, dub.StageQC: jobs.LaneGPU,
}

func stageTitle(st dub.Stage, title, lang string) string {
	switch st {
	case dub.StageFetch:
		return i18n.T("Dub: download %s", title)
	case dub.StageIngest:
		return i18n.T("Dub: read %s", title)
	case dub.StageSeparate:
		return i18n.T("Dub: separate voices of %s", title)
	case dub.StageTranscribe:
		return i18n.T("Dub: transcribe %s", title)
	case dub.StageReferences:
		return i18n.T("Dub: pick voices of %s", title)
	case dub.StageTranslate:
		return i18n.T("Dub: translate %s into %s", title, lang)
	case dub.StageSynthesize:
		return i18n.T("Dub: speak %s in %s", title, lang)
	case dub.StageMix:
		return i18n.T("Dub: mix %s in %s", title, lang)
	case dub.StageQC:
		return i18n.T("Dub: verify %s in %s", title, lang)
	}
	return title
}

// Run queues the stages a request needs, as one graph, and returns the
// job ids by stage key. Stages already done are skipped unless forced;
// a stage whose prerequisites are not done brings them in.
func (s *Dubs) Run(ctx context.Context, id string, req RunRequest) (map[string]string, error) {
	if s.cfg.Scheduler == nil {
		return nil, fmt.Errorf("dub: no job queue: %w", core.ErrUnsupported)
	}
	p, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.Source.Path == "" && p.Source.URL == "" {
		return nil, fmt.Errorf("dub %s: no source: %w", id, core.ErrInvalid)
	}
	active, _ := s.Active(ctx, id)
	if len(active) > 0 {
		return nil, fmt.Errorf("dub %s: a run is going on: %w", id, core.ErrBusy)
	}
	nodes := s.plan(p, req)
	if len(nodes) == 0 {
		return map[string]string{}, nil
	}
	ids, err := s.cfg.Scheduler.SubmitGraph(ctx, nodes)
	if err != nil {
		return nil, err
	}
	s.Update(ctx, id, func(p *dub.Project) error {
		p.Run = &dub.Run{Through: req.Through, Langs: req.Langs, At: s.cfg.Now(), Batch: req.Batch}
		return nil
	})
	return ids, nil
}

// Plan lists the stage keys a request would run (for the UI's summary).
func (s *Dubs) Plan(p *dub.Project, req RunRequest) []string {
	var out []string
	for _, n := range s.plan(p, req) {
		out = append(out, n.Key)
	}
	return out
}

func (s *Dubs) plan(p *dub.Project, req RunRequest) []jobs.Node {
	done := func(st dub.Stage, lang string) bool {
		if slices.Contains(req.Force, st) {
			return false
		}
		switch p.Stage(st, lang).Status {
		case dub.StatusDone, dub.StatusSkipped:
			return true
		}
		return false
	}
	order := map[dub.Stage]int{dub.StageReferences: 1, dub.StageTranslate: 2, dub.StageSynthesize: 3, dub.StageMix: 3, dub.StageQC: 4}
	through := order[req.Through]
	if through == 0 {
		through = 3
	}
	prio := req.Priority
	if req.Batch && prio == 0 {
		prio = jobs.PriorityBackground
	}
	var nodes []jobs.Node
	add := func(st dub.Stage, lang string, after []string, only []string) string {
		key := dub.StageKey(st, lang)
		params, _ := json.Marshal(StageParams{Dub: p.ID, Lang: lang, Force: slices.Contains(req.Force, st), Only: only})
		lane := stageLanes[st]
		if st == dub.StageTranslate && s.cfg.TranslateLane != nil {
			lane = s.cfg.TranslateLane()
		}
		nodes = append(nodes, jobs.Node{Key: key, Spec: jobs.Spec{
			Kind: stageKinds[st], Lane: lane, Priority: prio, Params: params, Title: stageTitle(st, p.Title, lang),
			Owner: DubOwner + p.ID, After: after, MaxAttempts: 2, Idempotent: true,
		}})
		return key
	}
	var last []string
	chain := func(st dub.Stage) {
		if !done(st, "") {
			last = []string{add(st, "", last, nil)}
		}
	}
	// QC alone checks tracks that exist.
	if req.Through == dub.StageQC {
		for _, l := range langsOf(p, req.Langs) {
			after := []string(nil)
			if p.Tracks[l].File == "" {
				after = []string{add(dub.StageSynthesize, l, nil, nil)}
				after = []string{add(dub.StageMix, l, after, nil)}
			}
			add(dub.StageQC, l, after, nil)
		}
		return nodes
	}
	if p.Source.Kind == dub.SourceURL && p.Source.Path == "" {
		chain(dub.StageFetch)
	}
	chain(dub.StageIngest)
	if p.Settings.KeepBackground || slices.Contains(req.Force, dub.StageSeparate) {
		chain(dub.StageSeparate)
	}
	transcribing := false
	if len(p.Segments) == 0 || slices.Contains(req.Force, dub.StageTranscribe) {
		last = []string{add(dub.StageTranscribe, "", last, nil)}
		transcribing = true
	}
	refsStale := false
	for _, sp := range p.SpeakerLabels() {
		if !slices.Contains(p.RefTried, sp) {
			refsStale = true
		}
	}
	if transcribing || refsStale || !done(dub.StageReferences, "") {
		last = []string{add(dub.StageReferences, "", last, nil)}
	}
	if through < 2 {
		return nodes
	}
	prep := last
	for _, l := range langsOf(p, req.Langs) {
		after := prep
		if len(p.Untranslated(l)) > 0 || slices.Contains(req.Force, dub.StageTranslate) || len(p.Segments) == 0 {
			after = []string{add(dub.StageTranslate, l, prep, nil)}
		}
		if through < 3 {
			continue
		}
		after = []string{add(dub.StageSynthesize, l, after, req.Only)}
		add(dub.StageMix, l, after, nil)
	}
	return nodes
}

// langsOf are the languages a request runs in.
func langsOf(p *dub.Project, langs []string) []string {
	if len(langs) > 0 {
		return langs
	}
	return p.Languages()
}

// Resume continues an interrupted run (Project.Run) where it stopped.
func (s *Dubs) Resume(ctx context.Context, id string) (map[string]string, error) {
	p, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.Run == nil {
		return nil, fmt.Errorf("dub %s: nothing to resume: %w", id, core.ErrInvalid)
	}
	return s.Run(ctx, id, RunRequest{Through: p.Run.Through, Langs: p.Run.Langs, Batch: p.Run.Batch})
}

// Interrupted reports whether p's last run stopped without finishing (no
// job of it is left, yet it did not get through).
func Interrupted(p *dub.Project, active []jobs.Job) bool {
	if p.Run == nil || len(active) > 0 {
		return false
	}
	for _, st := range p.Stages {
		if st.Status == dub.StatusRunning || st.Status == dub.StatusFailed {
			return true
		}
	}
	return false
}

// --- stage plumbing --------------------------------------------------------------------

// stageFunc does a stage's work on a copy of the project and returns the
// change to save (applied to the latest saved project) and warnings.
type stageFunc func(ctx context.Context, sc *stageCtx) error

// stageCtx is a running stage.
type stageCtx struct {
	s        *Dubs
	job      jobs.Job
	params   StageParams
	p        *dub.Project // as it was when the stage started
	report   func(jobs.Progress)
	warnings []string
	// skipped: the stage could not run (the engine lacks it): code.
	skipped string
}

func (sc *stageCtx) warn(code string) {
	if !slices.Contains(sc.warnings, code) {
		sc.warnings = append(sc.warnings, code)
	}
}

func (sc *stageCtx) dir() string { return sc.s.Dir(sc.p.ID) }

func (sc *stageCtx) path(name string) string { return sc.s.Path(sc.p.ID, name) }

// save applies fn to the latest project.
func (sc *stageCtx) save(ctx context.Context, fn func(*dub.Project)) error {
	_, err := sc.s.Update(ctx, sc.p.ID, func(p *dub.Project) error {
		fn(p)
		return nil
	})
	return err
}

// stage wraps a stage's work as an executor: it marks the stage running,
// then done (or skipped, or failed with the error's code), and finishes
// the run when it was its last stage.
func (s *Dubs) stage(st dub.Stage, fn stageFunc) jobs.Executor {
	return jobs.ExecutorFunc(func(ctx context.Context, job jobs.Job, report func(jobs.Progress), _ func(json.RawMessage)) (json.RawMessage, error) {
		var params StageParams
		if err := json.Unmarshal(job.Params, &params); err != nil || params.Dub == "" {
			return nil, &jobs.Error{Code: proto.CodeBadRequest, Message: "dub stage: bad params"}
		}
		p, err := s.Get(ctx, params.Dub)
		if err != nil {
			if errors.Is(err, core.ErrNotFound) {
				return nil, &jobs.Error{Code: DubCodeNoSource, Message: err.Error()}
			}
			return nil, err
		}
		key := dub.StageKey(st, params.Lang)
		mark := func(state dub.StageState) {
			state.Job, state.At = job.ID, s.cfg.Now()
			s.Update(context.WithoutCancel(ctx), p.ID, func(p *dub.Project) error {
				p.SetStage(st, params.Lang, state)
				if state.Status == dub.StatusDone || state.Status == dub.StatusSkipped {
					s.endRun(p)
				}
				return nil
			})
		}
		mark(dub.StageState{Status: dub.StatusRunning})
		sc := &stageCtx{s: s, job: job, params: params, p: p, report: report}
		err = fn(ctx, sc)
		switch {
		case err == nil && sc.skipped != "":
			mark(dub.StageState{Status: dub.StatusSkipped, Code: sc.skipped, Warnings: sc.warnings})
			return json.Marshal(map[string]any{"stage": key, "skipped": sc.skipped, "warnings": sc.warnings})
		case err == nil:
			mark(dub.StageState{Status: dub.StatusDone, Warnings: sc.warnings})
			return json.Marshal(map[string]any{"stage": key, "warnings": sc.warnings})
		case ctx.Err() != nil:
			mark(dub.StageState{Status: dub.StatusNone, Code: proto.CodeCancelled})
			return nil, ctx.Err()
		}
		var je *jobs.Error
		if !errors.As(err, &je) {
			je = &jobs.Error{Code: DubCodeFailed, Message: err.Error()}
		}
		mark(dub.StageState{Status: dub.StatusFailed, Code: je.Code, Message: je.Message, Warnings: sc.warnings})
		return nil, je
	})
}

// endRun forgets the run once nothing of it is left to do.
func (s *Dubs) endRun(p *dub.Project) {
	if p.Run == nil {
		return
	}
	for _, st := range p.Stages {
		if st.Status == dub.StatusRunning || st.Status == dub.StatusFailed {
			return
		}
	}
	through := p.Run.Through
	switch through {
	case dub.StageReferences:
		if p.Stage(dub.StageReferences, "").Status != dub.StatusDone && p.Stage(dub.StageReferences, "").Status != dub.StatusSkipped {
			return
		}
	case dub.StageTranslate:
		for _, l := range langsOf(p, p.Run.Langs) {
			if len(p.Untranslated(l)) > 0 && p.Stage(dub.StageTranslate, l).Status != dub.StatusDone {
				return
			}
		}
	case dub.StageQC:
		for _, l := range langsOf(p, p.Run.Langs) {
			if p.Stage(dub.StageQC, l).Status != dub.StatusDone {
				return
			}
		}
	default:
		for _, l := range langsOf(p, p.Run.Langs) {
			if p.Stage(dub.StageMix, l).Status != dub.StatusDone {
				return
			}
		}
	}
	p.Run = nil
}

// call runs an engine method inside a stage's job; with model set, its
// local snapshot is filled into *modelPath first.
func (sc *stageCtx) call(ctx context.Context, method, model string, params any, modelPath *string, out any, report func(jobs.Progress)) error {
	s := sc.s
	if model != "" && modelPath != nil && s.cfg.ModelPath != nil {
		dir, err := s.cfg.ModelPath(ctx, model)
		if err != nil {
			if errors.Is(err, core.ErrNotDownloaded) {
				data, _ := json.Marshal(map[string]string{"model": model})
				return &jobs.Error{Code: proto.CodeModelNotDownloaded, Message: err.Error(), Data: data}
			}
			return &jobs.Error{Code: proto.CodeInternal, Message: err.Error()}
		}
		*modelPath = dir
	}
	var ex jobs.Executor
	if s.cfg.Engine != nil {
		ex = s.cfg.Engine(method)
	}
	if ex == nil {
		return &jobs.Error{Code: CodeEngineUnavailable, Message: "The engine is not connected.", Retryable: true}
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	if report == nil {
		report = func(jobs.Progress) {}
	}
	sub := jobs.Job{ID: sc.job.ID + "." + method, Spec: jobs.Spec{Kind: method, Lane: jobs.Lane(dub.RoleOf(method)), Params: raw, Owner: sc.job.Owner}}
	res, err := ex.Run(ctx, sub, report, nil)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(res, out)
}

// missing reports an engine that lacks a method (or a model it needs is
// not downloaded): the stage is skipped rather than failed.
func missing(err error) bool {
	var je *jobs.Error
	if !errors.As(err, &je) {
		return false
	}
	return je.Code == proto.CodeMethodNotFound || je.Code == proto.CodeModelNotDownloaded || je.Code == proto.CodeUnsupported
}

// code is a stage error's code.
func code(err error) string {
	var je *jobs.Error
	if errors.As(err, &je) {
		return je.Code
	}
	return ""
}

func stageErr(code, format string, args ...any) error {
	return &jobs.Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// progress reports a fraction of a stage.
func (sc *stageCtx) progress(stage string, fraction float64, msg string) {
	if sc.report != nil {
		sc.report(jobs.Progress{Stage: stage, Fraction: fraction, Message: msg})
	}
}

// relName makes a path relative to the project's directory when inside it.
func (sc *stageCtx) relName(path string) string {
	if rel, err := filepath.Rel(sc.dir(), path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}

// dubFirst is the first non-empty string.
func dubFirst(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}
