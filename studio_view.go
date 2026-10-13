package main

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/settings"
)

func (a *app) studioPage(c *ui.Context) {
	t := c.Theme()
	header(c, i18n.T("Studio"), nil)
	if c.Shortcut(ui.Alt, ui.KeySlash) { // wherever the focus is, the script editor's too
		a.composer.insertOpen = !a.composer.insertOpen
	}
	if msg := a.composer.undoToast; msg != "" {
		a.composer.undoToast = ""
		c.ToastAction(msg, i18n.T("Undo"), a.undoScript)
	}
	ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Padding(px(t, 14), px(t, 20)).Gap(px(t, 10)).Children(func() {
			a.composerToolbar(c)
			a.scriptView(c)
			a.statsLine(c)
			a.generateBar(c)
			if a.job != nil {
				a.jobStatus(c)
			} else if a.jobError != "" {
				a.jobProblem(c)
			}
			if cl := a.lastClip(); cl != nil {
				a.outputPanel(c, cl, true)
			}
		})
		ui.Box(c).Width(1).Background(t.Border)
		ui.Scroll(c).Width(px(t, inspectorWidth)).Shrink(0).Background(t.Surface).Children(func() {
			ui.Column(c).Padding(px(t, 16)).Gap(px(t, 14)).Children(func() {
				a.inspector(c)
			})
		})
	})
	// After the inputs above applied this frame's edits.
	a.autosaveStudio()
}

// composerToolbar: insert menu, import, and the script's language.
func (a *app) composerToolbar(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
		a.insertMenu(c)
		if ui.Button(c.Key("import"), i18n.T("Import script…")).Tooltip(i18n.T("TXT, Markdown, DOCX, EPUB, SRT or VTT, inserted at the caret")).Clicked() {
			a.importScript()
		}
		ui.Spacer(c)
		a.languagePicker(c)
	})
}

// formatDuration is "45 s", "3 min 20 s", "1 h 05 min".
func formatDuration(seconds float64) string {
	s := int(math.Round(seconds))
	switch {
	case s < 60:
		return i18n.T("%d s", s)
	case s < 3600:
		if s%60 == 0 {
			return i18n.T("%d min", s/60)
		}
		return i18n.T("%d min %d s", s/60, s%60)
	}
	return i18n.T("%d h %02d min", s/3600, (s%3600)/60)
}

// typicalRTF is about how many seconds of audio a model makes per second
// on an M-series Mac, for the slow-generation warning.
func typicalRTF(family string) float64 {
	switch family {
	case "kokoro", "kitten":
		return 20
	case "soprano":
		return 8
	case "omnivoice":
		return 3
	case "marvis":
		return 1.5
	}
	return 2
}

// statsLine: characters, estimated length, chunks, and the readiness or
// slow-generation line.
func (a *app) statsLine(c *ui.Context) {
	t := c.Theme()
	st := a.scriptStats()
	speed := 1.0
	if a.preset().Has(catalog.CapSpeed) && a.speed > 0 {
		speed = a.speed
	}
	seconds := st.seconds / speed
	if a.preset().Has(catalog.CapDuration) && a.duration > 0 {
		seconds = a.duration
	}
	ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
		parts := i18n.T("%d characters", st.chars)
		if st.chunks > 0 {
			parts += " · " + i18n.T("≈ %s of audio", formatDuration(seconds))
		}
		if st.chunks > 1 {
			parts += " · " + i18n.N(st.chunks, "%d chunk", "%d chunks", st.chunks)
		}
		ui.Text(c, parts).FontSize(px(t, 12)).TextColor(t.TextMuted)
		if n := len(st.diags); n > 0 {
			msg := diagText(st.diags[0])
			if n > 1 {
				msg = i18n.N(n, "%d note about the markup", "%d notes about the markup", n) + ": " + msg
			}
			ui.Text(c, "⚠ "+msg).FontSize(px(t, 12)).TextColor(t.Warning).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
		}
	})
	if msg, warn := a.readiness(st); msg != "" {
		color := t.TextMuted
		if warn {
			color = t.Warning
		}
		ui.Row(c.Key("readiness")).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, msg).FontSize(px(t, 12)).TextColor(color).Grow(1).MinWidth(0)
			e := a.preset()
			if m := a.model(e); e.ID != "custom" && m.known && m.local.State != hub.Downloaded && !m.downloading && a.needDownload == nil {
				if ui.Button(c.Key("ready-download"), i18n.T("Download")).Tooltip(e.Repo).Clicked() {
					a.download(e)
				}
			}
		})
		return
	}
	if rtf := typicalRTF(a.preset().Family); seconds/rtf > 90 {
		ui.Text(c.Key("slow"), i18n.T("A long render: about %s of audio may take %s or more. It plays from the first chunk while the rest renders.",
			formatDuration(seconds), formatDuration(seconds/rtf))).FontSize(px(t, 12)).TextColor(t.Warning)
	}
}

