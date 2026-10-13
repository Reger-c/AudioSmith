package master

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/dsptest"
	"github.com/hehehai/AudioSmith/internal/dsp/loudness"
)

func speech(rate, ch int, dBFS float64) dsp.Buffer {
	s := dsptest.Syllables(rate, 40, 160, 0.18, 0.25, 0.3, dBFS)
	dsptest.Add(s, dsptest.Noise(rate, 1, -90, s.Seconds(), 3), 0)
	return dsp.Remix(s, ch)
}

func TestRawIsIdentity(t *testing.T) {
	in := speech(24000, 1, -12)
	out, r, err := Apply(in, Raw, nil)
	if err != nil || r.Loudness != nil {
		t.Fatal(err, r)
	}
	for i := range in.Samples {
		if in.Samples[i] != out.Samples[i] {
			t.Fatal("raw changed audio")
		}
	}
}

func TestPresetsHitTargets(t *testing.T) {
	for _, p := range Presets[1:] {
		t.Run(p.ID, func(t *testing.T) {
			t.Parallel()
			for _, c := range []struct{ rate, ch int }{{24000, 1}, {48000, 2}, {16000, 1}} {
				in := speech(c.rate, c.ch, -25)
				ref := in.Clone()
				out, r, err := Apply(in, p, nil)
				if err != nil {
					t.Fatal(err)
				}
				if in.Samples[100] != ref.Samples[100] {
					t.Fatal("input modified")
				}
				st := r.Loudness.After
				if math.Abs(st.Integrated-p.Loudness) > 0.2 || st.TruePeak > p.TruePeak+0.05 || out.Frames() != in.Frames() {
					t.Errorf("%d/%d: I %.2f (want %v), TP %.2f (≤ %v)", c.rate, c.ch, st.Integrated, p.Loudness, st.TruePeak, p.TruePeak)
				}
			}
		})
	}
	// An explicit target replaces the preset's own.
	_, r, _ := Apply(speech(24000, 1, -10), Podcast, &loudness.ACX)
	if r.Loudness.Target != "acx" || math.Abs(r.Loudness.After.Integrated+19) > 0.2 {
		t.Errorf("override: %+v", r.Loudness)
	}
}

// response measures the steady-state gain (dB) of p's filters at f Hz.
func response(t *testing.T, p Preset, rate int, f float64) float64 {
	p.Compressor, p.Loudness = nil, 0
	pr, err := NewProcessor(p, rate, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	x := dsptest.Sine(rate, 1, f, -20, 2, 0)
	pr.Process(x.Samples)
	tail := x.Slice(rate, 2*rate)
	return dsp.DB(dsp.RMS(tail.Samples)*math.Sqrt2) + 20
}

func TestTone(t *testing.T) {
	const r = 48000
	for _, p := range Presets[1:] {
		if g := response(t, p, r, 25); g > -5 {
			t.Errorf("%s: 25 Hz rumble only %.1f dB down", p.ID, g)
		}
		if g := response(t, p, r, 1000); math.Abs(g) > 1.5 {
			t.Errorf("%s: 1 kHz moved %.1f dB", p.ID, g)
		}
	}
	tilt := func(p Preset) float64 { return response(t, p, r, 10000) - response(t, p, r, 180) }
	if w, b := tilt(Warm), tilt(Bright); w > -2.5 || b < 2.5 {
		t.Errorf("tilt (10 kHz − 180 Hz): warm %.1f dB, bright %.1f dB", w, b)
	}
	if c := response(t, Cinematic, r, 100) - response(t, Cinematic, r, 1000); c < 1.5 {
		t.Errorf("cinematic low end +%.1f dB", c)
	}
}

func TestCompressionNarrowsRange(t *testing.T) {
	var parts []dsp.Buffer
	for i := range 6 {
		lvl := -26.0
		if i%2 == 1 {
			lvl = -14
		}
		parts = append(parts, dsptest.Syllables(24000, 20, 150, 0.18, 0.25, 0, lvl))
	}
	in := dsptest.Concat(parts...)
	m := loudness.NewMeter(24000, 1, loudness.Options{SkipTruePeak: true})
	m.Write(in.Samples)
	before, _, _ := m.Range()
	for _, p := range []Preset{Broadcast, Podcast} {
		out, _, _ := Apply(in, p, nil)
		m := loudness.NewMeter(24000, 1, loudness.Options{SkipTruePeak: true})
		m.Write(out.Samples)
		after, _, _ := m.Range()
		t.Logf("%s: LRA %.1f → %.1f LU", p.ID, before, after)
		if after > before-2.5 {
			t.Errorf("%s: LRA %.1f → %.1f", p.ID, before, after)
		}
	}
}

func TestLookupAndErrors(t *testing.T) {
	if p, err := Lookup("warm"); err != nil || p.ID != "warm" {
		t.Error(err)
	}
	if _, err := Lookup("loud"); err == nil {
		t.Error("unknown preset")
	}
	bad := Preset{ID: "x", EQ: []Band{{"notch", 1000, 1, 3}}}
	if _, _, err := Apply(speech(24000, 1, -20), bad, nil); err == nil {
		t.Error("bad band type")
	}
	if _, _, err := Apply(dsp.Buffer{}, Broadcast, nil); err == nil {
		t.Error("bad buffer")
	}
	if _, err := json.Marshal(Presets); err != nil {
		t.Error(err)
	}
	// Silence passes through without error.
	out, r, err := Apply(dsptest.Silence(24000, 1, 1), Broadcast, nil)
	if err != nil || dsp.Peak(out.Samples) != 0 || r.Loudness.Compliant {
		t.Errorf("silence: %v %+v", err, r.Loudness)
	}
}

func BenchmarkBroadcast24kMono(b *testing.B) {
	x := speech(24000, 1, -20)
	b.SetBytes(int64(len(x.Samples) * 4))
	for b.Loop() {
		Apply(x, Broadcast, nil)
	}
}
