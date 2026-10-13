package dynamics

import (
	"math"
	"testing"

	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/dsptest"
	"github.com/hehehai/AudioSmith/internal/dsp/filter"
)

func TestCompressorCurve(t *testing.T) {
	c := NewCompressor(48000, 1, CompressorParams{ThresholdDB: -20, Ratio: 4, MakeupDB: 2})
	for _, tc := range []struct{ in, want float64 }{{-30, 2}, {-20, 2}, {-10, 2 - 7.5}, {0, 2 - 15}} {
		if g := c.StaticGainDB(tc.in); math.Abs(g-tc.want) > 1e-9 {
			t.Errorf("hard knee %v dB: gain %v, want %v", tc.in, g, tc.want)
		}
	}
	k := NewCompressor(48000, 1, CompressorParams{ThresholdDB: -20, Ratio: 4, KneeDB: 6})
	// Soft knee: continuous, 0 below the knee, matching the hard curve above it.
	if k.StaticGainDB(-23.01) != 0 || math.Abs(k.StaticGainDB(-16.99)-c.StaticGainDB(-16.99)+2) > 0.01 {
		t.Error("knee edges")
	}
	prev := 0.0
	for x := -30.0; x < 0; x += 0.1 {
		g := k.StaticGainDB(x)
		if g > prev+1e-12 || g < prev-0.08 {
			t.Fatalf("knee not smooth at %.1f: %v after %v", x, g, prev)
		}
		prev = g
	}
}

func TestCompressorSteadyState(t *testing.T) {
	for _, ch := range []int{1, 2} {
		x := dsptest.Sine(48000, ch, 1000, -6, 2, 0)
		c := NewCompressor(48000, ch, CompressorParams{ThresholdDB: -20, Ratio: 4, AttackMs: 5, ReleaseMs: 100, RMSMs: 10})
		c.Process(x.Samples)
		tail := x.Slice(48000, 96000)
		got := dsp.DB(float64(dsp.Peak(tail.Samples)))
		if want := -20 + 14.0/4; math.Abs(got-want) > 0.3 {
			t.Errorf("%d ch: output %.2f dBFS, want %.2f", ch, got, want)
		}
		if c.MaxReductionDB() > -10 {
			t.Errorf("reduction %v", c.MaxReductionDB())
		}
	}
	// Below threshold: untouched apart from makeup.
	q := dsptest.Sine(48000, 1, 440, -30, 0.5, 0)
	ref := q.Clone()
	NewCompressor(48000, 1, CompressorParams{ThresholdDB: -20, Ratio: 4, AttackMs: 5, ReleaseMs: 50, RMSMs: 10, MakeupDB: 6}).Process(q.Samples)
	for i := range q.Samples {
		if math.Abs(float64(q.Samples[i])-dsp.Amp(6)*float64(ref.Samples[i])) > 1e-3*math.Abs(float64(ref.Samples[i]))+1e-7 {
			t.Fatalf("sample %d: %v vs %v", i, q.Samples[i], ref.Samples[i])
		}
	}
}

func TestCompressorAttackRelease(t *testing.T) {
	// A step from −40 to −6 dBFS: reduction builds over ~attack, recovers over ~release.
	rate := 48000
	x := dsptest.Concat(dsptest.Sine(rate, 1, 1000, -40, 0.5, 0), dsptest.Sine(rate, 1, 1000, -6, 0.5, 0), dsptest.Sine(rate, 1, 1000, -40, 1, 0))
	c := NewCompressor(rate, 1, CompressorParams{ThresholdDB: -20, Ratio: 10, AttackMs: 20, ReleaseMs: 200})
	c.Process(x.Samples)
	level := func(at float64) float64 {
		s := x.Slice(dsp.FramesAt(at, rate), dsp.FramesAt(at+0.002, rate))
		return dsp.DB(float64(dsp.Peak(s.Samples)))
	}
	if l := level(0.501); l < -8 {
		t.Errorf("attack too fast: %.1f dB 1 ms after the step", l)
	}
	if l := level(0.9); l > -17 {
		t.Errorf("not compressed after 400 ms: %.1f", l)
	}
	if l := level(1.02); l > -50 {
		t.Errorf("release too fast: %.1f dB at 20 ms after (want still reduced)", l)
	}
	if l := level(1.95); l < -40.5 {
		t.Errorf("not released after 950 ms: %.1f", l)
	}
}

