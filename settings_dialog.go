package main

// The settings dialog (0.3, ⌘,): its column of sections, the router of
// its sections (settingsSection), and the rows and titles every section
// is built of (settingRow, settingRowNote, sectionTitle). The settings
// service wiring is shell_settings.go.

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
)

// settingsIcon is a settings section's icon.
func settingsSectionIcon(id string) icons.Icon {
	switch id {
	case "general":
		return icons.Settings
	case "appearance":
		return icons.Pallete2
	case "models":
		return icons.CloudDownload
	case "studio":
		return icons.Soundwave
	case "engine":
		return icons.CpuBolt
	case "pronunciation":
		return icons.TextSquare
	case "dictation":
		return icons.Microphone3
	case "writing":
		return icons.PenNewRound
	case "permissions":
		return icons.LockKeyholeMinimalistic
	case "dubbing":
		return icons.VideoFramePlayHorizontal
	case "api":
		return icons.Programming
	case "audio-tools":
		return icons.CaseRound
	case "network":
		return icons.WiFiRouterMinimalistic
	case "privacy":
		return icons.ShieldCheck
	case "storage":
		return icons.Database
	case "usage":
		return icons.Chart2
	}
	return icons.InfoCircle
}

// openSettings shows the settings dialog at a section's path ("/models").
func (a *app) openSettings(sub string) {
	if a.setRouter.Path() != sub {
		a.setRouter.Replace(sub)
	}
	a.settingsOpen = true
}

// shownPath is the path of what shows: the settings dialog's section
// ("/settings/models") while it is open, else the page's.
func (a *app) shownPath() string {
	if a.settingsOpen {
		return "/settings" + a.setRouter.Path()
	}
	return a.router.Path()
}

// settingsDialog is Settings (⌘,): a dialog over the page, its sections
// in a column on the left (0.3: Typeless-style, research/ui-design-spec.md).
func (a *app) settingsDialog(c *ui.Context) {
	if !a.settingsOpen {
		return
	}
	t := c.Theme()
	k := tok(t)
	w, h := c.Size()
	margin := px(t, sp6)
	ui.DialogBase(c.Key("settings-dialog"), &a.settingsOpen, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.32))
		panel.Width(min(w-2*margin, px(t, 820))).Height(h-4*margin).Radius(px(t, radiusCard)).Background(k.Bg).Clip().
			Shadow(0, 16, 48, 0, ui.RGBA(0, 0, 0, 0.28)).Label(i18n.T("Settings"))
		ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			ui.Column(c).Width(min(px(t, 196), (w-2*margin)*0.28)).Shrink(0).Background(k.Sidebar).Children(func() {
				ui.Row(c).Height(px(t, 52)).Shrink(0).AlignItems(ui.Center).Padding(0, px(t, sp4)).Children(func() {
					ui.Text(c, i18n.T("Settings")).FontSize(px(t, fsSection)).FontWeight(600).SingleLine()
				})
				ui.Scroll(c).Grow(1).Children(func() {
					ui.Column(c).Padding(0, px(t, sp2), px(t, sp2)).Gap(px(t, 2)).Children(func() {
						cur := strings.TrimPrefix(a.setRouter.Path(), "/")
						cur, _, _ = strings.Cut(cur, "/")
						for _, s := range settingsSections() {
							sel := cur == s.id
							b := ui.ButtonBase(c.Key("settings-"+s.id)).Padding(px(t, 6), px(t, sp2)).Gap(px(t, sp2)).Radius(px(t, radiusControl)).
								Justify(ui.Start).Label(s.title).Checked(sel)
							switch {
							case sel:
								b.Background(k.Pressed)
							case b.Hovered():
								b.Background(k.Hover)
							}
							b.Children(func() {
								col := k.Text2
								if sel {
									col = k.Text
								}
								ui.Icon(c, settingsSectionIcon(s.id).Get(sel)).Size(px(t, 16), px(t, 16)).TextColor(col)
								txt := ui.Text(c, s.title).SingleLine().Ellipsis("…").MinWidth(0).Grow(1)
								if sel {
									txt.FontWeight(600)
								} else {
									txt.TextColor(k.G[8])
								}
							})
							if b.Clicked() {
								a.navigate("/settings/" + s.id)
							}
						}
					})
				})
			})
			ui.Box(c).Width(1).Background(k.Line)
			ui.Column(c).Grow(1).MinWidth(0).Children(func() {
				a.setRouter.View(c, func(r *ui.Route) {
					title := ""
					for _, s := range settingsSections() {
						if r.Match("/" + s.id) {
							title = s.title
						}
					}
					ui.Row(c).Height(px(t, 52)).Shrink(0).AlignItems(ui.Center).Padding(0, px(t, sp3), 0, px(t, sp6)).Gap(px(t, sp2)).Children(func() {
						ui.Text(c, title).FontSize(px(t, fsSection)).FontWeight(600).SingleLine().Ellipsis("…").MinWidth(0).Grow(1)
						if iconButton(c.Key("settings-close"), icons.Close, i18n.T("Close")).Clicked() {
							a.settingsOpen = false
						}
					})
					ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
						ui.Column(c).Padding(px(t, sp1), px(t, sp6), px(t, sp6)).Gap(px(t, sp4)).MaxWidth(px(t, 680)).Children(func() {
							a.settingsSection(c, r)
						})
					})
				}).Grow(1).MinHeight(0)
			})
		})
	})
}

