package mix

import (
	"math"
	"testing"

	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/dsptest"
)

func impulse(rate, ch, frames, at int, v float32) dsp.Buffer {
	b := dsp.New(rate, ch, frames)
	for c := range ch {
		b.Samples[at*ch+c] = v
	}
	return b
}

func TestSampleAccuratePlacement(t *testing.T) {
	const r = 48000
	tl := Timeline{Rate: r, Channels: 2, Tracks: []Track{
		{Name: "a", Clips: []Clip{{Audio: impulse(r, 2, 100, 0, 0.5), Offset: 12345}}},
		{Name: "b", GainDB: -6, Clips: []Clip{{Audio: impulse(r, 1, 100, 10, 0.5), Offset: 12335}}}, // lands on the same frame
		{Name: "muted", Mute: true, Clips: []Clip{{Audio: impulse(r, 2, 10, 0, 1), Offset: 0}}},
		{Name: "sliced", Clips: []Clip{{Audio: impulse(r, 1, 1000, 500, 0.25), Offset: 20000, From: 400, To: 600}}}, // impulse at 20100
		{Name: "early", Clips: []Clip{{Audio: impulse(r, 1, 100, 50, 0.1), Offset: -40}}},                           // impulse at 10
	}}
	out, err := Mix(tl)
	if err != nil {
		t.Fatal(err)
	}
	if out.Frames() != 20200 {
		t.Errorf("length %d", out.Frames())
	}
	want := map[int]float32{12345: 0.5 + 0.5*float32(dsp.Amp(-6)), 20100: 0.25, 10: 0.1}
	for i := range out.Frames() {
		for c := range 2 {
			v := out.Samples[i*2+c]
			if w, ok := want[i]; ok {
				if math.Abs(float64(v-w)) > 1e-6 {
					t.Errorf("frame %d ch %d = %v, want %v", i, c, v, w)
				}
			} else if v != 0 {
				t.Fatalf("frame %d ch %d = %v, want 0", i, c, v)
			}
		}
	}
}

func TestClipProcessing(t *testing.T) {
	const r = 48000
	tone := dsptest.Sine(24000, 1, 1000, -6, 1, 0) // other rate: resampled
	tl := Timeline{Rate: r, Channels: 2, Tracks: []Track{{Clips: []Clip{
		{Audio: tone, Offset: 0, Pan: -1, FadeIn: 0.1, FadeOut: 0.1, GainDB: -6},
	}}}}
	out, err := Mix(tl)
	if err != nil {
		t.Fatal(err)
	}
	if out.Frames() != 48000 {
		t.Fatalf("frames %d", out.Frames())
	}
	l, rr := dsp.Remix(out, 2).Channel(0), dsp.Remix(out, 2).Channel(1)
	mid := l[20000:28000]
	if g := dsp.DB(dsp.RMS(mid) * math.Sqrt2); math.Abs(g+12) > 0.05 {
		t.Errorf("left level %.2f dB, want −12", g)
	}
	if dsp.Peak(rr) != 0 {
		t.Error("hard left leaked right")
	}
	if math.Abs(float64(l[1])) > 1e-3 || math.Abs(float64(l[len(l)-1])) > 1e-3 {
		t.Error("fades")
	}
	// The resampled tone is still 1 kHz: 2000 zero crossings per second.
	zc := 0
	for i := 1; i < len(mid); i++ {
		if (mid[i-1] < 0) != (mid[i] < 0) {
			zc++
		}
	}
	if math.Abs(float64(zc)-2000*float64(len(mid))/r) > 2 {
		t.Errorf("zero crossings %d", zc)
	}
	if _, err := Mix(Timeline{}); err == nil {
		t.Error("bad format")
	}
	if _, err := Mix(Timeline{Rate: r, Channels: 1, Tracks: []Track{{Clips: []Clip{{Audio: dsp.Buffer{}}}}}}); err == nil {
		t.Error("bad clip")
	}
}

