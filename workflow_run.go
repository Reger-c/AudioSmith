package main

// The Run panel: what stops the workflow from running, the run's
// controls (Run, Resume, Start a new run, Cancel), its progress item by
// item, and the results with Play, Show in Finder and Export all.

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/workflow"
)

// wfRunPanel builds the Run panel.
func (a *app) wfRunPanel(c *ui.Context) {
	t := c.Theme()
	w := a.wf
	rep := a.wfReport()
	run := a.wfRun
	busy := a.wfBusy()
	if a.wfErr != "" {
		ui.Row(c.Key("wf-run-err")).Gap(px(t, 6)).Padding(px(t, 8)).Radius(6).Border(1, t.Danger).Children(func() {
			ui.Text(c, a.wfErr).FontSize(px(t, 12)).TextColor(t.Danger).Grow(1).MinWidth(0)
			if ui.Button(c.Key("x"), "×").Label(i18n.T("Dismiss")).Clicked() {
				a.wfErr = ""
			}
		})
	}
	// What stops a run.
	if errs := rep.Errors(); errs > 0 {
		ui.Text(c, i18n.N(errs, "%d problem to fix before running", "%d problems to fix before running", errs)).Bold().TextColor(t.Danger)
		for i, d := range rep.Diags {
			if d.Severity != workflow.Error {
				continue
			}
			label := d.Text()
			if n := w.Node(d.Node); n != nil {
				label = n.StepName(n.Kind.Label()) + ": " + label
			}
			node := d.Node
			if ui.ButtonBase(c.Key(fmt.Sprintf("wf-run-diag-%d", i))).Label(label).Children(func() {
				ui.Text(c, "• "+label).FontSize(px(t, 12)).TextColor(t.Danger)
			}).Clicked() && node != "" {
				a.wfSel, a.wfSelEdge, a.wfPanel = node, "", wfPanelStep
			}
		}
	} else if run == nil || a.wfStale() {
		ui.Text(c, i18n.N(rep.Items, "%d item to run", "%d items to run", rep.Items)).Bold()
	}
	if a.wfStale() && !busy {
		ui.Text(c, i18n.T("Settings changed since the last run. Running again makes only what changed.")).FontSize(px(t, 12)).TextColor(t.Warning)
	}
	// Controls.
	upToDate := run != nil && run.State == workflow.RunDone && !a.wfStale()
	ui.Row(c.Key("wf-run-buttons")).Gap(px(t, 6)).Wrap().Children(func() {
		if busy {
			if ui.Button(c.Key("wf-run-cancel"), i18n.T("Cancel")).Clicked() {
				a.wfCancel()
			}
			return
		}
		if upToDate {
			// Every result is made: only a new run makes anything.
			if ui.PrimaryButton(c.Key("wf-run-again"), i18n.T("Start a new run")).
				Tooltip(i18n.T("Make every result again, even those that are up to date")).Disabled(rep.Errors() > 0).Clicked() {
				a.wfStart(true)
			}
			return
		}
		label := i18n.T("Run workflow")
		switch {
		case a.wfResumable():
			label = i18n.T("Resume")
		case run != nil && run.State == workflow.RunFailed && !a.wfStale():
			label = i18n.T("Retry")
		}
		if ui.PrimaryButton(c.Key("wf-run-go"), label).Disabled(rep.Errors() > 0).Clicked() {
			a.wfStart(false)
		}
		if run != nil && run.Signature != "" && ui.Button(c.Key("wf-run-again"), i18n.T("Start a new run")).
			Tooltip(i18n.T("Make every result again, even those that are up to date")).Disabled(rep.Errors() > 0).Clicked() {
			a.wfStart(true)
		}
	})
	if run == nil || len(run.Items) == 0 {
		ui.Text(c, i18n.T("Results show here as each item is made. A run you cancel, or that stops when AudioSmith quits, resumes where it stopped.")).
			FontSize(px(t, 12)).TextColor(t.TextMuted)
		return
	}
	// Progress.
	done, failed, total := run.Counts()
	ui.Divider(c)
	ui.Row(c).AlignItems(ui.Center).Children(func() {
		ui.Text(c, i18n.T("%d / %d items ready", done, total)).Bold().Grow(1)
		ui.Text(c, wfRunStateText(run, busy)).FontSize(px(t, 12)).TextColor(t.TextMuted)
	})
	ui.Progress(c, float64(done)/float64(max(total, 1))).Label(i18n.T("%d / %d items ready", done, total))
	if j, ok := a.wfRunningJob(); ok {
		if n := w.Node(wfJobNode(j)); n != nil {
			msg := n.StepName(n.Kind.Label())
			if j.Progress.Message != "" {
				msg += " · " + j.Progress.Message
			}
			ui.Text(c, msg).FontSize(px(t, 12)).TextColor(t.TextMuted)
		}
	}
	if failed > 0 {
		ui.Text(c, i18n.N(failed, "%d item failed: fix it, then Retry.", "%d items failed: fix them, then Retry.", failed)).FontSize(px(t, 12)).TextColor(t.Danger)
	}
	if run.Code != "" && !busy {
		ui.Text(c, wfErrorText(run.Code, run.Err)).FontSize(px(t, 12)).TextColor(t.Danger)
	}
	outs := services.Outputs(w, run)
	if len(outs) > 0 && ui.Button(c.Key("wf-export-all"), i18n.T("Export all…")).Tooltip(i18n.T("Copy every result into a folder")).Clicked() {
		a.wfExportAll()
	}
	ui.Divider(c)
	for i := range run.Items {
		a.wfItemRow(c, i, &run.Items[i], outs)
	}
}

