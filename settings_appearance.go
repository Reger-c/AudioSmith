package main

// Accent palettes (M7a): Settings → Appearance offers a few accents over
// the light and dark themes; "" keeps MyGo's blue.

import (
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// accent is a palette's accent in one appearance.
type accent struct{ base, hover, pressed, text string }

// palettes are settings.Palettes' accents: light, dark.
var palettes = map[string][2]accent{
	"ocean":    {{"#0e7490", "#155e75", "#164e63", "#ffffff"}, {"#0891b2", "#06b6d4", "#0e7490", "#ffffff"}},
	"violet":   {{"#7c3aed", "#6d28d9", "#5b21b6", "#ffffff"}, {"#8b5cf6", "#a78bfa", "#7c3aed", "#ffffff"}},
	"forest":   {{"#15803d", "#166534", "#14532d", "#ffffff"}, {"#16a34a", "#22c55e", "#15803d", "#ffffff"}},
	"sunset":   {{"#ea580c", "#c2410c", "#9a3412", "#ffffff"}, {"#f97316", "#fb923c", "#ea580c", "#ffffff"}},
	"graphite": {{"#3f3f46", "#27272a", "#18181b", "#ffffff"}, {"#d4d4d8", "#e4e4e7", "#a1a1aa", "#18181b"}},
}

// applyPalette sets th's accent colours from the palette id.
func applyPalette(th *ui.Theme, id string) {
	p, ok := palettes[id]
	if !ok {
		return
	}
	ac := p[0]
	if th.Dark {
		ac = p[1]
	}
	th.Accent, th.AccentHover, th.AccentPressed, th.AccentText = ui.Hex(ac.base), ui.Hex(ac.hover), ui.Hex(ac.pressed), ui.Hex(ac.text)
	th.Selection = th.Accent.Alpha(0.3)
	th.Focus = th.Accent.Alpha(0.55)
}

// paletteName translates a palette's name.
func paletteName(id string) string {
	switch id {
	case "ocean":
		return i18n.T("Ocean")
	case "violet":
		return i18n.T("Violet")
	case "forest":
		return i18n.T("Forest")
	case "sunset":
		return i18n.T("Sunset")
	case "graphite":
		return i18n.T("Graphite")
	}
	return i18n.T("Blue (default)")
}

// paletteRow is the palette chooser: a swatch per palette.
func (a *app) paletteRow(c *ui.Context) {
	t := c.Theme()
	settingRow(c, "palette", i18n.T("Accent colour"), paletteName(a.prefs.Appearance.Palette), func() {
		ui.Row(c).Gap(px(t, 6)).Children(func() {
			for _, id := range settings.Palettes {
				col := ui.LightTheme().Accent
				if t.Dark {
					col = ui.DarkTheme().Accent
				}
				if p, ok := palettes[id]; ok {
					col = ui.Hex(p[0].base)
					if t.Dark {
						col = ui.Hex(p[1].base)
					}
				}
				b := ui.ButtonBase(c.Key("palette-" + id)).Width(px(t, 24)).Height(px(t, 24)).Radius(12).Background(col).
					Label(paletteName(id)).Tooltip(paletteName(id))
				if a.prefs.Appearance.Palette == id {
					b.Border(2, t.Text)
				}
				if b.Clicked() {
					pid := id
					a.updateSettings(func(s *settings.Settings) { s.Appearance.Palette = pid })
				}
			}
		})
	})
}

// sidebarRow chooses the sidebar's layout.
func (a *app) sidebarRow(c *ui.Context) {
	rail := 0
	if a.prefs.Appearance.Rail {
		rail = 1
	}
	settingRow(c, "sidebar-style", i18n.T("Sidebar"), i18n.T("Icons only leaves more room for the pages; their names show when the pointer rests on them."), func() {
		if ui.Segmented(c.Key("sidebar-seg"), &rail, i18n.T("Names and icons"), i18n.T("Icons only")).Label(i18n.T("Sidebar")).Changed() {
			a.setRail(rail == 1)
		}
	})
}
