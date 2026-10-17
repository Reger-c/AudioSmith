package main

// The window shell: the window, the sidebar and the router of pages, the
// status bar and the overlays every page shares (command palette, the
// shortcut sheet, the engine quick switch, notifications, the licence
// dialog), and the theme. Its parts:
//
//	shell_pages.go        sidebar sections and pages, titles, badges, the router
//	shell_nav.go          the sidebar, its search field and the icon rail
//	shell_commands.go     command registry, ⌘K palette, ? sheet, native menu, deep links
//	shell_status.go       status bar, notification centre, ⌘E quick switch
//	shell_logs.go         logs page and panel
//	shell_settings.go     settings service wiring, language, theme
//	settings_dialog.go    the settings dialog, its sections' router, rows and titles
//	settings_general.go   Settings → General, Appearance, Models, Engine, Studio
//	shell_models.go       Models state, downloads, licence gate and dialog
//	shell_models_view.go  Models page

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
		aboutState:   aboutState{updates: noUpdates{}},
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
	a.loadMediaTool()          // M7a: ffmpeg_settings.go
	a.migrateRemovedSettings() // models_removed.go (thread #43)
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
	a.systemLocale = systemLanguage()
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
	a.connectUpdates() // updates.go (thread #50)
	a.maybeOnboard()   // M7a: a fresh install opens the setup (onboarding.go)
}

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
	a.settingsDialog(c) // settings_dialog.go
	a.palette(c)
	a.shortcutSheet(c)
	a.onboarding(c) // M7a: the first-run wizard, under the licence dialog
	a.licenceModal(c)
	a.exportDialog(c) // export_dialog.go (thread #31)
	// family_coord.go: a shortcut conflict's card (not modal).
	a.shortcutConflictCard(c)
	a.modelsAlerts(c)
	a.removedAlert(c) // models_removed.go
	a.toastsView(c)   // last: over dialogs too
}

// toastsView gives the toasts their place and look: small ink toasts in
// the middle of the page's header (between its title and its actions,
// where it is empty), centred over the page; more stack under it. At the
// window's bottom (MyGo's place) they covered the bars and cards there:
// the Studio's Generate, a ready card's Save, the status bar. While the
// Settings dialog shows, they go to the foot of the dialog instead.
func (a *app) toastsView(c *ui.Context) {
	t := c.Theme()
	k := tok(t)
	ui.ToastViewportBase(c, func(vp ui.Element, toasts []ui.Toast) {
		w, _ := c.Size()
		page := a.pageWidth(c)
		vp.AlignItems(ui.Center).Gap(px(t, sp2))
		if a.settingsOpen {
			// Over the Settings dialog's foot: its title row is at the top.
			vp.Justify(ui.End).Padding(0, px(t, sp4), px(t, statusBarHeight+sp8))
		} else {
			vp.Justify(ui.Start).Padding(max(px(t, 4), (titleBarHeight-px(t, 36))/2), px(t, sp4), 0, w-page+px(t, sp4))
		}
		for _, ts := range toasts {
			p := ui.ToastBase(c, ts)
			box := p.Root.Row().AlignItems(ui.Center).Gap(px(t, sp3)).Padding(px(t, 9), px(t, 14)).Radius(px(t, radiusControl)).
				MaxWidth(min(px(t, 480), page-px(t, sp8))).Background(k.Ink).TextColor(k.OnInk).Shadow(0, 6, 20, 0, ui.RGBA(0, 0, 0, 0.22))
			// It fades out at the end.
			const fade = 200 * time.Millisecond
			switch left := p.Left(); {
			case left > 0 && left < fade:
				box.Opacity(float32(left) / float32(fade))
				c.AnimationFrame()
			case left > 0:
				c.After(left - fade)
			}
			box.Children(func() {
				ui.Text(c, ts.Title).MinWidth(0).Shrink(1)
				if ts.Action == "" {
					return
				}
				b := p.ActionButton().Padding(px(t, 3), px(t, 8)).Radius(px(t, 6)).TextColor(k.OnInk).FontWeight(600).Label(ts.Action).Shrink(0)
				if b.Hovered() {
					b.Background(k.OnInk.Alpha(0.14))
				}
				b.Children(func() { ui.Text(c, ts.Action).SingleLine() })
			})
		}
	})
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
