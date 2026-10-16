package services

// A workflow run's results: the final outputs item by item (Outputs) and
// exporting them all to a folder (ExportAll); split from
// workflow_steps.go.

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/hehehai/AudioSmith/internal/workflow"
)

// WorkflowOutput is a result the Results list shows: an item's value at
// a step with nothing after it (an export, or the end of a branch).
type WorkflowOutput struct {
	Item  string // the item's key
	Index int    // its place, 0-based
	Name  string // the item's name
	Node  string
	Value workflow.Value
}

// Outputs are a run's final results, item by item, in the graph's order.
func Outputs(w *workflow.Workflow, run *workflow.Run) []WorkflowOutput {
	if run == nil {
		return nil
	}
	rep := workflow.Validate(w, nil)
	var out []WorkflowOutput
	for i, it := range run.Items {
		for _, id := range rep.Order {
			n := w.Node(id)
			if n == nil || n.Kind == workflow.KindStart || len(w.Outgoing(id)) > 0 && n.Kind != workflow.KindExport {
				continue
			}
			s := it.Steps[id]
			if s == nil || s.Status != workflow.StepDone || s.Out == nil {
				continue
			}
			out = append(out, WorkflowOutput{Item: it.Key, Index: i, Name: it.Name, Node: id, Value: *s.Out})
		}
	}
	return out
}

// ExportAll copies every final result of the last run into dir, named by
// template (workflow.DefaultTemplate when ""), audio as WAV, and returns
// the files written.
func (s *Workflows) ExportAll(ctx context.Context, id, dir, template string) ([]string, error) {
	w, run, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if template == "" {
		template = workflow.DefaultTemplate
	}
	var written []string
	used := map[string]int{}
	for _, o := range Outputs(w, run) {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		n := w.Node(o.Node)
		format := workflow.ExportFormat("", o.Value.Type)
		if n.Kind == workflow.KindExport {
			format = workflow.ExportFormat(n.P.Format, o.Value.Type)
			if format == workflow.FormatM4A {
				format = workflow.FormatWAV
			}
		}
		base := workflow.FileName(template, workflow.NameVars{Workflow: w.Name, N: o.Index + 1, Item: o.Name,
			Step: n.StepName(n.Kind.Label()), Date: s.cfg.Now(), Lang: o.Value.Lang})
		if used[base]++; used[base] > 1 {
			base = fmt.Sprintf("%s-%d", base, used[base])
		}
		dst := filepath.Join(dir, base+"."+format)
		v := o.Value
		if filepath.IsAbs(v.File) {
			// An export's copy: from the step before it.
			if in, _ := wfInput(w, &run.Items[o.Index], o.Node); in != nil {
				v = *in
			}
		}
		if err := s.write(ctx, id, &v, format, dst, nil); err != nil {
			return written, err
		}
		written = append(written, dst)
	}
	return written, nil
}
