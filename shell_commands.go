package main

// Commands: one registry behind the ⌘K palette, the native menu and the
// window's shortcuts, plus the "?" sheet listing the shortcuts, and
// audiosmith:// deep links.

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// command is something the user can do from anywhere.
type command struct {
	id      string
	group   string // translated
	title   string // translated
	en      string // English title, so English words find it in any language
	keys    string // shortcut as shown, "⌘1"
	run     func()
	enabled bool
}

func itoa(n int) string { return fmt.Sprint(n) }

// commands lists the commands, in the palette's order.
func (a *app) commands() []command {
	nav := i18n.T("Go to")
	var cmds []command
	add := func(id, group, title, en, keys string, enabled bool, run func()) {
		cmds = append(cmds, command{id: id, group: group, title: title, en: en, keys: keys, run: run, enabled: enabled})
	}
	for i, p := range pages {
		add("go"+p.path, nav, pageTitle(p.path), p.title, "⌘"+itoa(i+1), true, func() { a.navigate(p.path) })
	}
	a.longformCommands(nav, add) // M3: Stories, Audiobook, Projects (longform_pages.go, thread #18)
	a.dubCommands(nav, add)      // M4: Dub, Batch (dub_pages.go, thread #19)
	a.m5Commands(nav, add)       // M5: Tools, Integrations, API reference (api_pages.go, thread #20)
	a.wfCommands(nav, add)       // M6: Workflows (workflow_pages.go, thread #21)
	a.callsCommands(nav, add)    // M6b: Calls (calls_pages.go, thread #23)
	add("go/settings", nav, i18n.T("Settings"), "Settings", "⌘,", true, func() { a.navigate("/settings") })
	for _, s := range settingsSections() {
		add("go/settings/"+s.id, i18n.T("Settings"), s.title, s.en, "", true, func() { a.navigate("/settings/" + s.id) })
	}

	studio := i18n.T("Studio")
	eng := a.engineSummary()
	add("generate", studio, i18n.T("Generate"), "Generate", "⌘↩", !eng.Busy && eng.State == engineReady, func() {
		a.navigate("/studio")
		a.generate()
	})
	add("cancel", studio, i18n.T("Cancel generation"), "Cancel generation", "", eng.Busy, a.cancel)
	add("preload", studio, i18n.T("Preload the model"), "Preload the model", "", !eng.Busy && eng.State == engineReady, a.preload)
	add("unload", studio, i18n.T("Unload the model"), "Unload the model", "", !eng.Busy && len(eng.Loaded) > 0, a.unload)
	add("restart", studio, i18n.T("Restart engine"), "Restart engine", "", true, a.restartEngine)

	// M2 (thread #17).
	speech := i18n.T("Speech to text")
	add("dictate", speech, i18n.T("Start or stop dictation"), "Start or stop dictation dictate", dictationGlyphs(a.dictShortcut), true, a.dictToggle)
	add("transcribe-file", speech, i18n.T("Transcribe a file…"), "Transcribe a file", "", a.trJob == nil, func() {
		a.navigate("/transcripts")
		a.chooseTranscribeFile()
	})

	view := i18n.T("View")
	add("palette", view, i18n.T("Command palette"), "Command palette", "⌘K", true, a.openPalette)
	add("switch", view, i18n.T("Engine quick switch"), "Engine quick switch", "⌘E", true, func() { a.switchOpen = true })
	add("logs-panel", view, i18n.T("Show or hide the logs panel"), "Show or hide the logs panel", "⇧⌘L", true, func() { a.logsPanel = !a.logsPanel })
	add("notifications", view, i18n.T("Notifications"), "Notifications", "", true, func() { a.noticesOpen = true })
	add("shortcuts", view, i18n.T("Keyboard shortcuts"), "Keyboard shortcuts", "?", true, func() { a.sheetOpen = true })

	look := i18n.T("Appearance")
	for _, th := range []struct{ id, title, en string }{
		{"system", i18n.T("Theme: follow the system"), "Theme: follow the system"},
		{"light", i18n.T("Theme: light"), "Theme: light"},
		{"dark", i18n.T("Theme: dark"), "Theme: dark"},
	} {
		add("theme/"+th.id, look, th.title, th.en, "", a.prefs.Appearance.Theme != th.id, func() {
			a.updateSettings(func(s *settings.Settings) { s.Appearance.Theme = th.id })
		})
	}
	lang := i18n.T("Language")
	for _, tag := range append([]string{""}, i18n.Supported()...) {
		title := i18n.T("Follow the system language")
		if tag != "" {
			title = i18n.Name(tag)
		}
		add("lang/"+tag, lang, title, "Language "+tag, "", a.prefs.General.Language != tag, func() {
			a.updateSettings(func(s *settings.Settings) { s.General.Language = tag })
		})
	}
	return cmds
}

