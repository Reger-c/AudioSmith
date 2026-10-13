// Package dsp is AudioSmith's pure-Go audio signal processing: the
// interleaved float32 Buffer every subpackage works on, plus small helpers
// (decibels, channel up/down-mix, gains, fades, ranges).
//
// Subpackages:
//
//	wav       WAV read/write (16/24-bit PCM, 32-bit float), streaming writer, atomic files
//	resample  polyphase windowed-sinc sample-rate conversion (streaming)
//	filter    biquads (RBJ), K-weighting, windowed-sinc FIR design
//	dynamics  compressor and look-ahead (true-)peak limiter
//	loudness  ITU-R BS.1770-4 / EBU R128 meter, true peak, normalisation presets
//	master    mastering chain presets (raw, broadcast, podcast, warm, bright, cinematic)
//	edit      concatenation with crossfades, silence, trimming, gaps; streaming joiner
//	qc        "Check audio" quality report with codes, severities and time ranges
//	analysis  onsets, YIN pitch, loudness contour, speaking rate, energy VAD, prosody
//	mix       timeline mixer (per-track gain/offset) and ducking
//
// Conventions: samples are float32 in [-1, 1], interleaved one per channel per
// frame (the same layout as mygo/audio.Decoder). Any sample rate and any
// channel count are accepted unless a function says otherwise; mono and
// stereo are what is tested. Nothing here does UI strings: results carry
// codes and numbers. No cgo, stdlib only.
//
// Imports: nothing internal.
package dsp

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// Buffer is interleaved PCM audio.
type Buffer struct {
	Rate     int       // frames per second
	Channels int       // samples per frame
	Samples  []float32 // interleaved, len = Frames()*Channels
}

// New returns a silent buffer of frames frames.
func New(rate, channels, frames int) Buffer {
	return Buffer{Rate: rate, Channels: channels, Samples: make([]float32, max(frames, 0)*channels)}
}

// ErrFormat reports an invalid rate, channel count or sample count.
var ErrFormat = errors.New("dsp: invalid audio format")

// Check reports whether b is well formed.
func (b Buffer) Check() error {
	if b.Rate <= 0 || b.Channels <= 0 || len(b.Samples)%b.Channels != 0 {
		return fmt.Errorf("%w: rate %d, channels %d, samples %d", ErrFormat, b.Rate, b.Channels, len(b.Samples))
	}
	return nil
}

// Frames returns the number of frames.
func (b Buffer) Frames() int {
	if b.Channels <= 0 {
		return 0
	}
	return len(b.Samples) / b.Channels
}

// Seconds returns the duration in seconds.
func (b Buffer) Seconds() float64 {
	if b.Rate <= 0 {
		return 0
	}
	return float64(b.Frames()) / float64(b.Rate)
}

// Duration returns the duration.
func (b Buffer) Duration() time.Duration {
	return time.Duration(b.Seconds() * float64(time.Second))
}

// Clone returns a deep copy.
func (b Buffer) Clone() Buffer {
	b.Samples = append([]float32(nil), b.Samples...)
	return b
}

// Slice returns frames [from, to), clamped, sharing b's samples.
func (b Buffer) Slice(from, to int) Buffer {
	n := b.Frames()
	from = min(max(from, 0), n)
	to = min(max(to, from), n)
	b.Samples = b.Samples[from*b.Channels : to*b.Channels]
	return b
}

// Channel returns a copy of channel c.
func (b Buffer) Channel(c int) []float32 {
	out := make([]float32, b.Frames())
	for i := range out {
		out[i] = b.Samples[i*b.Channels+c]
	}
	return out
}

// Deinterleave splits interleaved samples into one slice per channel.
func Deinterleave(samples []float32, channels int) [][]float32 {
	n := len(samples) / channels
	out := make([][]float32, channels)
	for c := range out {
		out[c] = make([]float32, n)
	}
	for i := range n {
		for c := range channels {
			out[c][i] = samples[i*channels+c]
		}
	}
	return out
}

// Interleave joins per-channel slices (of equal length) into interleaved samples.
func Interleave(chans [][]float32) []float32 {
	if len(chans) == 0 {
		return nil
	}
	n := len(chans[0])
	out := make([]float32, n*len(chans))
	for c, ch := range chans {
		for i, v := range ch[:n] {
			out[i*len(chans)+c] = v
		}
	}
	return out
}

