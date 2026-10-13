package main

import (
	"slices"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/gallery"
	"github.com/hehehai/AudioSmith/internal/i18n"
)

// clonePage is /voices/new: a voice from a clip.
func (a *app) clonePage(c *ui.Context) {
	t := c.Theme()
	s := &a.clone
	title := i18n.T("New voice from a clip")
	if v := a.findVoice(s.replaceFor); v != nil {
		title = i18n.T("Replace the reference of “%s”", v.Name)
	}
	header(c, title, func() {
		if ui.Button(c.Key("clone-back"), i18n.T("Cancel")).Clicked() {
			a.micCancel(&s.mic)
			a.navigate("/voices")
		}
	})
	if s.busy != "" || s.mic.phase != "" {
		c.After(100 * time.Millisecond) // the timer, the level
	}
	page := ui.Scroll(c.Key("clone-scroll")).Grow(1).MinHeight(0)
	if files := page.DroppedFiles(); files != nil {
		a.cloneDropped(files)
	}
	page.Children(func() {
		ui.Column(c).Padding(px(t, 20), px(t, 24)).Gap(px(t, 16)).MaxWidth(px(t, 780)).Children(func() {
			if !s.hasClip() {
				a.cloneSources(c)
			} else {
				a.cloneClip(c)
				a.cloneDetails(c)
				a.cloneSaveForm(c)
			}
		})
	})
}

// cloneSources are the two ways in: a file, or the microphone.
func (a *app) cloneSources(c *ui.Context) {
	t := c.Theme()
	s := &a.clone
	ui.Text(c, i18n.T("Use 5–15 seconds of clean speech: one speaker, no music, little room echo.")).TextColor(t.TextMuted)
	if s.busy != "" {
		ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c)
			ui.Text(c, s.busy)
		})
		return
	}
	ui.Row(c).Gap(px(t, 16)).AlignItems(ui.Stretch).Children(func() {
		// Upload zone.
		zone := ui.ButtonBase(c.Key("clone-upload")).Grow(1).Basis(0).MinHeight(px(t, 160)).Padding(px(t, 16)).
			Radius(12).Border(1, t.Border).BorderStyle(ui.BorderDashed).Label(i18n.T("Choose a file"))
		if files := zone.DroppedFiles(); files != nil {
			a.cloneDropped(files)
		}
		if zone.FileDragOver() {
			zone.Border(2, t.Accent).Background(t.SurfaceHover)
		}
		if zone.Clicked() {
			a.chooseCloneFile()
		}
		zone.Children(func() {
			ui.Column(c).Grow(1).Center().Gap(px(t, 6)).Children(func() {
				ui.Text(c, i18n.T("Drop a clip here, or click to choose")).Bold()
				hint := i18n.T("WAV, MP3, M4A, FLAC, OGG, AAC — or a video")
				ui.Text(c, hint).FontSize(px(t, 12)).TextColor(t.TextMuted).TextAlign(ui.Center)
			})
		})
		// Record zone.
		ui.Column(c.Key("clone-record")).Grow(1).Basis(0).MinHeight(px(t, 160)).Padding(px(t, 16)).Gap(px(t, 8)).
			Radius(12).Border(1, t.Border).AlignItems(ui.Center).Children(func() {
			a.micControls(c, &s.mic, func(path string) { a.cloneLoad(path, i18n.T("Recording"), true) })
		})
	})
	if s.err != "" {
		ui.Text(c, s.err).TextColor(t.Danger).FontSize(px(t, 12)).Selectable()
	}
}

