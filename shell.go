package main

// The window shell: the window, the sidebar and the router of pages, the
// status bar and the overlays every page shares (command palette, the
// shortcut sheet, the engine quick switch, notifications, the licence
// dialog), and the theme. Its parts live in shell_*.go:
//
//	shell_commands.go  command registry, ⌘K palette, ? sheet, native menu, deep links
//	shell_status.go    status bar, notification centre, ⌘E quick switch
//	shell_logs.go      logs page and panel
//	shell_settings.go  settings service wiring, settings pages, language, theme
//	shell_models.go    Models page, downloads, licence gate and dialog

import (
	"context"
	"io"
	"log"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/licence"
	"github.com/hehehai/AudioSmith/internal/logs"
	"github.com/hehehai/AudioSmith/internal/notify"
	"github.com/hehehai/AudioSmith/internal/settings"
)

const (
	titleBarHeight  = 52
	sidebarWidth    = 200
	inspectorWidth  = 300
	statusBarHeight = 26
)

// baseFontSize is the platform's text size at 100 %: px scales sizes
// designed for it with the UI scale (settings.Appearance.Scale).
var baseFontSize = ui.LightTheme().FontSize

// px scales a size in DIPs designed at 100 % to the theme in use:
// ui.Text(c, s).FontSize(px(t, 12)).
func px(t *ui.Theme, n float32) float32 { return n * t.FontSize / baseFontSize }

// shellState is the shell's view state and the services it owns.
type shellState struct {
	router *ui.Router
	// nav is the sidebar's choice; navShown the router path it was last
	// synced from (see syncNav).
	nav, navShown string
	lastSettings  string // settings section shown last

	bus      *core.Bus
	settings settings.Service
	prefs    settings.Settings // the settings, as this thread last saw them
	notes    *notify.Center
	notices  []core.Notification // newest first
	logs     *logs.Ring
	licences *licence.Gate
	accepted []core.LicenceAcceptance

	version string // the app's, for the status bar; "" in tests

	paletteOpen  bool
	paletteQuery string
	paletteSel   int
	sheetOpen    bool
	switchOpen   bool
	jobsOpen     bool
	jobList      map[string]jobs.Job // unfinished jobs of every owner, by id
	noticesOpen  bool
	logsPanel    bool
	logsSource   string
	logsQuery    string
	logsHeight   float32
	logsDirty    atomic.Bool // a redraw for new lines is on its way

	modelsState
	navState        // shell_nav.go (M7a)
	mediaToolsState // ffmpeg_settings.go (M7a): the optional ffmpeg
	storageState    // settings_storage.go (M7a)
	networkState    // settings_network.go (M7a)
	aboutState      // settings_about.go (M7a)
	settingsForm
	licenceDialog
	menuLocale string // locale the native menu was built in
	hasMenu    bool   // the app runs (not a test): it has a menu bar to build
}

func newShellState() shellState {
	return shellState{
		router:       ui.NewRouter("/studio"),
		lastSettings: "general",
		logsHeight:   220,
		prefs:        settings.Defaults(),
	}
}

// openShell wires the shell to the library: its bus, the settings and
// the licence gate it keeps in the database, and the notification centre,
// which keeps info and warnings there too (document "notifications").
// Logs stay in memory until main's wiring (connectShell) adds the files.
func (a *app) openShell(lib *library) {
	a.bus = lib.bus
	a.router.Transition = ui.TransitionFade
	a.settings = lib.settings
	a.prefs = lib.settings.Get()
	notes, err := notify.Open(context.Background(), lib.store.Documents(), a.bus)
	if err != nil {
		log.Printf("notifications: %v", err)
	}
	a.notes = notes
	a.notices, _ = notes.List(context.Background())
	a.logs = logs.New(logs.Options{Bus: a.bus})
	a.licences = lib.licences
	a.models = map[string]*modelState{}
	a.jobList = map[string]jobs.Job{}
	a.loadSettingsForm()

	core.On(a.bus, func(ev core.NotificationPosted) {
		a.post(func() { a.noticePosted(ev.Notification) })
	})
	core.On(a.bus, func(ev settings.Changed) {
		a.post(func() { a.settingsChanged(ev.Old, ev.New) })
	})
	core.On(a.bus, func(core.LicenceChanged) { a.loadAcceptances() })
	core.On(a.bus, func(core.LogAppended) {
		// Many lines may come at once: one redraw for all of them.
		if a.logsDirty.CompareAndSwap(false, true) {
			a.post(func() { a.logsDirty.Store(false) })
		}
	})
	a.applyLocale()
	a.loadAcceptances()
	a.loadMediaTool() // M7a: ffmpeg_settings.go
}

