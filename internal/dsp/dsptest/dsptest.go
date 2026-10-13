// Package dsptest makes synthetic test signals (sines, noise, silence,
// clicks, sequences) for the dsp packages' tests and benchmarks.
package dsptest

import (
	"math"
	"math/rand/v2"

	"github.com/hehehai/AudioSmith/internal/dsp"
)

// Sine returns seconds of a sine of freq Hz at peak level dBFS (amplitude
// 10^(dBFS/20)), the same on every channel, starting at phase (radians).
func Sine(rate, channels int, freq, dBFS, seconds, phase float64) dsp.Buffer {
	n := dsp.FramesAt(seconds, rate)
	b := dsp.New(rate, channels, n)
	a := dsp.Amp(dBFS)
	w := 2 * math.Pi * freq / float64(rate)
	for i := range n {
		v := float32(a * math.Sin(w*float64(i)+phase))
		for c := range channels {
			b.Samples[i*channels+c] = v
		}
	}
	return b
}

// Noise returns seconds of uniform white noise with RMS level dBFS (each
// channel independent), from a fixed seed.
func Noise(rate, channels int, dBFS, seconds float64, seed uint64) dsp.Buffer {
	n := dsp.FramesAt(seconds, rate)
	b := dsp.New(rate, channels, n)
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	// Uniform in [-a, a] has RMS a/√3.
	a := dsp.Amp(dBFS) * math.Sqrt(3)
	for i := range b.Samples {
		b.Samples[i] = float32(a * (2*r.Float64() - 1))
	}
	return b
}

// Silence returns seconds of digital silence.
func Silence(rate, channels int, seconds float64) dsp.Buffer {
	return dsp.New(rate, channels, dsp.FramesAt(seconds, rate))
}

// Click sets a one-sample impulse of amplitude amp at second at, on every channel.
func Click(b dsp.Buffer, at float64, amp float32) {
	i := dsp.FramesAt(at, b.Rate)
	if i >= 0 && i < b.Frames() {
		for c := range b.Channels {
			b.Samples[i*b.Channels+c] = amp
		}
	}
}

// Concat joins buffers of the same format end to end.
func Concat(parts ...dsp.Buffer) dsp.Buffer {
	out := dsp.Buffer{Rate: parts[0].Rate, Channels: parts[0].Channels}
	for _, p := range parts {
		out.Samples = append(out.Samples, p.Samples...)
	}
	return out
}

// Add mixes src into dst in place starting at frame at.
func Add(dst, src dsp.Buffer, at int) {
	for i := range src.Frames() {
		j := at + i
		if j < 0 || j >= dst.Frames() {
			continue
		}
		for c := range dst.Channels {
			dst.Samples[j*dst.Channels+c] += src.Samples[i*src.Channels+min(c, src.Channels-1)]
		}
	}
}

// Channels builds a buffer whose channel c is chans[c] (all the same format).
func Channels(chans ...dsp.Buffer) dsp.Buffer {
	cs := make([][]float32, len(chans))
	for i, b := range chans {
		cs[i] = b.Channel(0)
	}
	return dsp.Buffer{Rate: chans[0].Rate, Channels: len(chans), Samples: dsp.Interleave(cs)}
}

// Syllables returns a speech-like signal: n bursts of a harmonic tone
// (fundamental f0, 3 harmonics) shaped by a raised-cosine envelope of length
// burst seconds, one every period seconds, at peak level dBFS. It starts with
// lead seconds of silence.
func Syllables(rate int, n int, f0, burst, period, lead, dBFS float64) dsp.Buffer {
	total := lead + float64(n)*period
	b := dsp.New(rate, 1, dsp.FramesAt(total, rate))
	a := dsp.Amp(dBFS) / 1.75
	bl := dsp.FramesAt(burst, rate)
	for k := range n {
		start := dsp.FramesAt(lead+float64(k)*period, rate)
		for i := range bl {
			j := start + i
			if j >= b.Frames() {
				break
			}
			env := 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(bl))
			t := float64(j) / float64(rate)
			v := math.Sin(2*math.Pi*f0*t) + 0.5*math.Sin(2*math.Pi*2*f0*t) + 0.25*math.Sin(2*math.Pi*3*f0*t)
			b.Samples[j] = float32(a * env * v)
		}
	}
	return b
}
