package services

// Workflows (M6): WorkflowStudio's library and runs. A workflow
// (internal/workflow.Workflow) is saved by the editor's autosave in
// migration 006's table; its last run (workflow.Run: every item's
// checkpoint at every step) is saved beside it by the run's jobs. Files
// live in <data>/workflows/<id>/:
//
//	items/<item>.txt          the scripts, one per item (Start)
//	out/<item>/<step>.<ext>   each step's result for each item (WAV, TXT, JSON transcript)
//
// A run is one job per step that has work, kind "workflow.step", owned
// by "workflow:<id>", on the lane of the step's resources (Speak,
// Transcribe and Convert on the gpu lane; Translate on the network's;
// Source on the media lane; the others on the cpu lane), each after the
// jobs of the steps feeding it. A step's job takes the items one after
// another (a warm model on the gpu, as renders do with their chunks), so
// no job waits for another inside its executor. Each result is saved as
// it is made, with its recipe key (workflow.StepKeys): a run cancelled,
// or interrupted by a quit, goes on where it stopped, and a run after an
// edit makes again only the results whose recipe changed. One item
// failing fails that item, not the run; the run stops only when a step
// cannot work at all (a model not downloaded, the engine gone).
//
// Files: workflow.go (types, the library), workflow_run.go (runs),
// workflow_steps.go (a step's job), workflow_step_kinds.go (each kind of
// step), workflow_outputs.go (results).

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/store"
	"github.com/hehehai/AudioSmith/internal/translate"
	"github.com/hehehai/AudioSmith/internal/workflow"
)

// KindWorkflowStep is the job kind of a workflow step's run.
const KindWorkflowStep = "workflow.step"

// WorkflowOwner prefixes the owners of a workflow's jobs: "workflow:<id>".
const WorkflowOwner = "workflow:"

// Error codes of workflow runs (besides the engine's and the translators').
const (
	WorkflowCodeInvalid  = "workflow.invalid"   // the graph has errors (Run refuses)
	WorkflowCodeBusy     = "workflow.busy"      // a run is going on
	WorkflowCodeNoInput  = "workflow.no_input"  // a step got no value
	WorkflowCodeNoSTT    = "workflow.no_stt"    // no speech-to-text model
	WorkflowCodeNoSpeech = "workflow.no_speech" // transcription found nothing
	WorkflowCodeEmpty    = "workflow.empty"     // a step made empty text or silence
	WorkflowCodeFile     = "workflow.file"      // a clip or a result file can't be read or written
	WorkflowCodeVoice    = "workflow.voice"     // the step's voice can't be used
	WorkflowCodeEncode   = "workflow.encode"    // M4A needs the engine's media.encode
)

// WorkflowChanged is published after a workflow, or its run, is saved
// or deleted. Topic "workflow.changed".
type WorkflowChanged struct {
	ID string
	// Record sums up the workflow (as List returns it).
	Record store.WorkflowRecord
	// Workflow is a copy of the document when it changed (nil when only
	// the run did); Run a copy of the run when it changed.
	Workflow *workflow.Workflow
	Run      *workflow.Run
	Deleted  bool
}

func (WorkflowChanged) Topic() string { return "workflow.changed" }

// WorkflowSTT are the speech-to-text models Transcribe and Convert use.
type WorkflowSTT struct {
	Model, Aligner string
	Words          bool // the model times words itself
	// Languages, Other and OtherWords: as DubModels' STTLanguages,
	// OtherSTT and OtherWordTimes.
	Languages  []string
	Other      string
	OtherWords bool
}

// For is the model for a language (BCP-47; "" = detected) and whether
// it times words itself.
func (m WorkflowSTT) For(lang string) (string, bool) {
	if pickOther(m.Languages, m.Other, lang) {
		return m.Other, m.OtherWords
	}
	return m.Model, m.Words
}

