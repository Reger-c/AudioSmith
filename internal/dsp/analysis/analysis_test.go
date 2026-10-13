package analysis

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"testing"

	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/dsptest"
)

func sil(rate int, s float64) dsp.Buffer { return dsptest.Silence(rate, 1, s) }

// syl is speech-like: n syllables of f0 Hz, one every period seconds.
func syl(rate, n int, f0, burst, period, dBFS float64) dsp.Buffer {
	return dsptest.Syllables(rate, n, f0, burst, period, 0, dBFS)
}

// whisper is unvoiced: noise bursts with the syllable envelope.
func whisper(rate, n int, burst, period, dBFS float64) dsp.Buffer {
	nz := dsptest.Noise(rate, 1, dBFS, float64(n)*period, 9)
	b := dsp.New(rate, 1, nz.Frames())
	bl := dsp.FramesAt(burst, rate)
	for k := range n {
		s := dsp.FramesAt(float64(k)*period, rate)
		for i := range bl {
			if s+i < b.Frames() {
				b.Samples[s+i] = nz.Samples[s+i] * float32(0.5-0.5*math.Cos(2*math.Pi*float64(i)/float64(bl)))
			}
		}
	}
	return b
}

func withFloor(b dsp.Buffer) dsp.Buffer {
	dsptest.Add(b, dsptest.Noise(b.Rate, 1, -75, b.Seconds(), 3), 0)
	return b
}

func TestVAD(t *testing.T) {
	for _, rate := range []int{16000, 24000, 48000} {
		b := withFloor(dsptest.Concat(sil(rate, 1), syl(rate, 8, 150, 0.18, 0.25, -10), sil(rate, 1.5), syl(rate, 4, 200, 0.18, 0.25, -10), sil(rate, 0.5)))
		segs := VAD(b, VADOptions{})
		want := []dsp.Range{{Start: 1, End: 3}, {Start: 4.5, End: 5.5}}
		if len(segs) != 2 {
			t.Fatalf("%d: segments %v", rate, segs)
		}
		for i, w := range want {
			// The last syllable ends 70 ms early; hangover adds 150 ms.
			if math.Abs(segs[i].Start-w.Start) > 0.05 || math.Abs(segs[i].End-w.End) > 0.15 {
				t.Errorf("%d: segment %d = %+v, want ≈ %+v", rate, i, segs[i], w)
			}
		}
	}
	if s := VAD(sil(24000, 2), VADOptions{}); len(s) != 0 {
		t.Errorf("silence: %v", s)
	}
	if s := VAD(dsptest.Noise(24000, 1, -30, 2, 1), VADOptions{}); len(s) != 0 {
		t.Errorf("steady noise is not speech: %v", s)
	}
	if s := VAD(dsp.Buffer{Rate: 24000, Channels: 1}, VADOptions{}); len(s) != 0 {
		t.Error("empty")
	}
}

func TestOnsets(t *testing.T) {
	const r = 24000
	burst := func(f, sec float64) dsp.Buffer {
		b := dsptest.Sine(r, 1, f, -12, sec, 0)
		dsp.FadeIn(b, r/200, dsp.EqualPower)
		dsp.FadeOut(b, r/100, dsp.EqualPower)
		return b
	}
	// Speech onsets after silence.
	b := withFloor(dsptest.Concat(sil(r, 0.5), burst(300, 0.5), sil(r, 0.6), burst(400, 0.4), sil(r, 0.6), burst(250, 0.3), sil(r, 0.3)))
	on := Onsets(b, OnsetOptions{SpeechOnly: true})
	want := []float64{0.5, 1.6, 2.6}
	if len(on) != len(want) {
		t.Fatalf("speech onsets %+v", on)
	}
	for i, w := range want {
		if math.Abs(on[i].Time-w) > 0.015 || on[i].Kind != "speech" {
			t.Errorf("onset %d at %.3f, want %.3f", i, on[i].Time, w)
		}
	}
	// A note change inside continuous sound is a flux onset.
	c := dsptest.Concat(burst(220, 1), dsptest.Sine(r, 1, 523, -12, 1, 0))
	fl := Onsets(withFloor(c), OnsetOptions{FluxOnly: true})
	found := slices.ContainsFunc(fl, func(o Onset) bool { return math.Abs(o.Time-1) < 0.02 && o.Kind == "flux" })
	if !found {
		t.Errorf("note change at 1.0 s not found: %+v", fl)
	}
	// Syllables: one flux onset near each start.
	s := withFloor(dsptest.Concat(sil(r, 0.3), syl(r, 10, 140, 0.15, 0.3, -10)))
	all := Onsets(s, OnsetOptions{})
	hits := 0
	for k := range 10 {
		st := 0.3 + 0.3*float64(k)
		if slices.ContainsFunc(all, func(o Onset) bool { return o.Time >= st-0.01 && o.Time <= st+0.06 }) {
			hits++
		}
	}
	if hits < 9 || len(all) > 13 {
		t.Errorf("syllable onsets: %d of 10 hit, %d total: %+v", hits, len(all), all)
	}
	if _, err := json.Marshal(all); err != nil {
		t.Error(err)
	}
}

