package main

// The Studio's inspector (thread #42, phase C): what most takes need —
// the model, the voice, the speed — and, folded under 「高级」, the rest
// (sampling, refinement, seed, output options), pre-filled with the
// model's recommended settings (studio_presets.go). The generate bar says
// which model and voice will speak.

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
)

// studioInspectorState is the inspector's view state.
type studioInspectorState struct {
	voiceListOpen   bool
	refTranscribing string // the reference clip being transcribed ("" = none)
}

func (a *app) inspector(c *ui.Context) {
	t := c.Theme()
	p := a.preset()
	custom := a.modelLabel == catalog.CustomLabel

	about := i18n.T(p.About)
	if size := catalog.HumanSize(p.Size); size != "" {
		about += i18n.T(" Download %s.", size)
	}
	ui.Column(c).Gap(px(t, 6)).Children(func() {
		ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, i18n.T("Model")).FontWeight(600).Grow(1)
			infoTip(c.Key("model-about"), about)
		})
		a.modelPicker(c, "model", catalog.WorkflowStudio, p, func(e catalog.Entry) {
			if e.ID == "custom" {
				a.modelLabel = catalog.CustomLabel
			} else {
				a.modelLabel = e.Label
			}
			a.modelChanged()
		}, pickerOpts{Custom: true})
		if custom {
			ui.TextInput(c.Key("repo"), &a.customRepo).Placeholder("owner/model-name").Label(i18n.T("Repo"))
		}
		if !p.Licence.Commercial {
			caption(c, p.Licence.Name+" · "+i18n.T("non-commercial use only")).TextColor(t.Warning)
		}
	})
	a.studioVoiceField(c, p)
	if studioNeedsDescription(p) && !a.studioUsesSavedVoice(p) {
		ui.Field(c, i18n.T("Voice description"), func() {
			ui.TextArea(c.Key("description"), &a.voice).Lines(2, 5).Placeholder(i18n.T("e.g. a calm, warm female narrator, mid-thirties")).
				Label(i18n.T("Voice description"))
		})
	}
	if p.Has(catalog.CapSpeed) {
		ui.Field(c, i18n.T("Speed  %.2f×", a.speed), func() {
			snapped(ui.Slider(c.Key("speed"), &a.speed, 0.5, 2).Label(i18n.T("Speed")), &a.speed, 0.05)
		})
	}
	ui.Divider(c)
	a.advancedSection(c, "studio", a.studioChanges(), false, a.restoreRecommended, func() { a.studioAdvanced(c, p) })
}

// studioNeedsDescription: the model speaks a voice made from a
// description and has neither speakers nor cloning (Qwen3-TTS
// VoiceDesign), so the description is the voice.
func studioNeedsDescription(e catalog.Entry) bool {
	return e.Has(catalog.CapDesign) && len(e.Voices) == 0 && !e.Has(catalog.CapClone)
}

// splitVoice splits the voice field into a built-in speaker of ids and
// the style after it ("Vivian, calm and warm" → "Vivian", "calm and
// warm"); speaker is "" when the field names none.
func splitVoice(voice string, ids []string) (speaker, style string) {
	voice = strings.TrimSpace(voice)
	for _, id := range ids {
		if rest, ok := strings.CutPrefix(voice, id); ok && (rest == "" || strings.HasPrefix(rest, ",")) {
			return id, strings.TrimSpace(strings.TrimPrefix(rest, ","))
		}
	}
	return "", voice
}

// joinVoice is the voice field for a speaker and a style.
func joinVoice(speaker, style string) string {
	style = strings.TrimSpace(style)
	switch {
	case speaker == "":
		return style
	case style == "":
		return speaker
	}
	return speaker + ", " + style
}

