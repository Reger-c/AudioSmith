package main

import (
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/gallery"
	"github.com/hehehai/AudioSmith/internal/i18n"
	vd "github.com/hehehai/AudioSmith/internal/voicedesign"
)

// designPage is /design.
func (a *app) designPage(c *ui.Context) {
	t := c.Theme()
	d := &a.design
	d.reparse()
	a.syncLinked()
	title := i18n.T("Design")
	if v := a.findVoice(d.linked); v != nil {
		title = i18n.T("Design · %s", v.Name)
	}
	header(c, title, func() {
		if ui.Button(c.Key("design-new"), i18n.T("New design")).Clicked() {
			script := d.script
			d.reset()
			d.script = script
		}
		if ui.Button(c.Key("design-gallery"), i18n.T("Gallery")).Clicked() {
			a.navigate("/gallery")
		}
	})
	ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
		ui.Scroll(c.Key("design-recipe")).Width(px(t, 400)).Shrink(0).Background(t.Surface).Children(func() {
			ui.Column(c).Padding(px(t, 16)).Gap(px(t, 14)).Children(func() {
				a.designDescribe(c)
				a.designPresets(c)
				a.designDetails(c)
				a.designSeed(c)
				a.designSavedVoices(c)
			})
		})
		ui.Box(c).Width(1).Background(t.Border)
		ui.Scroll(c.Key("design-main")).Grow(1).MinWidth(0).Children(func() {
			ui.Column(c).Padding(px(t, 16), px(t, 20)).Gap(px(t, 12)).Children(func() {
				a.designTarget(c)
				a.designScript(c)
				a.designSaveForm(c)
			})
		})
	})
}

// designDescribe is the description box and what the parser made of it.
func (a *app) designDescribe(c *ui.Context) {
	t := c.Theme()
	d := &a.design
	loc := i18n.Locale()
	ui.Text(c, i18n.T("Describe the voice")).Bold()
	ui.TextArea(c.Key("design-description"), &d.description).Lines(3, 8).
		Placeholder(i18n.T("e.g. a warm elderly British storyteller, slightly raspy")).Label(i18n.T("Describe the voice"))
	d.reparse()
	res := d.parsed
	switch {
	case strings.TrimSpace(d.description) == "":
		ui.Text(c, i18n.T("Describe the voice, or pick a starting point. Either fills in the details below.")).FontSize(px(t, 12)).TextColor(t.TextMuted)
	case len(res.Matched) == 0:
		ui.Text(c, i18n.T("No voice traits recognised yet: try words like deep, elderly or British.")).FontSize(px(t, 12)).TextColor(t.TextMuted)
	default:
		ui.Row(c.Key("design-matched")).Gap(px(t, 6)).Wrap().Children(func() {
			for i, m := range res.Matched {
				info, _ := vd.Info(m.Category)
				op, _ := vd.Lookup(m.Category, m.Value)
				chip := ui.Badge(c.Key("m"+itoa(i)), m.Phrase+" → "+info.Label.In(loc)+": "+op.Label.In(loc))
				if !m.Applied {
					chip.Opacity(0.5).Tooltip(matchNote(m.Note))
				}
			}
		})
	}
	if len(res.Unmatched) > 0 {
		ui.Text(c, i18n.T("Couldn't map: %s", res.UnmatchedText())).FontSize(px(t, 12)).TextColor(t.TextMuted)
	}
	if e, ok := voiceCatalog().ByLabel(d.model); ok {
		if eng, ok := vd.EngineFor(e.Family, e.Repo); ok && eng.Vocabulary == vd.VocabFreeform && d.description != "" {
			ui.Text(c, i18n.T("This model reads your description as written; the details below are extra cues.")).FontSize(px(t, 12)).TextColor(t.TextMuted)
		}
	}
	if d.description != "" || !d.attrs.IsAuto() {
		if ui.Button(c.Key("design-reset"), i18n.T("Reset to description")).
			Tooltip(i18n.T("Drop your picks: the description alone decides")).Clicked() {
			d.attrs = d.parsed.Attrs.Canonical()
		}
	}
}

// matchNote explains a phrase the parser did not apply.
func matchNote(note string) string {
	switch note {
	case "negated":
		return i18n.T("Negated")
	case "overridden":
		return i18n.T("Overridden by a later phrase")
	case "default":
		return i18n.T("A weak hint that a clearer phrase replaced")
	}
	return ""
}

