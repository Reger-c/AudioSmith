package resample

import (
	"fmt"
	"math"
	"testing"

	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/dsptest"
)

// errDB compares got with an ideal sine (amplitude a, freq f, at got's rate)
// away from the edges and returns the error level relative to a, in dB.
func errDB(got dsp.Buffer, f, a float64) float64 {
	n := got.Frames()
	lo, hi := n/10, n-n/10
	var e float64
	for i := lo; i < hi; i++ {
		want := a * math.Sin(2*math.Pi*f*float64(i)/float64(got.Rate))
		for c := range got.Channels {
			d := float64(got.Samples[i*got.Channels+c]) - want
			e += d * d
		}
	}
	e /= float64((hi - lo) * got.Channels)
	return dsp.PowerDB(e / (a * a / 2))
}

func TestSineAccuracy(t *testing.T) {
	cases := []struct {
		in, out int
		f       float64
		q       Quality
		maxDB   float64 // error floor relative to the tone
	}{
		{24000, 44100, 1000, Good, -85},
		{24000, 48000, 8000, Good, -85},
		{44100, 48000, 5000, Good, -85},
		{48000, 44100, 15000, Good, -85},
		{48000, 24000, 9000, Good, -85},
		{44100, 24000, 440, Good, -85},
		{16000, 48000, 3000, Good, -85},
		{24000, 48000, 8000, Best, -100},
		{24000, 48000, 1000, Fast, -55},
		{44100, 44101, 2000, Good, -80}, // interpolated table
		{48000, 8000, 1000, Good, -85},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%d-%d-%v-q%d", c.in, c.out, c.f, c.q), func(t *testing.T) {
			src := dsptest.Sine(c.in, 2, c.f, -1, 0.5, 0)
			got := Buffer(src, c.out, c.q)
			if want := (int64(src.Frames())*int64(c.out) + int64(c.in) - 1) / int64(c.in); int64(got.Frames()) != want {
				t.Errorf("frames %d, want %d", got.Frames(), want)
			}
			e := errDB(got, c.f, dsp.Amp(-1))
			t.Logf("error %.1f dB", e)
			if e > c.maxDB {
				t.Errorf("error %.1f dB, want ≤ %v", e, c.maxDB)
			}
		})
	}
}

func TestAliasRejection(t *testing.T) {
	// Tones above the new Nyquist must vanish.
	for _, c := range []struct {
		in, out int
		f       float64
	}{{48000, 24000, 12500}, {48000, 24000, 18000}, {44100, 22050, 11500}, {48000, 44100, 22200}} {
		got := Buffer(dsptest.Sine(c.in, 1, c.f, 0, 0.5, 0), c.out, Good)
		mid := got.Slice(got.Frames()/10, got.Frames()*9/10)
		l := dsp.DB(dsp.RMS(mid.Samples) * math.Sqrt2)
		t.Logf("%d→%d %v Hz: %.1f dB", c.in, c.out, c.f, l)
		if l > -80 {
			t.Errorf("%d→%d %v Hz: %.1f dB left", c.in, c.out, c.f, l)
		}
	}
}

func TestStreamingMatchesOneShot(t *testing.T) {
	src := dsptest.Noise(24000, 2, -10, 0.7, 9)
	want := Buffer(src, 44100, Good)
	r := New(24000, 44100, 2, Good)
	var got []float32
	for s, k := src.Samples, 1; len(s) > 0; k = k*3 + 1 {
		n := min(len(s), 2*(k%2000))
		got = r.Process(got, s[:n])
		s = s[n:]
	}
	got = r.Flush(got)
	if len(got) != len(want.Samples) {
		t.Fatalf("%d samples, want %d", len(got), len(want.Samples))
	}
	for i := range got {
		if got[i] != want.Samples[i] {
			t.Fatalf("sample %d: %v vs %v", i, got[i], want.Samples[i])
		}
	}
	// Reuse after Reset.
	r.Reset()
	again := r.Flush(r.Process(nil, src.Samples))
	if len(again) != len(got) || again[1000] != got[1000] {
		t.Error("reset")
	}
}

func TestDCAndIdentity(t *testing.T) {
	b := dsp.New(22050, 1, 22050)
	for i := range b.Samples {
		b.Samples[i] = 0.5
	}
	got := Buffer(b, 48000, Good)
	mid := got.Slice(2000, got.Frames()-2000)
	for _, v := range mid.Samples {
		if math.Abs(float64(v)-0.5) > 1e-5 {
			t.Fatalf("DC %v", v)
		}
	}
	if same := Buffer(b, 22050, Good); &same.Samples[0] != &b.Samples[0] {
		t.Error("same rate must not copy")
	}
	r := New(48000, 48000, 2, Good)
	if out := r.Flush(r.Process(nil, []float32{1, 2, 3, 4})); len(out) != 4 {
		t.Error("identity stream")
	}
	if got := Buffer(dsp.Buffer{Rate: 24000, Channels: 1}, 48000, Good); got.Frames() != 0 {
		t.Error("empty")
	}
}

func TestConvert(t *testing.T) {
	st := dsptest.Sine(24000, 2, 440, -6, 0.2, 0)
	m := Convert(st, 48000, 1, Good)
	if m.Channels != 1 || m.Rate != 48000 || m.Frames() != 9600 {
		t.Errorf("%+v frames %d", m.Rate, m.Frames())
	}
	s := Convert(m, 44100, 2, Fast)
	if s.Channels != 2 || s.Rate != 44100 {
		t.Error("up-mix")
	}
}

func benchmark(b *testing.B, in, out int, q Quality) {
	src := dsptest.Noise(in, 2, -10, 10, 1)
	b.SetBytes(int64(len(src.Samples) * 4))
	for b.Loop() {
		Buffer(src, out, q)
	}
}

func BenchmarkGood24kTo48k(b *testing.B)  { benchmark(b, 24000, 48000, Good) }
func BenchmarkGood24kTo44k1(b *testing.B) { benchmark(b, 24000, 44100, Good) }
func BenchmarkGood48kTo44k1(b *testing.B) { benchmark(b, 48000, 44100, Good) }
func BenchmarkFast24kTo48k(b *testing.B)  { benchmark(b, 24000, 48000, Fast) }
func BenchmarkBest44k1To48k(b *testing.B) { benchmark(b, 44100, 48000, Best) }