// studioVoiceLabel names the Studio's voice for model e.
func (a *app) studioVoiceLabel(e catalog.Entry) string {
	if v := a.findVoice(a.studioVoice); v != nil {
		return v.Name
	}
	if a.studioClip != nil && e.Has(catalog.CapClone) {
		return i18n.T("Unsaved clip: %s", a.studioClip.Name)
	}
	if e.Has(catalog.CapClone) && strings.TrimSpace(a.refAudio) != "" {
		return i18n.T("Clip: %s", filepath.Base(a.refAudio))
	}
	if sp, _ := splitVoice(a.voice, e.AllVoiceIDs()); sp != "" {
		return sp
	}
	return i18n.T("Model default")
}

// studioVoiceField is the voice: one list of the model's speakers, the
// saved voices it can speak with, a clip to clone (models that clone)
// and Manage voices…; under it, what the choice needs saying.
func (a *app) studioVoiceField(c *ui.Context, e catalog.Entry) {
	t := c.Theme()
	k := tok(t)
	ui.Column(c.Key("voice-field")).Gap(px(t, 6)).Children(func() {
		ui.Text(c, i18n.T("Voice")).FontWeight(600)
		label := a.studioVoiceLabel(e)
		btn := ui.ButtonBase(c.Key("voice")).Padding(px(t, 6), px(t, 10)).Gap(px(t, sp2)).Radius(px(t, radiusControl)).
			Background(t.Surface).Border(1, t.Border).AlignItems(ui.Center).Label(i18n.T("Voice")).Tooltip(label).MinHeight(px(t, 32))
		if btn.Hovered() {
			btn.Background(t.SurfaceHover)
		}
		btn.Children(func() {
			if v := a.findVoice(a.studioVoice); v != nil {
				ui.Avatar(c, v.Name, a.portrait(v)).Size(px(t, 20), px(t, 20))
			}
			ui.Text(c, label).SingleLine().Ellipsis("…").MinWidth(0).Grow(1)
			ui.Icon(c, icons.AltArrowDown.Linear).Size(px(t, 14), px(t, 14)).TextColor(k.Text2).Shrink(0)
		})
		if btn.Clicked() {
			a.inspectorUI.voiceListOpen = !a.inspectorUI.voiceListOpen
		}
		open := a.inspectorUI.voiceListOpen
		ui.Popover(c, btn, &open, func() { a.studioVoiceList(c, e) })
		a.inspectorUI.voiceListOpen = a.inspectorUI.voiceListOpen && open

		switch v := a.findVoice(a.studioVoice); {
		case v != nil:
			ui.Row(c.Key("studio-voice-info")).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
				col := k.Text2
				text := voiceKindLabel(v)
				if ok, why := voiceUsable(v, e); !ok {
					col, text = t.Warning, why
				}
				caption(c, text).TextColor(col).Grow(1).MinWidth(0)
				if ok, _ := voiceUsable(v, e); !ok && canMakePortable(v) && e.Has(catalog.CapClone) {
					if ghostButton(c.Key("studio-voice-portable"), i18n.T("Make portable")).Tooltip(i18n.T("Save an approximate clone of this voice that models that clone can speak with")).Clicked() {
						a.makePortable(v)
					}
				} else if ghostButton(c.Key("studio-voice-edit"), i18n.T("Edit")).Clicked() {
					a.openVoiceEditor(v.ID)
				}
			})
		case e.Has(catalog.CapClone) && a.studioClip == nil && strings.TrimSpace(a.refAudio) != "":
			ui.Row(c.Key("ref-clip")).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
				text := i18n.T("Cloned from this clip; its transcript is under Advanced.")
				if a.inspectorUI.refTranscribing == a.refAudio {
					text = i18n.T("Transcribing the clip…")
				}
				caption(c, text).Grow(1).MinWidth(0)
				if iconButton(c.Key("clear-ref"), icons.Close, i18n.T("Clear")).Tooltip(i18n.T("Stop cloning this clip")).Clicked() {
					a.refAudio, a.refText = "", ""
				}
			})
		case e.NeedsReference && !a.studioUsesSavedVoice(e):
			caption(c.Key("ref-required"), i18n.T("Required: %s only clones voices.", e.Label)).TextColor(t.Warning)
		}
	})
}

