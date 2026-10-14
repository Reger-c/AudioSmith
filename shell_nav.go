package main

// The sidebar's layouts (M7a): the full sidebar, resizable, whose width
// and closed sections the settings remember, and the icon rail
// (Settings → Appearance, or the ‹ button) that keeps only the icons.

import (
	"slices"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// railWidth is the icon rail's width, DIPs at 100 %.
const railWidth = 52

// navState is the sidebar's view state.
type navState struct {
	navOpen    map[string]*bool // sections: open or closed
	sideW      float32          // the sidebar's width, DIPs at the theme's scale
	sideWScale float32          // the px factor sideW was computed at
	sideWAt    time.Time        // when the user last resized it (saved after a pause)
}

func (a *app) railMode() bool { return a.prefs.Appearance.Rail }

func (a *app) setRail(on bool) {
	a.updateSettings(func(s *settings.Settings) { s.Appearance.Rail = on })
}

// sectionOpen is the open state of a sidebar section, kept in the
// settings when the user closes it.
func (a *app) sectionOpen(id string) *bool {
	if a.navOpen == nil {
		a.navOpen = map[string]*bool{}
	}
	p, ok := a.navOpen[id]
	if !ok {
		open := !slices.Contains(a.prefs.Appearance.ClosedSections, id)
		p = &open
		a.navOpen[id] = p
	}
	return p
}

// saveClosedSections saves the sections the user closed or opened.
func (a *app) saveClosedSections() {
	var closed []string
	for _, g := range navGroups {
		if p, ok := a.navOpen[g.id]; ok && !*p {
			closed = append(closed, g.id)
		}
	}
	if slices.Equal(closed, a.prefs.Appearance.ClosedSections) {
		return
	}
	a.updateSettings(func(s *settings.Settings) { s.Appearance.ClosedSections = closed })
}

// sidebarFrame lays out the sidebar and the page: the rail, or the full
// sidebar beside a divider that resizes it.
func (a *app) sidebarFrame(c *ui.Context, page func()) {
	t := c.Theme()
	if a.railMode() {
		ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			ui.Column(c).Width(px(t, railWidth)).Shrink(0).Background(t.Surface).Children(func() { a.navRail(c) })
			ui.Box(c).Width(1).Background(t.Border)
			ui.Column(c).Grow(1).MinWidth(0).Background(t.Background).Children(page)
		})
		return
	}
	scale := px(t, 1)
	if a.sideW == 0 || a.sideWScale != scale {
		w := a.prefs.Appearance.SidebarWidth
		if w == 0 {
			w = sidebarWidth
		}
		a.sideW, a.sideWScale = px(t, float32(w)), scale
	}
	split := ui.Split(c.Key("sidebar-split"), &a.sideW, func() {
		ui.Column(c).Fill().Background(t.Surface).Children(func() { a.sidebar(c) })
	}, func() {
		ui.Column(c).Grow(1).MinWidth(0).Background(t.Background).Children(page)
	}).Grow(1).MinHeight(0)
	// Keep the width in its bounds, and save it once the drag rests.
	lo, hi := px(t, settings.MinSidebarWidth), px(t, settings.MaxSidebarWidth)
	if a.sideW < lo || a.sideW > hi {
		a.sideW = min(max(a.sideW, lo), hi)
	}
	if split.Changed() {
		a.sideWAt = time.Now()
		c.After(600 * time.Millisecond)
	}
	if !a.sideWAt.IsZero() && time.Since(a.sideWAt) >= 500*time.Millisecond {
		a.sideWAt = time.Time{}
		w := float64(a.sideW / scale)
		if w != a.prefs.Appearance.SidebarWidth {
			a.updateSettings(func(s *settings.Settings) { s.Appearance.SidebarWidth = w })
		}
	}
}

// navRail is the sidebar as a column of icons; their titles show as
// tooltips, a badge as a dot.
func (a *app) navRail(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Height(titleBarHeight).Shrink(0).DragWindow().AlignItems(ui.End).Justify(ui.Center).Padding(0, 0, px(t, 4), 0).Children(func() {
		b := railButton(c, "rail-off", "›", i18n.T("Show the sidebar"), false)
		if b.Clicked() {
			a.setRail(false)
		}
	})
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).AlignItems(ui.Center).Gap(px(t, 2)).Padding(px(t, 4), 0).Children(func() {
			for gi, g := range navGroups {
				if gi > 0 {
					ui.Box(c).Height(1).Width(px(t, 24)).Background(t.Border).Margin(px(t, 4), 0)
				}
				for _, p := range g.pages {
					a.railItem(c, p.path, p.icon, pageTitle(p.path))
				}
			}
		})
	})
	ui.Column(c).AlignItems(ui.Center).Padding(px(t, 6), 0).Children(func() {
		a.railItem(c, "/settings", settingsIcon, i18n.T("Settings"))
	})
}

// railItem is a page's icon in the rail.
func (a *app) railItem(c *ui.Context, path string, ic *ui.SVG, title string) {
	t := c.Theme()
	selected := section(a.router.Path()) == path
	tip := title
	if k := pageKeys(path); k != "" {
		tip += " (" + k + ")"
	}
	b := ui.ButtonBase(c.Key("rail" + path)).Width(px(t, 36)).Height(px(t, 32)).Radius(7).Center().Tooltip(tip).Label(title)
	switch {
	case selected:
		b.Background(t.Accent.Alpha(0.18))
	case b.Hovered():
		b.Background(t.SurfaceHover)
	}
	badge := a.navBadge(path)
	b.Children(func() {
		col := t.TextMuted
		if selected {
			col = t.Accent
		}
		ui.Icon(c, ic).TextColor(col).Width(px(t, 16)).Height(px(t, 16))
		if badge != "" {
			ui.Box(c).Absolute().Top(px(t, 4)).Right(px(t, 5)).Width(px(t, 7)).Height(px(t, 7)).Radius(4).Background(t.Accent)
		}
	})
	if b.Clicked() {
		a.navigate(path)
	}
}

// railButton is a small text button of the sidebar's title bar.
func railButton(c *ui.Context, key, glyph, label string, _ bool) ui.Element {
	t := c.Theme()
	b := ui.ButtonBase(c.Key(key)).Width(px(t, 24)).Height(px(t, 22)).Radius(5).Center().Tooltip(label).Label(label)
	if b.Hovered() {
		b.Background(t.SurfaceHover)
	}
	b.Children(func() { ui.Text(c, glyph).FontSize(px(t, 15)).TextColor(t.TextMuted) })
	return b
}
