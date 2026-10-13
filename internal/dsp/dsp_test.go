package dsp_test

import (
	"math"
	"testing"

	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/dsptest"
)

func TestBuffer(t *testing.T) {
	b := dsptest.Sine(48000, 2, 1000, 0, 0.5, 0)
	if b.Frames() != 24000 || b.Seconds() != 0.5 || b.Check() != nil {
		t.Fatalf("frames %d seconds %v", b.Frames(), b.Seconds())
	}
	if s := b.Slice(-5, 100); s.Frames() != 100 || &s.Samples[0] != &b.Samples[0] {
		t.Error("slice")
	}
	if s := b.Slice(23990, 99999); s.Frames() != 10 {
		t.Error("slice clamp")
	}
	if (dsp.Buffer{Rate: 1, Channels: 2, Samples: make([]float32, 3)}).Check() == nil {
		t.Error("odd sample count must fail Check")
	}
	if math.Abs(dsp.DB(dsp.Amp(-6))+6) > 1e-12 || dsp.FramesAt(0.05, 44100) != 2205 {
		t.Error("conversions")
	}
}

func TestRemix(t *testing.T) {
	st := dsp.Buffer{Rate: 8000, Channels: 2, Samples: []float32{1, 0, 0.5, 0.5, -1, 1}}
	m := dsp.Remix(st, 1)
	if m.Channels != 1 || len(m.Samples) != 3 || m.Samples[0] != 0.5 || m.Samples[1] != 0.5 || m.Samples[2] != 0 {
		t.Errorf("down-mix %v", m.Samples)
	}
	up := dsp.Remix(m, 2)
	if up.Channels != 2 || up.Samples[0] != 0.5 || up.Samples[1] != 0.5 {
		t.Errorf("up-mix %v", up.Samples)
	}
	five := dsp.Remix(st, 6)
	if five.Channels != 6 || five.Samples[0] != 1 || five.Samples[2] != 0 || five.Samples[6] != 0.5 {
		t.Errorf("2→6 %v", five.Samples[:12])
	}
	if dsp.Remix(st, 2).Samples[0] != st.Samples[0] {
		t.Error("same count must return the input")
	}
	d := dsp.Deinterleave(st.Samples, 2)
	if got := dsp.Interleave(d); len(got) != 6 || got[5] != 1 {
		t.Error("interleave round trip")
	}
}

func TestFades(t *testing.T) {
	for _, c := range []dsp.Curve{dsp.EqualPower, dsp.Linear} {
		b := dsp.Buffer{Rate: 100, Channels: 1, Samples: []float32{1, 1, 1, 1, 1, 1, 1, 1, 1, 1}}
		dsp.FadeIn(b, 4, c)
		dsp.FadeOut(b, 4, c)
		if b.Samples[0] >= b.Samples[1] || b.Samples[3] >= 1 || b.Samples[4] != 1 || b.Samples[9] >= b.Samples[8] {
			t.Errorf("curve %d: %v", c, b.Samples)
		}
		// In and out gains of a crossfade: power sums to 1 for EqualPower.
		x := 0.3
		gi, go_ := c.FadeGain(x), c.FadeGain(1-x)
		if c == dsp.EqualPower && math.Abs(gi*gi+go_*go_-1) > 1e-12 {
			t.Error("equal power")
		}
		if c == dsp.Linear && math.Abs(gi+go_-1) > 1e-12 {
			t.Error("linear")
		}
	}
	if dsp.Peak([]float32{0.1, -0.7, 0.3}) != 0.7 {
		t.Error("peak")
	}
	if r := dsp.RMS(dsptest.Sine(48000, 1, 1000, 0, 1, 0).Samples); math.Abs(r-math.Sqrt(0.5)) > 1e-4 {
		t.Errorf("rms %v", r)
	}
}