// designPresets are the starting points.
func (a *app) designPresets(c *ui.Context) {
	t := c.Theme()
	d := &a.design
	loc := i18n.Locale()
	ui.Text(c, i18n.T("Starting points")).Bold()
	ui.Row(c.Key("design-presets")).Gap(px(t, 6)).Wrap().Children(func() {
		for _, p := range gallery.Presets() {
			on := d.preset == p.ID && d.attrs.Summary("en") == p.Attrs.Summary("en")
			b := ui.Toggle(c.Key("preset-"+p.ID), &on, p.Name.In(loc)).Tooltip(p.Description.In(loc))
			if b.Changed() {
				if on {
					a.applyPreset(p)
				} else {
					d.preset, d.attrs = "", vd.Attributes{}
				}
			}
		}
	})
}

// designDetails are the attribute pickers.
func (a *app) designDetails(c *ui.Context) {
	t := c.Theme()
	d := &a.design
	loc := i18n.Locale()
	ui.Collapsible(c.Key("design-details"), i18n.T("Details"), &d.detailsOpen, func() {
		ui.Column(c).Gap(px(t, 10)).Children(func() {
			ui.Text(c, d.attrs.Summary(loc)).FontSize(px(t, 12)).TextColor(t.TextMuted)
			for _, cat := range vd.Categories {
				info, _ := vd.Info(cat)
				if cat == vd.Style {
					ui.Text(c, info.Label.In(loc)).FontSize(px(t, 12))
					ui.Row(c.Key("styles")).Gap(px(t, 4)).Wrap().Children(func() {
						for _, op := range info.Options {
							on := d.attrs.HasStyle(op.ID)
							if ui.Toggle(c.Key("style-"+op.ID), &on, op.Label.In(loc)).Changed() {
								if on {
									a.designCleared(d.pick(vd.Style, op.ID))
								} else {
									d.attrs = d.attrs.Without(vd.Style, op.ID)
								}
							}
						}
					})
					continue
				}
				labels := []string{i18n.T("Auto")}
				ids := []string{""}
				for _, op := range info.Options {
					labels, ids = append(labels, op.Label.In(loc)), append(ids, op.ID)
				}
				cur := labels[max(slices.Index(ids, d.attrs.Value(cat)), 0)]
				ui.Row(c.Key("cat-"+string(cat))).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
					ui.Text(c, info.Label.In(loc)).FontSize(px(t, 12)).Width(px(t, 110)).Shrink(0)
					if ui.Select(c.Key("pick-"+string(cat)), &cur, labels).Label(info.Label.In(loc)).Grow(1).Changed() {
						if i := slices.Index(labels, cur); i >= 0 {
							a.designCleared(d.pick(cat, ids[i]))
						}
					}
				})
			}
		})
	})
}

func (a *app) designCleared(cs []vd.Cleared) {
	if s := clearedText(cs); s != "" {
		a.toast(s)
	}
}

func (a *app) designSeed(c *ui.Context) {
	t := c.Theme()
	d := &a.design
	ui.Row(c.Key("design-seed")).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
		ui.Text(c, i18n.T("Seed")).FontSize(px(t, 12)).Width(px(t, 110)).Shrink(0)
		ui.NumberInput(c.Key("seed"), &d.seed, 0, float64(vd.MaxSeed), 1).Label(i18n.T("Seed")).Grow(1)
		if ui.Button(c.Key("new-seed"), i18n.T("New seed")).Tooltip(i18n.T("Another voice with the same description")).Clicked() {
			d.seed = float64(vd.NewSeed())
		}
	})
	ui.Text(c, i18n.T("The seed keeps the voice's identity while you change the details.")).FontSize(px(t, 11)).TextColor(t.TextMuted)
}

// designSavedVoices lists the saved designed voices: a click restores one.
func (a *app) designSavedVoices(c *ui.Context) {
	t := c.Theme()
	d := &a.design
	var designed []*core.Voice
	for i := range a.voices {
		if a.voices[i].Kind == core.VoiceDesigned {
			designed = append(designed, &a.voices[i])
		}
	}
	if len(designed) == 0 {
		return
	}
	ui.Collapsible(c.Key("design-saved"), i18n.N(len(designed), "%d saved design", "%d saved designs", len(designed)), &d.savedOpen, func() {
		ui.Column(c).Gap(px(t, 4)).Children(func() {
			for _, v := range designed {
				ui.Row(c.Key("saved-"+v.ID)).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
					ui.Avatar(c, v.Name, a.portrait(v))
					ui.Text(c, v.Name).Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
					label := i18n.T("Preview")
					if a.voicePlaying(v) {
						label = i18n.T("Stop")
					}
					if ui.Button(c.Key("play"), label).Clicked() {
						a.playVoice(v)
					}
					if ui.Button(c.Key("open"), i18n.T("Open")).Clicked() {
						a.openDesignFor(v)
					}
				})
			}
		})
	})
}