// micControls are a recorder's button, timer, level and device pickers.
func (a *app) micControls(c *ui.Context, m *micState, done func(path string)) {
	t := c.Theme()
	a.micEnded(m)
	switch m.phase {
	case "recording":
		secs := time.Since(m.started).Seconds()
		if ui.PrimaryButton(c.Key("mic-stop"), i18n.T("Stop recording")).Background(t.Danger).Clicked() {
			a.micStop(m)
		}
		ui.Text(c, i18n.T("%.1f s", secs)).FontSize(px(t, 18)).Bold()
		ui.Meter(c, float64(levelFraction(m.level)), 0, 1, nil).Width(px(t, 160)).Label(i18n.T("Input level"))
		if inputDetected(m.level) {
			ui.Text(c, i18n.T("Input detected")).FontSize(px(t, 12)).TextColor(t.Success)
		} else {
			ui.Text(c, i18n.T("No input detected")).FontSize(px(t, 12)).TextColor(t.TextMuted)
		}
	case "starting", "stopping":
		ui.Spinner(c)
		label := i18n.T("Starting…")
		if m.phase == "stopping" {
			label = i18n.T("Finishing…")
		}
		ui.Text(c, label).FontSize(px(t, 12)).TextColor(t.TextMuted)
	default:
		if ui.PrimaryButton(c.Key("mic-start"), i18n.T("Record")).Clicked() {
			a.micStart(m, done)
		}
		ui.Text(c, i18n.T("Your microphone is never played back.")).FontSize(px(t, 12)).TextColor(t.TextMuted)
	}
	busy := m.phase != ""
	ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
		labels, ids := []string{i18n.T("System default")}, []string{""}
		for _, d := range a.clone.devices {
			labels, ids = append(labels, d.Name), append(ids, d.ID)
		}
		dev := labels[max(slices.Index(ids, m.device), 0)]
		if ui.Select(c.Key("mic-device"), &dev, labels).Label(i18n.T("Input device")).Disabled(busy).Changed() {
			if i := slices.Index(labels, dev); i >= 0 {
				m.device = ids[i]
			}
		}
		ch := m.channels
		if ui.Segmented(c.Key("mic-channels"), &ch, i18n.T("Auto"), i18n.T("Mono"), i18n.T("Stereo")).Label(i18n.T("Channels")).Disabled(busy).Changed() {
			m.channels = ch
		}
	})
	if m.err != "" {
		ui.Text(c, m.err).FontSize(px(t, 12)).TextColor(t.Danger).TextAlign(ui.Center)
	} else if a.clone.devicesErr != "" && m.phase == "" {
		ui.Text(c, a.clone.devicesErr).FontSize(px(t, 12)).TextColor(t.TextMuted).TextAlign(ui.Center)
	}
}

// cloneClip is the loaded clip: waveform, trimmer, length advice, noise.
func (a *app) cloneClip(c *ui.Context) {
	t := c.Theme()
	s := &a.clone
	ui.Column(c.Key("clone-clip")).Gap(px(t, 10)).Padding(px(t, 14)).Radius(10).Border(1, t.Border).Background(t.Surface).Children(func() {
		ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, s.srcName).Bold().SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
			ui.Text(c, i18n.T("%.1f s", s.seconds)).TextColor(t.TextMuted)
			if ui.Button(c.Key("clone-change"), i18n.T("Change clip")).Clicked() {
				a.pl.Stop()
				s.gen++
				s.work, s.original, s.peaks, s.err, s.busy = "", "", nil, "", ""
			}
		})
		a.trimWave(c, s)
		ui.RangeSlider(c.Key("clone-trim"), &s.selLo, &s.selHi, 0, s.seconds, 0.1).Label(i18n.T("Part kept"))
		ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, i18n.T("%.1f–%.1f s · %.1f s kept", s.selLo, s.selHi, s.sel().Len())).FontSize(px(t, 12)).Grow(1)
			label := i18n.T("Play the part kept")
			if a.pl.Playing("clone-sel") {
				label = i18n.T("Stop")
			}
			if ui.Button(c.Key("clone-play"), label).Clicked() {
				if a.pl.Playing("clone-sel") {
					a.pl.Stop()
				} else {
					a.cloneSelection(func(path string, _ float64, err error) {
						if err == nil {
							a.pl.Toggle("clone-sel", path)
						}
					})
				}
			}
			if ui.Button(c.Key("clone-best"), i18n.T("Best part")).Tooltip(i18n.T("Keep the 15 seconds with the most speech")).Clicked() {
				w := bestWindow(s.speech, s.seconds, refIdealHigh)
				s.selLo, s.selHi = w.Start, w.End
			}
			if ui.Button(c.Key("clone-all"), i18n.T("Keep all")).Clicked() {
				s.selLo, s.selHi = 0, s.seconds
			}
		})
		note, tone := refAdvice(s.sel().Len(), s.transcript != "")
		color := t.TextMuted
		switch tone {
		case "warning":
			color = t.Warning
		case "danger":
			color = t.Danger
		}
		ui.Text(c, note).FontSize(px(t, 12)).TextColor(color)
		ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
			switch {
			case s.busy != "":
				ui.Spinner(c)
				ui.Text(c, s.busy).FontSize(px(t, 12))
			case s.cleaned:
				ui.Text(c, i18n.T("Noise cleaned")).FontSize(px(t, 12)).TextColor(t.Success)
				if ui.Button(c.Key("clone-unclean"), i18n.T("Undo")).Clicked() {
					a.cloneUndoClean()
				}
			default:
				if ui.Button(c.Key("clone-clean"), i18n.T("Clean reference noise")).Tooltip(i18n.T("Remove background noise and room echo")).Clicked() {
					a.cloneClean(false)
				}
				if s.cleanErr != "" {
					ui.Text(c, i18n.T("Could not clean the clip: %s", s.cleanErr)).FontSize(px(t, 12)).TextColor(t.Danger).Grow(1).MinWidth(0)
				}
			}
		})
	})
}

