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

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/i18n"
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

// --- runs ------------------------------------------------------------------------------

// WorkflowInvalidError is Run's refusal of a workflow with errors.
type WorkflowInvalidError struct{ Report *workflow.Report }

func (e *WorkflowInvalidError) Error() string {
	for _, d := range e.Report.Diags {
		if d.Severity == workflow.Error {
			return "workflow: " + d.Text()
		}
	}
	return "workflow: invalid"
}

func (e *WorkflowInvalidError) Unwrap() error { return core.ErrInvalid }

// Active lists a workflow's unfinished jobs.
func (s *Workflows) Active(ctx context.Context, id string) ([]jobs.Job, error) {
	if s.cfg.Scheduler == nil {
		return nil, nil
	}
	return s.cfg.Scheduler.List(ctx, jobs.Filter{Owner: WorkflowOwner + id})
}

// RunOptions tune a run.
type RunOptions struct {
	// Again makes every result anew (Start a new run), not only the ones
	// whose recipe changed.
	Again    bool
	Priority jobs.Priority
}

// workflowStepParams are a step job's params.
type workflowStepParams struct {
	Workflow string `json:"workflow"`
	Run      string `json:"run"`
	Node     string `json:"node"`
}

// Run starts (or resumes) a run: the results whose recipe is unchanged
// are kept, the others made by one job per step. It refuses an invalid
// workflow (*WorkflowInvalidError) and a workflow already running
// (core.ErrBusy). A run with nothing to do finishes at once.
func (s *Workflows) Run(ctx context.Context, id string, o RunOptions) (*workflow.Run, error) {
	if s.cfg.Scheduler == nil {
		return nil, fmt.Errorf("workflow: no job queue: %w", core.ErrUnsupported)
	}
	if active, _ := s.Active(ctx, id); len(active) > 0 {
		return nil, fmt.Errorf("workflow %s: a run is going on: %w", id, core.ErrBusy)
	}
	w, prev, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	rep := s.Validate(w)
	if !rep.OK() {
		return nil, &WorkflowInvalidError{Report: rep}
	}
	items := w.Items()
	keys := workflow.StepKeys(w, rep, items, s.salt())
	run, steps := workflow.Plan(w, rep, items, keys, prev, o.Again, func(v *workflow.Value) bool {
		_, err := os.Stat(s.Path(id, v.File))
		return err == nil
	})
	run.ID = core.NewID(s.cfg.Now())
	run.StartedAt = s.cfg.Now()
	// The scripts, as files the steps read.
	dir := s.Dir(id)
	for _, it := range items {
		if it.Text == "" {
			continue
		}
		path := filepath.Join(dir, filepath.FromSlash(workflow.ItemFile(it.Key)))
		if err := wfWriteFile(path, []byte(it.Text)); err != nil {
			return nil, err
		}
	}
	s.prune(id, items)
	if len(steps) == 0 {
		run.State, run.EndedAt = workflow.RunDone, s.cfg.Now()
		if err := s.putRun(ctx, id, run); err != nil {
			return nil, err
		}
		return run, nil
	}
	run.State = workflow.RunRunning
	// One job per step with work, after the jobs of the steps before it.
	inPlan := map[string]bool{}
	for _, st := range steps {
		inPlan[st] = true
	}
	var nodes []jobs.Node
	for _, st := range steps {
		n := w.Node(st)
		params, _ := json.Marshal(workflowStepParams{Workflow: id, Run: run.ID, Node: st})
		nodes = append(nodes, jobs.Node{Key: st, Spec: jobs.Spec{
			Kind: KindWorkflowStep, Lane: s.lane(n), Priority: o.Priority, Params: params,
			Title: i18n.T("Workflow %s: %s", w.Name, n.StepName(n.Kind.Label())), Owner: WorkflowOwner + id,
			After: ancestorsIn(w, st, inPlan), MaxAttempts: 2, Idempotent: true,
		}})
	}
	// The run is saved before its jobs start, so that they find it.
	if err := s.putRun(ctx, id, run); err != nil {
		return nil, err
	}
	if _, err := s.cfg.Scheduler.SubmitGraph(ctx, nodes); err != nil {
		s.update(ctx, id, run.ID, func(r *workflow.Run) { r.State, r.Code, r.Err = workflow.RunFailed, proto500, err.Error() })
		return nil, err
	}
	return run, nil
}

const proto500 = "internal"

// ancestorsIn are the nearest steps before id that are in the plan.
func ancestorsIn(w *workflow.Workflow, id string, in map[string]bool) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(string)
	walk = func(n string) {
		for _, e := range w.Incoming(n) {
			if seen[e.From] {
				continue
			}
			seen[e.From] = true
			if in[e.From] {
				out = append(out, e.From)
				continue
			}
			walk(e.From)
		}
	}
	walk(id)
	return out
}

