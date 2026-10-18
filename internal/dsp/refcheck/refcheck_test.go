package refcheck

import (
	"math"
	"slices"
	"testing"

	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/dsptest"
	"github.com/hehehai/AudioSmith/internal/dsp/loudness"
)

const rate = 24000

// phrase is speech-like: n syllables (0.2 s bursts every 0.22 s, so the
// gaps are too short to be pauses) at pitch f0, peaking at −6 dBFS.
func phrase(n int, f0 float64) dsp.Buffer {
	return dsptest.Syllables(rate, n, f0, 0.2, 0.22, 0, -6)
}

// talk is lead s of silence, then phrases at rising pitches with gaps
// between them, then trail s of silence.
func talk(lead, trail float64, gaps ...float64) dsp.Buffer {
	f0s := []float64{150, 190, 240, 300}
	parts := []dsp.Buffer{dsptest.Silence(rate, 1, lead)}
	for i := range len(gaps) + 1 {
		parts = append(parts, phrase(10, f0s[i%len(f0s)]))
		if i < len(gaps) {
			parts = append(parts, dsptest.Silence(rate, 1, gaps[i]))
		}
	}
	parts = append(parts, dsptest.Silence(rate, 1, trail))
	return dsptest.Concat(parts...)
}

// withNoise adds white noise at dBFS RMS.
func withNoise(b dsp.Buffer, dBFS float64) dsp.Buffer {
	out := b.Clone()
	dsptest.Add(out, dsptest.Noise(rate, 1, dBFS, b.Seconds(), 7), 0)
	return out
}

func near(t *testing.T, what string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %.3f, want %.3f ± %.3f", what, got, want, tol)
	}
}

func codes(hs []Hint) []string {
	var out []string
	for _, h := range hs {
		out = append(out, h.Code)
	}
	return out
}

// TestMeasureClean: a clean, lively clip measures as such: speech, edges,
// pauses, a capped SNR, pitch spread; no hints, graded Good.
func TestMeasureClean(t *testing.T) {
	b := talk(0.5, 0.4, 0.3, 0.3, 0.3) // 4 phrases of 2.2 s
	r := Measure(b, Options{CER: -1})
	near(t, "seconds", r.Seconds, 0.5+4*2.2+0.9+0.4, 0.01)
	near(t, "lead", r.Lead, 0.5, 0.03)
	near(t, "trail", r.Trail, 0.42, 0.05)
	near(t, "speech", r.Speech, 4*2.2-0.02*4, 0.15)
	if r.Pauses != 3 || r.LongestPause < 0.29 || r.LongestPause > 0.36 {
		t.Errorf("pauses %d, longest %.2f", r.Pauses, r.LongestPause)
	}
	if r.SNR < 59 || r.Noisy() {
		t.Errorf("SNR %.1f", r.SNR)
	}
	if r.F0 < 180 || r.F0 > 260 || r.F0Range < 8 {
		t.Errorf("F0 %.0f Hz, range %.1f st", r.F0, r.F0Range)
	}
	if r.Clipped != 0 || r.PeakDB > -5.9 {
		t.Errorf("clipped %v, peak %.1f", r.Clipped, r.PeakDB)
	}
	if hs := r.Hints(UseDialogue); len(hs) != 0 || r.Grade(UseDialogue) != Good {
		t.Errorf("hints %v, grade %s", hs, r.Grade(UseDialogue))
	}
}

// TestMeasureNoise: the SNR estimate follows the noise; 15–30 dB is
// denoised (Info), under 15 dB means recording again (Warn).
func TestMeasureNoise(t *testing.T) {
	clean := talk(0.5, 0.5, 0.4, 0.4, 0.4)
	prev := 100.0
	for _, c := range []struct {
		noise float64
		code  string
		grade Grade
	}{
		{-70, "", Good},
		{-38, CodeNoisy, OK},
		{-20, CodeVeryNoisy, Poor},
	} {
		r := Measure(withNoise(clean, c.noise), Options{CER: -1})
		if r.SNR >= prev {
			t.Errorf("noise %v dB: SNR %.1f not under %.1f", c.noise, r.SNR, prev)
		}
		prev = r.SNR
		got := codes(r.Hints(UseClone))
		if c.code == "" && (len(got) != 0 || r.Noisy()) || c.code != "" && !slices.Contains(got, c.code) {
			t.Errorf("noise %v dB: SNR %.1f, hints %v", c.noise, r.SNR, got)
		}
		if r.Grade(UseClone) != c.grade {
			t.Errorf("noise %v dB: grade %s", c.noise, r.Grade(UseClone))
		}
		if c.code != "" && !r.Noisy() {
			t.Errorf("noise %v dB: not noisy at %.1f dB", c.noise, r.SNR)
		}
	}
	// The caller's speech regions (Silero) are used as given.
	b := withNoise(clean, -38)
	r := Measure(b, Options{Speech: []dsp.Range{{Start: 0.5, End: b.Seconds() - 0.5}}, CER: -1})
	if r.Lead != 0.5 || r.Pauses != 0 {
		t.Errorf("given regions: lead %.2f, pauses %d", r.Lead, r.Pauses)
	}
}

