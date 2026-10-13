package filter

import (
	"math"
	"testing"

	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/dsptest"
)

func TestKWeighting48k(t *testing.T) {
	// ITU-R BS.1770-4, Tables 1 and 2.
	shelf, hp := KWeighting(48000)
	want := []float64{1.53512485958697, -2.69169618940638, 1.19839281085285, -1.69065929318241, 0.73248077421585}
	got := []float64{shelf.B0, shelf.B1, shelf.B2, shelf.A1, shelf.A2}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-12 {
			t.Errorf("shelf[%d] = %.15f, want %.14f", i, got[i], want[i])
		}
	}
	want = []float64{1, -2, 1, -1.99004745483398, 0.99007225036621}
	got = []float64{hp.B0, hp.B1, hp.B2, hp.A1, hp.A2}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-12 {
			t.Errorf("highpass[%d] = %.15f, want %.14f", i, got[i], want[i])
		}
	}
	// At every rate the curve has the same shape: ~+0.69 dB at 1 kHz,
	// ~+4 dB at high frequencies, steep low cut.
	for _, rate := range []int{16000, 22050, 24000, 44100, 48000, 96000} {
		s, h := KWeighting(rate)
		g1k := s.GainDB(rate, 1000) + h.GainDB(rate, 1000)
		g6k := s.GainDB(rate, 6000) + h.GainDB(rate, 6000)
		g20 := s.GainDB(rate, 20) + h.GainDB(rate, 20)
		// Bilinear warping bends the shelf slightly at low rates: +0.05 dB at
		// 1 kHz at 16 kHz, well inside Tech 3341's ±0.1 LU.
		if math.Abs(g1k-0.691) > 0.05 || math.Abs(g6k-3.95) > 0.25 || g20 > -10 {
			t.Errorf("%d Hz: %.3f dB @1k, %.2f @6k, %.1f @20", rate, g1k, g6k, g20)
		}
	}
}

func TestDesigns(t *testing.T) {
	const r = 48000
	cases := []struct {
		name string
		b    Biquad
		f    float64
		want float64
	}{
		{"hp at f0", HighPass(r, 100, math.Sqrt2/2), 100, -3.01},
		{"hp pass", HighPass(r, 100, math.Sqrt2/2), 5000, 0},
		{"hp stop", HighPass(r, 100, math.Sqrt2/2), 25, -24.1},
		{"lp at f0", LowPass(r, 8000, math.Sqrt2/2), 8000, -3.01},
		{"peak", Peaking(r, 3000, 1, 4), 3000, 4},
		{"peak off", Peaking(r, 3000, 1, 4), 100, 0},
		{"lowshelf", LowShelf(r, 200, math.Sqrt2/2, 3), 20, 3},
		{"highshelf", HighShelf(r, 8000, math.Sqrt2/2, -2), 20000, -2},
		{"bandpass", BandPass(r, 1000, 2), 1000, 0},
	}
	for _, c := range cases {
		if g := c.b.GainDB(r, c.f); math.Abs(g-c.want) > 0.15 {
			t.Errorf("%s: %.2f dB, want %.2f", c.name, g, c.want)
		}
	}
}

func TestChainMatchesResponse(t *testing.T) {
	hp := HighPass(48000, 200, 0.7071)
	for _, f := range []float64{50, 200, 1000} {
		x := dsptest.Sine(48000, 2, f, 0, 2, 0)
		c := NewChain(2, hp)
		c.Process(x.Samples)
		tail := x.Slice(48000, 96000)
		got := dsp.DB(dsp.RMS(tail.Samples) * math.Sqrt2)
		if want := hp.GainDB(48000, f); math.Abs(got-want) > 0.05 {
			t.Errorf("%v Hz: measured %.3f dB, response %.3f", f, got, want)
		}
	}
	// Process64 equals Process.
	a := dsptest.Noise(44100, 2, -10, 0.1, 2)
	b := make([]float64, len(a.Samples))
	for i, v := range a.Samples {
		b[i] = float64(v)
	}
	c1, c2 := NewChain(2, hp, Peaking(44100, 1000, 1, 3)), NewChain(2, hp, Peaking(44100, 1000, 1, 3))
	c1.Process(a.Samples)
	c2.Process64(b)
	for i := range b {
		if math.Abs(b[i]-float64(a.Samples[i])) > 1e-6 {
			t.Fatalf("sample %d differs", i)
		}
	}
}

func TestLowpassFIR(t *testing.T) {
	h := LowpassFIR(101, 0.1, KaiserBeta(80), 1)
	resp := func(f float64) float64 {
		var re, im float64
		for i, v := range h {
			re += v * math.Cos(2*math.Pi*f*float64(i))
			im -= v * math.Sin(2*math.Pi*f*float64(i))
		}
		return 20 * math.Log10(math.Hypot(re, im))
	}
	if g := resp(0); math.Abs(g) > 1e-9 {
		t.Errorf("DC %.3g dB", g)
	}
	if g := resp(0.1); math.Abs(g+6.02) > 0.3 {
		t.Errorf("cutoff %.2f dB, want −6", g)
	}
	if g := resp(0.05); math.Abs(g) > 0.01 {
		t.Errorf("passband %.4f dB", g)
	}
	for _, f := range []float64{0.16, 0.25, 0.4, 0.5} {
		if g := resp(f); g > -75 {
			t.Errorf("stopband %.2f: %.1f dB", f, g)
		}
	}
}

func BenchmarkKWeighting(b *testing.B) {
	x := dsptest.Noise(48000, 2, -20, 10, 1)
	s, h := KWeighting(48000)
	c := NewChain(2, s, h)
	b.SetBytes(int64(len(x.Samples) * 4))
	for b.Loop() {
		c.Process(x.Samples)
	}
}
