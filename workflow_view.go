package main

// The Workflows page (/workflows): the library and the step palette on
// the left, the toolbar and the canvas in the middle, the step's details
// or the run on the right.

import (
	"context"
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
	"github.com/hehehai/AudioSmith/internal/store"
	"github.com/hehehai/AudioSmith/internal/workflow"
)

var workflowsIcon = icons.Structure

// wfRailWidth is the library rail's width.
const wfRailWidth = 220

// workflowsPage is /workflows.
func (a *app) workflowsPage(c *ui.Context) {
	t := c.Theme()
	title := i18n.T("Workflows")
	if a.wf != nil && a.wf.Name != "" {
		title += " · " + a.wf.Name
	}
	header(c, title, func() { a.wfHeaderActions(c) })
	ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
		ui.Scroll(c.Key("wf-rail")).Width(px(t, wfRailWidth)).Shrink(0).Background(t.Surface).Children(func() {
			ui.Column(c).Padding(px(t, 12)).Gap(px(t, 10)).Children(func() { a.wfRail(c) })
		})
		ui.Box(c).Width(1).Background(t.Border)
		ui.Column(c.Key("wf-middle")).Grow(1).MinWidth(0).Children(func() {
			if a.wf == nil {
				a.wfEmpty(c)
				return
			}
			a.wfToolbar(c)
			ui.Divider(c)
			a.wfCanvasView(c)
		})
		if a.wf != nil {
			ui.Box(c).Width(1).Background(t.Border)
			ui.Column(c.Key("wf-panel")).Width(px(t, inspectorWidth+40)).Shrink(0).Background(t.Surface).Children(func() {
				ui.Row(c).Padding(px(t, 8), px(t, 12)).Children(func() {
					ui.Segmented(c.Key("wf-panel-tabs"), &a.wfPanel, i18n.T("Step"), i18n.T("Run")).Label(i18n.T("Panel")).Grow(1)
				})
				ui.Divider(c)
				ui.Scroll(c.Key("wf-panel-scroll")).Grow(1).MinHeight(0).Children(func() {
					ui.Column(c).Padding(px(t, 14)).Gap(px(t, 10)).Children(func() {
						if a.wfPanel == wfPanelRun {
							a.wfRunPanel(c)
						} else {
							a.wfInspector(c)
						}
					})
				})
			})
		}
	})
	a.wfConfirmDialog(c)
}

// wfHeaderActions are Run / Cancel.
func (a *app) wfHeaderActions(c *ui.Context) {
	if a.wf == nil {
		return
	}
	if a.wfBusy() {
		if ui.Button(c.Key("wf-cancel"), i18n.T("Cancel run")).Clicked() {
			a.wfCancel()
		}
		return
	}
	label := i18n.T("Run workflow")
	if a.wfResumable() {
		label = i18n.T("Resume")
	}
	if primaryButton(c.Key("wf-run"), label).Tooltip(i18n.T("Run every item through the steps")).Clicked() {
		a.wfStart(false)
	}
}

// wfResumable reports whether the last run stopped before its end.
func (a *app) wfResumable() bool {
	r := a.wfRun
	if r == nil || a.wfStale() {
		return false
	}
	if r.State == workflow.RunCancelled || r.State == workflow.RunRunning {
		return true
	}
	if r.State == workflow.RunFailed {
		done, _, total := r.Counts()
		return done < total
	}
	return false
}

// wfEmpty is the page without a workflow: the templates.
func (a *app) wfEmpty(c *ui.Context) {
	t := c.Theme()
	ui.Column(c.Key("wf-empty-page")).Grow(1).Center().Gap(px(t, 12)).Padding(px(t, 24)).Children(func() {
		ui.Icon(c, workflowsIcon.Linear).Size(px(t, 40), px(t, 40)).TextColor(t.TextMuted)
		ui.Text(c, i18n.T("Design a voice workflow")).FontSize(px(t, 18)).Bold()
		ui.Text(c, i18n.T("Chain steps — speak, transcribe, translate, convert, normalize, export — and run up to 50 scripts or clips through them. Steps save as you go; a run you stop resumes where it stopped.")).
			TextColor(t.TextMuted).MaxWidth(px(t, 520)).TextAlign(ui.Center)
		if !a.wfLoaded {
			return
		}
		ui.Row(c).Gap(px(t, 8)).Wrap().Justify(ui.Center).MaxWidth(px(t, 640)).Children(func() {
			if primaryButton(c.Key("wf-empty-new"), i18n.T("New workflow")).Clicked() {
				a.wfNew("")
			}
			for _, tp := range workflow.Templates {
				if ui.Button(c.Key("wf-empty-"+tp.ID), workflow.TemplateName(tp.ID)).Tooltip(workflow.TemplateAbout(tp.ID)).Clicked() {
					a.wfNew(tp.ID)
				}
			}
		})
	})
}

