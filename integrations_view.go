package main

import (
	"slices"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
)

// integrationsPage is /integrations: the cards.
func (a *app) integrationsPage(c *ui.Context) {
	t := c.Theme()
	pageHeader(c, i18n.T("Integrations"), func() {
		if iconButton(c.Key("integ-ref"), icons.Programming, i18n.T("API reference")).Clicked() {
			a.navigate("/api-reference")
		}
	})
	cols := 2
	if a.pageWidth(c) < px(t, 640) {
		cols = 1
	}
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(px(t, sp1), px(t, sp6), px(t, sp6)).Gap(px(t, sp3)).MaxWidth(px(t, 960)).Children(func() {
			ui.Row(c).Gap(px(t, sp2)).AlignItems(ui.Center).Children(func() {
				searchField(c, "integ-search", &a.integQuery, i18n.T("Search"), px(t, 320))
			})
			a.apiStatusLine(c)
			q := strings.ToLower(strings.TrimSpace(a.integQuery))
			var shown []integration
			for _, in := range integrations {
				hay := strings.ToLower(in.Name + " " + integCaption(in.Kind) + " " + integAbout(in.Slug))
				for _, cp := range in.Caps {
					hay += " " + strings.ToLower(integCaption(cp))
				}
				if q == "" || strings.Contains(hay, q) {
					shown = append(shown, in)
				}
			}
			if len(shown) == 0 {
				emptyState(c, icons.Magnifer, i18n.T("No matches"), "", nil)
				return
			}
			ui.Grid(c).Columns(cols).Gap(px(t, sp3)).Children(func() {
				for _, in := range shown {
					a.integrationCard(c, in)
				}
			})
		})
	})
}

// apiStatusLine says whether the Local API runs, with a link to its settings.
func (a *app) apiStatusLine(c *ui.Context) {
	t := c.Theme()
	k := tok(t)
	ui.Row(c.Key("api-line")).Gap(px(t, sp2)).AlignItems(ui.Center).Padding(px(t, 6), px(t, 6), px(t, 6), px(t, sp3)).Radius(px(t, radiusControl)).Background(k.Subtle).Children(func() {
		switch {
		case !a.prefs.API.Enabled:
			statusDot(c, t.Warning)
			ui.Text(c, i18n.T("The Local API is off: these setups work once it runs.")).FontSize(px(t, fsCaption)).Grow(1).MinWidth(0).Wrap()
		case a.apiErr != "":
			statusDot(c, t.Danger)
			ui.Text(c, a.apiErr).FontSize(px(t, fsCaption)).TextColor(t.Danger).Grow(1).MinWidth(0).Wrap()
		default:
			statusDot(c, t.Success)
			truncText(c, i18n.T("Local API at %s", a.apiBaseURL())).FontSize(px(t, fsCaption)).Grow(1)
		}
		if ghostButton(c.Key("api-line-settings"), i18n.T("Settings…")).Clicked() {
			a.navigate("/settings/api")
		}
	})
}

func (a *app) integrationCard(c *ui.Context, in integration) {
	t := c.Theme()
	k := tok(t)
	b := ui.ButtonBase(c.Key("integ-"+in.Slug)).Padding(px(t, sp3), px(t, sp4)).Radius(px(t, radiusCard)).Border(1, k.Line).Label(in.Name).AlignSelf(ui.Stretch).AlignItems(ui.Start)
	if b.Hovered() {
		b.Background(k.Subtle)
	}
	b.Children(func() {
		ui.Column(c).Gap(px(t, 6)).Grow(1).MinWidth(0).Children(func() {
			ui.Row(c).Gap(px(t, sp2)).AlignItems(ui.Center).Children(func() {
				ui.Text(c, in.Name).FontWeight(600).Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
				tagChip(c, integCaption(in.Kind))
				ui.Icon(c, icons.AltArrowRight.Linear).Size(px(t, 14), px(t, 14)).TextColor(k.Text3)
			})
			ui.Text(c, integAbout(in.Slug)).FontSize(px(t, fsCaption)).TextColor(k.Text2).Wrap()
			caps := make([]string, len(in.Caps))
			for i, cp := range in.Caps {
				caps[i] = integCaption(cp)
			}
			ui.Text(c, strings.Join(caps, " · ")).FontSize(px(t, fsMicro)).TextColor(k.Text3).Wrap()
		})
	})
	if b.Clicked() {
		a.navigate("/integrations/" + in.Slug)
	}
}