// WorkflowsConfig wires the workflow service. Store, Bus and Scheduler
// are required.
type WorkflowsConfig struct {
	Store     store.Workflows
	Bus       *core.Bus
	Scheduler jobs.Scheduler
	// Engine returns the executor of an engine method (stt.transcribe,
	// stt.align, media.decode, media.encode); nil, or a nil executor,
	// fails the step with engine_unavailable.
	Engine func(method string) jobs.Executor
	// Render speaks (svc.Render).
	Render    *Takes
	ModelPath func(ctx context.Context, repo string) (string, error)
	STT       func() WorkflowSTT
	// Caps describes a TTS model (as the dub pipeline's).
	Caps func(repo string) DubModelCaps
	// Voice resolves a voice reference for a model (as the dub
	// pipeline's: saved voices, "speaker:", "preset:"…).
	Voice func(ctx context.Context, ref, model, lang string) (dub.VoiceSpec, error)
	// Translator returns a translator by name ("" = the language model).
	Translator func(ctx context.Context, name string) (translate.Translator, error)
	// TranslateLane is the lane of Translate steps (net; gpu for the
	// engine's own LLM). nil = net.
	TranslateLane func() jobs.Lane
	// Check adds the app's findings to validation (models, voices,
	// translators); nil adds none.
	Check workflow.Check
	// Salt is what else results depend on (workflow.Salt); nil = the
	// speech-to-text model of STT.
	Salt  workflow.Salt
	Now   func() time.Time
	Logf  func(string, ...any)
	Async func(func())
}

// Workflows is the workflow service. Goroutine-safe.
type Workflows struct {
	cfg WorkflowsConfig
	mu  sync.Mutex // serializes read-modify-write of documents and runs
}

// NewWorkflows builds the service, registers its executor and follows
// its jobs (to close runs).
func NewWorkflows(cfg WorkflowsConfig) *Workflows {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logf == nil {
		cfg.Logf = log.Printf
	}
	if cfg.Async == nil {
		cfg.Async = func(fn func()) { go fn() }
	}
	if cfg.STT == nil {
		cfg.STT = func() WorkflowSTT { return WorkflowSTT{} }
	}
	if cfg.Caps == nil {
		cfg.Caps = func(string) DubModelCaps { return DubModelCaps{Clone: true, Speed: true} }
	}
	s := &Workflows{cfg: cfg}
	if cfg.Scheduler != nil {
		cfg.Scheduler.Register(KindWorkflowStep, s.Executor())
	}
	if cfg.Bus != nil {
		core.On(cfg.Bus, func(u jobs.Updated) {
			if u.Chunk == nil && u.Job.Kind == KindWorkflowStep && (u.Job.State.Terminal() || u.Job.State == jobs.StateInterrupted) {
				id := strings.TrimPrefix(u.Job.Owner, WorkflowOwner)
				var p workflowStepParams
				json.Unmarshal(u.Job.Params, &p)
				s.cfg.Async(func() { s.settle(context.Background(), id, p.Run) })
			}
		})
	}
	return s
}

// Executor runs workflow.step jobs (registered by NewWorkflows).
func (s *Workflows) Executor() jobs.Executor { return jobs.ExecutorFunc(s.step) }

// --- the library ---------------------------------------------------------------------

// Dir is the directory of workflow id's files.
func (s *Workflows) Dir(id string) string { return filepath.Join(s.cfg.Store.Dir(), id) }

// Path is a result file's absolute path ("" for "").
func (s *Workflows) Path(id, name string) string {
	if name == "" || filepath.IsAbs(name) {
		return name
	}
	return filepath.Join(s.Dir(id), filepath.FromSlash(name))
}

// Summary is a workflow's steps in a line ("Start → Speak → Export"), in
// English, for search.
func WorkflowSummary(w *workflow.Workflow) string {
	var parts []string
	for _, n := range w.Nodes {
		parts = append(parts, string(n.Kind))
	}
	return strings.Join(parts, " → ")
}