// settingsSection builds the section of the settings dialog r shows.
func (a *app) settingsSection(c *ui.Context, r *ui.Route) {
	t := c.Theme()
	switch {
	case r.Match("/general"):
		r.Title(i18n.T("General"))
		a.lastSettings = "general"
		a.generalSettings(c)
	case r.Match("/appearance"):
		r.Title(i18n.T("Appearance"))
		a.lastSettings = "appearance"
		a.appearanceSettings(c)
	case r.Match("/models"):
		r.Title(i18n.T("Models and downloads"))
		a.lastSettings = "models"
		a.modelSettings(c)
	case r.Match("/studio"):
		r.Title(i18n.T("Studio"))
		a.lastSettings = "studio"
		a.studioSettings(c)
	case r.Match("/engine"):
		r.Title(i18n.T("Engine"))
		a.lastSettings = "engine"
		a.engineSettings(c)
	case r.Match("/pronunciation"): // M1-G: pronunciation_settings.go
		r.Title(i18n.T("Pronunciation"))
		a.lastSettings = "pronunciation"
		a.pronunciationSettings(c)
	// M2 (thread #17).
	case r.Match("/dictation"):
		r.Title(i18n.T("Dictation"))
		a.lastSettings = "dictation"
		a.dictationSettings(c)
	case r.Match("/writing"):
		r.Title(i18n.T("Writing help"))
		a.lastSettings = "writing"
		a.writingSettings(c)
	case r.Match("/permissions"):
		r.Title(i18n.T("Permissions"))
		a.lastSettings = "permissions"
		a.permissionsSettings(c)
	case a.dubSettingsRoute(c, r): // M4: Settings → Dubbing (dub_pages.go, thread #19)
	case a.apiSettingsRoute(c, r): // M5: api_settings.go (thread #20)
	case a.m7SettingsRoute(c, r): // M7a: settings_pages.go
	default:
		ui.Text(c, i18n.T("There is no page at %s.", a.shownPath())).TextColor(t.TextMuted)
	}
}

// settingRow is a setting: its name, a ⓘ telling what it does (desc, ""
// for none) and its control on the right (formRow in ui_layout.go).
func settingRow(c *ui.Context, key, title, desc string, control func()) {
	formRow(c, key, title, desc, control)
}

// settingRowNote is a setting whose description is a state to read at
// once (a path, what is missing, a version): it shows under the name.
func settingRowNote(c *ui.Context, key, title, note string, control func()) {
	t := c.Theme()
	ui.Row(c.Key(key)).Wrap().GapX(px(t, sp4)).GapY(px(t, 6)).AlignItems(ui.Center).MinHeight(px(t, 32)).Children(func() {
		ui.Column(c).Grow(1).Basis(px(t, 200)).MinWidth(px(t, 140)).Gap(px(t, 2)).Children(func() {
			ui.Text(c, title)
			if note != "" {
				caption(c, note).Selectable()
			}
		})
		ui.Row(c).Wrap().Gap(px(t, sp2)).AlignItems(ui.Center).Shrink(1).MinWidth(0).Children(control)
	})
}

// sectionTitle heads a group of settings.
func sectionTitle(c *ui.Context, title string) {
	t := c.Theme()
	sectionTitleText(c, title).Margin(px(t, sp2), 0, 0, 0)
}

func sectionTitleText(c *ui.Context, title string) ui.Element {
	t := c.Theme()
	return ui.Text(c, title).FontSize(px(t, fsCaption)).FontWeight(600).TextColor(tok(t).Text2)
}

// sectionTitleTip is sectionTitle with a ⓘ beside it: the margin goes on
// the row, so the title and the ⓘ share one centre line.
func sectionTitleTip(c *ui.Context, title, tip string) {
	t := c.Theme()
	ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Margin(px(t, sp2), 0, 0, 0).Children(func() {
		sectionTitleText(c, title)
		infoTip(c, tip)
	})
}
