package main

// The M3 pages in the shell: a sidebar section, the routes, the titles
// and the palette's "Go to" entries (one call each from shell*.go).

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/jobs"
)

// longformPages are the sidebar's "Create" section.
var longformPages = []struct {
	path, title string
	icon        *ui.SVG
}{
	{"/stories", "Stories", storiesIcon},
	{"/audiobook", "Audiobook", audiobookIcon},
	{"/projects", "Projects", projectsIcon},
}

// longformPageTitle translates the M3 pages' titles.
func longformPageTitle(path string) (string, bool) {
	switch path {
	case "/stories":
		return i18n.T("Stories"), true
	case "/audiobook":
		return i18n.T("Audiobook"), true
	case "/projects":
		return i18n.T("Projects"), true
	}
	return "", false
}

// longformSidebar is the sidebar's section of the M3 pages; a running
// render shows as a badge.
func (a *app) longformSidebar(c *ui.Context) {
	ui.SidebarSection(c, i18n.T("Create"), nil, func() {
		for _, p := range longformPages {
			title, _ := longformPageTitle(p.path)
			item := ui.SidebarItem(c, p.path, p.icon, title)
			if r := a.activeRender(); r != nil && p.path == "/"+lfRoute(r.rec.Kind) {
				item.Children(func() { ui.Badge(c, itoa(r.body.Done())+"/"+itoa(len(r.body.Chapters))) })
			}
		}
	})
}

// longformRoute builds the M3 pages.
func (a *app) longformRoute(c *ui.Context, r *ui.Route) bool {
	switch {
	case r.Match("/stories"):
		r.Title(i18n.T("Stories"))
		a.storiesPage(c)
	case r.Match("/audiobook"):
		r.Title(i18n.T("Audiobook"))
		a.audiobookPage(c)
	case r.Match("/projects"):
		r.Title(i18n.T("Projects"))
		a.projectsPage(c)
	default:
		return false
	}
	return true
}

// longformCommands adds the palette's entries of the M3 pages.
func (a *app) longformCommands(nav string, add func(id, group, title, en, keys string, enabled bool, run func())) {
	for _, p := range longformPages {
		title, _ := longformPageTitle(p.path)
		add("go"+p.path, nav, title, p.title, "", true, func() { a.navigate(p.path) })
	}
	busy := a.activeRender() != nil
	book := i18n.T("Audiobook")
	add("lf-stop", book, i18n.T("Stop the book being rendered"), "Stop the book being rendered", "", busy, func() {
		if r := a.activeRender(); r != nil {
			p := a.lfPageOf(r.rec.Kind)
			p.render = r.rec.ID
			a.stopLf(p)
		}
	})
}

// followAlignments hands finished alignment jobs to the read-along (once,
// from openLongform).
func (a *app) followAlignments(lib *library) {
	core.On(lib.bus, func(u jobs.Updated) {
		if u.Chunk == nil && strings.HasPrefix(u.Job.Owner, readAlongOwner) && (u.Job.State.Terminal() || u.Job.State == jobs.StateInterrupted) {
			j := u.Job
			a.post(func() { a.alignDone(j) })
		}
	})
}
