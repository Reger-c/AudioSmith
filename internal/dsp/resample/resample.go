// Package resample converts sample rates with a polyphase Kaiser-windowed
// sinc filter. It streams: feed any chunk sizes to Process, then Flush.
// Output is delay-compensated (output frame k is the input at k·in/out
// seconds) and has exactly ceil(inFrames·out/in) frames.
//
// For rate ratios L/M (reduced) with L ≤ 2048 — every pair of common rates,
// e.g. 24k→44.1k is 147/80 — each output phase has its own exact taps.
// Larger L use a 1024-phase table with linear interpolation between phases.
package resample

import (
	"math"

	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/filter"
)

// Quality trades filter length for transition width and attenuation.
// Edges are fractions of the lower Nyquist frequency.
type Quality int

const (
	// Good (default): 96 taps per phase at up-sampling, ~90 dB stopband,
	// flat to 0.88·Nyquist, stopband from 1.0·Nyquist.
	Good Quality = iota
	// Fast: 24 taps, ~60 dB, flat to 0.70·Nyquist; previews and analysis.
	Fast
	// Best: 256 taps, ~110 dB, flat to 0.94·Nyquist; final exports.
	Best
)

type design struct {
	half   int     // taps per side at ratio ≥ 1
	atten  float64 // dB
	cutoff float64 // −6 dB point as a fraction of the lower Nyquist
}

func (q Quality) design() design {
	switch q {
	case Fast:
		return design{12, 60, 0.85}
	case Best:
		return design{128, 110, 0.97}
	}
	return design{48, 90, 0.94}
}

const (
	maxExactPhases = 2048
	tablePhases    = 1024
)

