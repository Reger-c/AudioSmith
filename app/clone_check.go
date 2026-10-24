package main

// The reference checker and the automatic cleaning on the Clone page
// (thread #83, after #81 §4.1–§4.2 and §4.5): a loaded clip is measured;
// a noisy one is denoised (only then: DeepFilterNet does nothing for a
// clean clip but harm it a little), and the part kept is tidied (its
// edges trimmed to 0.15 s, pauses over 0.7 s shortened, −18 LUFS). Both
// can be undone; the saved voice keeps the original too. A recording of
// 15–30 s keeps its best 10–14 s. Tips for recording sit by the
// recorder.

import (
	"fmt"
	"os"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/refcheck"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
)

// The window a long clip keeps: its best 10–14 s.
const (
	refWindowLow  = 10.0
	refWindowHigh = 14.0
)

// refWindow is the part of a clip kept at first: all of a clip up to
// 15 s; else its best 10–14 s, starting and ending at words
// (refcheck.PickWindow), or the 15 s with the most speech when no such
// window fits.
func refWindow(speech []span, total float64) span {
	if total <= refIdealHigh {
		return span{0, total}
	}
	rs := make([]dsp.Range, len(speech))
	for i, s := range speech {
		rs[i] = dsp.Range{Start: s.Start, End: s.End}
	}
	if w, ok := refcheck.PickWindow(rs, total, refWindowLow, refWindowHigh); ok {
		return span{w.Start, w.End}
	}
	return bestWindow(speech, total, refIdealHigh)
}

// tidyWAV tidies a cut WAV in place (refcheck.Tidy), with the clip's
// speech regions (seconds in the clip; sel is where the cut starts).
func tidyWAV(path string, sel span, speech []span) (float64, error) {
	b, _, err := wav.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var rs []dsp.Range
	if len(speech) > 0 {
		rs = []dsp.Range{}
		for _, s := range speech {
			if s.End > sel.Start && s.Start < sel.End {
				rs = append(rs, dsp.Range{Start: max(s.Start, sel.Start) - sel.Start, End: min(s.End, sel.End) - sel.Start})
			}
		}
	}
	out, _ := refcheck.Tidy(b, rs)
	if err := wav.WriteFile(path, out, wav.PCM16, wav.Options{}); err != nil {
		return 0, err
	}
	return out.Seconds(), nil
}

// cloneFirstCheck measures a new clip's part kept: a noisy one is
// denoised before it is transcribed.
func (a *app) cloneFirstCheck(gen int) {
	s := &a.clone
	work, sel, speech := s.work, s.sel(), append([]span{}, s.speech...)
	a.background(func() {
		r, err := measureRef(work, sel, speech, "", "")
		a.post(func() {
			if s.gen != gen {
				return
			}
			if err == nil {
				s.check, s.checkKey, s.noisy = &r, cloneCheckKey(s), r.Noisy()
			}
			if s.noisy {
				a.cloneClean(true)
				return
			}
			a.cloneTranscribe()
		})
	})
}

// cloneCheckKey is what the check depends on.
func cloneCheckKey(s *cloneState) string {
	return fmt.Sprintf("%s|%.2f|%.2f|%s|%s", s.work, s.selLo, s.selHi, s.transcript, s.autoText)
}

// cloneRecheck measures the part kept again when it, the clip or the
// transcript changed (one check at a time).
func (a *app) cloneRecheck() {
	s := &a.clone
	key := cloneCheckKey(s)
	if !s.hasClip() || s.busy != "" || s.checking || s.check == nil || key == s.checkKey {
		return
	}
	s.checking = true
	gen, work, sel, speech := s.gen, s.work, s.sel(), append([]span{}, s.speech...)
	transcript, heard := strings.TrimSpace(s.transcript), strings.TrimSpace(s.autoText)
	a.background(func() {
		r, err := measureRef(work, sel, speech, transcript, heard)
		a.post(func() {
			if s.gen != gen {
				return
			}
			s.checking = false
			if err == nil {
				s.check, s.checkKey = &r, key
			}
		})
	})
}