func (s *Workflows) record(w *workflow.Workflow) store.WorkflowRecord {
	return store.WorkflowRecord{ID: w.ID, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt, Name: w.Name, Summary: WorkflowSummary(w)}
}

// Save creates or updates a workflow (the editor's autosave), normalised.
func (s *Workflows) Save(ctx context.Context, w *workflow.Workflow) (*workflow.Workflow, error) {
	var oldName string
	created := true
	if w.ID != "" {
		if old, err := s.cfg.Store.Get(ctx, w.ID); err == nil {
			oldName, created = old.Name, false
		}
	}
	saved, err := s.save(ctx, w)
	name, id := w.Name, w.ID
	if saved != nil {
		name, id = saved.Name, saved.ID
	}
	logSave(ctx, "workflow", created, oldName, id, name, err)
	return saved, err
}

func (s *Workflows) save(ctx context.Context, w *workflow.Workflow) (*workflow.Workflow, error) {
	w = w.Clone()
	if w.ID == "" {
		w.ID = core.NewID(s.cfg.Now())
	}
	if w.CreatedAt.IsZero() {
		w.CreatedAt = s.cfg.Now()
	}
	w.UpdatedAt = s.cfg.Now()
	w.Normalize()
	doc, err := json.Marshal(w)
	if err != nil {
		return nil, err
	}
	rec := s.record(w)
	rec.Doc = doc
	s.mu.Lock()
	err = s.cfg.Store.PutDoc(ctx, rec)
	if err == nil {
		if old, gerr := s.cfg.Store.Get(ctx, w.ID); gerr == nil {
			rec.State, rec.Items, rec.Done = old.State, old.Items, old.Done
		}
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	rec.Doc = nil
	s.publish(WorkflowChanged{ID: w.ID, Record: rec, Workflow: w.Clone()})
	return w, nil
}

func (s *Workflows) publish(ev WorkflowChanged) {
	if s.cfg.Bus != nil {
		s.cfg.Bus.Publish(ev)
	}
}

// Get reads a workflow and its last run (nil before the first).
func (s *Workflows) Get(ctx context.Context, id string) (*workflow.Workflow, *workflow.Run, error) {
	r, err := s.cfg.Store.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	return decodeWorkflow(r)
}

func decodeWorkflow(r store.WorkflowRecord) (*workflow.Workflow, *workflow.Run, error) {
	w := &workflow.Workflow{}
	if err := json.Unmarshal(r.Doc, w); err != nil {
		return nil, nil, fmt.Errorf("workflow %s: %w", r.ID, err)
	}
	w.ID = r.ID
	w.Normalize()
	var run *workflow.Run
	if len(r.Run) > 0 {
		run = &workflow.Run{}
		if err := json.Unmarshal(r.Run, run); err != nil {
			run = nil // a damaged run is forgotten: the next one starts over
		}
	}
	return w, run, nil
}

// List lists the workflows (without documents), most recent first.
func (s *Workflows) List(ctx context.Context) ([]store.WorkflowRecord, error) {
	return s.cfg.Store.List(ctx)
}

// Delete cancels a workflow's run and removes it with its files.
func (s *Workflows) Delete(ctx context.Context, id string) error {
	s.Cancel(ctx, id)
	s.mu.Lock()
	err := s.cfg.Store.Delete(ctx, id)
	s.mu.Unlock()
	logDelete(ctx, "workflow", id, "", err)
	if err != nil {
		return err
	}
	if dir := s.Dir(id); filepath.Base(dir) == id && id != "" {
		os.RemoveAll(dir)
	}
	s.publish(WorkflowChanged{ID: id, Deleted: true})
	return nil
}

// Validate checks a workflow with the app's own findings.
func (s *Workflows) Validate(w *workflow.Workflow) *workflow.Report {
	return workflow.Validate(w, s.cfg.Check)
}
