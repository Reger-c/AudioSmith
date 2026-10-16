package main

// The shell's pages (M7a): the sidebar's sections and pages in order, the
// ⌘1–9 shortcuts, the pages' titles and badges, and the router that
// builds the page shown. Each area keeps its own routes, titles and
// badges in its file (longform_pages.go, dub_pages.go, …).

import (
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
)

// navPage is a page of the sidebar.
type navPage struct {
	path, title string // title: English, translated where shown (pageTitle)
	icon        icons.Icon
}

// navGroup is a section of the sidebar.
type navGroup struct {
	id    string // English title, translated by groupTitle
	pages []navPage
}

// navGroups are the sidebar's sections and pages, in order (M7a). Each
// area keeps its page's view, route and badge in its own file.
var navGroups = []navGroup{
	{"Create", []navPage{
		{"/studio", "Studio", studioIcon},
		{"/stories", "Stories", storiesIcon},       // M3: longform_pages.go
		{"/audiobook", "Audiobook", audiobookIcon}, // M3
		{"/dub", "Dub", dubIcon},                   // M4: dub_pages.go
	}},
	{"Voices", []navPage{ // voices.go (thread V)
		{"/gallery", "Discover voices", galleryIcon},
		{"/voices", "My voices", voicesIcon},
		{"/design", "Design", designIcon},
		{"/clone", "Clone", cloneIcon},
	}},
	{"Library", []navPage{
		{"/projects", "Projects", projectsIcon}, // M3
		{"/history", "History", historyIcon},
		{"/transcripts", "Transcribe", transcribeIcon}, // M2: transcribe*.go
	}},
	{"Automate", []navPage{
		{"/workflows", "Workflows", workflowsIcon}, // M6: workflow_pages.go
		{"/batch", "Batch", batchIcon},             // M4
		{"/calls", "Calls", callsIcon},             // M6b: calls_pages.go
	}},
	{"Utilities", []navPage{ // M5: api_pages.go
		{"/tools", "Tools", toolsIcon},
		{"/integrations", "Integrations", integrationsIcon},
	}},
	{"System", []navPage{
		{"/models", "Models", modelsIcon},
		{"/engine", "Engine", engineIcon},
		{"/logs", "Logs", logsIcon},
	}},
}

// shortcutPages are the pages ⌘1…⌘9 open, in that order: the ones used
// most. The others are a click or the palette (⌘K) away.
var shortcutPages = []string{"/studio", "/stories", "/audiobook", "/dub", "/gallery", "/voices", "/design", "/projects", "/transcripts"}

// pages are every page of the sidebar, in its order.
var pages = func() []navPage {
	var out []navPage
	for _, g := range navGroups {
		out = append(out, g.pages...)
	}
	return out
}()

// pageKeys is the shortcut of a page ("⌘1"), "" when it has none.
func pageKeys(path string) string {
	for i, p := range shortcutPages {
		if p == path {
			return "⌘" + itoa(i+1)
		}
	}
	return ""
}

// groupTitle translates a sidebar section's title.
func groupTitle(id string) string {
	switch id {
	case "Create":
		return i18n.T("Create")
	case "Voices":
		return i18n.T("Voices")
	case "Library":
		return i18n.T("Library")
	case "Automate":
		return i18n.T("Automate")
	case "Utilities":
		return i18n.T("Utilities")
	case "System":
		return i18n.T("System")
	}
	return id
}

// navBadge is what a page's sidebar item shows after its title: a count
// or progress, "" for nothing. Areas answer for their pages.
func (a *app) navBadge(path string) string {
	switch path {
	case "/history":
		if n := len(a.takes); n > 0 {
			return itoa(n)
		}
	case "/models":
		if n := a.downloadsRunning(); n > 0 {
			return itoa(n)
		}
	}
	if b, ok := a.longformBadge(path); ok { // M3: longform_pages.go
		return b
	}
	if b, ok := a.dubBadge(path); ok { // M4: dub_pages.go
		return b
	}
	if b, ok := a.wfBadge(path); ok { // M6: workflow_pages.go
		return b
	}
	if b, ok := a.callsBadge(path); ok { // M6b: calls_pages.go
		return b
	}
	return ""
}

// pageTitle translates a page's title (literal calls, for the key scan).
func pageTitle(path string) string {
	switch path {
	case "/studio":
		return i18n.T("Studio")
	case "/history":
		return i18n.T("History")
	case "/models":
		return i18n.T("Models")
	case "/engine":
		return i18n.T("Engine")
	case "/logs":
		return i18n.T("Logs")
	case "/settings":
		return i18n.T("Settings")
	case "/transcripts": // M2
		return i18n.T("Transcribe")
	}
	if t, ok := voicePageTitle(path); ok { // voices.go (thread V)
		return t
	}
	if t, ok := dubPageTitle(path); ok { // M4: dub_pages.go (thread #19)
		return t
	}
	if t, ok := longformPageTitle(path); ok { // M3: longform_pages.go (thread #18)
		return t
	}
	if t, ok := m5PageTitle(path); ok { // M5: api_pages.go (thread #20)
		return t
	}
	if t, ok := wfPageTitle(path); ok { // M6: workflow_pages.go (thread #21)
		return t
	}
	if t, ok := callsPageTitle(path); ok { // M6b: calls_pages.go (thread #23)
		return t
	}
	return path
}

var (
	studioIcon   = icons.Soundwave
	historyIcon  = icons.History
	engineIcon   = icons.CpuBolt
	modelsIcon   = icons.Box
	logsIcon     = icons.ClipboardList
	settingsIcon = icons.Settings
	bellIcon     = icons.Bell
	searchIcon   = icons.Magnifer
)

// pages builds the page the router shows.
func (a *app) pages(c *ui.Context) {
	a.router.View(c, func(r *ui.Route) {
		switch {
		case r.Match("/studio"):
			r.Title(i18n.T("Studio"))
			a.studioPage(c)
		case r.Match("/history"):
			r.Title(i18n.T("History"))
			a.historyPage(c)
		case r.Match("/models"):
			r.Title(i18n.T("Models"))
			a.modelsPage(c)
		case r.Match("/engine"):
			r.Title(i18n.T("Engine"))
			a.enginePage(c)
		case r.Match("/logs"):
			r.Title(i18n.T("Logs"))
			a.logsPage(c)
		case a.voicesRoute(c, r): // /voices, /voices/new, /design, /gallery (voices.go, thread V)
		case r.Match("/transcripts"): // M2: transcribe_view.go
			r.Title(i18n.T("Transcribe"))
			a.transcribePage(c)
		case a.longformRoute(c, r): // M3: /stories, /audiobook, /projects (longform_pages.go, thread #18)
		case a.dubRoute(c, r): // M4: /dub, /batch (dub_pages.go, thread #19)
		case a.callsRoute(c, r): // M6b: /calls, /calls/setup, /integrations/twilio (calls_pages.go, thread #23)
		case a.m5Route(c, r): // M5: /tools, /integrations, /api-reference (api_pages.go, thread #20)
		case a.wfRoute(c, r): // M6: /workflows (workflow_pages.go, thread #21)
		default:
			r.Title(i18n.T("Not found"))
			a.notFoundPage(c, r.Path())
		}
	}).Grow(1).MinHeight(0)
}

func (a *app) notFoundPage(c *ui.Context, path string) {
	pageHeader(c, i18n.T("Not found"), nil)
	emptyState(c, icons.Magnifer, i18n.T("There is no page at %s.", path), "", func() {
		if ui.Button(c.Key("go-studio"), i18n.T("Go to the Studio")).Clicked() {
			a.navigate("/studio")
		}
	})
}
