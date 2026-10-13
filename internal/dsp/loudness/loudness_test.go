package loudness

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"

	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/dsptest"
)

// tone is a stereo 1 kHz sine at dBFS per channel (EBU test signals).
func tone(rate int, dBFS, seconds float64) dsp.Buffer {
	return dsptest.Sine(rate, 2, 1000, dBFS, seconds, 0)
}

func seq(rate int, parts ...[2]float64) dsp.Buffer {
	var bs []dsp.Buffer
	for _, p := range parts {
		if math.IsInf(p[0], -1) {
			bs = append(bs, dsptest.Silence(rate, 2, p[1]))
		} else {
			bs = append(bs, tone(rate, p[0], p[1]))
		}
	}
	return dsptest.Concat(bs...)
}

// EBU Tech 3341 (2016) §2.9 minimum-requirement cases 1–5, 9, 10 and 12, and
// EBU Tech 3342 (2016) LRA cases 1–4, synthesised from their descriptions
// (stereo 1 kHz sines; the authentic-programme cases 7, 8 of 3341 and 5, 6
// of 3342 need the EBU files). Rates other than 48 kHz exercise the derived
// K-weighting.
func TestTech3341Integrated(t *testing.T) {
	cases := []struct {
		name  string
		parts [][2]float64
		want  float64
	}{
		{"case1 −23 dBFS 20 s", [][2]float64{{-23, 20}}, -23},
		{"case2 −33 dBFS 20 s", [][2]float64{{-33, 20}}, -33},
		{"case3 −36/−23/−36", [][2]float64{{-36, 10}, {-23, 60}, {-36, 10}}, -23},
		{"case4 −72/−36/−23/−36/−72", [][2]float64{{-72, 10}, {-36, 10}, {-23, 60}, {-36, 10}, {-72, 10}}, -23},
		{"case5 −26/−20/−26", [][2]float64{{-26, 20}, {-20, 20.1}, {-26, 20}}, -23},
	}
	for _, rate := range []int{48000, 44100, 24000} {
		t.Run(fmt.Sprint(rate), func(t *testing.T) {
			t.Parallel()
			for _, c := range cases {
				b := seq(rate, c.parts...)
				got := Integrated(b)
				if math.Abs(got-c.want) > 0.1 {
					t.Errorf("%d Hz %s: I = %.3f LUFS, want %.1f ±0.1", rate, c.name, got, c.want)
				}
			}
		})
	}
	// Case 1 also reads −23.0 momentary and short-term.
	m := NewMeter(48000, 2, Options{})
	m.Write(tone(48000, -23, 20).Samples)
	if math.Abs(m.Momentary()+23) > 0.1 || math.Abs(m.ShortTerm()+23) > 0.1 {
		t.Errorf("M %.2f S %.2f", m.Momentary(), m.ShortTerm())
	}
}

func TestTech3341ShortTermMomentary(t *testing.T) {
	const rate = 48000
	// Case 9: 1.34 s at −20 dBFS and 1.66 s at −30 dBFS, repeated: every
	// full 3 s window reads −23.0 short-term.
	var parts [][2]float64
	for range 5 {
		parts = append(parts, [2]float64{-20, 1.34}, [2]float64{-30, 1.66})
	}
	m := NewMeter(rate, 2, Options{SkipTruePeak: true})
	m.Write(seq(rate, parts...).Samples)
	for i, v := range m.ShortTermSeries() {
		if math.Abs(v+23) > 0.1 {
			t.Errorf("case9: short-term %d = %.3f", i, v)
			break
		}
	}
	// Case 10 (adapted): 3 s bursts at −23 dBFS at offsets off the 100 ms
	// grid: max short-term −23.0 ±0.1.
	for _, off := range []float64{0, 0.01, 0.033, 0.05, 0.099} {
		m := NewMeter(rate, 2, Options{SkipTruePeak: true})
		m.Write(seq(rate, [2]float64{math.Inf(-1), 1 + off}, [2]float64{-23, 3}, [2]float64{math.Inf(-1), 1}).Samples)
		if st := m.Stats().ShortTermMax; math.Abs(st+23) > 0.1 {
			t.Errorf("case10 offset %v: S max %.3f", off, st)
		}
	}
	// Case 12: 0.18 s at −20 dBFS and 0.22 s at −30 dBFS, repeated: every
	// momentary value reads −23.0 (at the 0.4 s period's alignment).
	parts = nil
	for range 25 {
		parts = append(parts, [2]float64{-20, 0.18}, [2]float64{-30, 0.22})
	}
	m = NewMeter(rate, 2, Options{SkipTruePeak: true})
	m.Write(seq(rate, parts...).Samples)
	for i, v := range m.MomentarySeries() {
		if math.Abs(v+23) > 0.1 {
			t.Errorf("case12: momentary %d = %.3f", i, v)
			break
		}
	}
}