// connectShell adds what only the real app has: log files in logDir (the
// Go log package writes there too), the app's version, the system
// language and deep links. main calls it before App.Run.
func (a *app) connectShell(logDir string) {
	a.logDir = logDir // M7a: Settings → Storage, Diagnostics
	if logDir != "" {
		a.logs.Close()
		a.logs = logs.New(logs.Options{Dir: logDir, Bus: a.bus})
		log.SetOutput(io.MultiWriter(os.Stderr, a.logs.Writer("app")))
	}
	mygo.App.OnOpenURL(func(u string) { a.post(func() { a.openURL(u) }) })
}

// closeShell waits for the notifications to be saved and flushes the
// logs; it runs on quit. Downloads are jobs: the scheduler stops them,
// and they resume at the next start.
func (a *app) closeShell() {
	a.notes.Close()
	a.logs.Close()
}

// navPage is a page of the sidebar.
type navPage struct {
	path, title string // title: English, translated where shown (pageTitle)
	icon        *ui.SVG
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
		{"/voices", "Voices", voicesIcon},
		{"/design", "Design", designIcon},
		{"/gallery", "Gallery", galleryIcon},
	}},
	{"Library", []navPage{
		{"/projects", "Projects", projectsIcon}, // M3
		{"/history", "History", historyIcon},
		{"/transcripts", "Transcribe", transcribeIcon}, // M2: transcribe*.go
	}},
	{"Automate", []navPage{
		{"/workflows", "Workflows", workflowsIcon}, // M6: workflow_pages.go
		{"/batch", "Batch", batchIcon},             // M4
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
var shortcutPages = []string{"/studio", "/stories", "/audiobook", "/dub", "/voices", "/design", "/gallery", "/projects", "/transcripts"}

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
	return path
}

// navigate goes to a router path ("/models", "/settings/general"); it
// does nothing when that page shows already. "/settings" opens the
// section shown last.
func (a *app) navigate(path string) {
	if path == "/settings" || path == "/settings/" {
		path = "/settings/" + a.lastSettings
	}
	if rest, ok := strings.CutPrefix(path, "/settings/"); ok && rest != "" {
		a.lastSettings, _, _ = strings.Cut(rest, "/")
	}
	if path == a.router.Path() {
		return
	}
	a.router.Push(path)
	if path == "/models" {
		a.refreshModels()
	}
}

// showPage goes to a page by name ("studio", "history", "engine",
// "models", "logs", "settings"). Other areas navigate only through it
// (and read currentPage).
func (a *app) showPage(name string) { a.navigate("/" + name) }

// currentPage is the name of the page shown: the first part of its path.
func (a *app) currentPage() string { return section(a.router.Path())[1:] }

// section is the first part of a path: "/settings/models" → "/settings".
func section(path string) string {
	if i := strings.IndexByte(path[1:], '/'); i >= 0 {
		return path[:i+1]
	}
	return path
}

// openWindow opens the main window; it runs in App.WhenReady.
func (a *app) openWindow() {
	a.version = mygo.App.Version()
	a.systemLocale = mygo.App.Locale()
	a.hasMenu = true
	a.applyLocale() // the system's language, unless the settings name one
	a.buildMenu()
	a.win = mygo.NewWindow(mygo.WindowOptions{
		Title:          "AudioSmith",
		Width:          1100,
		Height:         720,
		MinWidth:       820,
		MinHeight:      520,
		StateKey:       "main",
		TitleBarStyle:  mygo.TitleBarHiddenInset,
		TitleBarHeight: titleBarHeight,
		Content:        ui.View(a.view),
	})
	a.refreshModels()
}

func icon(shapes string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round">` + shapes + `</svg>`))
}

var (
	studioIcon   = icon(`<path d="M2 8h1.5M5 5v6M8 3v10M11 5.5v5M14 8h-1"/>`)
	historyIcon  = icon(`<circle cx="8" cy="8" r="6"/><path d="M8 4.5V8l2.5 1.5"/>`)
	engineIcon   = icon(`<rect x="3.5" y="3.5" width="9" height="9" rx="1.5"/><path d="M6 1.5v2M10 1.5v2M6 12.5v2M10 12.5v2M1.5 6h2M1.5 10h2M12.5 6h2M12.5 10h2"/>`)
	modelsIcon   = icon(`<path d="M8 1.8 13.5 5v6L8 14.2 2.5 11V5z"/><path d="M2.5 5 8 8.2 13.5 5M8 8.2v6"/>`)
	logsIcon     = icon(`<rect x="2.5" y="2" width="11" height="12" rx="1.5"/><path d="M5 5.5h6M5 8h6M5 10.5h4"/>`)
	settingsIcon = icon(`<circle cx="8" cy="8" r="2.2"/><path d="M8 1.5v2M8 12.5v2M1.5 8h2M12.5 8h2M3.4 3.4l1.4 1.4M11.2 11.2l1.4 1.4M3.4 12.6l1.4-1.4M11.2 4.8l1.4-1.4"/>`)
	bellIcon     = icon(`<path d="M4 11V7a4 4 0 0 1 8 0v4l1 1.5H3z"/><path d="M6.5 14a1.5 1.5 0 0 0 3 0"/>`)
	searchIcon   = icon(`<circle cx="7" cy="7" r="4.5"/><path d="m10.5 10.5 3.5 3.5"/>`)
)

func (a *app) view(c *ui.Context) {
	a.applyTheme(c)
	t := c.Theme()
	if a.win == nil || a.prefs.Appearance.ReduceMotion {
		// Tests and snapshots show every change at once, as does the
		// user's Reduce motion.
		c.DisableAnimations()
	}
	for _, m := range a.toasts {
		c.Toast(m)
	}
	a.toasts = nil
	a.shortcuts(c)
	if a.engineSummary().Busy {
		// Keep the elapsed time current.
		c.After(500 * time.Millisecond)
	}
	if a.pl.Active() {
		// Notice the end of playback, so Pause turns back into Play.
		c.After(200 * time.Millisecond)
	}
	a.syncNav()

	ui.Column(c).Fill().AlignItems(ui.Stretch).Children(func() {
		a.sidebarFrame(c, func() { // shell_nav.go: the sidebar or the icon rail
			a.pages(c)
			if a.logsPanel && section(a.router.Path()) != "/logs" {
				ui.Divider(c)
				ui.Column(c.Key("logs-panel")).Height(px(t, a.logsHeight)).Shrink(0).Children(func() {
					a.logsView(c, true)
				})
			}
		})
		ui.Divider(c)
		a.statusBar(c)
	})
	a.palette(c)
	a.shortcutSheet(c)
	a.licenceModal(c)
	a.modelsAlerts(c)
}

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
		case a.m5Route(c, r): // M5: /tools, /integrations, /api-reference (api_pages.go, thread #20)
		case a.wfRoute(c, r): // M6: /workflows (workflow_pages.go, thread #21)
		case r.Match("/settings/{section...}"):
			r.Title(i18n.T("Settings"))
			a.settingsPage(c, r)
		default:
			r.Title(i18n.T("Not found"))
			a.notFoundPage(c, r.Path())
		}
	}).Grow(1).MinHeight(0)
}

