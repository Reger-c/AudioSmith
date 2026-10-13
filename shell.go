package main

// The window shell: the window itself, the sidebar and page switching,
// page headers. Thread C replaces the page switch with ui.Router, adds the
// status bar, command palette, settings and ⌘E here and in new shell_*.go
// files.

import (
	"fmt"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/i18n"
)

const (
	titleBarHeight = 52
	sidebarWidth   = 200
	inspectorWidth = 300
)

// shellState is the shell's view state.
type shellState struct {
	page string // sidebar selection: "studio", "history" or "engine"
}

func newShellState() shellState {
	return shellState{page: "studio"}
}

// openWindow opens the main window; it runs in App.WhenReady.
func (a *app) openWindow() {
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
}

func icon(shapes string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round">` + shapes + `</svg>`))
}

var (
	studioIcon  = icon(`<path d="M2 8h1.5M5 5v6M8 3v10M11 5.5v5M14 8h-1"/>`)
	historyIcon = icon(`<circle cx="8" cy="8" r="6"/><path d="M8 4.5V8l2.5 1.5"/>`)
	engineIcon  = icon(`<rect x="3.5" y="3.5" width="9" height="9" rx="1.5"/><path d="M6 1.5v2M10 1.5v2M6 12.5v2M10 12.5v2M1.5 6h2M1.5 10h2M12.5 6h2M12.5 10h2"/>`)
)

func (a *app) view(c *ui.Context) {
	t := c.Theme()
	for _, m := range a.toasts {
		c.Toast(m)
	}
	a.toasts = nil

	if c.Shortcut(ui.Cmd, ui.KeyEnter) {
		a.generate()
	}
	if a.job != nil {
		// Keep the elapsed time current.
		c.After(500 * time.Millisecond)
	}
	if a.pl.Active() {
		// Notice the end of playback, so Pause turns back into Play.
		c.After(200 * time.Millisecond)
	}

	ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Width(sidebarWidth).Shrink(0).Background(t.Surface).Children(func() {
			a.sidebar(c)
		})
		ui.Box(c).Width(1).Background(t.Border)
		ui.Column(c).Grow(1).MinWidth(0).Background(t.Background).Children(func() {
			switch a.page {
			case "history":
				a.historyPage(c)
			case "engine":
				a.enginePage(c)
			default:
				a.studioPage(c)
			}
		})
	})
}

func (a *app) sidebar(c *ui.Context) {
	t := c.Theme()
	ui.Box(c).Height(titleBarHeight).Shrink(0).DragWindow()
	ui.Sidebar(c, &a.page, func() {
		ui.SidebarSection(c, "AudioSmith", nil, func() {
			ui.SidebarItem(c, "studio", studioIcon, i18n.T("Studio"))
			ui.SidebarItem(c, "history", historyIcon, i18n.T("History")).Children(func() {
				if n := len(a.takes); n > 0 {
					ui.Badge(c, fmt.Sprint(n))
				}
			})
			ui.SidebarItem(c, "engine", engineIcon, i18n.T("Engine"))
		})
	}).Grow(1).Background(ui.Transparent).Label(i18n.T("Sections"))

	// The engine's status, at the bottom.
	ui.Column(c).Padding(12, 14).Gap(4).Children(func() {
		color, label := t.Warning, i18n.T("Engine starting…")
		switch a.engState {
		case engineReady:
			color, label = t.Success, i18n.T("Engine ready")
		case engineFailed:
			color, label = t.Danger, i18n.T("Engine stopped")
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Size(8, 8).Radius(4).Background(color)
			ui.Text(c, label).FontSize(12)
		})
		model := i18n.T("No model loaded")
		if a.loadedModel != "" {
			model = catalog.Builtin().DisplayName(a.loadedModel)
		}
		ui.Text(c, model).FontSize(11).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
	})
}

// header is a page's title bar, which drags the window.
func header(c *ui.Context, title string, trailing func()) {
	ui.Row(c).Height(titleBarHeight).Shrink(0).AlignItems(ui.Center).Padding(0, 16, 0, 20).Gap(8).DragWindow().Children(func() {
		ui.Text(c, title).FontSize(15).Bold().SingleLine().Grow(1)
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
