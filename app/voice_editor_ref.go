package main

// The voice editor's reference (thread #83): a clone's reference quality
// and its automatic cleaning, which can be undone; a designed voice's
// reference for clone and dialogue models (apart from its sample, which
// previews it): play it, make it, make it again. An imported voice's
// reference is cleaned once it is imported.

import (
	"context"
	"os"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp/refcheck"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
)

// editorReference is the reference's quality and cleaning, or a designed
// voice's reference.
func (a *app) editorReference(c *ui.Context, v *core.Voice) {
	t := c.Theme()
	id := v.ID
	if v.Kind == core.VoiceDesigned {
		if v.Locked {
			return // it clones from its locked take
		}
		a.designedReference(c, v)
		return
	}
	if v.Kind != core.VoiceCloned || v.RefCheck == nil && v.RefOriginal == "" {
		return
	}
	ui.Column(c.Key("editor-ref")).Gap(px(t, sp1)).Children(func() {
		refCheckView(c, "editor-ref-check", v.RefCheck, refcheck.UseClone)
		if v.RefOriginal == "" {
			return
		}
		ui.Row(c).Gap(px(t, sp2)).AlignItems(ui.Center).Wrap().Children(func() {
			caption(c, cleanedText(v.RefCheck)).Grow(1).Basis(px(t, 160)).MinWidth(0)
			if ghostButton(c.Key("editor-ref-revert"), i18n.T("Use the original")).Tooltip(i18n.T("Undo the automatic cleaning of the reference")).Clicked() {
				a.voiceAction(func(ctx context.Context, svc core.VoiceService) (core.Voice, error) {
					return svc.RevertReference(ctx, id)
				}, i18n.T("The original reference is back"))
			}
		})
	})
}

// cleanedText says what the automatic cleaning did to a reference.
func cleanedText(q *core.RefQuality) string {
	var parts []string
	if q != nil {
		for _, s := range q.Cleaned {
			switch s {
			case "denoised":
				parts = append(parts, i18n.T("noise removed"))
			case "tidied":
				parts = append(parts, i18n.T("silences trimmed, loudness evened out"))
			}
		}
	}
	if len(parts) == 0 {
		return i18n.T("Cleaned automatically.")
	}
	return i18n.T("Cleaned up: %s", strings.Join(parts, i18n.T(", ")))
}

// designedReference is a designed voice's reference: its state, play,
// make (again).
func (a *app) designedReference(c *ui.Context, v *core.Voice) {
	t := c.Theme()
	k := tok(t)
	ui.Column(c.Key("editor-dref")).Gap(px(t, sp1)).Children(func() {
		ui.Row(c).Gap(px(t, sp2)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, i18n.T("Reference for cloning and dialogue")).FontSize(px(t, fsCaption)).FontWeight(600).Grow(1).MinWidth(0)
			infoTip(c, i18n.T("Models that clone, and Stories' whole dialogue, copy this voice from a reference: about 12 seconds of lively everyday speech, the best of three takes. The sample above is for previews."))
		})
		ui.Row(c).Gap(px(t, sp2)).AlignItems(ui.Center).Wrap().Children(func() {
			switch m := a.refMaking(v.ID); {
			case m != nil:
				done, total := m.progress()
				ui.Spinner(c)
				caption(c, i18n.T("Making a reference… %d of %d takes", done, total)).Grow(1).MinWidth(0)
				return
			case v.RefAudio != "":
				pid := "voiceref:" + v.ID
				if playToggle(c.Key("editor-dref-play"), a.pl.Playing(pid), i18n.T("Play the reference"), i18n.T("Play the reference")).Clicked() {
					if err := a.pl.Toggle(pid, a.voicePath(v, v.RefAudio)); err != nil {
						a.toastWarn(i18n.T("Could not play: %v", err))
					}
				}
				caption(c, i18n.T("%.1f s", v.RefSeconds)).Tooltip(v.RefText)
				truncText(c, v.RefText).FontSize(px(t, fsCaption)).TextColor(k.Text3).Grow(1).Basis(px(t, 80))
			default:
				caption(c, i18n.T("None yet: it is made when first needed.")).Grow(1).MinWidth(0)
			}
			label := i18n.T("Make reference")
			if v.RefAudio != "" {
				label = i18n.T("Make it again")
			}
			if ghostButton(c.Key("editor-dref-make"), label).Clicked() {
				a.makeReference(v, nil)
			}
		})
		if err := a.refFailed(v.ID); err != "" {
			statusMark(c, icons.DangerCircle, t.Danger, i18n.T("Could not make the reference: %s", err)).Tooltip(err)
		}
		if v.RefAudio != "" {
			refCheckView(c, "editor-dref-check", v.RefCheck, refcheck.UseDialogue)
		}
	})
}

// cleanImportedReference checks and cleans an imported voice's
// reference (denoised when noisy, tidied), keeping the original.
func (a *app) cleanImportedReference(v core.Voice) {
	if v.RefAudio == "" || v.RefOriginal != "" {
		return
	}
	id, src, work := v.ID, a.voicePath(&v, v.RefAudio), a.workPath("import")
	transcript := v.RefText
	a.decodeClip(src, work, func(secs float64, err error) {
		if err != nil {
			os.Remove(work)
			return
		}
		a.background(func() {
			r, err := measureRef(work, span{}, nil, "", "")
			a.post(func() {
				if err != nil {
					os.Remove(work)
					return
				}
				if !r.Noisy() {
					a.saveCleanedImport(id, work, "", transcript, r, secs)
					return
				}
				clean := a.workPath("import-clean")
				a.enhanceClip(work, clean, func(err error) {
					if err != nil { // no model: tidied only
						os.Remove(clean)
						a.saveCleanedImport(id, work, "", transcript, r, secs)
						return
					}
					a.saveCleanedImport(id, clean, work, transcript, r, secs)
				})
			})
		})
	})
}

// saveCleanedImport tidies path (denoised from extra, when not "") and
// makes it the voice's reference.
func (a *app) saveCleanedImport(id, path, extra, transcript string, r refcheck.Report, secs float64) {
	svc := a.lib.svc.Voices
	a.background(func() {
		defer os.Remove(path)
		if extra != "" {
			defer os.Remove(extra)
		}
		b, _, err := wav.ReadFile(path)
		if err != nil {
			return
		}
		out, _ := refcheck.Tidy(b, nil)
		if err := wav.WriteFile(path, out, wav.PCM16, wav.Options{}); err != nil {
			return
		}
		cleaned := []string{"tidied"}
		if extra != "" {
			cleaned = []string{"denoised", "tidied"}
		}
		o := textOptions(transcript)
		r.Rate = 0
		if o.Units > 0 && r.Body() > 0 {
			r.Rate, r.CJK = float64(o.Units)/r.Body(), o.CJK
		}
		q := refQuality(r, cleaned, secs)
		saved, err := svc.CleanReference(context.Background(), id, path, out.Seconds(), q)
		a.post(func() {
			if err == nil {
				a.voiceChanged(core.VoiceChanged{Voice: saved})
			}
		})
	})
}