// studioVoiceList is the voice's popover.
func (a *app) studioVoiceList(c *ui.Context, e catalog.Entry) {
	t := c.Theme()
	k := tok(t)
	ids := e.VoiceIDs()
	speaker, style := splitVoice(a.voice, e.AllVoiceIDs())
	saved := a.findVoice(a.studioVoice)
	clip := a.studioClip != nil && a.studioVoice == "" && e.Has(catalog.CapClone)
	ref := !clip && saved == nil && e.Has(catalog.CapClone) && strings.TrimSpace(a.refAudio) != ""
	done := func() { a.inspectorUI.voiceListOpen = false }
	row := func(key, label, sub string, chosen bool, pick func()) {
		b := ui.ButtonBase(c.Key("vl-"+key)).Padding(px(t, 5), px(t, sp2)).Radius(px(t, radiusControl)).Gap(px(t, sp2)).AlignItems(ui.Center).Label(label)
		switch {
		case chosen:
			b.Background(k.Pressed)
		case b.Hovered():
			b.Background(k.Hover)
		}
		if b.Clicked() {
			done()
			pick()
		}
		b.Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Children(func() {
				ui.Text(c, label).SingleLine().Ellipsis("…").MinWidth(0)
				if sub != "" {
					ui.Text(c, sub).FontSize(px(t, fsMicro)).TextColor(k.Text2).SingleLine().Ellipsis("…").MinWidth(0)
				}
			})
			if chosen {
				iconMark(c, icons.CheckCircle, k.AccentInk, i18n.T("Chosen"))
			}
		})
	}
	section := func(title string) {
		ui.Text(c, title).FontSize(px(t, fsMicro)).FontWeight(600).TextColor(k.Text3).Padding(px(t, 6), px(t, sp2), px(t, 2))
	}
	clearOthers := func() { a.studioVoice, a.studioClip, a.refAudio, a.refText = "", nil, "", "" }
	_, h := c.Size()
	ui.Scroll(c.Key("voice-list-scroll")).MaxHeight(max(px(t, 220), h*0.6)).Children(func() {
		ui.Column(c.Key("voice-list")).Width(px(t, 300)).Gap(px(t, 1)).Padding(px(t, 6)).Children(func() {
			if !e.NeedsReference {
				section(i18n.T("Model voices"))
				if len(ids) == 0 {
					row("default", i18n.T("Model default"), "", saved == nil && !clip && !ref, func() { clearOthers() })
				}
				for _, id := range ids {
					sub := ""
					if p, ok := catalog.PresetFor(e.ID); ok && p.VoiceFor(i18n.Locale()) == id {
						sub = i18n.T("Recommended")
					}
					row("sp-"+id, id, sub, saved == nil && !clip && !ref && speaker == id, func() {
						clearOthers()
						a.voice = joinVoice(id, style)
					})
				}
			}
			var mine []*core.Voice
			for i := range a.voices {
				v := &a.voices[i]
				if ok, _ := voiceUsable(v, e); ok {
					mine = append(mine, v)
				}
			}
			if len(mine) > 0 || clip {
				section(i18n.T("My voices"))
			}
			if clip {
				row("clip", i18n.T("Unsaved clip: %s", a.studioClip.Name), "", true, func() {})
			}
			for _, v := range mine {
				row("v-"+v.ID, v.Name, voiceKindLabel(v), saved != nil && saved.ID == v.ID, func() {
					a.studioVoice, a.studioClip = v.ID, nil
				})
			}
			ui.Divider(c)
			if e.Has(catalog.CapClone) {
				label := i18n.T("Clone a clip…")
				if ref {
					label = i18n.T("Clip: %s", filepath.Base(a.refAudio))
				}
				row("ref", label, i18n.T("A few seconds of clean speech; not saved to your voices"), ref, func() {
					a.studioVoice, a.studioClip = "", nil
					a.chooseRefAudio()
				})
			}
			if speaker != "" && saved == nil && !clip && !ref && !e.Has(catalog.CapClone) {
				row("portable", i18n.T("Make a portable copy of %s…", speaker), i18n.T("An approximate clone of it, for models that clone"), false, func() {
					a.makeSpeakerPortable(e, speaker)
				})
			}
			row("manage", i18n.T("Manage voices…"), "", false, func() { a.navigate("/voices") })
		})
	})
}