func TestPitch(t *testing.T) {
	cases := []struct {
		name   string
		b      dsp.Buffer
		median float64
		tol    float64
	}{
		{"sine 220 @24k", dsptest.Sine(24000, 1, 220, -12, 1, 0), 220, 1},
		{"sine 100 @48k stereo", dsptest.Sine(48000, 2, 100, -12, 1, 0), 100, 0.5},
		{"sine 400 @44.1k", dsptest.Sine(44100, 1, 400, -12, 1, 0), 400, 2},
		{"harmonic 150", syl(24000, 4, 150, 0.2, 0.25, -6), 150, 1.5},
		{"low 75", syl(24000, 4, 75, 0.2, 0.25, -6), 75, 1},
	}
	for _, c := range cases {
		pc := Pitch(c.b, PitchOptions{})
		st := pc.Stats()
		if math.Abs(st.MedianHz-c.median) > c.tol || st.Voiced < 0.6 {
			t.Errorf("%s: median %.2f Hz (want %v), voiced %.2f", c.name, st.MedianHz, c.median, st.Voiced)
		}
	}
	// A glide from 150 to 300 Hz spans an octave.
	g := dsp.New(24000, 1, 48000)
	var ph float64
	for i := range g.Samples {
		f := 150 * math.Pow(2, float64(i)/48000)
		ph += 2 * math.Pi * f / 24000
		g.Samples[i] = float32(0.3 * math.Sin(ph))
	}
	st := Pitch(g, PitchOptions{}).Stats()
	if st.RangeST < 8.5 || st.RangeST > 10.5 || st.StdST < 3 {
		t.Errorf("glide: range %.2f st (want ≈ 9.6, the 10–90%% span of 12), std %.2f", st.RangeST, st.StdST)
	}
	// Noise is mostly unvoiced; silence is not counted at all.
	if v := Pitch(dsptest.Noise(24000, 1, -12, 1, 2), PitchOptions{}).Stats().Voiced; v > 0.2 {
		t.Errorf("noise voiced %.2f", v)
	}
	sp := Pitch(dsptest.Concat(sil(24000, 1), dsptest.Sine(24000, 1, 200, -12, 1, 0)), PitchOptions{})
	if v := sp.Stats().Voiced; v < 0.9 {
		t.Errorf("silence counted as unvoiced: %.2f", v)
	}
	if st := Pitch(sil(24000, 0.5), PitchOptions{}).Stats(); st.MedianHz != 0 {
		t.Error("silence pitch")
	}
}

func TestLoudnessContour(t *testing.T) {
	c := LoudnessContour(dsptest.Sine(48000, 1, 1000, -20, 2, 0), 0, 0)
	for i, v := range c.Values {
		if math.Abs(v+23.01) > 0.1 {
			t.Fatalf("value %d at %.2f s: %.2f LUFS", i, c.Time(i), v)
		}
	}
	if len(c.Values) != 17 || math.Abs(c.Time(0)-0.2) > 1e-9 {
		t.Errorf("%d values, first at %v", len(c.Values), c.Time(0))
	}
}