// readiness says what keeps Generate from working, or "" when nothing
// does; warn is true for a problem (not just the engine starting).
func (a *app) readiness(st scriptStats) (msg string, warn bool) {
	p := a.preset()
	switch {
	case a.engState == engineStarting:
		return i18n.T("The engine is starting…"), false
	case a.engState == engineFailed:
		return i18n.T("The engine is not running. Restart it on the Engine page."), true
	case p.Repo == "":
		return i18n.T("Type the Hugging Face repo of the model to use."), true
	case st.chars == 0:
		return i18n.T("Type, paste or import a script."), false
	case st.chunks == 0:
		return i18n.T("The script has nothing to say: only markup."), true
	}
	if m := a.model(p); p.ID != "custom" && m.known && m.local.State != hub.Downloaded && a.needDownload == nil {
		return i18n.T("%s is not downloaded yet.", p.Label), true
	}
	if p.Has(catalog.CapClone) && len(p.Voices) == 0 && !p.Has(catalog.CapVoiceStyle) && a.refAudio == "" && a.voice == "" {
		return i18n.T("No reference clip: the model speaks in its default voice."), false
	}
	return "", false
}

func (a *app) generateBar(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
		a.voicePopover(c)
		a.qualityPopover(c)
		if s := a.voiceControlsSummary(); s != "" {
			ui.Text(c, s).FontSize(px(t, 12)).TextColor(t.TextMuted)
		}
		ui.Spacer(c)
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

// jobProblem shows why the last job failed or did not start, with a
// Download button when the model is missing.
func (a *app) jobProblem(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
		ui.Text(c, a.jobError).TextColor(t.Danger).FontSize(px(t, 12)).Selectable().Grow(1).MinWidth(0)
		e := a.needDownload
		if e == nil {
			return
		}
		m := a.model(*e)
		switch {
		case m.downloading:
			text := i18n.T("Downloading %s", e.Label)
			if m.progress.TotalBytes > 0 {
				text = i18n.T("Downloading %s · %d%%", e.Label, int(100*m.progress.Bytes/m.progress.TotalBytes))
			}
			ui.Text(c, text).FontSize(px(t, 12)).TextColor(t.TextMuted)
			if ui.Button(c.Key("dl-cancel"), i18n.T("Cancel")).Clicked() {
				a.cancelDownload(*e)
			}
		default:
			if ui.Button(c.Key("dl-model"), i18n.T("Download")).Tooltip(e.Repo).Clicked() {
				a.download(*e)
			}
		}
	})
}

func (a *app) jobStatus(c *ui.Context) {
	t := c.Theme()
	j := a.job
	ui.Column(c).Gap(px(t, 6)).Children(func() {
		ui.Progress(c, j.fraction).Label(j.status)
		ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
			status := j.status
			if j.kind == "synthesize" && a.renderTake == j.takeID && a.renderChunks > 1 && a.renderChunk > 0 && !j.cancelling {
				status += " · " + i18n.T("chunk %d of %d", a.renderChunk, a.renderChunks)
				if a.renderCached > 0 {
					status += " · " + i18n.N(a.renderCached, "%d from the cache", "%d from the cache", a.renderCached)
				}
			}
			ui.Text(c, status).FontSize(px(t, 12)).Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
			info := i18n.T("%.0f s", time.Since(j.started).Seconds())
			if j.audioSeconds > 0 {
				info = i18n.T("%.1f s of audio · ", j.audioSeconds) + info
			}
			ui.Text(c, info).FontSize(px(t, 12)).TextColor(t.TextMuted)
			if a.previewAvailable() {
				label := i18n.T("Play preview")
				if a.pl.Playing(a.previewID) {
					label = i18n.T("Pause preview")
				}
				if ui.Button(c.Key("preview"), label).Tooltip(i18n.T("Listen to what is rendered so far")).Clicked() {
					switch {
					case a.pl.Playing(a.previewID):
						a.pl.Stop()
					default:
						a.playPreview()
					}
				}
			}
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
	ui.Text(c, about).FontSize(px(t, 12)).TextColor(t.TextMuted)
	if !p.Licence.Commercial {
		ui.Text(c, p.Licence.Name+" · "+i18n.T("non-commercial use only")).FontSize(px(t, 12)).TextColor(t.Warning)
	}

	ui.Row(c).Gap(px(t, 8)).Children(func() {
		loaded := p.Repo != "" && p.Repo == a.loadedModel
		label := i18n.T("Preload")
		if loaded {
			label = i18n.T("Loaded")
		}
		if ui.Button(c.Key("preload"), label).
			Disabled(loaded || a.job != nil || a.engState != engineReady || p.Repo == "").
			Tooltip(i18n.T("Load the model now, so the first take starts at once")).
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
	a.studioVoiceSection(c, p)
	ui.Divider(c)
	if ui.Checkbox(c.Key("autoplay"), &a.autoplay, i18n.T("Play each take when it is ready")).Changed() {
		on := a.autoplay
		a.updateSettings(func(s *settings.Settings) { s.Studio.Autoplay = on })
	}
	if !a.autoplay {
		return
	}
	ui.Text(c, i18n.T("Long scripts play from the first chunk while the rest renders.")).FontSize(px(t, 11)).TextColor(t.TextMuted)
}
