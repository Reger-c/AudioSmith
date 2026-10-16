package services

// Workflow runs: Run (one job per step with work, each after the steps
// feeding it), lanes and salts, Cancel, and the run's record kept up to
// date by its jobs (putRun, writeRun, update, settle); split from
// workflow.go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/store"
	"github.com/hehehai/AudioSmith/internal/workflow"
)

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
	// Inputs, when set, are one-off inputs of this run in place of the
	// input step's (an agent's run, thread #35): the saved workflow is not
	// changed, and the results of its own inputs are kept.
	Inputs *WorkflowInputs
}

// WorkflowInputs are a run's own inputs: scripts for a Start step, clips
// for a Source step.
type WorkflowInputs struct {
	Scripts []workflow.Script
	Clips   []workflow.Clip
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
	if o.Inputs != nil {
		in := w.InputNode()
		if in == nil {
			return nil, fmt.Errorf("workflow %s: no input step: %w", id, core.ErrInvalid)
		}
		in.P.Scripts, in.P.Clips = o.Inputs.Scripts, o.Inputs.Clips
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
	if o.Inputs == nil {
		s.prune(id, items)
	}
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
			return stt.Model + "|" + stt.Other + "|" + stt.Aligner
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