func TestTech3342Range(t *testing.T) {
	cases := []struct {
		parts [][2]float64
		want  float64
	}{
		{[][2]float64{{-20, 20}, {-30, 20}}, 10},
		{[][2]float64{{-20, 20}, {-15, 20}}, 5},
		{[][2]float64{{-40, 20}, {-20, 20}}, 20},
		{[][2]float64{{-50, 20}, {-35, 20}, {-20, 20}, {-35, 20}, {-50, 20}}, 15},
	}
	for i, c := range cases {
		m := NewMeter(48000, 2, Options{SkipTruePeak: true})
		m.Write(seq(48000, c.parts...).Samples)
		lra, _, _ := m.Range()
		t.Logf("3342 case %d: LRA %.2f LU (want %v ±1)", i+1, lra, c.want)
		if math.Abs(lra-c.want) > 1 {
			t.Errorf("3342 case %d: LRA %.2f, want %v ±1", i+1, lra, c.want)
		}
	}
}

func TestTruePeakStats(t *testing.T) {
	// Tech 3341 cases 15–18: 48 kHz sines at −6 dBFS whose samples miss the
	// crest; true peak −6.0 dBTP (+0.2/−0.4).
	for _, c := range []struct{ f, ph float64 }{{12000, 0}, {12000, math.Pi / 4}, {8000, math.Pi / 3}, {6000, 3 * math.Pi / 8}} {
		b := dsptest.Sine(48000, 2, c.f, -6, 1, c.ph)
		// Fade the ends: a sine switched on mid-cycle overshoots (Gibbs),
		// which a true-peak meter rightly reports.
		dsp.FadeIn(b, 480, dsp.EqualPower)
		dsp.FadeOut(b, 480, dsp.EqualPower)
		st := Measure(b)
		t.Logf("%v Hz phase %.3f: sample peak %.2f dBFS, true peak %.2f dBTP", c.f, c.ph, st.SamplePeak, st.TruePeak)
		if st.TruePeak < -6.4 || st.TruePeak > -5.8 {
			t.Errorf("%v Hz %.2f rad: %.2f dBTP", c.f, c.ph, st.TruePeak)
		}
		if st.SamplePeak > st.TruePeak+1e-9 {
			t.Error("sample peak above true peak")
		}
	}
}