func (a *app) notFoundPage(c *ui.Context, path string) {
	t := c.Theme()
	header(c, i18n.T("Not found"), nil)
	ui.Column(c).Grow(1).Center().Gap(px(t, 8)).Children(func() {
		ui.Text(c, i18n.T("There is no page at %s.", path)).TextColor(t.TextMuted)
		if ui.Button(c.Key("go-studio"), i18n.T("Go to the Studio")).Clicked() {
			a.navigate("/studio")
		}
	})
}

// syncNav makes the sidebar follow the router (Back, deep links, ⌘1…),
// but not undo a choice in the sidebar the router has not seen yet.
func (a *app) syncNav() {
	if p := section(a.router.Path()); p != a.navShown {
		a.nav, a.navShown = p, p
	}
}

func (a *app) sidebar(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Height(titleBarHeight).Shrink(0).DragWindow().AlignItems(ui.End).Justify(ui.End).Padding(0, px(t, 8), px(t, 4), 0).Children(func() {
		if railButton(c, "rail-on", "‹", i18n.T("Show icons only"), false).Clicked() {
			a.setRail(true)
		}
	})
	ui.Sidebar(c, &a.nav, func() {
		for _, g := range navGroups {
			open := a.sectionOpen(g.id)
			ui.SidebarSection(c.Key("nav-"+g.id), groupTitle(g.id), open, func() {
				for _, p := range g.pages {
					item := ui.SidebarItem(c, p.path, p.icon, pageTitle(p.path))
					if b := a.navBadge(p.path); b != "" {
						item.Children(func() { ui.Badge(c, b) })
					}
				}
			})
		}
		ui.SidebarSection(c.Key("nav-App"), i18n.T("App"), nil, func() {
			ui.SidebarItem(c, "/settings", settingsIcon, i18n.T("Settings"))
		})
	}).Grow(1).Background(ui.Transparent).Label(i18n.T("Sections")).OnChange(func() {
		a.navigate(a.nav)
	})
	a.saveClosedSections()
}