// TestMeasureClipping: full-scale samples over 0.1 % are clipping.
func TestMeasureClipping(t *testing.T) {
	b := talk(0.3, 0.3, 0.3)
	for i, v := range b.Samples {
		b.Samples[i] = float32(max(min(float64(v)*4, 1), -1))
	}
	r := Measure(b, Options{CER: -1})
	if r.Clipped <= ClipLimit || !slices.Contains(codes(r.Hints(UseClone)), CodeClipping) || r.Grade(UseClone) != Poor {
		t.Errorf("clipped %.4f, hints %v", r.Clipped, r.Hints(UseClone))
	}
}

// TestMeasureHints: short, flat, slow, mismatched and silent clips.
func TestMeasureHints(t *testing.T) {
	// Silence.
	r := Measure(dsptest.Silence(rate, 1, 3), Options{CER: -1})
	if got := codes(r.Hints(UseClone)); !slices.Equal(got, []string{CodeNoSpeech}) {
		t.Errorf("silence: %v", got)
	}
	// 4.4 s of speech: too short; 6.6 s: fine to clone, short for dialogue.
	r = Measure(talk(0.2, 0.2, 0.3), Options{CER: -1})
	if got := codes(r.Hints(UseClone)); !slices.Contains(got, CodeTooShort) {
		t.Errorf("4.4 s: %v", got)
	}
	r = Measure(talk(0.2, 0.2, 0.3, 0.3), Options{CER: -1})
	if got := codes(r.Hints(UseClone)); slices.Contains(got, CodeTooShort) || slices.Contains(got, CodeShort) {
		t.Errorf("6.6 s, clone: %v", got)
	}
	if got := codes(r.Hints(UseDialogue)); !slices.Contains(got, CodeShort) {
		t.Errorf("6.6 s, dialogue: %v", got)
	}
	// One pitch: flat.
	flat := dsptest.Concat(dsptest.Silence(rate, 1, 0.2), phrase(45, 200), dsptest.Silence(rate, 1, 0.2))
	r = Measure(flat, Options{CER: -1})
	if got := codes(r.Hints(UseClone)); !slices.Contains(got, CodeFlat) || r.F0Range > 1 {
		t.Errorf("flat: %.1f st, %v", r.F0Range, got)
	}
	// Rate: 30 characters over ~9 s is slow for dialogue, 45 is not;
	// clones are not told.
	b := talk(0.2, 0.2, 0.3, 0.3, 0.3)
	slow := Measure(b, Options{Units: 30, CJK: true, CER: -1})
	fast := Measure(b, Options{Units: 45, CJK: true, CER: -1})
	if !slices.Contains(codes(slow.Hints(UseDialogue)), CodeSlow) || slices.Contains(codes(fast.Hints(UseDialogue)), CodeSlow) ||
		slices.Contains(codes(slow.Hints(UseClone)), CodeSlow) {
		t.Errorf("rate %.2f / %.2f", slow.Rate, fast.Rate)
	}
	// A transcript the recognizer does not hear.
	r = Measure(b, Options{CER: 0.25})
	if got := codes(r.Hints(UseClone)); !slices.Contains(got, CodeMismatch) || r.Grade(UseClone) != Poor {
		t.Errorf("mismatch: %v", got)
	}
}