func TestMonoAndSilence(t *testing.T) {
	mono := dsptest.Sine(48000, 1, 1000, -20, 5, 0)
	st := Measure(mono)
	// One channel of a −20 dBFS sine: −23.01 LUFS; dual mono: −20.0.
	if math.Abs(st.Integrated+23.01) > 0.05 {
		t.Errorf("mono %.2f", st.Integrated)
	}
	m := NewMeter(48000, 1, Options{DualMono: true})
	m.Write(mono.Samples)
	if math.Abs(m.Integrated()+20) > 0.05 {
		t.Errorf("dual mono %.2f", m.Integrated())
	}
	sil := Measure(dsptest.Silence(24000, 2, 2))
	if sil.Integrated != Floor || sil.TruePeak != Floor || sil.Range != 0 {
		t.Errorf("silence %+v", sil)
	}
	if _, err := json.Marshal(sil); err != nil {
		t.Errorf("stats must marshal: %v", err)
	}
	short := Measure(dsptest.Sine(48000, 2, 1000, -23, 0.3, 0))
	if short.Integrated != Floor || short.MomentaryMax > -22 || short.MomentaryMax < -24 {
		t.Errorf("0.3 s: %+v", short)
	}
	// Streaming in odd chunks equals one shot.
	n := dsptest.Noise(44100, 2, -18, 5, 3)
	a := NewMeter(44100, 2, Options{})
	for s := n.Samples; len(s) > 0; {
		k := min(len(s), 2*1237)
		a.Write(s[:k])
		s = s[k:]
	}
	if got, want := a.Stats(), Measure(n); math.Abs(got.Integrated-want.Integrated) > 1e-9 || got.TruePeak != want.TruePeak {
		t.Errorf("streaming %+v vs %+v", got, want)
	}
}

func speechLike(rate, ch int, dBFS float64) dsp.Buffer {
	s := dsptest.Syllables(rate, 60, 140, 0.18, 0.25, 0.5, dBFS)
	dsptest.Add(s, dsptest.Noise(rate, 1, -100, s.Seconds(), 11), 0)
	return dsp.Remix(s, ch)
}

func TestNormalizePresets(t *testing.T) {
	for _, tgt := range []Target{ACX, Podcast, Broadcast} {
		t.Run(tgt.ID, func(t *testing.T) {
			t.Parallel()
			for _, ch := range []int{1, 2} {
				for _, level := range []float64{-40, -10, -1} {
					in := speechLike(24000, ch, level)
					out, r := Normalize(in, tgt, NormalizeOptions{})
					if out.Frames() != in.Frames() {
						t.Fatalf("frames changed")
					}
					if math.Abs(r.After.Integrated-tgt.Integrated) > 0.15 {
						t.Errorf("%s %dch %v dB: I %.2f, want %.1f", tgt.ID, ch, level, r.After.Integrated, tgt.Integrated)
					}
					if r.After.TruePeak > tgt.TruePeak+0.05 {
						t.Errorf("%s %dch %v dB: TP %.2f over %.1f", tgt.ID, ch, level, r.After.TruePeak, tgt.TruePeak)
					}
					for _, c := range r.Checks {
						if c.Code != "rms" && !c.OK {
							t.Errorf("%s %dch %v dB: check %+v failed", tgt.ID, ch, level, c)
						}
					}
				}
			}
		})
	}
	// Input untouched.
	in := speechLike(24000, 1, -30)
	ref := in.Clone()
	Normalize(in, Podcast, NormalizeOptions{})
	for i := range in.Samples {
		if in.Samples[i] != ref.Samples[i] {
			t.Fatal("input modified")
		}
	}
}