// wfRail is the library, the templates and the step palette.
func (a *app) wfRail(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).AlignItems(ui.Center).Children(func() {
		ui.Text(c, i18n.T("Workflows")).Bold().Grow(1)
		ui.MenuButton(c.Key("wf-add"), "+", func(m *ui.Menu) {
			if m.Item(i18n.T("New workflow")).Chosen() {
				a.wfNew("")
			}
			m.Separator()
			for _, tp := range workflow.Templates {
				if m.Item(workflow.TemplateName(tp.ID)).Chosen() {
					a.wfNew(tp.ID)
				}
			}
		}).Label(i18n.T("New workflow"))
	})
	if a.wfLoaded && len(a.wfList) == 0 {
		ui.Text(c, i18n.T("No workflows yet.")).FontSize(px(t, 12)).TextColor(t.TextMuted)
	}
	ui.Column(c.Key("wf-list")).Gap(2).Children(func() {
		for _, r := range a.wfList {
			a.wfListRow(c, r)
		}
	})
	ui.Divider(c)
	ui.Text(c, i18n.X("workflow", "Steps")).Bold()
	ui.Text(c, i18n.T("Click to add after the selected step, or drag onto the canvas.")).FontSize(px(t, 11.5)).TextColor(t.TextMuted)
	locked := a.wfLocked()
	ui.Column(c.Key("wf-palette")).Gap(4).Children(func() {
		for _, k := range workflow.Kinds {
			kk := k
			b := ui.ButtonBase(c.Key("wf-pal-" + string(k))).Label(i18n.T("Add %s", k.Label())).Tooltip(k.About()).Disabled(locked)
			b.Padding(px(t, 5), px(t, 8)).Radius(8).Gap(px(t, 8)).AlignItems(ui.Center)
			if b.Hovered() && !locked {
				b.Background(t.SurfaceHover)
			}
			if !locked {
				b.Drag(kk)
			}
			b.Children(func() {
				ui.Box(c).Size(px(t, 20), px(t, 20)).Radius(5).Background(wfKindColor(kk)).Center().Shrink(0).Children(func() {
					ui.Icon(c, wfKindIcons[kk].Linear).Size(px(t, 13), px(t, 13)).TextColor(ui.RGB(255, 255, 255))
				})
				ui.Text(c, kk.Label()).FontSize(px(t, 13)).SingleLine().Grow(1).MinWidth(0)
			})
			if b.Clicked() {
				a.wfAddStep(kk, -1, -1)
			}
		}
	})
}

// wfListRow is a workflow in the library: open, rename, duplicate, delete.
func (a *app) wfListRow(c *ui.Context, r store.WorkflowRecord) {
	t := c.Theme()
	id := r.ID
	open := a.wf != nil && a.wf.ID == id
	if a.wfRenaming == id {
		in := ui.TextInput(c.Key("wf-rename-"+id), &a.wfRenameTx).Label(i18n.T("Name")).AutoFocus()
		if in.Submitted() {
			a.wfRename(id, a.wfRenameTx)
		}
		if in.Shortcut(0, ui.KeyEscape) {
			a.wfRenaming = ""
		}
		return
	}
	row := ui.ButtonBase(c.Key("wf-row-"+id)).Label(r.Name).Padding(px(t, 6), px(t, 8)).Radius(8).Gap(px(t, 6)).AlignItems(ui.Center)
	switch {
	case open:
		row.Background(t.Selection)
	case row.Hovered():
		row.Background(t.SurfaceHover)
	}
	row.Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Children(func() {
			ui.Text(c, firstOf(r.Name, i18n.T("Untitled workflow"))).FontSize(px(t, 13)).SingleLine().Ellipsis("…")
			if s := wfRecordState(r); s != "" {
				ui.Text(c, s).FontSize(px(t, 11)).TextColor(t.TextMuted).SingleLine()
			}
		})
		ui.Button(c.Key("wf-row-menu-"+id), "⋯").Menu(func(m *ui.Menu) {
			if m.Item(i18n.T("Rename")).Chosen() {
				a.wfRenaming, a.wfRenameTx = id, r.Name
			}
			if m.Item(i18n.T("Duplicate")).Chosen() {
				if !open {
					a.wfOpen(id)
				}
				a.wfDuplicateID(id)
			}
			m.Separator()
			if m.Item(i18n.T("Delete")).Chosen() {
				a.wfConfirm, a.wfAsk = id, true
			}
		}).Label(i18n.T("More actions"))
	})
	if row.Clicked() && !open {
		a.wfOpen(id)
	}
}