// studioAdvanced are the Studio's 「高级」 controls, those the model takes.
func (a *app) studioAdvanced(c *ui.Context, p catalog.Entry) {
	t := c.Theme()
	saved := a.studioUsesSavedVoice(p)
	if p.Has(catalog.CapVoiceStyle) && !studioNeedsDescription(p) && !saved {
		if ids := p.AllVoiceIDs(); len(ids) > 0 {
			speaker, style := splitVoice(a.voice, ids)
			fieldInfo(c, i18n.T("Style"), i18n.T("How the speaker sounds, in a few words: “calm and warm”, “excited”."), func() {
				if ui.TextInput(c.Key("style"), &style).Placeholder(i18n.T("e.g. calm and warm")).Label(i18n.T("Style")).Changed() {
					a.voice = joinVoice(speaker, style)
				}
			})
		} else {
			ui.Field(c, i18n.T("Voice description"), func() {
				ui.TextArea(c.Key("description"), &a.voice).Lines(1, 4).Placeholder(i18n.T("Optional: describe the voice to make")).Label(i18n.T("Voice description"))
			})
		}
	}
	if p.ID == "custom" && !saved {
		ui.Field(c, i18n.T("Voice"), func() {
			ui.TextInput(c.Key("custom-voice"), &a.voice).Placeholder(i18n.T("Model default")).Label(i18n.T("Voice"))
		})
	}
	if p.Has(catalog.CapClone) && strings.TrimSpace(a.refAudio) != "" && !saved && a.studioClip == nil {
		ui.Field(c, i18n.T("Reference transcript"), func() {
			ui.TextArea(c.Key("ref-text"), &a.refText).Lines(2, 4).Placeholder(i18n.T("What the reference clip says")).Label(i18n.T("Reference transcript"))
		})
	}
	if p.Has(catalog.CapDuration) {
		fieldInfo(c, i18n.T("Duration (seconds, 0 = natural)"), i18n.T("A duration overrides the speed."), func() {
			numberInput(c.Key("duration"), &a.duration, 0, 600, 0.5, numOpts{Label: i18n.T("Duration")})
		})
	}
	if a.traits().Steps {
		tier := slices.Index(tierSteps, a.shownSteps())
		fieldInfo(c, i18n.T("Voice refinement"), i18n.T("More steps sound cleaner and take longer."), func() {
			if segmentedFit(c.Key("tier"), &tier, i18n.T("Fast"), i18n.T("Balanced"), i18n.T("Quality")).Label(i18n.T("Voice refinement")).Changed() && tier >= 0 {
				a.steps = tierSteps[tier]
			}
		})
	}
	rec := a.recommended()
	if p.Has(catalog.CapGuidance) {
		g := shown(a.guidance, rec.Guidance, 2)
		ui.Field(c, i18n.T("Prompt guidance  %.1f", g), func() {
			if snapped(ui.Slider(c.Key("guidance"), &g, 1, 4).Label(i18n.T("Prompt guidance")), &g, 0.1).Changed() {
				a.guidance = g
			}
		})
	}
	if p.Has(catalog.CapSampling) {
		temp, topP := shown(a.temperature, rec.Temperature, 0.9), shown(a.topP, rec.TopP, 1)
		ui.Field(c, i18n.T("Temperature  %.2f", temp), func() {
			if snapped(ui.Slider(c.Key("temperature"), &temp, 0.05, 1.5).Label(i18n.T("Temperature")), &temp, 0.05).Changed() {
				a.temperature = temp
			}
		})
		ui.Field(c, i18n.T("Top P  %.2f", topP), func() {
			if snapped(ui.Slider(c.Key("topp"), &topP, 0.05, 1).Label(i18n.T("Top P")), &topP, 0.05).Changed() {
				a.topP = topP
			}
		})
	}
	formRow(c, "seed-pin-row", i18n.T("Pin the seed"), i18n.T("The same seed and settings make the same take."), func() {
		ui.Switch(c.Key("seed-pin"), &a.seedPinned).Label(i18n.T("Pin the seed"))
	})
	if a.seedPinned {
		ui.Row(c).Gap(px(t, sp2)).AlignItems(ui.Center).Children(func() {
			numberInput(c.Key("seed"), &a.seed, 1, 1<<31-1, 1, numOpts{Label: i18n.T("Seed"), Grow: true})
			if iconButton(c.Key("new-seed"), icons.Refresh, i18n.T("New seed")).Clicked() {
				a.newSeed()
			}
		})
	}
	formRow(c, "normalize-row", i18n.T("Read numbers as words"), i18n.T("Numbers, times, dates, money and abbreviations, before the model reads them."), func() {
		ui.Switch(c.Key("normalize"), &a.normalize).Label(i18n.T("Read numbers as words"))
	})
	formRow(c, "even-out-row", i18n.T("Even out volume"), i18n.T("Masters the take to broadcast loudness (−18 LUFS)."), func() {
		ui.Switch(c.Key("even-out"), &a.evenOut).Label(i18n.T("Even out volume"))
	})
}

