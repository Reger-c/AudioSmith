package main

// The 「高级」 section the creation pages share (thread #42): the settings
// most users never change, folded at the foot of a page's inspector or
// panel, open or closed per page (remembered in the settings). Its header
// says whether the page uses the model's recommended settings or how
// many it changes; inside, 「恢复推荐设置」 comes first.

import (
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// advancedOpen says whether page's 「高级」 section is open.
func (a *app) advancedOpen(page string) bool {
	return slices.Contains(a.prefs.Appearance.Advanced, page)
}

// setAdvancedOpen remembers page's 「高级」 section open or closed.
func (a *app) setAdvancedOpen(page string, open bool) {
	if a.advancedOpen(page) == open {
		return
	}
	a.updateSettings(func(s *settings.Settings) {
		s.Appearance.Advanced = slices.DeleteFunc(s.Appearance.Advanced, func(p string) bool { return p == page })
		if open {
			s.Appearance.Advanced = append(s.Appearance.Advanced, page)
			slices.Sort(s.Appearance.Advanced)
		}
	})
}

// changesLabel is the header's summary: 「推荐设置」 or 「已改 N 项」.
func changesLabel(changes []string) string {
	if n := len(changes); n > 0 {
		return i18n.N(n, "%d change", "%d changes", n)
	}
	return i18n.T("Recommended settings")
}

// advancedSection is page's 「高级」 section: changes names what differs
// from the recommendation (the header counts them, its tooltip lists
// them), reset restores the recommendation (nil: no button), body the
// controls.
func (a *app) advancedSection(c *ui.Context, page string, changes []string, locked bool, reset func(), body func()) {
	t := c.Theme()
	open := a.advancedOpen(page)
	collapsible(c.Key("adv-"+page), i18n.T("Advanced"), &open, func() {
		caption(c, changesLabel(changes)).SingleLine().Ellipsis("…").MinWidth(0).Shrink(1).Tooltip(strings.Join(changes, " · "))
	}, func() {
		if reset != nil {
			ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
				if ghostIconButton(c.Key("adv-reset"), icons.Restart, i18n.T("Restore recommended")).Label(i18n.T("Restore recommended")).
					Tooltip(i18n.T("Use the model's tuned settings again")).Disabled(locked || len(changes) == 0).Clicked() {
					reset()
				}
				infoTip(c.Key("adv-reset-tip"), i18n.T("Pre-filled with the model's tuned settings."))
			})
		}
		body()
	})
	a.setAdvancedOpen(page, open)
}