// FramesAt converts seconds to frames at rate, rounding to nearest.
func FramesAt(seconds float64, rate int) int {
	return int(math.Round(seconds * float64(rate)))
}

// SecondsAt converts frames to seconds at rate.
func SecondsAt(frames int, rate int) float64 { return float64(frames) / float64(rate) }

// DB converts an amplitude ratio to decibels (−Inf for 0).
func DB(amp float64) float64 { return 20 * math.Log10(amp) }

// Amp converts decibels to an amplitude ratio.
func Amp(db float64) float64 { return math.Pow(10, db/20) }

// PowerDB converts a power (mean square) ratio to decibels.
func PowerDB(p float64) float64 { return 10 * math.Log10(p) }

// Range is a time range in seconds, [Start, End).
type Range struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// Len returns End − Start.
func (r Range) Len() float64 { return r.End - r.Start }

// Remix converts b to channels channels:
//   - mono → n: the mono signal is copied to every channel;
//   - n → mono: the channels are averaged ((L+R)/2 for stereo);
//   - other: channel i takes source channel i, extra channels are silent and
//     dropped channels are lost (callers wanting a fold-down use mono).
//
// It returns b itself when the count already matches.
func Remix(b Buffer, channels int) Buffer {
	if channels <= 0 || channels == b.Channels {
		return b
	}
	n := b.Frames()
	out := New(b.Rate, channels, n)
	switch {
	case b.Channels == 1:
		for i := range n {
			v := b.Samples[i]
			for c := range channels {
				out.Samples[i*channels+c] = v
			}
		}
	case channels == 1:
		inv := 1 / float32(b.Channels)
		for i := range n {
			var s float32
			for _, v := range b.Samples[i*b.Channels : (i+1)*b.Channels] {
				s += v
			}
			out.Samples[i] = s * inv
		}
	default:
		k := min(channels, b.Channels)
		for i := range n {
			copy(out.Samples[i*channels:i*channels+k], b.Samples[i*b.Channels:i*b.Channels+k])
		}
	}
	return out
}

// Gain multiplies samples by g in place.
func Gain(samples []float32, g float32) {
	if g == 1 {
		return
	}
	for i := range samples {
		samples[i] *= g
	}
}

// Curve is a fade shape.
type Curve int

const (
	// EqualPower fades with sin/cos quarter waves: the summed power of two
	// uncorrelated signals stays constant across a crossfade.
	EqualPower Curve = iota
	// Linear fades linearly: constant summed amplitude for correlated signals.
	Linear
)

// FadeGain returns the fade-in gain at position x in [0, 1].
func (c Curve) FadeGain(x float64) float64 {
	x = min(max(x, 0), 1)
	if c == Linear {
		return x
	}
	return math.Sin(x * math.Pi / 2)
}

// FadeIn ramps the first frames frames of b up from silence, in place.
func FadeIn(b Buffer, frames int, c Curve) {
	frames = min(frames, b.Frames())
	for i := range frames {
		g := float32(c.FadeGain((float64(i) + 0.5) / float64(frames)))
		for ch := range b.Channels {
			b.Samples[i*b.Channels+ch] *= g
		}
	}
}

// FadeOut ramps the last frames frames of b down to silence, in place.
func FadeOut(b Buffer, frames int, c Curve) {
	n := b.Frames()
	frames = min(frames, n)
	for i := range frames {
		g := float32(c.FadeGain((float64(i) + 0.5) / float64(frames)))
		j := n - 1 - i
		for ch := range b.Channels {
			b.Samples[j*b.Channels+ch] *= g
		}
	}
}

// Peak returns the largest absolute sample value (NaN samples ignored).
func Peak(samples []float32) float32 {
	var m float32
	for _, v := range samples {
		if v < 0 {
			v = -v
		}
		if v > m {
			m = v
		}
	}
	return m
}

// RMS returns the root mean square of samples (all channels together).
func RMS(samples []float32) float64 {
	if len(samples) == 0 {
		return 0
	}
	var s float64
	for _, v := range samples {
		s += float64(v) * float64(v)
	}
	return math.Sqrt(s / float64(len(samples)))
}
