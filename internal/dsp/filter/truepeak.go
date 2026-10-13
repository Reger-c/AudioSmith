package filter

import "math"

// TruePeakFactor returns the oversampling factor used for true-peak
// detection at rate: enough to reach ≥ 192 kHz, between 1 and 8
// (48 kHz → 4×, the BS.1770-4 Annex 2 figure; 44.1 kHz → 5×; 24 kHz → 8×;
// 96 kHz → 2×; ≥ 192 kHz → 1×, i.e. sample peak).
func TruePeakFactor(rate int) int {
	return min(max((192000+rate-1)/rate, 1), 8)
}

// TruePeak finds inter-sample peaks: it upsamples each channel by
// TruePeakFactor with a Kaiser-windowed sinc interpolator (48 taps per
// phase, β 8, cutoff at the input Nyquist, so phase 0 is the input itself)
// and reports, per input sample, the largest magnitude among the sample and
// the interpolated points after it. It streams with Delay() frames of latency.
type TruePeak struct {
	l, half, taps, ch int
	table             []float32 // phases 1..l-1, taps each
	buf               [][]float32
	pending           int // frames in buf not yet reported
	started           bool
}

const tpHalf = 24

// NewTruePeak returns a detector for channels channels at rate.
func NewTruePeak(rate, channels int) *TruePeak {
	t := &TruePeak{l: TruePeakFactor(rate), half: tpHalf, taps: 2 * tpHalf, ch: channels}
	t.table = make([]float32, max(t.l-1, 0)*t.taps)
	for p := 1; p < t.l; p++ {
		frac := float64(p) / float64(t.l)
		row := t.table[(p-1)*t.taps : p*t.taps]
		var sum float64
		w := make([]float64, t.taps)
		for i := range t.taps {
			x := float64(i-t.half+1) - frac
			w[i] = Sinc(x) * Kaiser(x/float64(t.half), 8)
			sum += w[i]
		}
		for i := range w {
			row[i] = float32(w[i] / sum)
		}
	}
	t.Reset()
	return t
}

// Factor returns the oversampling factor.
func (t *TruePeak) Factor() int { return t.l }

// Delay returns the latency in frames: Process reports frame n once frame
// n+Delay() has been given.
func (t *TruePeak) Delay() int { return t.half }

// Reset clears the history.
func (t *TruePeak) Reset() {
	t.buf = make([][]float32, t.ch)
	for c := range t.buf {
		t.buf[c] = make([]float32, t.half-1, 4096)
	}
	t.pending = 0
}

// Process consumes interleaved frames and appends to dst, interleaved, the
// peak magnitude of every frame that has become reportable.
func (t *TruePeak) Process(dst, in []float32) []float32 {
	n := len(in) / t.ch
	for c := range t.ch {
		b := t.buf[c]
		for i := range n {
			b = append(b, in[i*t.ch+c])
		}
		t.buf[c] = b
	}
	t.pending += n
	return t.emit(dst, false)
}

// Flush reports the last Delay() frames (as if followed by silence).
func (t *TruePeak) Flush(dst []float32) []float32 {
	for c := range t.ch {
		t.buf[c] = append(t.buf[c], make([]float32, t.half)...)
	}
	dst = t.emit(dst, true)
	t.Reset()
	return dst
}

func (t *TruePeak) emit(dst []float32, flush bool) []float32 {
	// buf[c][k] holds frame base+k; the oldest pending frame sits at index
	// len - pending (before flush padding) and needs half frames after it.
	pad := 0
	if flush {
		pad = t.half
	}
	first := len(t.buf[0]) - pad - t.pending // index of the oldest unreported frame
	ready := t.pending
	if !flush {
		ready = t.pending - t.half
	}
	for k := range max(ready, 0) {
		idx := first + k
		for c := range t.ch {
			b := t.buf[c]
			m := float32(math.Abs(float64(b[idx])))
			for p := 1; p < t.l; p++ {
				row := t.table[(p-1)*t.taps : p*t.taps]
				s := Dot(b[idx-t.half+1:idx+t.half+1], row)
				if s < 0 {
					s = -s
				}
				m = max(m, s)
			}
			dst = append(dst, m)
		}
	}
	if ready > 0 {
		t.pending -= ready
	}
	// Keep only the history the next frames need.
	keep := t.pending + pad + t.half - 1
	if drop := len(t.buf[0]) - keep; drop > 8192 && !flush {
		for c := range t.ch {
			m := copy(t.buf[c], t.buf[c][drop:])
			t.buf[c] = t.buf[c][:m]
		}
	}
	return dst
}

// Dot returns Σ x[i]·h[i] over len(x) (h must be at least as long).
func Dot(x, h []float32) float32 {
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
