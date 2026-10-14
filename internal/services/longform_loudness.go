package services

import (
	"math"

	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/loudness"
)

// LoudnessReport is a chapter's or a book's loudness, measured on the
// file written, and how it fares against the book's target (the checks
// of loudness.Verify, measured while streaming so a book of any length
// needs no more memory than a block).
type LoudnessReport struct {
	Target     string           `json:"target,omitempty"` // "acx", "podcast"; "" = measured only
	Integrated float64          `json:"integrated"`       // LUFS
	TruePeak   float64          `json:"truePeak"`         // dBTP
	Range      float64          `json:"range"`            // LRA, LU
	RMS        float64          `json:"rms"`              // dBFS
	NoiseFloor float64          `json:"noiseFloor"`       // dBFS RMS of the quietest 0.5 s
	GainDB     float64          `json:"gainDb,omitempty"` // gain applied to reach the target
	LimitDB    float64          `json:"limitDb,omitempty"`
	Seconds    float64          `json:"seconds"`
	Checks     []loudness.Check `json:"checks,omitempty"`
	Compliant  bool             `json:"compliant,omitempty"`
}

// loudnessTarget is the preset of a book's Loudness; nil for Off.
func loudnessTarget(id string) *loudness.Target {
	if t, ok := loudness.Targets[id]; ok && id != "broadcast" {
		return &t
	}
	return nil
}

// verifier measures a stream for a LoudnessReport.
type verifier struct {
	m        *loudness.Meter
	channels int
	sumSq    float64
	samples  int64
	frames   int64

	// The noise floor: energy per step (a tenth of 0.5 s), summed over the
	// last ten steps (a window), lowest window kept.
	step    int
	stepE   float64
	stepN   int
	ring    [10]float64
	ringN   int
	ringI   int
	lowest  float64
	haveWin bool
}

func newVerifier(rate, channels int) *verifier {
	return &verifier{m: loudness.NewMeter(rate, channels, loudness.Options{}), channels: channels,
		step: max(dsp.FramesAt(0.5, rate)/10, 1), lowest: math.Inf(1)}
}

func (v *verifier) Write(s []float32) {
	v.m.Write(s)
	ch := v.channels
	for i := 0; i+ch <= len(s); i += ch {
		var e float64
		for _, x := range s[i : i+ch] {
			e += float64(x) * float64(x)
		}
		v.sumSq += e
		v.samples += int64(ch)
		v.frames++
		v.stepE += e
		v.stepN++
		if v.stepN == v.step {
			v.ring[v.ringI] = v.stepE
			v.ringI = (v.ringI + 1) % len(v.ring)
			if v.ringN < len(v.ring) {
				v.ringN++
			}
			if v.ringN == len(v.ring) {
				var w float64
				for _, x := range v.ring {
					w += x
				}
				v.lowest = min(v.lowest, w)
				v.haveWin = true
			}
			v.stepE, v.stepN = 0, 0
		}
	}
}

// Report measures what was written against target (nil: no checks).
func (v *verifier) Report(target *loudness.Target) LoudnessReport {
	st := v.m.Stats()
	r := LoudnessReport{Integrated: st.Integrated, TruePeak: st.TruePeak, Range: st.Range, Seconds: st.Seconds,
		RMS: loudness.Floor, NoiseFloor: loudness.Floor}
	if v.samples > 0 && v.sumSq > 0 {
		r.RMS = max(dsp.PowerDB(v.sumSq/float64(v.samples)), loudness.Floor)
	}
	switch {
	case v.haveWin:
		if v.lowest > 0 {
			r.NoiseFloor = max(dsp.PowerDB(v.lowest/float64(v.step*len(v.ring)*v.channels)), loudness.Floor)
		}
	default:
		r.NoiseFloor = r.RMS // shorter than the window: measured whole
	}
	for _, f := range []*float64{&r.Integrated, &r.TruePeak, &r.RMS, &r.NoiseFloor, &r.Range} {
		*f = math.Round(*f*100) / 100
	}
	if target == nil {
		return r
	}
	r.Target = target.ID
	t := *target
	if r.Integrated <= loudness.Floor {
		r.Checks = append(r.Checks, loudness.Check{Code: "silent", Value: r.Integrated})
	} else {
		x := math.Round(r.Integrated*10) / 10
		r.Checks = append(r.Checks, loudness.Check{Code: "integrated", OK: x >= t.MinIntegrated && x <= t.MaxIntegrated,
			Value: r.Integrated, Min: t.MinIntegrated, Max: t.MaxIntegrated})
	}
	r.Checks = append(r.Checks, loudness.Check{Code: "true_peak", OK: math.Round(r.TruePeak*10)/10 <= t.TruePeak, Value: r.TruePeak, Max: t.TruePeak})
	if t.RMSMin != 0 || t.RMSMax != 0 {
		x := math.Round(r.RMS*10) / 10
		r.Checks = append(r.Checks, loudness.Check{Code: "rms", OK: x >= t.RMSMin && x <= t.RMSMax, Value: r.RMS, Min: t.RMSMin, Max: t.RMSMax})
	}
	if t.NoiseFloor != 0 {
		r.Checks = append(r.Checks, loudness.Check{Code: "noise_floor", OK: r.NoiseFloor <= t.NoiseFloor, Value: r.NoiseFloor, Max: t.NoiseFloor})
	}
	r.Compliant = true
	for _, c := range r.Checks {
		r.Compliant = r.Compliant && c.OK
	}
	return r
}
