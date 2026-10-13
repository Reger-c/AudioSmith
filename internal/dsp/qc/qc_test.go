package qc

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"

	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/dsptest"
)

const rate = 24000

// speech is a speech-like signal: syllables every 250 ms after a short lead.
func speech(seconds, dBFS float64) dsp.Buffer {
	n := int(seconds / 0.25)
	s := dsptest.Syllables(rate, n, 150, 0.18, 0.25, 0, dBFS)
	dsptest.Add(s, dsptest.Noise(rate, 1, -80, s.Seconds(), 5), 0)
	return s
}

func sil(sec float64) dsp.Buffer { return dsptest.Silence(rate, 1, sec) }

func codes(r Report) map[string][]Issue {
	m := map[string][]Issue{}
	for _, is := range r.Issues {
		m[is.Code] = append(m[is.Code], is)
	}
	return m
}

func TestCheck(t *testing.T) {
	type want struct {
		code       string
		start, end float64 // expected range, ±tol
		tol        float64
	}
	nan := speech(3, -6)
	nan.Samples[rate] = float32(math.NaN())
	nan.Samples[rate+5] = float32(math.Inf(1))
	clipped := speech(4, -6)
	// The syllable at 2.0 s is driven 12 dB into the rails.
	for i := 2 * rate; i < 2*rate+rate*18/100; i++ {
		clipped.Samples[i] = min(max(clipped.Samples[i]*4, -1), 1)
	}
	dc := speech(3, -10)
	for i := range dc.Samples {
		dc.Samples[i] += 0.05
	}
	click := dsptest.Sine(rate, 1, 220, -12, 3, 0)
	click.Samples[rate] += 0.6

	cases := []struct {
		name string
		b    dsp.Buffer
		want []want
		ok   bool
	}{
		{"clean speech", speech(6, -6), nil, true},
		{"clean sine", dsptest.Sine(rate, 1, 440, -12, 3, 0), nil, true},
		{"clean noise", dsptest.Noise(rate, 1, -20, 3, 2), nil, true},
		{"empty", dsp.Buffer{Rate: rate, Channels: 1}, []want{{"empty", 0, 0, 0}}, false},
		{"silence", sil(2), []want{{"silent", 0, 2, 0}}, false},
		{"nan", nan, []want{{"invalid_samples", 1, 1, 0.001}}, false},
		{"clipping", clipped, []want{{"clipping", 2.05, 2.13, 0.03}}, false},
		{"silence gap", dsptest.Concat(speech(2, -6), sil(3), speech(2, -6)), []want{{"silence_gap", 2, 5, 0.1}}, false},
		{"lead silence", dsptest.Concat(sil(1.5), speech(3, -6)), []want{{"edge_silence", 0, 1.5, 0.02}}, true},
		{"tail silence", dsptest.Concat(speech(3, -6), sil(1.2)), []want{{"edge_silence", 3, 4.2, 0.1}}, true},
		{"dc offset", dc, []want{{"dc_offset", 0, 3, 0}}, false},
		{"low level", speech(4, -40), []want{{"low_level", 0, 4, 0}}, false},
		{"jump up", dsptest.Concat(speech(6, -24), speech(6, -6)), []want{{"loudness_jump", 4.5, 7.5, 1.6}}, false},
		{"jump down", dsptest.Concat(speech(6, -6), speech(6, -22)), []want{{"loudness_jump", 4.5, 7.5, 1.6}}, false},
		{"click", click, []want{{"click", 1, 1, 0.001}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Check(c.b, Options{})
			got := codes(r)
			for _, w := range c.want {
				is := got[w.code]
				if len(is) != 1 {
					t.Errorf("%s: %d issues %+v", w.code, len(is), r.Issues)
					continue
				}
				if math.Abs(is[0].Start-w.start) > w.tol || math.Abs(is[0].End-w.end) > w.tol+0.011 {
					t.Errorf("%s: range [%.3f, %.3f), want [%v, %v) ±%v", w.code, is[0].Start, is[0].End, w.start, w.end, w.tol)
				}
				delete(got, w.code)
			}
			for code, is := range got {
				t.Errorf("unexpected %s: %+v", code, is)
			}
			if r.OK != c.ok {
				t.Errorf("OK = %v", r.OK)
			}
			if _, err := json.Marshal(r); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestJumpSign(t *testing.T) {
	up := codes(Check(dsptest.Concat(speech(6, -24), speech(6, -6)), Options{}))["loudness_jump"]
	down := codes(Check(dsptest.Concat(speech(6, -6), speech(6, -22)), Options{}))["loudness_jump"]
	if len(up) != 1 || up[0].Value < 14 || len(down) != 1 || down[0].Value > -12 {
		t.Errorf("up %+v down %+v", up, down)
	}
	// A smaller step stays under the default 8 LU.
	if j := codes(Check(dsptest.Concat(speech(6, -12), speech(6, -6)), Options{}))["loudness_jump"]; len(j) != 0 {
		t.Errorf("6 dB step flagged: %+v", j)
	}
	// …unless the threshold is lowered.
	if j := codes(Check(dsptest.Concat(speech(6, -12), speech(6, -6)), Options{JumpLU: 4}))["loudness_jump"]; len(j) != 1 {
		t.Errorf("JumpLU 4: %+v", j)
	}
}

func TestStereoAndStreaming(t *testing.T) {
	l := speech(4, -6)
	r := dsptest.Silence(rate, 1, l.Seconds())
	for i := range r.Samples {
		r.Samples[i] = 0.03 // DC on the right channel only
	}
	st := dsptest.Channels(l, r)
	st.Samples[2*rate*2+1] = float32(math.NaN())
	want := Check(st, Options{})
	c := NewChecker(rate, 2, Options{})
	for s := st.Samples; len(s) > 0; {
		k := min(len(s), 2*777)
		c.Write(s[:k])
		s = s[k:]
	}
	got := c.Report()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("streaming differs:\n%+v\n%+v", got, want)
	}
	dc := codes(want)["dc_offset"]
	if len(dc) != 1 || dc[0].Channel != 1 {
		t.Errorf("dc %+v", dc)
	}
	if len(codes(want)["invalid_samples"]) != 1 {
		t.Error("nan in stereo")
	}
}

func TestTruncation(t *testing.T) {
	b := speech(8, -6)
	for i := range 300 {
		b.Samples[i*rate/40] = float32(math.NaN())
	}
	r := Check(b, Options{MaxIssues: 50})
	if n := len(codes(r)["invalid_samples"]); n != 50 || !r.Truncated {
		t.Errorf("%d issues, truncated %v", n, r.Truncated)
	}
}

func BenchmarkCheck(b *testing.B) {
	x := speech(30, -6)
	b.SetBytes(int64(len(x.Samples) * 4))
	for b.Loop() {
		Check(x, Options{})
	}
}