// cloneQuality is the part kept's quality as the voice keeps it.
func (a *app) cloneQuality() *core.RefQuality {
	s := &a.clone
	if s.check == nil {
		return nil
	}
	q := refQuality(*s.check, cloneCleaned(s), s.sel().Len())
	if s.noisy && !strings.Contains(strings.Join(q.Hints, ","), refcheck.CodeNoisy) && s.check.SNR >= refcheck.NoisySNR {
		// Denoised since: say so.
		q.Hints = append([]string{refcheck.CodeNoisy}, q.Hints...)
		if q.Grade == string(refcheck.Good) {
			q.Grade = string(refcheck.OK)
		}
	}
	return q
}

// cloneCleaned lists what the automatic cleaning did.
func cloneCleaned(s *cloneState) []string {
	var out []string
	if s.cleaned {
		out = append(out, "denoised")
	}
	if s.tidy {
		out = append(out, "tidied")
	}
	return out
}

// cloneOriginal writes the part kept as it came (not denoised, not
// tidied) for the saved voice's Undo; "" when nothing was cleaned.
func (a *app) cloneOriginal(done func(path string)) {
	s := &a.clone
	if !s.cleaned && !s.tidy {
		done("")
		return
	}
	src := s.work
	if s.cleaned && s.original != "" {
		src = s.original
	}
	sel, total, out := s.sel(), s.seconds, a.workPath("orig")
	a.background(func() {
		_, err := cutPart(src, out, sel, total)
		a.post(func() {
			if err != nil {
				os.Remove(out)
				out = ""
			}
			done(out)
		})
	})
}

// cloneCleanup is the clip's quality and what the cleaning did, with Undo.
func (a *app) cloneCleanup(c *ui.Context) {
	t := c.Theme()
	s := &a.clone
	a.cloneRecheck()
	refCheckView(c, "clone-check", a.cloneQuality(), refcheck.UseClone)
	ui.Row(c.Key("clone-cleanup")).Gap(px(t, sp2)).AlignItems(ui.Center).Wrap().Children(func() {
		switch {
		case s.busy != "":
			ui.Spinner(c)
			ui.Text(c, s.busy).FontSize(px(t, fsCaption))
			return
		case s.cleaned || s.tidy:
			var parts []string
			if s.cleaned {
				parts = append(parts, i18n.T("noise removed"))
			}
			if s.tidy {
				parts = append(parts, i18n.T("silences trimmed, loudness evened out"))
			}
			statusMark(c, icons.CheckCircle, t.Success, i18n.T("Cleaned up: %s", strings.Join(parts, i18n.T(", "))))
			if ghostButton(c.Key("clone-unclean"), i18n.T("Undo")).Tooltip(i18n.T("Keep the clip as it came")).Clicked() {
				s.tidy = false
				a.cloneUndoClean()
			}
		default:
			if ghostButton(c.Key("clone-tidy"), i18n.T("Clean up")).Tooltip(i18n.T("Trim the silences and even out the loudness")).Clicked() {
				s.tidy = true
			}
		}
		if !s.cleaned {
			if ghostIconButton(c.Key("clone-clean"), icons.MagicStick3, i18n.T("Clean reference noise")).Label(i18n.T("Clean reference noise")).Tooltip(i18n.T("Remove background noise and room echo")).Clicked() {
				a.cloneClean(false)
			}
		}
		if s.cleanErr != "" {
			ui.Text(c, i18n.T("Could not clean the clip: %s", s.cleanErr)).FontSize(px(t, fsCaption)).TextColor(t.Danger).Grow(1).MinWidth(0)
		}
	})
}

// recordingTips are tips for recording one's own voice (#81 §4.5), folded.
func (a *app) recordingTips(c *ui.Context) {
	t := c.Theme()
	s := &a.clone
	collapsible(c.Key("clone-tips"), i18n.T("Tips for recording your voice"), &s.tipsOpen, nil, func() {
		ui.Column(c).Gap(px(t, 4)).Children(func() {
			for _, tip := range []string{
				i18n.T("Find a quiet small room (curtains, a wardrobe or a sofa help) and turn off fans, air conditioning, the TV and music."),
				i18n.T("Keep the microphone 20–30 cm from your mouth."),
				i18n.T("Don't read a script: talk, about your day for example, at your usual pace. 15–30 seconds is enough: the app keeps the best 10 seconds or so."),
				i18n.T("Only you: nobody else talking."),
				i18n.T("Speak the way you want the voice to sound: clones copy the delivery."),
				i18n.T("Listen back: if it distorts, echoes or hisses, record again."),
			} {
				caption(c, "• "+tip)
			}
		})
	})
}
