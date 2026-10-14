package main

import (
	"slices"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/i18n"
)

// integrationsPage is /integrations: the cards.
func (a *app) integrationsPage(c *ui.Context) {
	t := c.Theme()
	header(c, i18n.T("Integrations"), func() {
		ui.TextInput(c.Key("integ-search"), &a.integQuery).Placeholder(i18n.T("Search")).Width(px(t, 200)).Label(i18n.T("Search"))
	})
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(px(t, 20)).Gap(px(t, 14)).Children(func() {
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
				ui.Text(c, i18n.T("No matches")).TextColor(t.TextMuted)
				return
			}
			ui.Grid(c).Columns(2).Gap(px(t, 12)).Children(func() {
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
	ui.Row(c.Key("api-line")).Gap(px(t, 8)).AlignItems(ui.Center).Padding(px(t, 10), px(t, 12)).Radius(8).Background(t.Surface).Children(func() {
		switch {
		case !a.prefs.API.Enabled:
			ui.Text(c, i18n.T("The Local API is off: these setups work once it runs.")).TextColor(t.Warning).Grow(1).Wrap()
		case a.apiErr != "":
			ui.Text(c, a.apiErr).TextColor(t.Danger).Grow(1).Wrap()
		default:
			ui.Text(c, i18n.T("Local API at %s", a.apiBaseURL())).Grow(1).SingleLine()
		}
		if ui.Button(c.Key("api-line-settings"), i18n.T("Settings…")).Clicked() {
			a.navigate("/settings/api")
		}
	})
}

func (a *app) integrationCard(c *ui.Context, in integration) {
	t := c.Theme()
	b := ui.ButtonBase(c.Key("integ-"+in.Slug)).Padding(px(t, 14)).Radius(10).Border(1, t.Border).Label(in.Name)
	if b.Hovered() {
		b.Background(t.SurfaceHover)
	} else {
		b.Background(t.Surface)
	}
	b.Children(func() {
		ui.Column(c).Gap(px(t, 6)).Grow(1).Children(func() {
			ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
				ui.Text(c, in.Name).Bold().Grow(1)
				ui.Badge(c, integCaption(in.Kind))
			})
			ui.Text(c, integAbout(in.Slug)).FontSize(px(t, 12)).TextColor(t.TextMuted).Wrap()
			caps := make([]string, len(in.Caps))
			for i, cp := range in.Caps {
				caps[i] = integCaption(cp)
			}
			ui.Text(c, strings.Join(caps, " · ")).FontSize(px(t, 11)).TextColor(t.Accent)
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
	header(c, title, func() {
		if ui.Button(c.Key("integ-back"), i18n.T("All integrations")).Clicked() {
			a.navigate("/integrations")
		}
	})
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(px(t, 20)).Gap(px(t, 14)).MaxWidth(px(t, 760)).Children(func() {
			if !ok {
				ui.Text(c, i18n.T("No matches")).TextColor(t.TextMuted)
				return
			}
			ui.Text(c, integAbout(in.Slug)).Wrap()
			ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
				ui.Link(c.Key("integ-site"), in.URL, in.URL).FontSize(px(t, 12))
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
				ui.Row(c.Key("integ-voices")).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
					ui.Text(c, i18n.T("Give each agent its own voice in Settings → Local API.")).FontSize(px(t, 12)).TextColor(t.TextMuted).Grow(1)
					if ui.Button(c.Key("integ-voices-go"), i18n.T("Open")).Clicked() {
						a.navigate("/settings/api")
					}
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
		ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, blk.Title).Bold().Grow(1)
			if blk.File != "" {
				if ui.Button(c.Key("save"), i18n.T("Save…")).Clicked() {
					a.saveBlock(blk)
				}
			}
			if ui.Button(c.Key("copy"), i18n.T("Copy")).Clicked() {
				c.Services().WriteClipboard(blk.Code)
				a.toast(i18n.T("Copied"))
			}
		})
		if blk.Hint != "" {
			ui.Text(c, blk.Hint).FontSize(px(t, 12)).TextColor(t.TextMuted).Wrap()
		}
		ui.Column(c).Padding(px(t, 10), px(t, 12)).Radius(6).Background(t.Surface).Border(1, t.Border).Children(func() {
			ui.Text(c, blk.Code).Font(monoFont).FontSize(px(t, 12)).Wrap().Selectable()
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