// header is a page's title bar, which drags the window.
func header(c *ui.Context, title string, trailing func()) {
	t := c.Theme()
	ui.Row(c).Height(titleBarHeight).Shrink(0).AlignItems(ui.Center).Padding(0, px(t, 16), 0, px(t, 20)).Gap(px(t, 8)).DragWindow().Children(func() {
		ui.Text(c, title).FontSize(px(t, 15)).Bold().SingleLine().Grow(1)
		if trailing != nil {
			trailing()
		}
	})
	ui.Divider(c)
}

// orDash shows "—" for an empty value.
func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// showFatal explains, in a dialog, why AudioSmith cannot start (its
// library did not open), then quits. It runs instead of the app.
func showFatal(err error) {
	log.Print(err)
	mygo.App.WhenReady(func() {
		i18n.SetLocale(mygo.App.Locale())
		go func() {
			dataDir, _ := mygo.App.Path(mygo.PathUserData)
			res, _ := mygo.Dialog.Message(mygo.MessageOptions{
				Type:    mygo.MessageError,
				Title:   "AudioSmith",
				Message: i18n.T("AudioSmith cannot open its library"),
				Detail: i18n.T("Your takes and settings are kept in a database in %s. It could not be opened: %v", dataDir, err) +
					"\n\n" + i18n.T("If a newer AudioSmith created it, use that version. Nothing was changed."),
				Buttons: []string{i18n.T("Quit"), i18n.T("Show the folder")},
			})
			if res.Button == 1 {
				mygo.Shell.OpenPath(dataDir)
			}
			mygo.App.Quit()
		}()
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
