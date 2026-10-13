package loudness

import (
	"math"

	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/dynamics"
)

// Target is a delivery loudness specification.
type Target struct {
	ID         string  `json:"id"`         // "acx", "podcast", "broadcast", or custom
	Integrated float64 `json:"integrated"` // LUFS to normalise to
	// MinIntegrated and MaxIntegrated bound an acceptable integrated loudness.
	MinIntegrated float64 `json:"minIntegrated"`
	MaxIntegrated float64 `json:"maxIntegrated"`
	TruePeak      float64 `json:"truePeak"` // ceiling, dBTP
	// NoiseFloor, if non-zero, is the highest allowed noise floor (dBFS RMS
	// of the quietest 500 ms window).
	NoiseFloor float64 `json:"noiseFloor,omitempty"`
	// RMSMin and RMSMax, if non-zero, bound the overall RMS level (dBFS,
	// unweighted, sine RMS reads 3 dB under its peak) as ACX checks it.
	RMSMin float64 `json:"rmsMin,omitempty"`
	RMSMax float64 `json:"rmsMax,omitempty"`
}

// Presets.
var (
	// ACX (Audible): −19 LUFS integrated, accepted −23…−18 (also checked as
	// RMS, ACX's own measure), true peak ≤ −3 dBTP, noise floor ≤ −60 dB RMS.
	ACX = Target{ID: "acx", Integrated: -19, MinIntegrated: -23, MaxIntegrated: -18, TruePeak: -3, NoiseFloor: -60, RMSMin: -23, RMSMax: -18}
	// Podcast: −16 LUFS ±1 LU (Apple/Spotify-style stereo podcast), −1.5 dBTP.
	Podcast = Target{ID: "podcast", Integrated: -16, MinIntegrated: -17, MaxIntegrated: -15, TruePeak: -1.5}
	// Broadcast: EBU R128, −23 LUFS ±0.5 LU (non-live tolerance), −1 dBTP.
	Broadcast = Target{ID: "broadcast", Integrated: -23, MinIntegrated: -23.5, MaxIntegrated: -22.5, TruePeak: -1}
)

// Targets lists the presets by ID.
var Targets = map[string]Target{"acx": ACX, "podcast": Podcast, "broadcast": Broadcast}

// Check is one compliance test of a Report.
type Check struct {
	Code  string  `json:"code"` // "integrated", "true_peak", "noise_floor", "rms", "silent"
	OK    bool    `json:"ok"`
	Value float64 `json:"value"`
	Min   float64 `json:"min,omitempty"`
	Max   float64 `json:"max,omitempty"`
}

// Report describes a normalisation (or just a compliance check).
type Report struct {
	Target     string  `json:"target"`
	Before     Stats   `json:"before"`
	After      Stats   `json:"after"`
	GainDB     float64 `json:"gainDb"`     // static gain applied
	LimitDB    float64 `json:"limitDb"`    // deepest limiter reduction (≤ 0; 0 = untouched)
	NoiseFloor float64 `json:"noiseFloor"` // dBFS RMS, after
	RMS        float64 `json:"rms"`        // dBFS, after
	Checks     []Check `json:"checks"`
	Compliant  bool    `json:"compliant"`
}

// NormalizeOptions tune Normalize.
type NormalizeOptions struct {
	// NoLimiter lowers the gain to respect the ceiling instead of limiting
	// peaks: the target loudness may then be missed.
	NoLimiter bool
	// DualMono measures mono as if on two channels (+3.01 LU), as some
	// podcast tools do; the result is then 3 dB quieter than by BS.1770.
	DualMono bool
}

// limiter settings used to hold the ceiling: a 0.1 dB margin under it
// covers the gain smoothing's effect on inter-sample peaks.
const (
	limiterMargin    = 0.1
	limiterLookahead = 5.0
	limiterRelease   = 100.0
)

// Plan returns the static gain that brings a programme with stats st to t,
// and whether peaks must be limited to keep under t.TruePeak. A silent
// programme gets 0 dB.
func Plan(st Stats, t Target) (gainDB float64, limit bool) {
	if st.Integrated <= Floor {
		return 0, false
	}
	gainDB = t.Integrated - st.Integrated
	return gainDB, st.TruePeak+gainDB > t.TruePeak
}

// Stream applies a planned gain and the ceiling limiter to a stream, for
// two-pass normalisation of renders too long to hold in memory: meter the
// first pass with a Meter, Plan, then pass the audio through a Stream.
type Stream struct {
	g   float32
	lim *dynamics.Limiter
	buf []float32
}

// NewStream returns a gain+limiter stage. With limit false it only applies gain.
func NewStream(rate, channels int, gainDB, ceilingDB float64, limit bool) *Stream {
	s := &Stream{g: float32(dsp.Amp(gainDB))}
	if limit {
		s.lim = dynamics.NewLimiter(rate, channels, dynamics.LimiterParams{
			CeilingDB: ceilingDB - limiterMargin, LookaheadMs: limiterLookahead, ReleaseMs: limiterRelease, TruePeak: true})
	}
	return s
}

// Process appends processed output for in to dst. in is not modified.
func (s *Stream) Process(dst, in []float32) []float32 {
	s.buf = append(s.buf[:0], in...)
	dsp.Gain(s.buf, s.g)
	if s.lim == nil {
		return append(dst, s.buf...)
	}
	return s.lim.Process(dst, s.buf)
}