func TestSpeakingRate(t *testing.T) {
	for _, c := range []struct {
		burst, period float64
		n             int
	}{{0.18, 0.25, 20}, {0.13, 0.18, 30}, {0.25, 0.4, 12}} {
		b := withFloor(dsptest.Concat(sil(24000, 0.5), syl(24000, c.n, 140, c.burst, c.period, -10), sil(24000, 0.5)))
		rate, n := SpeakingRate(b, RateOptions{})
		want := 1 / c.period
		t.Logf("period %.2f: %d nuclei, %.2f syl/s (want %.2f)", c.period, n, rate, want)
		if abs(n-c.n) > 1 || math.Abs(rate-want)/want > 0.12 {
			t.Errorf("period %.2f: %d nuclei (want %d), %.2f syl/s (want %.2f)", c.period, n, c.n, rate, want)
		}
		if nv := Nuclei(b, RateOptions{Voiced: true}); abs(len(nv)-c.n) > 1 {
			t.Errorf("voiced nuclei %d", len(nv))
		}
	}
	// Whispered syllables have nuclei but no voicing.
	w := withFloor(whisper(24000, 10, 0.18, 0.25, -15))
	if n := len(Nuclei(w, RateOptions{})); abs(n-10) > 1 {
		t.Errorf("whisper nuclei %d", n)
	}
	if n := len(Nuclei(w, RateOptions{Voiced: true})); n > 2 {
		t.Errorf("whisper voiced nuclei %d", n)
	}
}

func abs(x int) int { return max(x, -x) }

func TestProsodyMirror(t *testing.T) {
	const r = 24000
	line := func(f0, period, dBFS float64) Prosody {
		return Analyze(withFloor(dsptest.Concat(sil(r, 0.3), syl(r, int(4/period), f0, period*0.72, period, dBFS), sil(r, 0.3))))
	}
	var lines []Prosody
	for i := range 3 {
		lines = append(lines, line(150+float64(i), 0.25, -14))
	}
	base := Baseline(lines)
	if math.Abs(base.Pitch.MedianHz-151) > 2 || math.Abs(base.Rate-4) > 0.5 {
		t.Fatalf("baseline %+v", base)
	}
	cases := []struct {
		name string
		p    Prosody
		want []string
	}{
		{"same", line(151, 0.25, -14), nil},
		{"urgent", line(195, 0.18, -6), []string{"higher", "louder", "faster"}},
		{"calm", line(120, 0.34, -20), []string{"lower", "softer", "slower"}},
		{"whispered", Analyze(withFloor(dsptest.Concat(sil(r, 0.3), whisper(r, 16, 0.18, 0.25, -30), sil(r, 0.3)))), []string{"whispered"}},
	}
	for _, c := range cases {
		d := Compare(c.p, base)
		got := slices.DeleteFunc(slices.Clone(d.Directions), func(s string) bool { return s == "animated" || s == "flat" })
		if fmt.Sprint(got) != fmt.Sprint(append([]string{}, c.want...)) {
			t.Errorf("%s: directions %v, want %v (delta %+v)", c.name, d.Directions, c.want, d)
		}
	}
	if _, err := json.Marshal(Compare(lines[0], base)); err != nil {
		t.Error(err)
	}
	if b := Baseline(nil); b.Loudness > -100 {
		t.Error("empty baseline")
	}
}

func BenchmarkPitch(b *testing.B) {
	x := syl(24000, 40, 150, 0.18, 0.25, -6)
	b.SetBytes(int64(len(x.Samples) * 4))
	for b.Loop() {
		Pitch(x, PitchOptions{})
	}
}

func BenchmarkOnsets(b *testing.B) {
	x := syl(24000, 40, 150, 0.18, 0.25, -6)
	b.SetBytes(int64(len(x.Samples) * 4))
	for b.Loop() {
		Onsets(x, OnsetOptions{})
	}
}

func BenchmarkAnalyze(b *testing.B) {
	x := syl(24000, 40, 150, 0.18, 0.25, -6)
	b.SetBytes(int64(len(x.Samples) * 4))
	for b.Loop() {
		Analyze(x)
	}
}