// TestTidy: edges cut to 0.15 s, a long pause to 0.5 s, short pauses
// kept, −18 LUFS with peaks under −1.5 dBTP.
func TestTidy(t *testing.T) {
	b := talk(1.2, 0.9, 0.3, 1.6, 0.4)
	b2 := b.Clone()
	out, did := Tidy(b, nil)
	if !slices.Equal(b.Samples, b2.Samples) {
		t.Fatal("Tidy changed its input")
	}
	if did.Shortened != 1 || !did.Changed() {
		t.Errorf("tidied %+v", did)
	}
	want := b.Seconds() - (1.2 - 0.15) - (0.9 - 0.15) - (1.6 - 0.5)
	near(t, "length", out.Seconds(), want, 0.08)
	r := Measure(out, Options{CER: -1})
	near(t, "lead", r.Lead, 0.15, 0.04)
	near(t, "trail", r.Trail, 0.15, 0.06)
	near(t, "longest pause", r.LongestPause, 0.5, 0.05)
	if r.Pauses != 3 {
		t.Errorf("pauses %d", r.Pauses)
	}
	st := loudness.Measure(out)
	near(t, "LUFS", st.Integrated, LUFS, 0.2)
	if st.TruePeak > Ceiling+0.05 {
		t.Errorf("true peak %.2f", st.TruePeak)
	}
	// A quiet, already tidy clip only gets louder.
	quiet := talk(0.1, 0.1, 0.3)
	dsp.Gain(quiet.Samples, 0.1)
	out, did = Tidy(quiet, nil)
	if did.Changed() || out.Frames() != quiet.Frames() || did.GainDB < 5 {
		t.Errorf("quiet: %+v, %d frames", did, out.Frames())
	}
	// No speech: as it was, evened out (silence stays silence).
	out, did = Tidy(dsptest.Silence(rate, 1, 2), nil)
	if did.Changed() || out.Frames() != 2*rate {
		t.Errorf("silence: %+v", did)
	}
}

// TestPickWindow: the best 10–14 s of a recording, starting and ending
// at words, avoiding long pauses.
func TestPickWindow(t *testing.T) {
	// 30 s: talk 1–6, a 3 s pause, 9–20 with short pauses, 22–29.
	speech := []dsp.Range{{1, 6}, {9, 12}, {12.3, 15}, {15.4, 20}, {22, 29}}
	w, ok := PickWindow(speech, 30, 10, 14)
	if !ok || math.Abs(w.Start-8.85) > 0.01 || w.Len() < 10 || w.Len() > 15 {
		t.Errorf("window %+v %v", w, ok)
	}
	// One unbroken stretch: nothing fits, the caller decides.
	if _, ok := PickWindow([]dsp.Range{{1, 29}}, 30, 10, 14); ok {
		t.Error("unbroken stretch fitted")
	}
	// Too little speech.
	if _, ok := PickWindow([]dsp.Range{{1, 3}, {4, 6}}, 30, 10, 14); ok {
		t.Error("short speech fitted")
	}
	// A long pause inside costs: the window without it wins.
	speech = []dsp.Range{{0.5, 6}, {9.5, 13}, {13.4, 20}, {20.3, 24}}
	w, _ = PickWindow(speech, 25, 10, 14)
	if w.Start < 9 {
		t.Errorf("window over the pause %+v", w)
	}
}

// TestPick: a candidate with faults loses to one without; among those
// without, the pitch closest to the voice's wins.
func TestPick(t *testing.T) {
	good := Report{Seconds: 12, Speech: 11, F0: 220, F0Range: 9, SNR: 60, CER: 0}
	child := good
	child.F0 = 360
	misheard := good
	misheard.CER = 0.08
	short := good
	short.Speech = 5
	cs := []Candidate{{Raw: misheard, Tidy: misheard}, {Raw: child, Tidy: child}, {Raw: good, Tidy: good}, {Raw: short, Tidy: short}}
	if got := Pick(cs, 225); got != 2 {
		t.Errorf("picked %d", got)
	}
	if f := cs[0].Faults(); !slices.Equal(f, []string{CodeMismatch}) {
		t.Errorf("faults %v", f)
	}
	// Without a pitch to match, the livelier intonation wins.
	lively := good
	lively.F0Range = 12
	if got := Pick([]Candidate{{Raw: good, Tidy: good}, {Raw: lively, Tidy: lively}}, 0); got != 1 {
		t.Errorf("picked %d", got)
	}
	// All with faults: the fewest.
	short.CER = 0.08
	if got := Pick([]Candidate{{Raw: misheard, Tidy: short}, {Raw: misheard, Tidy: misheard}}, 0); got != 1 {
		t.Errorf("picked %d", got)
	}
	if Pick(nil, 0) != -1 {
		t.Error("picked from none")
	}
}

// TestGradeOf: codes graded as reports are; dialogue-only codes do not
// count for clones.
func TestGradeOf(t *testing.T) {
	b := talk(0.2, 0.2, 0.3, 0.3)
	r := Measure(b, Options{Units: 20, CJK: true, CER: -1})
	for _, u := range []Use{UseClone, UseDialogue} {
		if got := GradeOf(codes(r.Hints(UseDialogue)), u); got != r.Grade(u) {
			t.Errorf("use %d: %s, report says %s", u, got, r.Grade(u))
		}
	}
	if GradeOf([]string{CodeSlow, CodeShort}, UseClone) != Good || GradeOf([]string{CodeClipping}, UseClone) != Poor {
		t.Error("grades")
	}
}