// trimWave draws the clip's waveform with the part kept highlighted; a
// click plays from there.
func (a *app) trimWave(c *ui.Context, s *cloneState) {
	t := c.Theme()
	w := ui.ButtonBase(c.Key("clone-wave")).Height(px(t, 72)).Label(i18n.T("Waveform")).Tooltip(i18n.T("Click to listen from here"))
	if w.Clicked() {
		if x, _, ok := w.PointerPosition(); ok && s.work != "" {
			if a.pl.Current() != "clone" {
				a.pl.Toggle("clone", s.work)
			}
			a.pl.Seek("clone", x/max(w.Bounds().W, 1))
		}
	}
	peaks, total, lo, hi := s.peaks, s.seconds, s.selLo, s.selHi
	accent, muted := t.Accent, t.TextMuted.Alpha(0.45)
	pl := &a.pl
	w.Draw(func(p *ui.Painter, r ui.Rect) {
		if total <= 0 {
			return
		}
		x0, x1 := r.X+r.W*float32(lo/total), r.X+r.W*float32(hi/total)
		p.Fill(ui.Rect{X: x0, Y: r.Y, W: max(x1-x0, 1), H: r.H}, accent.Alpha(0.12), 4)
		n := len(peaks)
		if n == 0 {
			p.Line(r.X, r.Y+r.H/2, r.X+r.W, r.Y+r.H/2, 1, muted)
			return
		}
		step := r.W / float32(n)
		bar := max(step*0.6, 1)
		for i, v := range peaks {
			h := max(v*r.H, 2)
			x := r.X + float32(i)*step
			color := muted
			if x >= x0 && x <= x1 {
				color = accent
			}
			p.Fill(ui.Rect{X: x, Y: r.Y + (r.H-h)/2, W: bar, H: h}, color, bar/2)
		}
		if prog := pl.Progress("clone"); pl.Playing("clone") {
			x := r.X + r.W*prog
			p.Line(x, r.Y, x, r.Y+r.H, 1.5, accent)
			p.AnimationFrame()
		}
	})
}

// cloneDetails are the transcript, the style hint and the language.
func (a *app) cloneDetails(c *ui.Context) {
	t := c.Theme()
	s := &a.clone
	ui.Field(c, i18n.T("Transcript"), func() {
		ui.TextArea(c.Key("clone-transcript"), &s.transcript).Lines(2, 6).
			Placeholder(i18n.T("(Optional) What the clip says. Leave it empty to let the engine listen.")).Label(i18n.T("Transcript"))
	})
	ui.Row(c.Key("clone-asr")).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
		switch s.asr {
		case "running":
			ui.Spinner(c)
			ui.Text(c, i18n.T("Transcribing…")).FontSize(px(t, 12)).TextColor(t.TextMuted)
		case "noasr":
			ui.Text(c, i18n.T("No speech-to-text model is installed: transcribing needs one.")).FontSize(px(t, 12)).TextColor(t.Warning).Grow(1)
			if ui.Button(c.Key("asr-models"), i18n.T("Models")).Clicked() {
				a.navigate("/models")
			}
			if ui.Button(c.Key("asr-retry"), i18n.T("Retry transcription")).Clicked() {
				a.cloneTranscribe()
			}
		case "failed":
			ui.Text(c, i18n.T("Transcription failed")).FontSize(px(t, 12)).TextColor(t.Danger).Grow(1).Tooltip(s.asrErr)
			if ui.Button(c.Key("asr-retry"), i18n.T("Retry transcription")).Clicked() {
				a.cloneTranscribe()
			}
		default:
			if ui.Button(c.Key("asr-run"), i18n.T("Transcribe the part kept")).Clicked() {
				a.cloneTranscribe()
			}
		}
	})
	a.styleField(c, "clone-style", &s.style)
	ui.Field(c, i18n.T("Language"), func() {
		ui.Autocomplete(c.Key("clone-language"), &s.language, voiceLanguages()).Placeholder(i18n.T("Auto")).Label(i18n.T("Language"))
	})
}