// designTarget is the model picker and what it will be sent.
func (a *app) designTarget(c *ui.Context) {
	t := c.Theme()
	d := &a.design
	ms := designModels()
	if len(ms) == 0 {
		ui.Text(c, i18n.T("No model here can design voices. Download one that can, such as OmniVoice, on the Models page.")).TextColor(t.Warning)
		if ui.Button(c.Key("design-models"), i18n.T("Models")).Clicked() {
			a.navigate("/models")
		}
		return
	}
	labels := make([]string, len(ms))
	for i, m := range ms {
		labels[i] = m.Label
	}
	if !slices.Contains(labels, d.model) {
		d.model = labels[0]
	}
	e, _ := voiceCatalog().ByLabel(d.model)
	ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
		ui.Text(c, i18n.T("Model")).FontSize(px(t, 12))
		ui.Select(c.Key("design-model"), &d.model, labels).Label(i18n.T("Model")).Grow(1).MaxWidth(px(t, 320))
		if !e.Licence.Commercial {
			ui.Text(c, e.Licence.Name+" · "+i18n.T("non-commercial use only")).FontSize(px(t, 12)).TextColor(t.Warning)
		}
	})
	low, err := vd.Lower(designTarget(e), d.recipe(), vd.Options{Lang: designTextLang(d.speechLang())})
	if err != nil {
		return
	}
	loc := i18n.Locale()
	ui.Column(c.Key("design-lowered")).Gap(px(t, 4)).Padding(px(t, 10)).Radius(8).Background(t.Surface).Children(func() {
		ui.Text(c, i18n.T("Sent to %s", e.Label)).FontSize(px(t, 11)).TextColor(t.TextMuted)
		voice := low.Voice
		if voice == "" {
			voice = i18n.T("(nothing: the model picks a voice)")
		}
		ui.Text(c, voice).FontSize(px(t, 13)).Selectable()
		if low.DescriptionDropped && d.description != "" {
			ui.Text(c, i18n.T("This model only understands the details, not the description as written.")).FontSize(px(t, 12)).TextColor(t.Warning)
		}
		if len(low.Dropped) > 0 {
			var parts []string
			for _, x := range low.Dropped {
				if op, ok := vd.Lookup(x.Category, x.ID); ok {
					parts = append(parts, op.Label.In(loc))
				}
			}
			ui.Text(c, i18n.T("%s can't express: %s", e.Label, strings.Join(parts, ", "))).FontSize(px(t, 12)).TextColor(t.Warning)
		}
	})
}

// designScript is the line to preview and the preview.
func (a *app) designScript(c *ui.Context) {
	t := c.Theme()
	d := &a.design
	ui.Text(c, i18n.T("Script")).Bold()
	ui.TextArea(c.Key("design-script"), &d.script).Lines(3, 8).Placeholder(i18n.T("What should this voice say?")).Label(i18n.T("Script"))
	j := a.voiceJob("design", "")
	ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
		ui.Field(c, i18n.T("Language"), func() {
			ui.Autocomplete(c.Key("design-language"), &d.language, voiceLanguages()).Placeholder(i18n.T("Auto")).Label(i18n.T("Language"))
		}).Width(px(t, 160))
		ui.Box(c).Grow(1)
		_, ok := voiceCatalog().ByLabel(d.model)
		if ui.PrimaryButton(c.Key("design-generate"), i18n.T("Generate preview")).
			Disabled(!ok || j.running() || strings.TrimSpace(d.script) == "").Clicked() {
			a.designGenerate()
		}
	})
	switch {
	case j == nil:
	case j.running():
		ui.Progress(c, j.fraction).Label(j.status)
		ui.Text(c, j.status).FontSize(px(t, 12)).TextColor(t.TextMuted)
	case j.err != "":
		ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, j.err).FontSize(px(t, 12)).TextColor(t.Danger).Grow(1).Selectable()
			if j.needDownload != nil && ui.Button(c.Key("design-download"), i18n.T("Download")).Clicked() {
				a.download(*j.needDownload)
			}
		})
	default:
		a.clipCard(c, j.take, true)
	}
}

// designSaveForm saves the draft as a voice.
func (a *app) designSaveForm(c *ui.Context) {
	t := c.Theme()
	d := &a.design
	ui.Divider(c)
	ui.Text(c, i18n.T("Save as voice profile")).Bold()
	ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
		ui.TextInput(c.Key("design-name"), &d.saveName).Placeholder(i18n.T("Name")).Label(i18n.T("Profile name")).Grow(1).MaxWidth(px(t, 320))
		label := i18n.T("Save as voice")
		if v := a.findVoice(d.linked); v != nil && v.Name == strings.TrimSpace(d.saveName) {
			label = i18n.T("Update voice")
		}
		if ui.PrimaryButton(c.Key("design-save"), label).Disabled(strings.TrimSpace(d.saveName) == "").Clicked() {
			a.designSave()
		}
	})
}