// wfDuplicateID duplicates a workflow of the list.
func (a *app) wfDuplicateID(id string) {
	if a.wf != nil && a.wf.ID == id {
		a.wfDuplicate()
		return
	}
	svc := a.wfSvc
	a.background(func() {
		w, _, err := svc.Get(context.Background(), id)
		a.post(func() {
			if err != nil {
				a.toast(i18n.T("Could not open the workflow: %v", err))
				return
			}
			a.wf = w
			a.wfDuplicate()
		})
	})
}

// wfRecordState is a list row's second line.
func wfRecordState(r store.WorkflowRecord) string {
	switch workflow.RunState(r.State) {
	case workflow.RunRunning:
		return i18n.T("Running · %d/%d", r.Done, r.Items)
	case workflow.RunDone:
		return i18n.N(r.Done, "%d item ready", "%d items ready", r.Done)
	case workflow.RunFailed:
		return i18n.T("Stopped · %d/%d ready", r.Done, r.Items)
	case workflow.RunCancelled:
		return i18n.T("Cancelled · %d/%d ready", r.Done, r.Items)
	}
	return ""
}

// wfToolbar is above the canvas: zoom, fit, remove.
func (a *app) wfToolbar(c *ui.Context) {
	t := c.Theme()
	rep := a.wfReport()
	ui.Row(c.Key("wf-toolbar")).Padding(px(t, 6), px(t, 12)).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
		if n := rep.Errors(); n > 0 {
			if ui.Button(c.Key("wf-errors"), i18n.N(n, "%d problem", "%d problems", n)).TextColor(t.Danger).Clicked() {
				a.wfPanel = wfPanelRun
			}
		} else if a.wf != nil && len(a.wf.Nodes) > 0 {
			ui.Text(c, i18n.N(rep.Items, "Ready · %d item", "Ready · %d items", rep.Items)).FontSize(px(t, 12)).TextColor(t.TextMuted)
		}
		if a.wfLocked() && a.wfBusy() {
			ui.Text(c, i18n.T("Locked while it runs")).FontSize(px(t, 12)).TextColor(t.TextMuted)
		}
		ui.Box(c).Grow(1)
		del := a.wfSel != "" || a.wfSelEdge != ""
		if del && ui.Button(c.Key("wf-remove"), i18n.T("Remove")).Disabled(a.wfLocked()).
			Tooltip(i18n.T("Remove the selected step or link (Delete)")).Clicked() {
			a.wfRemoveSelected()
		}
		if ui.Button(c.Key("wf-zoom-out"), "−").Label(i18n.T("Zoom out")).Tooltip(i18n.T("Zoom out (⌘−)")).Clicked() {
			a.wfZoom(1 / 1.2)
		}
		if ui.Button(c.Key("wf-zoom-reset"), itoa(int(a.wf.View.Zoom*100+0.5))+"%").Label(i18n.T("Actual size")).Tooltip(i18n.T("Actual size (⌘0)")).Clicked() {
			a.wfResetZoom()
		}
		if ui.Button(c.Key("wf-zoom-in"), "+").Label(i18n.T("Zoom in")).Tooltip(i18n.T("Zoom in (⌘=)")).Clicked() {
			a.wfZoom(1.2)
		}
		if ui.Button(c.Key("wf-fit"), i18n.T("Fit")).Tooltip(i18n.T("Show every step")).Clicked() {
			a.wfFit()
		}
	})
}

// wfConfirmDialog asks before deleting a workflow.
func (a *app) wfConfirmDialog(c *ui.Context) {
	if !a.wfAsk {
		return
	}
	id := a.wfConfirm
	name := ""
	if i := slices.IndexFunc(a.wfList, func(r store.WorkflowRecord) bool { return r.ID == id }); i >= 0 {
		name = a.wfList[i].Name
	}
	switch ui.AlertDialog(c.Key("wf-confirm"), &a.wfAsk, i18n.T("Delete “%s”?", name),
		i18n.T("The workflow, its last run and the results kept in the app are deleted. Files it exported stay."), i18n.T("Delete"), i18n.T("Cancel")) {
	case 0:
		a.wfAsk = false
		a.wfDelete(id)
	case 1:
		a.wfAsk = false
	}
}