// matches reports whether every word of q is in the command's words.
func (cmd command) matches(q string) bool {
	hay := strings.ToLower(cmd.title + " " + cmd.group + " " + cmd.en + " " + cmd.id)
	for _, w := range strings.Fields(strings.ToLower(q)) {
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

func (a *app) paletteItems() []command {
	var out []command
	for _, cmd := range a.commands() {
		if cmd.enabled && cmd.matches(a.paletteQuery) {
			out = append(out, cmd)
		}
	}
	return out
}

func (a *app) openPalette() {
	if !a.paletteOpen {
		a.paletteOpen, a.paletteQuery, a.paletteSel = true, "", 0
	}
}

// runCommand closes the overlays and runs cmd.
func (a *app) runCommand(cmd command) {
	a.paletteOpen, a.sheetOpen = false, false
	cmd.run()
}

// shortcuts handles the window's keys. Each opens rather than toggles
// what the native menu's items also open, so a key that reaches both
// does the same thing once.
func (a *app) shortcuts(c *ui.Context) {
	if c.Shortcut(ui.Cmd, ui.KeyEnter) {
		a.generate()
	}
	if c.Shortcut(ui.Cmd, ui.KeyK) {
		a.openPalette()
	}
	if c.Shortcut(ui.Cmd, ui.KeyE) {
		a.switchOpen = true
	}
	if c.Shortcut(ui.Cmd, ui.KeyComma) {
		a.navigate("/settings")
	}
	for i, p := range pages {
		if c.Shortcut(ui.Cmd, ui.Key1+ui.Key(i)) {
			a.navigate(p.path)
		}
	}
	if c.Shortcut(ui.Cmd|ui.Shift, ui.KeyL) {
		a.logsPanel = !a.logsPanel
	}
	if !a.scriptFocused() && c.Shortcut(ui.Shift, ui.KeySlash) { // M1-G: "?" is text in the script editor
		a.sheetOpen = true
	}
}

// palette is the ⌘K command palette.
func (a *app) palette(c *ui.Context) {
	t := c.Theme()
	ui.DialogBase(c, &a.paletteOpen, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.2)).Justify(ui.Start).Padding(px(t, 90), 0, 0, 0)
		panel.Width(px(t, 560)).Radius(12).Background(t.Background).Border(1, t.Border).
			Shadow(0, 12, 32, 0, ui.RGBA(0, 0, 0, 0.25)).Label(i18n.T("Command palette"))
		items := a.paletteItems()
		a.paletteSel = max(0, min(a.paletteSel, len(items)-1))
		ui.Row(c).Padding(px(t, 10), px(t, 12)).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, searchIcon).TextColor(t.TextMuted)
			// A text input rather than ui.SearchField, whose placeholder
			// is not translated; Escape closes the palette.
			search := ui.TextInput(c.Key("palette-search"), &a.paletteQuery).AutoFocus().Grow(1).
				Placeholder(i18n.T("Type a command or a page…")).Label(i18n.T("Search commands"))
			if search.Changed() {
				a.paletteSel = 0
			}
			if search.Shortcut(0, ui.KeyDown) && len(items) > 0 {
				a.paletteSel = (a.paletteSel + 1) % len(items)
			}
			if search.Shortcut(0, ui.KeyUp) && len(items) > 0 {
				a.paletteSel = (a.paletteSel - 1 + len(items)) % len(items)
			}
			if search.Submitted() && len(items) > 0 {
				a.runCommand(items[a.paletteSel])
			}
		})
		ui.Divider(c)
		ui.Scroll(c).MaxHeight(px(t, 360)).Children(func() {
			ui.Column(c).Padding(px(t, 6)).Children(func() {
				if len(items) == 0 {
					ui.Text(c, i18n.T("No matching commands")).TextColor(t.TextMuted).Padding(px(t, 10))
				}
				for i, cmd := range items {
					row := ui.ButtonBase(c.Key("cmd-"+cmd.id)).Padding(px(t, 7), px(t, 10)).Radius(6).Label(cmd.title)
					if i == a.paletteSel {
						row.Background(t.Accent.Alpha(0.15))
					}
					row.Children(func() {
						ui.Row(c).Grow(1).Gap(px(t, 10)).AlignItems(ui.Center).Children(func() {
							ui.Text(c, cmd.title).Grow(1).SingleLine().Ellipsis("…")
							ui.Text(c, cmd.group).FontSize(px(t, 11)).TextColor(t.TextMuted)
							if cmd.keys != "" {
								ui.Text(c, cmd.keys).FontSize(px(t, 11)).TextColor(t.TextMuted).Width(px(t, 36))
							}
						})
					})
					if row.Clicked() {
						a.runCommand(cmd)
					}
				}
			})
		})
	})
}