func TestNormalizeNoLimiterAndChecks(t *testing.T) {
	// A crest-heavy signal can't reach −16 LUFS under −1.5 dBTP without
	// limiting: NoLimiter stops at the ceiling and the check fails.
	b := dsptest.Silence(48000, 1, 10)
	for i := range 10 {
		dsptest.Click(b, float64(i)+0.5, 0.9)
	}
	dsptest.Add(b, dsptest.Sine(48000, 1, 300, -40, 10, 0), 0)
	_, r := Normalize(b, Podcast, NormalizeOptions{NoLimiter: true})
	if r.After.TruePeak > Podcast.TruePeak+0.05 || r.Compliant {
		t.Errorf("no limiter: %+v", r)
	}
	// Noise floor check: hiss at −50 dB RMS fails ACX's −60.
	noisy := speechLike(44100, 1, -6)
	dsptest.Add(noisy, dsptest.Noise(44100, 1, -45, noisy.Seconds(), 2), 0)
	_, r = Normalize(noisy, ACX, NormalizeOptions{})
	var nf *Check
	for i := range r.Checks {
		if r.Checks[i].Code == "noise_floor" {
			nf = &r.Checks[i]
		}
	}
	if nf == nil || nf.OK || r.Compliant {
		t.Errorf("noise floor check: %+v", r.Checks)
	}
	// Silent input: unchanged, "silent" check.
	_, r = Normalize(dsptest.Silence(24000, 1, 1), ACX, NormalizeOptions{})
	if r.GainDB != 0 || r.Checks[0].Code != "silent" || r.Compliant {
		t.Errorf("silent: %+v", r)
	}
	if _, err := json.Marshal(r); err != nil {
		t.Error(err)
	}
	// Verify does not change anything.
	v := Verify(speechLike(24000, 2, -20), Broadcast)
	if v.Before != v.After || v.GainDB != 0 {
		t.Error("verify")
	}
}

func TestStreamTwoPass(t *testing.T) {
	in := speechLike(48000, 2, -3)
	m := NewMeter(48000, 2, Options{})
	m.Write(in.Samples)
	gain, limit := Plan(m.Stats(), Podcast)
	s := NewStream(48000, 2, gain, Podcast.TruePeak, limit)
	var out []float32
	for p := in.Samples; len(p) > 0; {
		k := min(len(p), 2*4096)
		out = s.Process(out, p[:k])
		p = p[k:]
	}
	out = s.Flush(out)
	st := Measure(dsp.Buffer{Rate: 48000, Channels: 2, Samples: out})
	if len(out) != len(in.Samples) || st.TruePeak > Podcast.TruePeak+0.05 || math.Abs(st.Integrated-Podcast.Integrated) > 0.5 {
		t.Errorf("two-pass: %+v (limit %v)", st, limit)
	}
}

func TestNoiseFloor(t *testing.T) {
	b := dsptest.Concat(dsptest.Noise(48000, 1, -20, 2, 1), dsptest.Noise(48000, 1, -65, 1, 2), dsptest.Noise(48000, 1, -20, 2, 3))
	if nf := NoiseFloor(b, 0.5); math.Abs(nf+65) > 0.5 {
		t.Errorf("noise floor %.2f", nf)
	}
	if !math.IsInf(NoiseFloor(dsptest.Silence(48000, 2, 1), 0.5), -1) {
		t.Error("silence")
	}
	if r := RMS(dsptest.Sine(48000, 2, 1000, 0, 1, 0)); math.Abs(r+3.01) > 0.01 {
		t.Errorf("rms %v", r)
	}
}

func BenchmarkMeter48kStereo(b *testing.B) {
	x := dsptest.Noise(48000, 2, -20, 10, 1)
	b.SetBytes(int64(len(x.Samples) * 4))
	for b.Loop() {
		m := NewMeter(48000, 2, Options{})
		m.Write(x.Samples)
		m.Stats()
	}
}

func BenchmarkMeterNoTruePeak(b *testing.B) {
	x := dsptest.Noise(48000, 2, -20, 10, 1)
	b.SetBytes(int64(len(x.Samples) * 4))
	for b.Loop() {
		m := NewMeter(48000, 2, Options{SkipTruePeak: true})
		m.Write(x.Samples)
		m.Stats()
	}
}

func BenchmarkNormalize24kMono(b *testing.B) {
	x := speechLike(24000, 1, -3)
	b.SetBytes(int64(len(x.Samples) * 4))
	for b.Loop() {
		Normalize(x, ACX, NormalizeOptions{})
	}
}

func BenchmarkMeterSpeech24kMono(b *testing.B) {
	x := speechLike(24000, 1, -3)
	b.SetBytes(int64(len(x.Samples) * 4))
	for b.Loop() {
		m := NewMeter(24000, 1, Options{})
		m.Write(x.Samples)
		m.Stats()
	}
}