// Flush appends the limiter's tail.
func (s *Stream) Flush(dst []float32) []float32 {
	if s.lim == nil {
		return dst
	}
	return s.lim.Flush(dst)
}

// MaxReductionDB returns the deepest limiter reduction so far (0 if none).
func (s *Stream) MaxReductionDB() float64 {
	if s.lim == nil {
		return 0
	}
	return s.lim.MaxReductionDB()
}

func apply(b dsp.Buffer, gainDB, ceil float64, limit bool) (dsp.Buffer, float64) {
	s := NewStream(b.Rate, b.Channels, gainDB, ceil, limit)
	out := make([]float32, 0, len(b.Samples))
	out = s.Flush(s.Process(out, b.Samples))
	return dsp.Buffer{Rate: b.Rate, Channels: b.Channels, Samples: out}, s.MaxReductionDB()
}

// Normalize returns b brought to t's integrated loudness with true peaks
// under t's ceiling, and a report. Limiting lowers the loudness a little, so
// the gain is corrected and the audio redone (at most twice) until the
// result is within 0.1 LU of the target. b is not modified. Silent input is
// returned unchanged (a "silent" check fails).
func Normalize(b dsp.Buffer, t Target, opt NormalizeOptions) (dsp.Buffer, Report) {
	mo := Options{DualMono: opt.DualMono}
	before := MeasureWith(b, mo)
	r := Report{Target: t.ID, Before: before}
	gain, limit := Plan(before, t)
	out := b
	if before.Integrated > Floor {
		if limit && opt.NoLimiter {
			gain = t.TruePeak - before.TruePeak
			limit = false
		}
		for range 3 {
			var red float64
			out, red = apply(b, gain, t.TruePeak, limit)
			r.GainDB, r.LimitDB = gain, red
			if !limit {
				break
			}
			got := IntegratedWith(out, mo)
			if math.Abs(got-t.Integrated) <= 0.1 {
				break
			}
			gain += t.Integrated - got
		}
	}
	r.After = MeasureWith(out, mo)
	finish(&r, out, t)
	return out, r
}

// Verify checks b against t without changing it (Before and After are equal).
func Verify(b dsp.Buffer, t Target) Report {
	st := Measure(b)
	r := Report{Target: t.ID, Before: st, After: st}
	finish(&r, b, t)
	return r
}

func finish(r *Report, b dsp.Buffer, t Target) {
	r.NoiseFloor = floor(NoiseFloor(b, 0.5))
	r.RMS = floor(RMS(b))
	a := r.After
	if a.Integrated <= Floor {
		r.Checks = append(r.Checks, Check{Code: "silent", Value: a.Integrated})
	} else {
		// Rounded to 0.1 as meters display; avoids failing −18.04 against −18.
		v := math.Round(a.Integrated*10) / 10
		r.Checks = append(r.Checks, Check{Code: "integrated", OK: v >= t.MinIntegrated && v <= t.MaxIntegrated,
			Value: a.Integrated, Min: t.MinIntegrated, Max: t.MaxIntegrated})
	}
	r.Checks = append(r.Checks, Check{Code: "true_peak", OK: math.Round(a.TruePeak*10)/10 <= t.TruePeak, Value: a.TruePeak, Max: t.TruePeak})
	if t.RMSMin != 0 || t.RMSMax != 0 {
		v := math.Round(r.RMS*10) / 10
		r.Checks = append(r.Checks, Check{Code: "rms", OK: v >= t.RMSMin && v <= t.RMSMax, Value: r.RMS, Min: t.RMSMin, Max: t.RMSMax})
	}
	if t.NoiseFloor != 0 {
		r.Checks = append(r.Checks, Check{Code: "noise_floor", OK: r.NoiseFloor <= t.NoiseFloor, Value: r.NoiseFloor, Max: t.NoiseFloor})
	}
	r.Compliant = true
	for _, c := range r.Checks {
		r.Compliant = r.Compliant && c.OK
	}
}

// RMS returns the overall RMS level of b in dBFS (all channels pooled;
// unweighted, so a full-scale sine reads −3.01 dB, as ACX tools show it).
func RMS(b dsp.Buffer) float64 { return dsp.DB(dsp.RMS(b.Samples)) }

// NoiseFloor returns the RMS level (dBFS) of the quietest window of
// seconds seconds (stepping by a tenth of it), the "room tone" figure ACX
// limits. Digital silence gives −Inf. Audio shorter than the window is
// measured whole.
func NoiseFloor(b dsp.Buffer, seconds float64) float64 {
	n := b.Frames()
	w := max(dsp.FramesAt(seconds, b.Rate), 1)
	if n == 0 {
		return math.Inf(-1)
	}
	if n <= w {
		return dsp.DB(dsp.RMS(b.Samples))
	}
	step := max(w/10, 1)
	// Prefix sums of per-frame energy.
	pre := make([]float64, n+1)
	for i := range n {
		var e float64
		for _, v := range b.Samples[i*b.Channels : (i+1)*b.Channels] {
			e += float64(v) * float64(v)
		}
		pre[i+1] = pre[i] + e
	}
	low := math.Inf(1)
	for s := 0; s+w <= n; s += step {
		low = min(low, pre[s+w]-pre[s])
	}
	return dsp.PowerDB(max(low, 0) / float64(w*b.Channels))
}
