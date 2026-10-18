package refcheck

import (
	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/loudness"
)

// Tidy's settings (#81 §4.2).
const (
	EdgeKeep  = 0.15  // seconds of silence kept before the first word and after the last
	PauseKeep = 0.5   // a long pause becomes this long
	LUFS      = -18.0 // the references' loudness
	Ceiling   = -1.5  // dBTP
	fade      = 0.01  // seconds of fade at each cut
)

// Target is the references' loudness target.
var Target = loudness.Target{ID: "reference", Integrated: LUFS, MinIntegrated: LUFS - 1, MaxIntegrated: LUFS + 1, TruePeak: Ceiling}

// Tidied says what Tidy did.
type Tidied struct {
	Trimmed   float64 `json:"trimmed"`   // seconds of leading and trailing silence removed
	Shortened int     `json:"shortened"` // long pauses shortened
	Removed   float64 `json:"removed"`   // seconds removed from them
	GainDB    float64 `json:"gainDb"`
}

// Changed reports whether Tidy cut anything.
func (t Tidied) Changed() bool { return t.Trimmed > 0.005 || t.Shortened > 0 }

// Tidy returns b (mono) with its leading and trailing silence cut to
// EdgeKeep, every pause over LongPause cut to PauseKeep (in its middle),
// with 10 ms fades at the cuts, evened out to Target. speech are its
// speech regions (nil: Speech finds them); without any, only the loudness
// changes. b is not modified.
func Tidy(b dsp.Buffer, speech []dsp.Range) (dsp.Buffer, Tidied) {
	m := dsp.Remix(b, 1)
	var t Tidied
	if m.Frames() == 0 || m.Rate <= 0 {
		return m, t
	}
	total := m.Seconds()
	if speech == nil {
		speech = Speech(m)
	}
	speech = clean(speech, total)
	var cuts []dsp.Range
	if len(speech) > 0 {
		if s := speech[0].Start - EdgeKeep; s > 0.005 {
			cuts = append(cuts, dsp.Range{Start: 0, End: s})
			t.Trimmed += s
		}
		for i := 1; i < len(speech); i++ {
			gap := dsp.Range{Start: speech[i-1].End, End: speech[i].Start}
			if gap.Len() > LongPause {
				c := dsp.Range{Start: gap.Start + PauseKeep/2, End: gap.End - PauseKeep/2}
				cuts = append(cuts, c)
				t.Shortened++
				t.Removed += c.Len()
			}
		}
		if e := speech[len(speech)-1].End + EdgeKeep; total-e > 0.005 {
			cuts = append(cuts, dsp.Range{Start: e, End: total})
			t.Trimmed += total - e
		}
	}
	out := cut(m, cuts)
	n, rep := loudness.Normalize(out, Target, loudness.NormalizeOptions{})
	t.GainDB = rep.GainDB
	return n, t
}

// cut returns m without the ranges cuts (sorted, apart), fading 10 ms in
// and out at each cut.
func cut(m dsp.Buffer, cuts []dsp.Range) dsp.Buffer {
	if len(cuts) == 0 {
		return m.Clone()
	}
	f := dsp.FramesAt(fade, m.Rate)
	out := dsp.Buffer{Rate: m.Rate, Channels: 1, Samples: make([]float32, 0, len(m.Samples))}
	from := 0
	keep := func(to int, fadeIn, fadeOut bool) {
		if to <= from {
			return
		}
		piece := dsp.Buffer{Rate: m.Rate, Channels: 1, Samples: append([]float32(nil), m.Samples[from:to]...)}
		if fadeIn {
			dsp.FadeIn(piece, min(f, piece.Frames()/2), dsp.Linear)
		}
		if fadeOut {
			dsp.FadeOut(piece, min(f, piece.Frames()/2), dsp.Linear)
		}
		out.Samples = append(out.Samples, piece.Samples...)
	}
	for _, c := range cuts {
		a := min(max(dsp.FramesAt(c.Start, m.Rate), from), m.Frames())
		keep(a, from > 0, true)
		from = min(max(dsp.FramesAt(c.End, m.Rate), a), m.Frames())
	}
	keep(m.Frames(), from > 0, false)
	return out
}

// PickWindow picks the best stretch of lo–hi seconds of a longer
// recording (#81 §4.5: record 15–30 s, keep the best 10–14 s): windows
// start EdgeKeep before a word and end EdgeKeep after one, so no word is
// cut; the one with the most speech wins, less the time its long pauses
// waste and up to 1 s over hi. ok is false when no window fits (one long
// stretch of speech, or too little): the caller keeps its own choice.
func PickWindow(speech []dsp.Range, total, lo, hi float64) (w dsp.Range, ok bool) {
	speech = clean(speech, total)
	best := -1e9
	for i := range speech {
		start := max(speech[i].Start-EdgeKeep, 0)
		speechIn, waste := 0.0, 0.0
		for j := i; j < len(speech); j++ {
			speechIn += speech[j].Len()
			if j > i {
				waste += max(speech[j].Start-speech[j-1].End-LongPause, 0)
			}
			end := min(speech[j].End+EdgeKeep, total)
			n := end - start
			if n > hi+1 {
				break
			}
			if n < lo {
				continue
			}
			score := speechIn - waste - max(n-hi, 0)
			if score > best+1e-9 {
				best, w, ok = score, dsp.Range{Start: start, End: end}, true
			}
		}
	}
	return w, ok
}
