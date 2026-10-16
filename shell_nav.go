package main

// The sidebar's layouts (M7a): the full sidebar, resizable, whose width
// and closed sections the settings remember, and the icon rail
// (Settings → Appearance, or the ‹ button) that keeps only the icons;
// then the full sidebar itself (sections, badges, the search field that
// opens the palette, Settings). Its pages are in shell_pages.go.

import (
	"slices"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
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
	k := tok(t)
	headerInset = 0
	if a.railMode() {
		// The window controls (the traffic lights) reach past the rail:
		// page titles move over to keep clear of them.
		headerInset = max(0, c.TitleBar().Left-px(t, railWidth)-px(t, sp6)+px(t, sp3))
		ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			ui.Column(c).Width(px(t, railWidth)).Shrink(0).Background(k.Sidebar).Children(func() { a.navRail(c) })
			ui.Box(c).Width(1).Background(k.Line)
			ui.Column(c).Grow(1).MinWidth(0).Background(k.Bg).Children(page)
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
		ui.Column(c).Fill().Background(k.Sidebar).Children(func() { a.sidebar(c) })
	}, func() {
		ui.Column(c).Grow(1).MinWidth(0).Background(k.Bg).Children(page)
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
	k := tok(t)
	if bar := c.TitleBar(); bar.Left > 0 {
		// The traffic lights sit at the top of the rail: the button goes
		// under them, in a row of its own.
		ui.Box(c).Height(titleBarHeight).Shrink(0).DragWindow()
		ui.Row(c).Shrink(0).Justify(ui.Center).Children(func() {
			if iconButton(c.Key("rail-off"), icons.SidebarMinimalistic, i18n.T("Show the sidebar")).Clicked() {
				a.setRail(false)
			}
		})
	} else {
		ui.Row(c).Height(titleBarHeight).Shrink(0).DragWindow().AlignItems(ui.End).Justify(ui.Center).Padding(0, 0, px(t, sp1), 0).Children(func() {
			if iconButton(c.Key("rail-off"), icons.SidebarMinimalistic, i18n.T("Show the sidebar")).Clicked() {
				a.setRail(false)
			}
		})
	}
	ui.Column(c).AlignItems(ui.Center).Padding(px(t, sp1), 0).Shrink(0).Children(func() {
		a.railButton(c, "rail-search", icons.Magnifer, i18n.T("Command palette"), i18n.T("Command palette")+" (⌘K)", false, "", a.openPalette)
	})
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).AlignItems(ui.Center).Gap(px(t, 2)).Padding(px(t, sp1), 0).Children(func() {
			for gi, g := range navGroups {
				if gi > 0 {
					ui.Box(c).Height(1).Width(px(t, 20)).Background(k.Line).Margin(px(t, 6), 0)
				}
				for _, p := range g.pages {
					a.railItem(c, p.path, p.icon, pageTitle(p.path))
				}
			}
		})
	})
	ui.Column(c).AlignItems(ui.Center).Padding(px(t, 6), 0, px(t, 10)).Children(func() {
		a.railButton(c, "rail/settings", icons.Settings, i18n.T("Settings"), i18n.T("Settings")+" (⌘,)", a.settingsOpen, "", func() { a.navigate("/settings") })
	})
}

// railItem is a page's icon in the rail.
func (a *app) railItem(c *ui.Context, path string, ic icons.Icon, title string) {
	tip := title
	if k := pageKeys(path); k != "" {
		tip += " (" + k + ")"
	}
	selected := !a.settingsOpen && section(a.router.Path()) == path
	a.railButton(c, "rail"+path, ic, title, tip, selected, a.navBadge(path), func() { a.navigate(path) })
}

// railButton is a button of the rail: Bold on the pressed face while
// selected, a dot for a badge.
func (a *app) railButton(c *ui.Context, key string, ic icons.Icon, label, tip string, selected bool, badge string, click func()) {
	t := c.Theme()
	k := tok(t)
	b := ui.ButtonBase(c.Key(key)).Size(px(t, 36), px(t, 32)).Radius(px(t, radiusControl)).Center().Tooltip(tip).Label(label)
	switch {
	case selected:
		b.Background(k.Pressed)
	case b.Hovered():
		b.Background(k.Hover)
	}
	b.Children(func() {
		col := k.Text2
		if selected {
			col = k.Text
		}
		ui.Icon(c, ic.Get(selected)).Size(px(t, 18), px(t, 18)).TextColor(col)
		if badge != "" {
			ui.Box(c).Absolute().Top(px(t, 4)).Right(px(t, 5)).Size(px(t, 7), px(t, 7)).Radius(px(t, 4)).Background(k.Accent)
		}
	})
	if b.Clicked() {
		click()
	}
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