// styleField is a style hint input with the presets' hints as chips.
func (a *app) styleField(c *ui.Context, key string, style *string) {
	t := c.Theme()
	ui.Field(c, i18n.T("Style"), func() {
		ui.TextInput(c.Key(key), style).Placeholder(i18n.T("e.g. whisper, or calm and slow")).Label(i18n.T("Style"))
	})
	ui.Row(c.Key(key+"-presets")).Gap(px(t, 6)).Wrap().Children(func() {
		loc := i18n.Locale()
		for _, p := range gallery.Presets() {
			hint := p.StyleHint.In(loc)
			if hint == "" {
				continue
			}
			if ui.Button(c.Key(key+"-"+p.ID), p.Name.In(loc)).Tooltip(hint).Clicked() {
				*style = hint
			}
		}
	})
}

// cloneSaveForm is the name and portrait, and what to do with the clip.
func (a *app) cloneSaveForm(c *ui.Context) {
	t := c.Theme()
	s := &a.clone
	problem := a.cloneProblem()
	ui.Divider(c)
	if s.replaceFor != "" {
		ui.Text(c, i18n.T("Saving replaces this voice's reference. Its lock and its ownership verification are cleared, because the new clip may be another speaker.")).
			FontSize(px(t, 12)).TextColor(t.TextMuted)
		ui.Row(c).Gap(px(t, 8)).Justify(ui.End).AlignItems(ui.Center).Children(func() {
			if problem != "" {
				ui.Text(c, problem).FontSize(px(t, 12)).TextColor(t.TextMuted).Grow(1)
			}
			if ui.PrimaryButton(c.Key("clone-replace"), i18n.T("Replace reference")).Disabled(problem != "" || s.saving).Clicked() {
				a.cloneSave()
			}
		})
		return
	}
	ui.Text(c, i18n.T("Save as voice profile")).Bold()
	ui.Row(c).Gap(px(t, 14)).AlignItems(ui.Center).Children(func() {
		a.portraitPicker(c, "clone-portrait", s.name, s.portraitBmp, a.clonePortrait, func() { s.portrait, s.portraitBmp = nil, nil })
		ui.Field(c, i18n.T("Profile name"), func() {
			ui.TextInput(c.Key("clone-name"), &s.name).Placeholder(i18n.T("e.g. My narrator voice")).Label(i18n.T("Profile name"))
		}).Grow(1)
	})
	ui.Row(c).Gap(px(t, 8)).Justify(ui.End).AlignItems(ui.Center).Children(func() {
		if problem != "" {
			ui.Text(c, problem).FontSize(px(t, 12)).TextColor(t.TextMuted).Grow(1)
		}
		if ui.Button(c.Key("clone-once"), i18n.T("Use without saving")).Disabled(problem != "").
			Tooltip(i18n.T("Use the clip in the Studio once, without adding it to your voices")).Clicked() {
			a.cloneUseOnce()
		}
		if ui.PrimaryButton(c.Key("clone-save"), i18n.T("Save voice")).Disabled(problem != "" || s.saving).Clicked() {
			a.cloneSave()
		}
	})
}

// portraitPicker is an avatar that takes a dropped or chosen image, with
// Remove when there is one.
func (a *app) portraitPicker(c *ui.Context, key, name string, bmp *ui.Bitmap, use func(path string), remove func()) {
	t := c.Theme()
	ui.Column(c.Key(key)).Gap(px(t, 4)).AlignItems(ui.Center).Children(func() {
		b := ui.ButtonBase(c.Key(key+"-pick")).Radius(px(t, 32)).Label(i18n.T("Choose a portrait")).
			Tooltip(i18n.T("Click or drop an image: PNG, JPG or WebP up to 5 MB"))
		if files := b.DroppedFiles(); files != nil {
			use(files[0])
		}
		if b.FileDragOver() {
			b.Border(2, t.Accent)
		}
		if b.Clicked() {
			a.choosePortrait(use)
		}
		if name == "" {
			name = "?"
		}
		b.Children(func() {
			ui.Avatar(c, name, bmp).Size(px(t, 64), px(t, 64))
		})
		if bmp != nil && ui.Button(c.Key(key+"-remove"), i18n.T("Remove photo")).Clicked() {
			remove()
		}
	})
}
