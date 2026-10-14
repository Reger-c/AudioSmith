package stretch

import (
	"math"
	"testing"

	"github.com/hehehai/AudioSmith/internal/dsp"
)

func sine(rate int, hz, seconds float64, ch int) dsp.Buffer {
	n := int(float64(rate) * seconds)
	b := dsp.New(rate, ch, n)
	for i := range n {
		v := float32(0.5 * math.Sin(2*math.Pi*hz*float64(i)/float64(rate)))
		for c := range ch {
			b.Samples[i*ch+c] = v
		}
	}
	return b
}

// zeroCrossHz estimates a tone's frequency from its zero crossings, away
// from the edges.
func zeroCrossHz(b dsp.Buffer) float64 {
	m := mixdown(b)
	lo, hi := len(m)/10, len(m)*9/10
	n := 0
	for i := lo + 1; i < hi; i++ {
		if (m[i-1] < 0) != (m[i] < 0) {
			n++
		}
	}
	return float64(n) / 2 / (float64(hi-lo) / float64(b.Rate))
}

func rms(s []float32) float64 {
	var e float64
	for _, v := range s {
		e += float64(v) * float64(v)
	}
	return math.Sqrt(e / float64(max(len(s), 1)))
}

func TestPitchKept(t *testing.T) {
	for _, rate := range []int{16000, 24000, 48000} {
		for _, ratio := range []float64{0.67, 0.8, 1.25, 1.5} {
			in := sine(rate, 220, 2, 1)
			out := Stretch(in, ratio, Options{})
			want := int(math.Round(float64(in.Frames()) * ratio))
			if out.Frames() != want {
				t.Fatalf("rate %d ratio %v: %d frames, want %d", rate, ratio, out.Frames(), want)
			}
			if hz := zeroCrossHz(out); math.Abs(hz-220) > 3 {
				t.Errorf("rate %d ratio %v: %.1f Hz, want 220", rate, ratio, hz)
			}
			// No dropouts or level jumps in the middle.
			mid := out.Samples[out.Frames()/10 : out.Frames()*9/10]
			if r := rms(mid); math.Abs(r-0.5/math.Sqrt2) > 0.03 {
				t.Errorf("rate %d ratio %v: rms %.3f", rate, ratio, r)
			}
		}
	}
}

func TestStereoAndExactLength(t *testing.T) {
	in := sine(24000, 300, 1.3, 2)
	out := ToLength(in, 20000, Options{})
	if out.Frames() != 20000 || out.Channels != 2 {
		t.Fatalf("%d frames, %d channels", out.Frames(), out.Channels)
	}
	for i := 0; i < out.Frames(); i += 997 {
		if out.Samples[2*i] != out.Samples[2*i+1] {
			t.Fatal("channels differ")
		}
	}
	if same := ToLength(in, in.Frames(), Options{}); same.Frames() != in.Frames() || same.Samples[100] != in.Samples[100] {
		t.Error("ratio 1 changed the audio")
	}
	if e := ToLength(dsp.Buffer{Rate: 24000, Channels: 1}, 10, Options{}); e.Frames() != 10 {
		t.Error("empty input")
	}
}

// Bursts (syllables) keep their order and their count when squeezed.
func TestBurstsKeepTheirShape(t *testing.T) {
	rate := 24000
	b := dsp.New(rate, 1, rate*2)
	for k := range 4 { // 4 bursts of 150 ms, every 500 ms
		start := k*rate/2 + rate/10
		for i := range rate * 15 / 100 {
			b.Samples[start+i] = float32(0.5 * math.Sin(2*math.Pi*180*float64(i)/float64(rate)))
		}
	}
	out := Stretch(b, 0.7, Options{})
	hop := rate / 100
	bursts, in := 0, false
	for i := 0; i+hop <= out.Frames(); i += hop {
		loud := rms(out.Samples[i:i+hop]) > 0.1
		if loud && !in {
			bursts++
		}
		in = loud
	}
	if bursts != 4 {
		t.Errorf("%d bursts after stretching, want 4", bursts)
	}
}

func BenchmarkStretch5s(b *testing.B) {
	in := sine(24000, 180, 5, 1)
	for b.Loop() {
		Stretch(in, 0.8, Options{})
	}
}