// studioSummary is the generate bar's caption: the model and the voice
// that will speak, the speed, how many settings differ from the
// recommended ones. A click shows the inspector.
func (a *app) studioSummary(c *ui.Context) {
	t := c.Theme()
	k := tok(t)
	p := a.preset()
	parts := []string{p.Label, a.studioVoiceLabel(p)}
	if p.ID == "custom" {
		parts[0] = catalog.ShortRepo(p.Repo)
	}
	if p.Has(catalog.CapSpeed) && a.speed > 0 && a.speed != 1 {
		parts = append(parts, strconv.FormatFloat(a.speed, 'f', 2, 64)+"×")
	}
	changes := slices.DeleteFunc(a.studioChanges(), func(s string) bool { return s == i18n.T("Speed") })
	if len(changes) > 0 {
		parts = append(parts, changesLabel(changes))
	}
	text := strings.Join(parts, " · ")
	b := ghostBase(c.Key("studio-summary")).Padding(px(t, 4), px(t, 6)).Label(text).Tooltip(i18n.T("Model and voice: change them in the inspector")).MinWidth(0).Shrink(1)
	b.Children(func() {
		ui.Text(c, text).FontSize(px(t, fsCaption)).TextColor(k.Text2).SingleLine().Ellipsis("…").MinWidth(0)
	})
	if b.Clicked() {
		a.composer.panel, a.composer.panelSet = true, true
	}
}

// transcribeRef fills the reference transcript of a clip just chosen, in
// the background, if the user has not typed one meanwhile.
func (a *app) transcribeRef(path string) {
	if strings.TrimSpace(a.refText) != "" {
		return
	}
	a.inspectorUI.refTranscribing = path
	a.transcribeClip(path, "", func(text string, err error) {
		a.post(func() {
			if a.inspectorUI.refTranscribing == path {
				a.inspectorUI.refTranscribing = ""
			}
			if err == nil && a.refAudio == path && strings.TrimSpace(a.refText) == "" {
				a.refText = text
			}
		})
	})
}