// lane is where a step's job runs.
func (s *Workflows) lane(n *workflow.Node) jobs.Lane {
	switch n.Kind {
	case workflow.KindSpeak, workflow.KindTranscribe, workflow.KindConvert:
		return jobs.LaneGPU
	case workflow.KindTranslate:
		if s.cfg.TranslateLane != nil {
			return s.cfg.TranslateLane()
		}
		return jobs.LaneNet
	case workflow.KindSource:
		return jobs.LaneMedia
	case workflow.KindExport:
		if n.P.Format == workflow.FormatM4A {
			return jobs.LaneMedia
		}
	}
	return jobs.LaneCPU
}

// salt is what results depend on besides their steps.
func (s *Workflows) salt() workflow.Salt {
	if s.cfg.Salt != nil {
		return s.cfg.Salt
	}
	stt := s.cfg.STT()
	return func(n *workflow.Node) string {
		switch n.Kind {
		case workflow.KindTranscribe, workflow.KindConvert:
			return stt.Model + "|" + stt.Aligner
		}
		return ""
	}
}

// prune removes the results of items gone from the inputs.
func (s *Workflows) prune(id string, items []workflow.Item) {
	keep := map[string]bool{}
	for _, it := range items {
		keep[it.Key] = true
	}
	for _, sub := range []string{"out", "items"} {
		entries, _ := os.ReadDir(filepath.Join(s.Dir(id), sub))
		for _, e := range entries {
			name := strings.TrimSuffix(e.Name(), ".txt")
			if !keep[name] {
				os.RemoveAll(filepath.Join(s.Dir(id), sub, e.Name()))
			}
		}
	}
}

// Cancel stops a workflow's run: its jobs are cancelled, the steps they
// were on go back to waiting, and the run can resume later.
func (s *Workflows) Cancel(ctx context.Context, id string) error {
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
	s.update(ctx, id, "", func(r *workflow.Run) {
		if r.State != workflow.RunRunning {
			return
		}
		r.State, r.EndedAt = workflow.RunCancelled, s.cfg.Now()
		for i := range r.Items {
			for _, st := range r.Items[i].Steps {
				if st.Status == workflow.StepRunning {
					st.Status = workflow.StepPending
				}
			}
		}
	})
	return errors.Join(errs...)
}

// putRun saves a run and publishes it.
func (s *Workflows) putRun(ctx context.Context, id string, run *workflow.Run) error {
	s.mu.Lock()
	rec, err := s.writeRun(ctx, id, run)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	s.publish(WorkflowChanged{ID: id, Record: rec, Run: run.Clone()})
	return nil
}

// writeRun saves a run; the caller holds s.mu.
func (s *Workflows) writeRun(ctx context.Context, id string, run *workflow.Run) (store.WorkflowRecord, error) {
	data, err := json.Marshal(run)
	if err != nil {
		return store.WorkflowRecord{}, err
	}
	done, _, total := run.Counts()
	rec := store.WorkflowRecord{ID: id, State: string(run.State), Items: total, Done: done, Run: data}
	if err := s.cfg.Store.PutRun(ctx, rec); err != nil {
		return rec, err
	}
	if cur, err := s.cfg.Store.Get(ctx, id); err == nil {
		cur.Doc, cur.Run = nil, nil
		rec = cur
	}
	rec.Run = nil
	return rec, nil
}

// update applies fn to the saved run (when it is run runID; "" = any),
// saves it and publishes it. It returns the run after fn.
func (s *Workflows) update(ctx context.Context, id, runID string, fn func(*workflow.Run)) (*workflow.Run, error) {
	s.mu.Lock()
	r, err := s.cfg.Store.Get(ctx, id)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	_, run, err := decodeWorkflow(r)
	if err != nil || run == nil || (runID != "" && run.ID != runID) {
		s.mu.Unlock()
		if err == nil {
			err = fmt.Errorf("workflow %s: run %s is over: %w", id, runID, core.ErrNotFound)
		}
		return nil, err
	}
	fn(run)
	rec, err := s.writeRun(ctx, id, run)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	s.publish(WorkflowChanged{ID: id, Record: rec, Run: run.Clone()})
	return run, nil
}

// settle closes a run once none of its jobs is left: done, or failed
// when an item failed or a step could not work.
func (s *Workflows) settle(ctx context.Context, id, runID string) {
	if active, err := s.Active(ctx, id); err != nil || len(active) > 0 {
		return
	}
	s.update(ctx, id, runID, func(r *workflow.Run) {
		if r.State != workflow.RunRunning {
			return
		}
		failed, unfinished := false, false
		for i := range r.Items {
			for _, st := range r.Items[i].Steps {
				switch st.Status {
				case workflow.StepFailed:
					failed = true
				case workflow.StepPending, workflow.StepRunning:
					unfinished = true
					st.Status = workflow.StepPending
				}
			}
		}
		r.EndedAt = s.cfg.Now()
		switch {
		case failed || unfinished:
			r.State = workflow.RunFailed
		default:
			r.State = workflow.RunDone
		}
	})
}

// wfWriteFile writes a file atomically, making its folder.
func wfWriteFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}
