package main

// The reference checker in the UI (thread #83, after #81 §4.1): what
// dsp/refcheck measures, said simply where references come in (Clone, an
// imported voice, the voice editor, Dub's speakers, Stories' whole
// dialogue). One line grades the reference (good, fine, needs work) and
// at most two hints say what to do; none of it blocks. The measures stay
// out of sight.

import (
	"path/filepath"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/refcheck"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
	"github.com/hehehai/AudioSmith/internal/text"
)

// textOptions are the checker's options for a transcript: its size and
// script; the error rate unknown.
func textOptions(s string) refcheck.Options {
	w, c := text.Count(s)
	if c > w {
		return refcheck.Options{Units: c, CJK: true, CER: -1}
	}
	return refcheck.Options{Units: w, CER: -1}
}

// textDrift is the recognizer's error rate on what should be said.
func textDrift(want, heard string) float64 { return dub.Drift(want, heard) }

// refCER is the recognizer's character error rate on what a reference
// made from a voice should say (spaces and punctuation left out, so a
// word written in two does not count; #93).
func refCER(want, heard string) float64 { return dub.CharDrift(want, heard) }

// uniquePath is a new WAV file in dir.
func uniquePath(dir, prefix string) string {
	return filepath.Join(dir, prefix+"-"+core.NewID(time.Now())+".wav")
}

// measureRef measures the part sel of a WAV with its speech regions
// (seconds in the file), its transcript and what the recognizer heard
// ("" when it did not listen, or the transcript is the recognizer's).
func measureRef(path string, sel span, speech []span, transcript, heard string) (refcheck.Report, error) {
	b, _, err := wav.ReadFile(path)
	if err != nil {
		return refcheck.Report{}, err
	}
	from := min(max(dsp.FramesAt(sel.Start, b.Rate), 0), b.Frames())
	to := min(max(dsp.FramesAt(sel.End, b.Rate), from), b.Frames())
	if sel.End <= sel.Start {
		from, to = 0, b.Frames()
	}
	b = b.Slice(from, to)
	start := dsp.SecondsAt(from, b.Rate)
	o := textOptions(firstOf(transcript, heard))
	if transcript != "" && heard != "" && transcript != heard {
		o.CER = textDrift(transcript, heard)
	}
	if len(speech) > 0 {
		o.Speech = []dsp.Range{}
		for _, s := range speech {
			if s.End > sel.Start && (sel.End <= sel.Start || s.Start < sel.End) {
				o.Speech = append(o.Speech, dsp.Range{Start: s.Start - start, End: s.End - start})
			}
		}
	}
	return refcheck.Measure(b, o), nil
}

// refQuality is a report as a voice keeps it: its hints for dialogue
// (a superset: refCheckView shows those of the use at hand), its grade as
// a clone reference, what the cleaning did, and how long the original was.
func refQuality(r refcheck.Report, cleaned []string, originalSeconds float64) *core.RefQuality {
	q := &core.RefQuality{Grade: string(r.Grade(refcheck.UseClone)), Cleaned: cleaned, OriginalSeconds: originalSeconds}
	for _, h := range r.Hints(refcheck.UseDialogue) {
		q.Hints = append(q.Hints, h.Code)
	}
	return q
}

// refGradeText says a grade.
func refGradeText(g string) string {
	switch refcheck.Grade(g) {
	case refcheck.Good:
		return i18n.T("Reference quality: good")
	case refcheck.OK:
		return i18n.T("Reference quality: fine")
	case refcheck.Poor:
		return i18n.T("Reference quality: needs work")
	}
	return ""
}

// refHintText says what a hint means and what to do; denoised says the
// noise was already removed.
func refHintText(code string, denoised bool) string {
	switch code {
	case refcheck.CodeNoSpeech:
		return i18n.T("No speech found: use a clip of someone talking.")
	case refcheck.CodeTooShort:
		return i18n.T("Too short: say a few more sentences; about 10 seconds is best.")
	case refcheck.CodeVeryNoisy:
		return i18n.T("Very noisy: record again somewhere quiet.")
	case refcheck.CodeClipping:
		return i18n.T("Too loud, so it distorts: move a little away from the microphone and record again.")
	case refcheck.CodeMismatch:
		return i18n.T("The text and the recording don't match: check the transcript.")
	case refcheck.CodeNoisy:
		if denoised {
			return i18n.T("There was background noise: it was removed automatically. For a better result, record again somewhere quiet.")
		}
		return i18n.T("There is background noise: clean it, or record again somewhere quiet.")
	case refcheck.CodeShort:
		return i18n.T("A little short for dialogue: 8–15 seconds works best.")
	case refcheck.CodeFlat:
		return i18n.T("The delivery is a little flat, and clones copy it: speak as you would in a conversation.")
	case refcheck.CodeSlow:
		return i18n.T("It sounds read slowly: talk at a conversational pace for livelier dialogue.")
	}
	return ""
}

// refCheckView shows a reference's grade for use u and its first two
// hints.
func refCheckView(c *ui.Context, key string, q *core.RefQuality, u refcheck.Use) {
	if q == nil || q.Grade == "" {
		return
	}
	t := c.Theme()
	k := tok(t)
	denoised := slices.Contains(q.Cleaned, "denoised")
	g := refcheck.GradeOf(q.Hints, u)
	ic, col := icons.CheckCircle, t.Success
	switch g {
	case refcheck.OK:
		ic, col = icons.InfoCircle, k.Text2
	case refcheck.Poor:
		ic, col = icons.DangerTriangle, t.Warning
	}
	ui.Column(c.Key(key)).Gap(px(t, 2)).MinWidth(0).Children(func() {
		statusMark(c, ic, col, refGradeText(string(g)))
		shown := 0
		for _, h := range q.Hints {
			if shown == 2 || !refcheck.ForUse(h, u) {
				continue
			}
			if s := refHintText(h, denoised); s != "" {
				caption(c, s).MinWidth(0)
				shown++
			}
		}
	})
}
