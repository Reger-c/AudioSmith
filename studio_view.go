package main

import (
	"math"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/i18n"
)

func (a *app) studioPage(c *ui.Context) {
	t := c.Theme()
	header(c, i18n.T("Studio"), nil)
	ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Padding(16, 20).Gap(12).Children(func() {
			ui.TextArea(c.Key("script"), &a.text).
				Placeholder(i18n.T("Type or paste the text to speak…")).
				Label(i18n.T("Script")).
				Grow(1).MinHeight(120).
				FontSize(15)
			a.generateBar(c)
			if a.job != nil {
				a.jobStatus(c)
			} else if a.jobError != "" {
				ui.Text(c, a.jobError).TextColor(t.Danger).FontSize(12).Selectable()
			}
			if cl := a.lastClip(); cl != nil {
				a.clipCard(c, cl, true)
			}
		})
		ui.Box(c).Width(1).Background(t.Border)
		ui.Scroll(c).Width(inspectorWidth).Shrink(0).Background(t.Surface).Children(func() {
			ui.Column(c).Padding(16).Gap(14).Children(func() {
				a.inspector(c)
			})
		})
	})
}

func (a *app) generateBar(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		n := utf8.RuneCountInString(strings.TrimSpace(a.text))
		ui.Text(c, i18n.T("%d characters", n)).FontSize(12).TextColor(t.TextMuted).Grow(1)
		if a.job != nil {
			if ui.Button(c.Key("cancel"), i18n.T("Cancel")).Clicked() {
				a.cancel()
			}
		}
		label := i18n.T("Generate")
		if a.job != nil && a.job.kind == "synthesize" {
			label = i18n.T("Generating…")
		}
		if ui.PrimaryButton(c.Key("generate"), label).
			Disabled(!a.canGenerate()).
			Tooltip(i18n.T("Generate speech (⌘↩)")).
			Clicked() {
			a.generate()
		}
	})
}

func (a *app) jobStatus(c *ui.Context) {
	t := c.Theme()
	j := a.job
	ui.Column(c).Gap(6).Children(func() {
		ui.Progress(c, j.fraction).Label(j.status)
		ui.Row(c).Gap(8).Children(func() {
			ui.Text(c, j.status).FontSize(12).Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
			info := i18n.T("%.0f s", time.Since(j.started).Seconds())
			if j.audioSeconds > 0 {
				info = i18n.T("%.1f s of audio · ", j.audioSeconds) + info
			}
			ui.Text(c, info).FontSize(12).TextColor(t.TextMuted)
		})
	})
}

func (a *app) inspector(c *ui.Context) {
	t := c.Theme()
	p := a.preset()
	custom := a.modelLabel == catalog.CustomLabel

	ui.Field(c, i18n.T("Model"), func() {
		if ui.Select(c.Key("model"), &a.modelLabel, catalog.Builtin().Labels(catalog.TaskTTS)).Label(i18n.T("Model")).Changed() {
			a.modelChanged()
		}
	})
	if custom {
		ui.Field(c, i18n.T("Repo"), func() {
			ui.TextInput(c.Key("repo"), &a.customRepo).Placeholder("owner/model-name")
		})
	}
	about := i18n.T(p.About)
	if size := catalog.HumanSize(p.Size); size != "" {
		about += i18n.T(" Download %s.", size)
	}
	ui.Text(c, about).FontSize(12).TextColor(t.TextMuted)

	ui.Row(c).Gap(8).Children(func() {
		loaded := p.Repo != "" && p.Repo == a.loadedModel
		label := i18n.T("Preload")
		if loaded {
			label = i18n.T("Loaded")
		}
		if ui.Button(c.Key("preload"), label).
			Disabled(loaded || a.job != nil || a.engState != engineReady || p.Repo == "").
			Tooltip(i18n.T("Download and load the model now")).
			Clicked() {
			a.preload()
		}
		if ui.Button(c.Key("unload"), i18n.T("Unload")).
			Disabled(a.loadedModel == "" || a.job != nil).
			Tooltip(i18n.T("Free the memory the loaded model takes")).
			Clicked() {
			a.unload()
		}
	})

	ui.Divider(c)

	voiceLabel := i18n.T("Voice")
	if p.Has(catalog.CapVoiceStyle) {
		voiceLabel = i18n.T("Voice / style")
	}
	if voices := p.VoiceIDs(); len(voices) > 0 || custom {
		ui.Field(c, voiceLabel, func() {
			ui.Autocomplete(c.Key("voice"), &a.voice, voices).Placeholder(i18n.T("Model default")).Label(voiceLabel)
		})
	}
	if languages := p.LanguageOptions(); len(languages) > 0 || custom {
		ui.Field(c, i18n.T("Language"), func() {
			ui.Autocomplete(c.Key("language"), &a.language, languages).Placeholder(i18n.T("Auto")).Label(i18n.T("Language"))
		})
	}
	if p.Has(catalog.CapSpeed) {
		ui.Field(c, i18n.T("Speed  %.2f×", a.speed), func() {
			snapped(ui.Slider(c.Key("speed"), &a.speed, 0.5, 2).Label(i18n.T("Speed")), &a.speed, 0.05)
		})
	}
	if p.Has(catalog.CapSampling) {
		ui.Checkbox(c.Key("defaults"), &a.modelDefaults, i18n.T("Use the model's sampling defaults"))
		if !a.modelDefaults {
			ui.Field(c, i18n.T("Temperature  %.2f", a.temperature), func() {
				snapped(ui.Slider(c.Key("temperature"), &a.temperature, 0, 1.5).Label(i18n.T("Temperature")), &a.temperature, 0.05)
			})
			ui.Field(c, i18n.T("Top P  %.2f", a.topP), func() {
				snapped(ui.Slider(c.Key("topp"), &a.topP, 0.05, 1).Label(i18n.T("Top P")), &a.topP, 0.05)
			})
			ui.Field(c, i18n.T("Max tokens"), func() {
				ui.NumberInput(c.Key("maxtokens"), &a.maxTokens, 50, 8000, 50).Label(i18n.T("Max tokens"))
			})
		}
	}
	if p.Has(catalog.CapClone) {
		ui.Divider(c)
		ui.Text(c, i18n.T("Voice cloning")).Bold()
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			name := i18n.T("No reference clip")
			if a.refAudio != "" {
				name = filepath.Base(a.refAudio)
			}
			ui.Text(c, name).FontSize(12).Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
			if ui.Button(c.Key("choose-ref"), i18n.T("Choose…")).Clicked() {
				a.chooseRefAudio()
			}
			if a.refAudio != "" && ui.Button(c.Key("clear-ref"), i18n.T("Clear")).Clicked() {
				a.refAudio, a.refText = "", ""
			}
		})
		ui.Field(c, i18n.T("Reference transcript"), func() {
			ui.TextArea(c.Key("ref-text"), &a.refText).Lines(2, 5).Placeholder(i18n.T("What the reference clip says")).Label(i18n.T("Reference transcript"))
		})
	}
	ui.Divider(c)
	ui.Checkbox(c.Key("autoplay"), &a.autoplay, i18n.T("Play each take when it is ready"))
}

// snapped rounds a slider's value to a step, so labels and requests show
// 1.05 rather than 1.0473; the slider moves smoothly in between.
func snapped(e ui.Element, v *float64, step float64) ui.Element {
	if e.Changed() {
		*v = math.Round(*v/step) * step
	}
	return e
}