// shortcutSheet lists the keyboard shortcuts ("?").
func (a *app) shortcutSheet(c *ui.Context) {
	t := c.Theme()
	ui.Modal(c, &a.sheetOpen, func() {
		ui.Text(c, i18n.T("Keyboard shortcuts")).FontSize(px(t, 15)).Bold()
		groups := []struct {
			title string
			rows  [][2]string
		}{
			{i18n.T("Navigation"), [][2]string{
				{"⌘K", i18n.T("Command palette")},
				{"⌘1 … ⌘5", i18n.T("Studio, History, Models, Engine, Logs")},
				{"⌘,", i18n.T("Settings")},
				{"⌘[  ⌘]", i18n.T("Back, forward")},
				{"⌘E", i18n.T("Engine quick switch")},
				{"⇧⌘L", i18n.T("Show or hide the logs panel")},
				{"?", i18n.T("Keyboard shortcuts")},
				{"Esc", i18n.T("Close a dialog or popover")},
			}},
			{i18n.T("Studio"), [][2]string{
				{"⌘↩", i18n.T("Generate")},
			}},
		}
		ui.Column(c).Gap(px(t, 12)).Width(px(t, 420)).Children(func() {
			for _, g := range groups {
				ui.Text(c, g.title).FontSize(px(t, 12)).Bold().TextColor(t.TextMuted)
				ui.Grid(c).ColumnTracks(ui.Fixed(px(t, 90)), ui.Fr(1)).GapX(px(t, 12)).GapY(px(t, 6)).Children(func() {
					for _, r := range g.rows {
						ui.Text(c, r[0]).Font("monospace").FontSize(px(t, 12))
						ui.Text(c, r[1])
					}
				})
			}
		})
		ui.Row(c).Justify(ui.End).Children(func() {
			if ui.Button(c.Key("sheet-close"), i18n.T("Close")).Clicked() {
				a.sheetOpen = false
			}
		})
	})
}