// Resampler converts interleaved audio from one rate to another.
type Resampler struct {
	inRate, outRate int
	l, m            int64 // out/in = l/m, reduced
	ch              int
	half            int       // taps per side
	taps            int       // 2·half
	table           []float32 // rows of taps: l rows (exact) or tablePhases+1 rows
	exact           bool
	buf             [][]float32 // per channel, frames from base on
	base            int64       // absolute input frame of buf[c][0]
	n0              int64       // input frame at or before the next output
	p               int64       // phase numerator of the next output, in [0, l)
	nIn, nOut       int64
	flushed         bool
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// New returns a resampler from inRate to outRate for channels channels.
func New(inRate, outRate, channels int, q Quality) *Resampler {
	g := gcd(int64(inRate), int64(outRate))
	r := &Resampler{inRate: inRate, outRate: outRate, l: int64(outRate) / g, m: int64(inRate) / g, ch: channels}
	if r.l == r.m {
		return r
	}
	d := q.design()
	ratio := min(1, float64(r.l)/float64(r.m))
	fc := d.cutoff * ratio // cutoff relative to the input Nyquist
	r.half = int(math.Ceil(float64(d.half) / ratio))
	r.taps = 2 * r.half
	beta := filter.KaiserBeta(d.atten)
	rows := int(r.l)
	r.exact = r.l <= maxExactPhases
	if !r.exact {
		rows = tablePhases + 1
	}
	r.table = make([]float32, rows*r.taps)
	row := make([]float64, r.taps)
	for p := range rows {
		frac := float64(p) / float64(r.l)
		if !r.exact {
			frac = float64(p) / tablePhases
		}
		var sum float64
		for i := range r.taps {
			// Tap i multiplies input frame n0 + d, d = i − half + 1, which
			// lies x input frames from the output instant n0 + frac.
			x := float64(i-r.half+1) - frac
			row[i] = fc * filter.Sinc(fc*x) * filter.Kaiser(x/float64(r.half), beta)
			sum += row[i]
		}
		for i := range r.taps {
			// Unity gain at DC for every phase.
			r.table[p*r.taps+i] = float32(row[i] / sum)
		}
	}
	r.Reset()
	return r
}

// Reset forgets all input, ready for a new stream.
func (r *Resampler) Reset() {
	r.buf = make([][]float32, r.ch)
	for c := range r.buf {
		r.buf[c] = make([]float32, max(r.half-1, 0), 4096)
	}
	r.base = -int64(max(r.half-1, 0))
	r.n0, r.p, r.nIn, r.nOut, r.flushed = 0, 0, 0, 0, false
}

// Rates returns the input and output rates.
func (r *Resampler) Rates() (in, out int) { return r.inRate, r.outRate }

// OutFrames returns how many frames inFrames input frames become.
func (r *Resampler) OutFrames(inFrames int64) int64 {
	return (inFrames*r.l + r.m - 1) / r.m
}

// Process appends to dst the output that in (interleaved, whole frames)
// makes available and returns it. Up to about half a filter length of
// output waits for later input or Flush.
func (r *Resampler) Process(dst, in []float32) []float32 {
	if r.l == r.m {
		r.nIn += int64(len(in) / r.ch)
		r.nOut = r.nIn
		return append(dst, in...)
	}
	frames := len(in) / r.ch
	for c := range r.ch {
		b := r.buf[c]
		for i := range frames {
			b = append(b, in[i*r.ch+c])
		}
		r.buf[c] = b
	}
	r.nIn += int64(frames)
	return r.run(dst, -1)
}

// Flush appends the remaining output, so that the stream has exactly
// OutFrames(total input) frames, and returns it. Call Reset to reuse.
func (r *Resampler) Flush(dst []float32) []float32 {
	if r.flushed || r.l == r.m {
		r.flushed = true
		return dst
	}
	r.flushed = true
	for c := range r.ch {
		r.buf[c] = append(r.buf[c], make([]float32, r.half+1)...)
	}
	return r.run(dst, r.OutFrames(r.nIn))
}

func (r *Resampler) run(dst []float32, limit int64) []float32 {
	have := r.base + int64(len(r.buf[0]))
	for r.n0+int64(r.half) < have && (limit < 0 || r.nOut < limit) {
		start := int(r.n0 - int64(r.half) + 1 - r.base)
		if r.exact {
			row := r.table[int(r.p)*r.taps : (int(r.p)+1)*r.taps]
			for c := range r.ch {
				dst = append(dst, dot(r.buf[c][start:start+r.taps], row))
			}
		} else {
			pf := float64(r.p) / float64(r.l) * tablePhases
			i0 := int(pf)
			f := float32(pf - float64(i0))
			row0 := r.table[i0*r.taps : (i0+1)*r.taps]
			row1 := r.table[(i0+1)*r.taps : (i0+2)*r.taps]
			for c := range r.ch {
				x := r.buf[c][start : start+r.taps]
				dst = append(dst, (1-f)*dot(x, row0)+f*dot(x, row1))
			}
		}
		r.nOut++
		r.p += r.m
		r.n0 += r.p / r.l
		r.p %= r.l
	}
	// Drop input no future output needs.
	if drop := int(r.n0 - int64(r.half) + 1 - r.base); drop > 8192 {
		for c := range r.ch {
			n := copy(r.buf[c], r.buf[c][drop:])
			r.buf[c] = r.buf[c][:n]
		}
		r.base += int64(drop)
	}
	return dst
}

func dot(x, h []float32) float32 {
	h = h[:len(x)]
	var s0, s1, s2, s3 float32
	i := 0
	for ; i+4 <= len(x); i += 4 {
		s0 += x[i] * h[i]
		s1 += x[i+1] * h[i+1]
		s2 += x[i+2] * h[i+2]
		s3 += x[i+3] * h[i+3]
	}
	for ; i < len(x); i++ {
		s0 += x[i] * h[i]
	}
	return (s0 + s1) + (s2 + s3)
}

// Buffer resamples a whole buffer to rate.
func Buffer(b dsp.Buffer, rate int, q Quality) dsp.Buffer {
	if b.Rate == rate {
		return b
	}
	r := New(b.Rate, rate, b.Channels, q)
	out := make([]float32, 0, int(r.OutFrames(int64(b.Frames())))*b.Channels)
	out = r.Process(out, b.Samples)
	out = r.Flush(out)
	return dsp.Buffer{Rate: rate, Channels: b.Channels, Samples: out}
}

// Convert resamples b to rate and remixes it to channels (see dsp.Remix).
func Convert(b dsp.Buffer, rate, channels int, q Quality) dsp.Buffer {
	if channels < b.Channels {
		b = dsp.Remix(b, channels) // fewer channels to resample
		return Buffer(b, rate, q)
	}
	return dsp.Remix(Buffer(b, rate, q), channels)
}
