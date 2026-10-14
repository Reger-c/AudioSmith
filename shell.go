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
	"github.com/hehehai/AudioSmith/internal/icons"
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
	// The settings dialog (0.3): open, and the router of its sections
	// ("/general"), which navigate drives from "/settings/…" paths.
	settingsOpen bool
	setRouter    *ui.Router

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
	onboardingState // onboarding.go (M7a): the first-run wizard
	settingsForm
	licenceDialog
	menuLocale string // locale the native menu was built in
	hasMenu    bool   // the app runs (not a test): it has a menu bar to build
}

func newShellState() shellState {
	return shellState{
		router:       ui.NewRouter("/studio"),
		setRouter:    ui.NewRouter("/general"),
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

// navigate goes to a router path ("/models"); it does nothing when that
// page shows already. "/settings/general" opens the settings dialog at
// that section, over the page, and "/settings" at the section shown last;
// any other path closes it.
func (a *app) navigate(path string) {
	if path == "/settings" || path == "/settings/" {
		path = "/settings/" + a.lastSettings
	}
	if rest, ok := strings.CutPrefix(path, "/settings/"); ok && rest != "" {
		a.lastSettings, _, _ = strings.Cut(rest, "/")
		a.openSettings("/" + rest)
		return
	}
	a.settingsOpen = false
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
	a.maybeOnboard() // M7a: a fresh install opens the setup (onboarding.go)
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
	a.settingsDialog(c) // shell_settings.go
	a.palette(c)
	a.shortcutSheet(c)
	a.onboarding(c) // M7a: the first-run wizard, under the licence dialog
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
	k := tok(t)
	ui.Row(c).Height(titleBarHeight).Shrink(0).DragWindow().AlignItems(ui.Center).Justify(ui.End).Padding(0, px(t, sp2), 0, 0).Children(func() {
		if iconButton(c.Key("rail-on"), icons.SidebarMinimalistic, i18n.T("Show icons only")).Clicked() {
			a.setRail(true)
		}
	})
	a.sidebarSearch(c)
	// The sidebar's own theme: icons in the secondary grey, the chosen
	// page's in the text colour (MyGo draws a sidebar item's icon in the
	// accent, and the chosen one's in the accent's text colour).
	side := *t
	side.Accent, side.AccentText = k.Text2, k.Text
	side.SurfaceHover, side.SurfacePressed = k.G[2], k.G[3]
	chosen := side
	chosen.Accent = k.Text
	// The list scrolls: a fade at its foot says there is more below.
	ui.Column(c).Grow(1).MinHeight(0).Children(func() {
		ui.Sidebar(c, &a.nav, func() {
			for _, g := range navGroups {
				open := a.sectionOpen(g.id)
				ui.SidebarSection(c.Key("nav-"+g.id), groupTitle(g.id), open, func() {
					for _, p := range g.pages {
						sel := p.path == a.nav
						if sel {
							c.SetTheme(&chosen)
						} else {
							c.SetTheme(&side)
						}
						item := ui.SidebarItem(c, p.path, p.icon.Get(sel), pageTitle(p.path)).Padding(px(t, 5), px(t, sp2))
						c.SetTheme(t)
						if sel {
							item.Background(k.Pressed).FontWeight(600)
						} else {
							item.TextColor(k.G[8])
						}
						if b := a.navBadge(p.path); b != "" {
							item.Children(func() { ui.Badge(c, b) })
						}
					}
				}).Margin(px(t, 6), 0, 0, 0)
			}
		}).Grow(1).MinHeight(0).Background(ui.Transparent).Padding(0, px(t, 10), px(t, sp4)).Label(i18n.T("Sections")).OnChange(func() {
			a.navigate(a.nav)
		})
		ui.Box(c).Absolute().Left(0).Right(0).Bottom(0).Height(px(t, 20)).Gradient(k.Sidebar.Alpha(0), k.Sidebar, 180).PassThrough()
	})
	a.saveClosedSections()
	ui.Column(c).Shrink(0).Padding(px(t, 6), px(t, 10), px(t, 8)).BorderWidth(1, 0, 0, 0).BorderColor(k.Line).Children(func() {
		b := ghostBase(c.Key("nav-settings")).Padding(px(t, 6), px(t, sp2)).Gap(px(t, sp2)).Justify(ui.Start).Label(i18n.T("Settings")).Tooltip(i18n.T("Settings") + " (⌘,)")
		b.Children(func() {
			ui.Icon(c, icons.Settings.Get(a.settingsOpen)).Size(px(t, 16), px(t, 16)).TextColor(k.Text2)
			ui.Text(c, i18n.T("Settings")).TextColor(k.G[8]).SingleLine().Grow(1)
		})
		if b.Clicked() {
			a.navigate("/settings")
		}
	})
}

// sidebarSearch is the sidebar's search field: it opens the palette.
func (a *app) sidebarSearch(c *ui.Context) {
	t := c.Theme()
	k := tok(t)
	ui.Column(c).Shrink(0).Padding(0, px(t, 10), px(t, sp2)).Children(func() {
		b := ui.ButtonBase(c.Key("nav-search")).Padding(px(t, 6), px(t, sp2)).Gap(px(t, sp2)).Radius(px(t, radiusControl)).
			Background(k.Bg).Border(1, k.Line).Justify(ui.Start).Label(i18n.T("Command palette")).Tooltip(i18n.T("Command palette") + " (⌘K)")
		if b.Hovered() {
			b.Border(1, k.G[4])
		}
		b.Children(func() {
			ui.Icon(c, icons.Magnifer.Linear).Size(px(t, 14), px(t, 14)).TextColor(k.Text3)
			ui.Text(c, i18n.T("Search")).FontSize(px(t, fsCaption)).TextColor(k.Text3).SingleLine().Grow(1).MinWidth(0)
			ui.Text(c, "⌘K").FontSize(px(t, fsMicro)).TextColor(k.Text3)
		})
		if b.Clicked() {
			a.openPalette()
		}
	})
}

// header is a page's title bar, which drags the window, above a hairline
// (pageHeader in ui_kit.go without it).
func header(c *ui.Context, title string, trailing func()) {
	pageHeader(c, title, trailing)
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