func TestRenderBlocksMatchesMix(t *testing.T) {
	const r = 24000
	var clips []Clip
	for i := range 50 {
		clips = append(clips, Clip{Audio: dsptest.Noise(r, 1, -20, 0.3+0.01*float64(i%5), uint64(i)), Offset: i * 5000, GainDB: float64(-i % 7)})
	}
	tl := Timeline{Rate: r, Channels: 2, Tracks: []Track{
		{Name: "dialogue", Clips: clips},
		{Name: "music", Clips: []Clip{{Audio: dsptest.Sine(r, 2, 220, -10, 12, 0)}}, Duck: &Duck{Keys: []int{0}}},
	}}
	whole, _ := Mix(tl)
	m, _ := NewMixer(tl)
	blk := make([]float32, 2*1001)
	for from := 0; from < m.Length(); from += 1001 {
		m.Render(blk, from)
		for i := range blk {
			j := from*2 + i
			if j < len(whole.Samples) && blk[i] != whole.Samples[j] {
				t.Fatalf("frame %d differs", j/2)
			}
		}
	}
}

func TestDucking(t *testing.T) {
	const r = 24000
	bg := dsptest.Sine(r, 1, 220, -6, 10, 0)
	speech := func(at, sec float64) Clip {
		return Clip{Audio: dsptest.Noise(r, 1, -20, sec, 1), Offset: dsp.FramesAt(at, r)}
	}
	tl := Timeline{Rate: r, Channels: 1, Tracks: []Track{
		{Name: "music", Clips: []Clip{{Audio: bg}}, Duck: &Duck{Keys: []int{1}, DepthDB: -15}},
		{Name: "voice", Clips: []Clip{speech(2, 2), speech(4.3, 1), speech(7, 1)}}, // 2–4 and 4.3–5.3 merge
	}}
	m, _ := NewMixer(tl)
	env := m.ducks[0]
	gain := func(at float64) float64 { return dsp.DB(env.At(dsp.FramesAt(at, r))) }
	checks := []struct{ at, want, tol float64 }{
		{0.5, 0, 0.01}, {1.84, 0, 0.01}, // before the attack ramp (0.15 s)
		{1.925, -4.6, 0.05},                                 // mid-ramp (raised cosine: half the amplitude change)
		{2.0, -15, 0.01}, {3, -15, 0.01}, {4.15, -15, 0.01}, // merged with 4.3–5.3: no recovery
		{5.3, -15, 0.01}, {5.75, 0, 0.01}, // released after 0.4 s
		{6.925, -4.6, 0.05}, {7.5, -15, 0.01}, {9, 0, 0.01},
	}
	for _, c := range checks {
		if g := gain(c.at); math.Abs(g-c.want) > c.tol {
			t.Errorf("gain at %.3f s = %.2f dB, want %.1f", c.at, g, c.want)
		}
	}
	if len(env.Regions()) != 2 {
		t.Errorf("regions %v", env.Regions())
	}
	// Sidechain on buffers.
	key := dsptest.Concat(dsptest.Silence(r, 1, 2), dsptest.Noise(r, 1, -20, 2, 3), dsptest.Silence(r, 1, 6))
	d := Sidechain(bg, key, 0, Duck{})
	at := func(b dsp.Buffer, s float64) float64 {
		x := b.Slice(dsp.FramesAt(s, r), dsp.FramesAt(s+0.1, r))
		return dsp.DB(dsp.RMS(x.Samples) * math.Sqrt2)
	}
	if l := at(d, 3); math.Abs(l+18) > 0.2 {
		t.Errorf("ducked level %.2f, want −18", l)
	}
	if l := at(d, 8); math.Abs(l+6) > 0.1 {
		t.Errorf("released level %.2f, want −6", l)
	}
}

func BenchmarkRenderDialogue(b *testing.B) {
	const r = 48000
	var clips []Clip
	for i := range 200 {
		clips = append(clips, Clip{Audio: dsptest.Noise(r, 1, -20, 2, uint64(i)), Offset: i * r * 3})
	}
	tl := Timeline{Rate: r, Channels: 2, Tracks: []Track{
		{Clips: clips},
		{Clips: []Clip{{Audio: dsptest.Noise(r, 2, -30, 600, 1)}}, Duck: &Duck{Keys: []int{0}}},
	}}
	m, _ := NewMixer(tl)
	blk := make([]float32, 2*4800)
	b.SetBytes(int64(m.Length() * 2 * 4))
	for b.Loop() {
		for from := 0; from < m.Length(); from += 4800 {
			m.Render(blk, from)
		}
	}
}
