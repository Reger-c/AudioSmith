package main

// The M6 page in the shell: a sidebar section, the route, the title and
// the palette's entries (one call each from shell*.go and app.go).

import (
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/workflow"
)

// wfPageTitle translates the M6 page's title.
func wfPageTitle(path string) (string, bool) {
	if path == "/workflows" {
		return i18n.T("Workflows"), true
	}
	return "", false
}

// wfSidebar is the sidebar's "Automate" section; a running workflow
// shows as a badge.
func (a *app) wfSidebar(c *ui.Context) {
	ui.SidebarSection(c, i18n.T("Automate"), nil, func() {
		item := ui.SidebarItem(c, "/workflows", workflowsIcon, i18n.T("Workflows"))
		if a.wfBusy() && a.wfRun != nil {
			done, _, total := a.wfRun.Counts()
			item.Children(func() { ui.Badge(c, itoa(done)+"/"+itoa(total)) })
		}
	})
}

// wfRoute builds /workflows.
func (a *app) wfRoute(c *ui.Context, r *ui.Route) bool {
	if !r.Match("/workflows") {
		return false
	}
	r.Title(i18n.T("Workflows"))
	a.workflowsPage(c)
	return true
}

// wfCommands adds the palette's entries of the M6 page.
func (a *app) wfCommands(nav string, add func(id, group, title, en, keys string, enabled bool, run func())) {
	add("go/workflows", nav, i18n.T("Workflows"), "Workflows", "", true, func() { a.navigate("/workflows") })
	group := i18n.T("Workflows")
	add("wf-new", group, i18n.T("New workflow"), "New workflow", "", true, func() {
		a.navigate("/workflows")
		a.wfNew("")
	})
	for _, tp := range workflow.Templates {
		id := tp.ID
		add("wf-template-"+id, group, i18n.T("New workflow: %s", workflow.TemplateName(id)), "New workflow: "+tp.Name, "", true, func() {
			a.navigate("/workflows")
			a.wfNew(id)
		})
	}
	add("wf-run", group, i18n.T("Run the workflow"), "Run the workflow", "", a.wf != nil && !a.wfBusy(), func() {
		a.navigate("/workflows")
		a.wfStart(false)
	})
	add("wf-cancel", group, i18n.T("Cancel the workflow's run"), "Cancel the workflow's run", "", a.wfBusy(), a.wfCancel)
}

// wfItemState is a workflow's last run on the Projects page (the item's detail).
func wfItemState(detail string) string {
	switch workflow.RunState(detail) {
	case workflow.RunRunning:
		return i18n.T("Running")
	case workflow.RunDone:
		return i18n.T("Done")
	case workflow.RunFailed:
		return i18n.T("Stopped")
	case workflow.RunCancelled:
		return i18n.T("Cancelled")
	}
	return i18n.T("Not run yet")
}

// wfOpenPage opens a workflow on its page (the Projects page, deep links).
func (a *app) wfOpenPage(id string) {
	a.navigate("/workflows")
	if a.wf == nil || a.wf.ID != id {
		a.wfOpen(id)
	}
}