// buildMenu sets the native menu bar in the current language; the
// language switch calls it again. Items with shortcuts open what the
// window's own shortcuts open (see shortcuts).
func (a *app) buildMenu() {
	a.menuLocale = i18n.Locale()
	if !a.hasMenu {
		return
	}
	act := func(fn func()) func(*mygo.MenuItem, *mygo.Window) {
		return func(*mygo.MenuItem, *mygo.Window) { a.post(fn) }
	}
	goItems := []*mygo.MenuItem{}
	for i, p := range pages {
		goItems = append(goItems, &mygo.MenuItem{Label: pageTitle(p.path), Accelerator: "CmdOrCtrl+" + itoa(i+1), Click: act(func() { a.navigate(p.path) })})
	}
	mygo.App.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
		{Label: "AudioSmith", Submenu: []*mygo.MenuItem{
			{Role: mygo.RoleAbout, Label: i18n.T("About AudioSmith")},
			mygo.Separator(),
			{Label: i18n.T("Settings…"), Accelerator: "CmdOrCtrl+,", Click: act(func() { a.navigate("/settings") })},
			mygo.Separator(),
			{Role: mygo.RoleServices, Label: i18n.T("Services")},
			mygo.Separator(),
			{Role: mygo.RoleHide, Label: i18n.T("Hide AudioSmith")},
			{Role: mygo.RoleHideOthers, Label: i18n.T("Hide Others")},
			{Role: mygo.RoleUnhide, Label: i18n.T("Show All")},
			mygo.Separator(),
			{Role: mygo.RoleQuit, Label: i18n.T("Quit AudioSmith")},
		}},
		{Label: i18n.T("Edit"), Submenu: []*mygo.MenuItem{
			{Role: mygo.RoleUndo, Label: i18n.T("Undo")},
			{Role: mygo.RoleRedo, Label: i18n.T("Redo")},
			mygo.Separator(),
			{Role: mygo.RoleCut, Label: i18n.T("Cut")},
			{Role: mygo.RoleCopy, Label: i18n.T("Copy")},
			{Role: mygo.RolePaste, Label: i18n.T("Paste")},
			{Role: mygo.RoleSelectAll, Label: i18n.T("Select All")},
		}},
		{Label: i18n.T("View"), Submenu: []*mygo.MenuItem{
			{Label: i18n.T("Command palette"), Accelerator: "CmdOrCtrl+K", Click: act(a.openPalette)},
			{Label: i18n.T("Engine quick switch"), Accelerator: "CmdOrCtrl+E", Click: act(func() { a.switchOpen = true })},
			{Label: i18n.T("Show or hide the logs panel"), Click: act(func() { a.logsPanel = !a.logsPanel })},
			{Label: i18n.T("Notifications"), Click: act(func() { a.noticesOpen = true })},
			mygo.Separator(),
			{Role: mygo.RoleToggleFullScreen, Label: i18n.T("Enter Full Screen")},
		}},
		{Label: i18n.T("Go"), Submenu: append(goItems,
			mygo.Separator(),
			&mygo.MenuItem{Label: i18n.T("Settings"), Click: act(func() { a.navigate("/settings") })},
		)},
		{Label: i18n.T("Window"), Submenu: []*mygo.MenuItem{
			{Role: mygo.RoleMinimize, Label: i18n.T("Minimize")},
			{Role: mygo.RoleZoom, Label: i18n.T("Zoom")},
			mygo.Separator(),
			{Role: mygo.RoleFront, Label: i18n.T("Bring All to Front")},
		}},
		{Label: i18n.T("Help"), Submenu: []*mygo.MenuItem{
			{Label: i18n.T("Keyboard shortcuts"), Click: act(func() { a.sheetOpen = true })},
			{Label: i18n.T("Logs"), Click: act(func() { a.navigate("/logs") })},
		}},
	}))
}

// openURL follows an audiosmith:// deep link: audiosmith://models,
// audiosmith://settings/models, audiosmith:///studio. Router paths in
// notifications go through it too.
func (a *app) openURL(raw string) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "" && !strings.EqualFold(u.Scheme, "audiosmith")) {
		a.toast(i18n.T("Unknown link: %s", raw))
		return
	}
	path := u.Path
	if u.Scheme != "" && u.Host != "" {
		path = "/" + u.Host + u.Path
	}
	path = "/" + strings.Trim(path, "/")
	switch section(path) {
	case "/studio", "/history", "/models", "/engine", "/logs", "/transcripts": // M2: /transcripts
		a.navigate(section(path))
	case "/settings":
		if path == "/settings" {
			path = "/settings/" + a.lastSettings
		}
		a.navigate(path)
	default:
		a.toast(i18n.T("Unknown link: %s", raw))
	}
}