func TestLimiterSamplePeak(t *testing.T) {
	for _, ch := range []int{1, 2} {
		x := dsptest.Noise(44100, ch, -6, 1, 5) // peaks up to ~−1.2 dBFS
		dsp.Gain(x.Samples, 2)                  // and now well over 0 dBFS
		in := x.Clone()
		l := NewLimiter(44100, ch, LimiterParams{CeilingDB: -1})
		out := l.Buffer(x.Samples)
		if len(out) != len(in.Samples) {
			t.Fatalf("len %d, want %d", len(out), len(in.Samples))
		}
		ceil := float32(dsp.Amp(-1))
		if p := dsp.Peak(out); p > ceil {
			t.Errorf("%d ch: peak %v over ceiling %v", ch, p, ceil)
		}
		// Gain only goes down, and output follows input (no time shift).
		for i := range out {
			if math.Abs(float64(out[i])) > math.Abs(float64(in.Samples[i]))+1e-6 || out[i]*in.Samples[i] < 0 {
				t.Fatalf("sample %d: out %v, in %v", i, out[i], in.Samples[i])
			}
		}
	}
	// Quiet audio passes bit-exactly.
	q := dsptest.Sine(48000, 2, 300, -12, 0.3, 0)
	out := NewLimiter(48000, 2, LimiterParams{CeilingDB: -1}).Buffer(q.Samples)
	for i := range out {
		if out[i] != q.Samples[i] {
			t.Fatalf("quiet sample %d changed", i)
		}
	}
}

func TestLimiterTruePeak(t *testing.T) {
	for _, rate := range []int{24000, 44100, 48000} {
		x := dsptest.Noise(rate, 2, -3, 1, 6)
		dsptest.Add(x, dsptest.Sine(rate, 2, float64(rate)/4, 0, 1, math.Pi/4), 0)
		l := NewLimiter(rate, 2, LimiterParams{CeilingDB: -1.5, TruePeak: true})
		out := l.Buffer(x.Samples)
		if len(out) != len(x.Samples) {
			t.Fatalf("len")
		}
		tp := filter.NewTruePeak(rate, 2)
		p := tp.Flush(tp.Process(nil, out))
		got := dsp.DB(float64(dsp.Peak(p)))
		t.Logf("%d Hz: output true peak %.2f dBTP (ceiling −1.5), reduction %.1f dB", rate, got, l.MaxReductionDB())
		if got > -1.5+0.1 {
			t.Errorf("%d: true peak %.2f over ceiling", rate, got)
		}
	}
}

func TestLimiterStreaming(t *testing.T) {
	x := dsptest.Noise(48000, 2, 0, 0.5, 8)
	want := NewLimiter(48000, 2, LimiterParams{CeilingDB: -2, TruePeak: true}).Buffer(x.Clone().Samples)
	l := NewLimiter(48000, 2, LimiterParams{CeilingDB: -2, TruePeak: true})
	var got []float32
	for s := x.Samples; len(s) > 0; {
		k := min(len(s), 2*517)
		got = l.Process(got, s[:k])
		s = s[k:]
	}
	got = l.Flush(got)
	if len(got) != len(want) {
		t.Fatalf("len %d vs %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("sample %d differs", i)
		}
	}
	if NewLimiter(48000, 2, LimiterParams{}).Latency() != 240 {
		t.Error("default look-ahead 5 ms")
	}
}

func BenchmarkCompressor(b *testing.B) {
	x := dsptest.Noise(48000, 2, -10, 10, 1)
	c := NewCompressor(48000, 2, CompressorParams{ThresholdDB: -20, Ratio: 3, AttackMs: 10, ReleaseMs: 100, RMSMs: 10})
	b.SetBytes(int64(len(x.Samples) * 4))
	for b.Loop() {
		c.Process(x.Samples)
	}
}

func BenchmarkLimiterTruePeak(b *testing.B) {
	x := dsptest.Noise(48000, 2, -3, 10, 1)
	l := NewLimiter(48000, 2, LimiterParams{CeilingDB: -1, TruePeak: true})
	b.SetBytes(int64(len(x.Samples) * 4))
	var out []float32
	for b.Loop() {
		out = l.Process(out[:0], x.Samples)
	}
}
