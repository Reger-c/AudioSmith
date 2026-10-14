// Package stretch changes the length of audio without changing its pitch,
// by WSOLA (waveform-similarity overlap-add): the output is built from
// Hann-windowed frames of the input taken every synthesis hop, each one
// picked within a small search window around where the tempo puts it so
// that it continues the previous frame's waveform best (the highest
// normalised cross-correlation with the natural continuation). Frames
// overlap by half, where Hann windows sum to one.
//
// For speech, ratios between about 0.67 and 1.5 sound natural (the
// dubbing pipeline caps them there); beyond, words smear or stutter.
// Channels share the frames chosen on their mix, so stereo images stay.
//
// Stdlib only. A 5 s line at 24 kHz takes about 30 ms on one core.
package stretch

import (
	"math"

	"github.com/hehehai/AudioSmith/internal/dsp"
)

// Options tune the stretch; zero values take the defaults.
type Options struct {
	// Frame is the frame length, seconds (default 0.03: long enough for
	// two periods of a low voice).
	Frame float64
	// Search is how far either side of its nominal place a frame may be
	// taken from, seconds (default 0.012).
	Search float64
}

func (o Options) norm() Options {
	if o.Frame <= 0 {
		o.Frame = 0.03
	}
	if o.Search <= 0 {
		o.Search = 0.012
	}
	return o
}

// Limits of the ratio Stretch accepts; it clamps to them.
const (
	MinRatio = 0.25
	MaxRatio = 4
)

// Stretch returns b made ratio times as long (ratio < 1: faster), at the
// same pitch. Its length is round(frames × ratio). A ratio within 0.1 %
// of 1 returns a copy.
func Stretch(b dsp.Buffer, ratio float64, o Options) dsp.Buffer {
	ratio = min(max(ratio, MinRatio), MaxRatio)
	return ToLength(b, int(math.Round(float64(b.Frames())*ratio)), o)
}

// ToLength returns b stretched (or squeezed) to exactly frames frames.
func ToLength(b dsp.Buffer, frames int, o Options) dsp.Buffer {
	n := b.Frames()
	ch := max(b.Channels, 1)
	if frames <= 0 || n == 0 {
		return dsp.Buffer{Rate: b.Rate, Channels: ch, Samples: make([]float32, max(frames, 0)*ch)}
	}
	if abs(frames-n) <= n/1000 {
		return fit(b.Clone(), frames)
	}
	o = o.norm()
	win := max(int(o.Frame*float64(b.Rate)), 64)
	win -= win % 2
	hs := win / 2 // synthesis hop
	search := max(int(o.Search*float64(b.Rate)), 1)
	tempo := float64(n) / float64(frames) // input frames per output frame

	mono := mixdown(b)
	w := hann(win)
	outLen := frames + win
	out := make([]float32, outLen*ch)
	norm := make([]float32, outLen) // window sum, for the edges

	prev := 0 // input position of the previous frame
	for k := 0; k*hs < frames; k++ {
		nominal := int(math.Round(float64(k*hs) * tempo))
		pos := nominal
		if k > 0 {
			// The frame that would continue the previous one seamlessly.
			natural := prev + hs
			pos = bestOffset(mono, natural, nominal, search, win, hs)
		}
		pos = min(max(pos, 0), max(n-1, 0))
		at := k * hs
		for i := range win {
			src := pos + i
			if src >= n {
				break
			}
			g := w[i]
			norm[at+i] += g
			for c := range ch {
				out[(at+i)*ch+c] += b.Samples[src*ch+c] * g
			}
		}
		prev = pos
	}
	// Where fewer than two windows overlap (the first half frame, the end
	// of the input), divide by the window sum so the edges do not fade.
	for i := range outLen {
		if s := norm[i]; s > 0.1 && (s < 0.999 || s > 1.001) {
			for c := range ch {
				out[i*ch+c] /= s
			}
		}
	}
	return fit(dsp.Buffer{Rate: b.Rate, Channels: ch, Samples: out}, frames)
}

// bestOffset finds the input position near nominal whose frame best
// continues the previous one, whose natural continuation starts at
// natural: the highest normalised cross-correlation over the overlap.
func bestOffset(x []float32, natural, nominal, search, win, overlap int) int {
	n := len(x)
	lo, hi := nominal-search, nominal+search
	if lo < 0 {
		lo = 0
	}
	if hi+overlap > n {
		hi = n - overlap
	}
	if natural+overlap > n || hi < lo {
		return min(max(nominal, 0), max(n-1, 0))
	}
	ref := x[natural : natural+overlap]
	var refE float64
	for _, v := range ref {
		refE += float64(v) * float64(v)
	}
	if refE < 1e-9 {
		return nominal // silence: any place will do
	}
	best, bestScore := nominal, math.Inf(-1)
	// Coarse search every other sample, then refine around the best.
	score := func(p int) float64 {
		var dot, e float64
		seg := x[p : p+overlap]
		for i := 0; i < overlap; i += 2 {
			a, b := float64(ref[i]), float64(seg[i])
			dot += a * b
			e += b * b
		}
		if e < 1e-12 {
			return math.Inf(-1)
		}
		return dot / math.Sqrt(e)
	}
	for p := lo; p <= hi; p += 2 {
		if s := score(p); s > bestScore {
			best, bestScore = p, s
		}
	}
	for _, p := range []int{best - 1, best + 1} {
		if p >= lo && p <= hi {
			if s := score(p); s > bestScore {
				best, bestScore = p, s
			}
		}
	}
	return best
}

// fit truncates or zero-pads b to frames.
func fit(b dsp.Buffer, frames int) dsp.Buffer {
	ch := max(b.Channels, 1)
	want := frames * ch
	switch {
	case len(b.Samples) > want:
		b.Samples = b.Samples[:want]
	case len(b.Samples) < want:
		b.Samples = append(b.Samples, make([]float32, want-len(b.Samples))...)
	}
	return b
}

func mixdown(b dsp.Buffer) []float32 {
	ch := max(b.Channels, 1)
	if ch == 1 {
		return b.Samples
	}
	out := make([]float32, b.Frames())
	for i := range out {
		var s float32
		for c := range ch {
			s += b.Samples[i*ch+c]
		}
		out[i] = s / float32(ch)
	}
	return out
}

func hann(n int) []float32 {
	w := make([]float32, n)
	for i := range n {
		w[i] = float32(0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n)))
	}
	return w
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