// integrationPage is /integrations/{slug}.
func (a *app) integrationPage(c *ui.Context, slug string) {
	t := c.Theme()
	in, ok := integrationBySlug(slug)
	title := i18n.T("Integrations")
	if ok {
		title = in.Name
	}
	pageHeader(c, title, func() {
		if ghostIconButton(c.Key("integ-back"), icons.AltArrowLeft, i18n.T("All integrations")).Label(i18n.T("All integrations")).Clicked() {
			a.navigate("/integrations")
		}
	})
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(px(t, sp1), px(t, sp6), px(t, sp6)).Gap(px(t, sp4)).MaxWidth(px(t, 760)).Children(func() {
			if !ok {
				emptyState(c, icons.Magnifer, i18n.T("No matches"), "", nil)
				return
			}
			ui.Text(c, integAbout(in.Slug)).Wrap()
			ui.Row(c).Gap(px(t, sp1)).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, icons.SquareArrowRightUp.Linear).Size(px(t, 14), px(t, 14)).TextColor(tok(t).Text2)
				ui.Link(c.Key("integ-site"), in.URL, in.URL).FontSize(px(t, fsCaption))
			})
			a.apiStatusLine(c)
			if a.apiHasKey && a.apiSrv != nil && a.apiSrv.Key() != "" {
				settingRow(c, "integ-key", i18n.T("Put my API key in the setups"), i18n.T("Off: setups read it from the AUDIOSMITH_API_KEY environment variable when a key is required. Anyone who sees a setup with the key can use AudioSmith."), func() {
					ui.Switch(c.Key("integ-key-switch"), &a.apiForm.keyIncl).Label(i18n.T("Put my API key in the setups"))
				})
			}
			for i, blk := range in.Blocks(a.integEnvOf()) {
				a.codeBlock(c, in.Slug+"-"+itoa(i), blk)
			}
			if slices.Contains(in.Caps, "MCP server") {
				ui.Column(c.Key("integ-voices")).Children(func() {
					banner(c, icons.UserSpeakRounded, tok(t).Text2, i18n.T("Give each agent its own voice in Settings → Local API."), func() {
						if ghostButton(c.Key("integ-voices-go"), i18n.T("Open")).Clicked() {
							a.navigate("/settings/api")
						}
					})
				})
			}
		})
	})
}

// codeBlock shows a setup block: its title and hint, the code, Copy and
// Save….
func (a *app) codeBlock(c *ui.Context, key string, blk integBlock) {
	t := c.Theme()
	ui.Column(c.Key("block-" + key)).Gap(px(t, 6)).Children(func() {
		ui.Row(c).Gap(px(t, sp1)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, blk.Title).FontWeight(600).Grow(1).MinWidth(0)
			if blk.Hint != "" {
				infoTip(c.Key("hint"), blk.Hint)
			}
		})
		ui.Row(c).Gap(px(t, sp1)).AlignItems(ui.Start).Padding(px(t, sp3), px(t, 6), px(t, sp3), px(t, sp3)).Radius(px(t, radiusControl)).Background(tok(t).Subtle).Children(func() {
			ui.Text(c, blk.Code).Font(monoFont).FontSize(px(t, fsCaption)).Wrap().Selectable().Grow(1).MinWidth(0)
			if blk.File != "" {
				if iconButton(c.Key("save"), icons.Export, i18n.T("Save…")).Clicked() {
					a.saveBlock(blk)
				}
			}
			if iconButton(c.Key("copy"), icons.Copy, i18n.T("Copy")).Clicked() {
				c.Services().WriteClipboard(blk.Code)
				a.toast(i18n.T("Copied"))
			}
		})
	})
}

// saveBlock writes a block to a file.
func (a *app) saveBlock(blk integBlock) {
	go func() {
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: a.win, Title: i18n.T("Save…"), DefaultPath: blk.File})
		if err != nil || path == "" {
			return
		}
		err = saveSnippet(path, blk.Code)
		a.post(func() {
			if err != nil {
				a.toast(i18n.T("Could not save: %v", err))
				return
			}
			a.toast(i18n.T("Saved"))
		})
	}()
}