// wfRunStateText says where a run is.
func wfRunStateText(r *workflow.Run, busy bool) string {
	switch {
	case busy:
		return i18n.T("Running")
	case r.State == workflow.RunDone:
		if !r.EndedAt.IsZero() && !r.StartedAt.IsZero() {
			return i18n.T("Done in %s", r.EndedAt.Sub(r.StartedAt).Round(time.Second).String())
		}
		return i18n.T("Done")
	case r.State == workflow.RunFailed:
		return i18n.T("Stopped")
	case r.State == workflow.RunCancelled:
		return i18n.T("Cancelled")
	case r.State == workflow.RunRunning:
		return i18n.T("Interrupted")
	}
	return ""
}

// wfRunningJob is the open workflow's running job, if any.
func (a *app) wfRunningJob() (jobs.Job, bool) {
	for _, j := range a.wfJobs {
		if j.State == jobs.StateRunning {
			return j, true
		}
	}
	return jobs.Job{}, false
}

// wfJobNode is the step a job works for.
func wfJobNode(j jobs.Job) string {
	var p struct {
		Node string `json:"node"`
	}
	json.Unmarshal(j.Params, &p)
	return p.Node
}

// wfItemRow is an item: its state and, open, its results.
func (a *app) wfItemRow(c *ui.Context, i int, it *workflow.RunItem, outs []services.WorkflowOutput) {
	t := c.Theme()
	w := a.wf
	key := it.Key
	st := it.State()
	open := a.wfExpanded[key]
	ui.Column(c.Key("wf-item-"+key)).Gap(px(t, 4)).Padding(px(t, 6), 0).Children(func() {
		head := ui.ButtonBase(c.Key("head")).Label(it.Name).Gap(px(t, 6)).AlignItems(ui.Center)
		head.Children(func() {
			mark, col := "○", t.TextMuted
			switch st {
			case workflow.StepDone:
				mark, col = "✓", t.Success
			case workflow.StepFailed:
				mark, col = "✕", t.Danger
			case workflow.StepRunning:
				mark, col = "◐", t.Accent
			}
			ui.Text(c, mark).TextColor(col).Width(px(t, 14))
			ui.Text(c, fmt.Sprintf("%02d", i+1)).FontSize(px(t, 11)).TextColor(t.TextMuted)
			ui.Text(c, firstOf(it.Name, i18n.T("Item %d", i+1))).FontSize(px(t, 12.5)).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
			if st == workflow.StepRunning {
				for _, id := range a.wfOrder() {
					if s := it.Steps[id]; s != nil && s.Status == workflow.StepRunning {
						if n := w.Node(id); n != nil {
							ui.Text(c, n.StepName(n.Kind.Label())).FontSize(px(t, 11)).TextColor(t.Accent).SingleLine()
						}
						break
					}
				}
			}
			ui.Text(c, map[bool]string{true: "▾", false: "▸"}[open]).TextColor(t.TextMuted)
		})
		if head.Clicked() {
			a.wfExpanded[key] = !open
		}
		// Failures show at once.
		for _, id := range a.wfOrder() {
			s := it.Steps[id]
			n := w.Node(id)
			if s == nil || n == nil || s.Status != workflow.StepFailed {
				continue
			}
			ui.Text(c, n.StepName(n.Kind.Label())+": "+wfErrorText(s.Code, s.Err)).FontSize(px(t, 11.5)).TextColor(t.Danger).MaxLines(3)
		}
		if !open && st != workflow.StepDone {
			return
		}
		for j, o := range outs {
			if o.Item != key {
				continue
			}
			a.wfOutputRow(c, j, o)
		}
		if open {
			for _, id := range a.wfOrder() {
				s := it.Steps[id]
				n := w.Node(id)
				if s == nil || n == nil {
					continue
				}
				for _, wcode := range s.Warnings {
					ui.Text(c, n.StepName(n.Kind.Label())+": "+wfWarningText(wcode)).FontSize(px(t, 11)).TextColor(t.Warning)
				}
				if s.Status == workflow.StepSkipped && s.Code == workflow.SkipBranch {
					ui.Text(c, i18n.T("%s: not on this item's branch", n.StepName(n.Kind.Label()))).FontSize(px(t, 11)).TextColor(t.TextMuted)
				}
			}
		}
	})
}

