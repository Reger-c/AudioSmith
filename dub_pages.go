package main

// The M4 pages in the shell: a sidebar section (Dub, Batch), the routes,
// the titles, the palette's entries and Settings → Dubbing (one call
// each from shell*.go, app.go and main.go).

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/services"
)

// dubPages are the sidebar's "Dubbing" section.
var dubPages = []struct {
	path, title string
	icon        *ui.SVG
}{
	{"/dub", "Dub", dubIcon},
	{"/batch", "Batch", batchIcon},
}

// dubPageTitle translates the M4 pages' titles.
func dubPageTitle(path string) (string, bool) {
	switch path {
	case "/dub":
		return i18n.T("Dub"), true
	case "/batch":
		return i18n.T("Batch"), true
	}
	return "", false
}

// dubSidebar is the sidebar's section of the M4 pages; running dubs
// show as badges.
func (a *app) dubSidebar(c *ui.Context) {
	ui.SidebarSection(c, i18n.T("Dubbing"), nil, func() {
		for _, p := range dubPages {
			title, _ := dubPageTitle(p.path)
			item := ui.SidebarItem(c, p.path, p.icon, title)
			n := 0
			switch p.path {
			case "/dub":
				if a.dubBusy() {
					n = 1
				}
			case "/batch":
				n = len(a.batch.jobs)
			}
			if n > 0 {
				item.Children(func() { ui.Badge(c, itoa(n)) })
			}
		}
	})
}

// dubRoute builds the M4 pages.
func (a *app) dubRoute(c *ui.Context, r *ui.Route) bool {
	switch {
	case r.Match("/dub"):
		r.Title(i18n.T("Dub"))
		a.dubPage(c)
	case r.Match("/batch"):
		r.Title(i18n.T("Batch"))
		a.batchPage(c)
	default:
		return false
	}
	return true
}

// dubCommands adds the palette's entries of the M4 pages.
func (a *app) dubCommands(nav string, add func(id, group, title, en, keys string, enabled bool, run func())) {
	for _, p := range dubPages {
		title, _ := dubPageTitle(p.path)
		add("go"+p.path, nav, title, p.title, "", true, func() { a.navigate(p.path) })
	}
	dubbing := i18n.T("Dubbing")
	add("dub-new", dubbing, i18n.T("Dub a video or audio file…"), "Dub a video or audio file…", "", true, func() {
		a.navigate("/dub")
		a.chooseDubSource()
	})
	add("dub-cancel", dubbing, i18n.T("Stop the dub being made"), "Stop the dub being made", "", a.dubBusy(), a.dubCancel)
}

// dubSettingsSection is Settings → Dubbing's entry.
func dubSettingsSection() settingsSection {
	return settingsSection{"dubbing", i18n.T("Dubbing"), "Dubbing"}
}

// dubSettingsRoute builds Settings → Dubbing.
func (a *app) dubSettingsRoute(c *ui.Context, r *ui.Route) bool {
	if !r.Match("/dubbing") {
		return false
	}
	r.Title(i18n.T("Dubbing"))
	a.lastSettings = "dubbing"
	a.dubbingSettings(c)
	return true
}

// registerDubMedia serves the dub preview's media (before the window opens).
func (a *app) registerDubMedia() {
	mygo.Protocol.Handle(dubMediaScheme, dubMediaHandler(func() *services.Dubs { return a.dubSvc }))
}