// wfOrder is the open workflow's steps in run order.
func (a *app) wfOrder() []string { return a.wfReport().Order }

// wfOutputRow is one result: what it is, Play or its text, Show in Finder.
func (a *app) wfOutputRow(c *ui.Context, j int, o services.WorkflowOutput) {
	t := c.Theme()
	w := a.wf
	n := w.Node(o.Node)
	if n == nil {
		return
	}
	path := a.wfSvc.Path(w.ID, o.Value.File)
	ui.Column(c.Key(fmt.Sprintf("wf-out-%d", j))).Gap(px(t, 3)).Padding(px(t, 6), px(t, 8)).Radius(6).Background(t.Background).Children(func() {
		ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
			label := n.StepName(n.Kind.Label())
			switch o.Value.Type {
			case workflow.TypeAudio:
				label += " · " + lfClock(time.Duration(o.Value.Seconds*float64(time.Second)))
			default:
				label += " · " + i18n.N(o.Value.Chars, "%d character", "%d characters", o.Value.Chars)
			}
			ui.Text(c, label).FontSize(px(t, 11.5)).TextColor(t.TextMuted).Grow(1).MinWidth(0).SingleLine()
			if o.Value.Type == workflow.TypeAudio {
				id := "wf:" + o.Item + ":" + o.Node
				play := "▶"
				if a.pl.Playing(id) {
					play = "❚❚"
				}
				if ui.Button(c.Key("play"), play).Label(i18n.T("Play")).Tooltip(i18n.T("Play")).Clicked() {
					src := path
					if strings.HasSuffix(strings.ToLower(path), ".m4a") {
						// The player reads WAV: play what was encoded.
						if in := a.wfExportInput(o); in != "" {
							src = in
						}
					}
					if err := a.pl.Toggle(id, src); err != nil {
						a.toast(i18n.T("Can't play: %v", err))
					}
				}
			}
			if a.win != nil && ui.Button(c.Key("reveal"), i18n.T("Show in Finder")).Clicked() {
				mygo.Shell.ShowItemInFolder(path)
			}
		})
		if o.Value.Type != workflow.TypeAudio && o.Value.Text != "" {
			ui.Text(c, o.Value.Text).FontSize(px(t, 12)).MaxLines(4).Ellipsis("…").Selectable()
		}
	})
}

// wfExportInput is the WAV an export step encoded, for playing.
func (a *app) wfExportInput(o services.WorkflowOutput) string {
	run := a.wfRun
	if run == nil {
		return ""
	}
	i := slices.IndexFunc(run.Items, func(it workflow.RunItem) bool { return it.Key == o.Item })
	if i < 0 {
		return ""
	}
	for _, e := range a.wf.Incoming(o.Node) {
		if s := run.Items[i].Steps[e.From]; s != nil && s.Out != nil && s.Out.Type == workflow.TypeAudio {
			return a.wfSvc.Path(a.wf.ID, s.Out.File)
		}
	}
	return ""
}
