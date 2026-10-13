// Package master is the "even out volume" mastering chain: high-pass →
// tone EQ → compressor → loudness normalisation under a true-peak ceiling
// (look-ahead limiter). Presets are data (Preset) so the UI and API can show
// and tweak them; the parameter choices are AudioSmith's own.
//
//	id         high-pass  EQ                                      compressor (thr/ratio/knee, att/rel)  loudness / ceiling
//	raw        —          —                                       —                                     — (untouched)
//	broadcast  80 Hz      +1.5 dB peak 3 kHz Q1                   −20 dB 3:1 k6, 5/120 ms               −18 LUFS / −1 dBTP
//	podcast    90 Hz      −1.5 dB peak 250 Hz, +2 dB peak 4 kHz   −22 dB 4:1 k6, 5/100 ms               −16 LUFS / −1.5 dBTP
//	warm       60 Hz      +2.5 dB low shelf 200 Hz, −2 dB HS 8k   −20 dB 2:1 k8, 15/200 ms              −18 LUFS / −1 dBTP
//	bright     100 Hz     +1.5 dB peak 3 kHz, +3 dB HS 6 kHz      −20 dB 2.5:1 k6, 5/120 ms             −18 LUFS / −1 dBTP
//	cinematic  40 Hz      +3 dB low shelf 120 Hz, +1.5 dB HS 10k  −24 dB 2:1 k10, 20/250 ms             −20 LUFS / −1 dBTP
//
// Level independence: before the compressor, Apply brings the programme to
// the reference loudness Reference (−20 LUFS), so a preset compresses a
// quiet take as much as a loud one; thresholds are relative to that. The
// compressors use a 10 ms RMS detector (a sine reads its peak level).
// Silent input skips the gain and loudness steps.
//
// Long renders: meter pass 1 (loudness.Meter), then stream pass 2 through
// NewProcessor(p, rate, ch, Reference−measured) and a loudness.Stream for
// the final gain and ceiling (meter the processed audio between them, or
// accept the compressor's small level change).
package master

import (
	"fmt"
	"math"

	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/dynamics"
	"github.com/hehehai/AudioSmith/internal/dsp/filter"
	"github.com/hehehai/AudioSmith/internal/dsp/loudness"
)

// Band is one EQ band.
type Band struct {
	Type   string  `json:"type"` // "lowshelf", "highshelf", "peak"
	Hz     float64 `json:"hz"`
	Q      float64 `json:"q"`
	GainDB float64 `json:"gainDb"`
}

// Preset is a mastering chain configuration. Zero fields disable stages.
type Preset struct {
	ID         string                     `json:"id"`
	HighPassHz float64                    `json:"highPassHz,omitempty"`
	EQ         []Band                     `json:"eq,omitempty"`
	Compressor *dynamics.CompressorParams `json:"compressor,omitempty"`
	Loudness   float64                    `json:"loudness,omitempty"` // integrated LUFS target; 0 = none
	TruePeak   float64                    `json:"truePeak,omitempty"` // ceiling dBTP with Loudness
}

func comp(thr, ratio, knee, att, rel float64) *dynamics.CompressorParams {
	return &dynamics.CompressorParams{ThresholdDB: thr, Ratio: ratio, KneeDB: knee, AttackMs: att, ReleaseMs: rel, RMSMs: 10}
}

// Presets.
var (
	Raw       = Preset{ID: "raw"}
	Broadcast = Preset{ID: "broadcast", HighPassHz: 80,
		EQ:         []Band{{"peak", 3000, 1, 1.5}},
		Compressor: comp(-20, 3, 6, 5, 120), Loudness: -18, TruePeak: -1}
	Podcast = Preset{ID: "podcast", HighPassHz: 90,
		EQ:         []Band{{"peak", 250, 1, -1.5}, {"peak", 4000, 1, 2}},
		Compressor: comp(-22, 4, 6, 5, 100), Loudness: -16, TruePeak: -1.5}
	Warm = Preset{ID: "warm", HighPassHz: 60,
		EQ:         []Band{{"lowshelf", 200, 0.7071, 2.5}, {"highshelf", 8000, 0.7071, -2}},
		Compressor: comp(-20, 2, 8, 15, 200), Loudness: -18, TruePeak: -1}
	Bright = Preset{ID: "bright", HighPassHz: 100,
		EQ:         []Band{{"peak", 3000, 1, 1.5}, {"highshelf", 6000, 0.7071, 3}},
		Compressor: comp(-20, 2.5, 6, 5, 120), Loudness: -18, TruePeak: -1}
	Cinematic = Preset{ID: "cinematic", HighPassHz: 40,
		EQ:         []Band{{"lowshelf", 120, 0.7071, 3}, {"highshelf", 10000, 0.7071, 1.5}},
		Compressor: comp(-24, 2, 10, 20, 250), Loudness: -20, TruePeak: -1}
)

// Presets lists the presets in display order.
var Presets = []Preset{Raw, Broadcast, Podcast, Warm, Bright, Cinematic}

// Lookup returns the preset with id.
func Lookup(id string) (Preset, error) {
	for _, p := range Presets {
		if p.ID == id {
			return p, nil
		}
	}
	return Preset{}, fmt.Errorf("master: unknown preset %q", id)
}

// stages returns the biquads of p at rate (bands above Nyquist dropped).
func (p Preset) stages(rate int) ([]filter.Biquad, error) {
	var st []filter.Biquad
	ny := float64(rate) / 2
	if p.HighPassHz > 0 && p.HighPassHz < ny {
		st = append(st, filter.HighPass(rate, p.HighPassHz, math.Sqrt2/2))
	}
	for _, b := range p.EQ {
		if b.Hz <= 0 || b.Hz >= ny*0.95 || b.GainDB == 0 {
			continue
		}
		q := b.Q
		if q <= 0 {
			q = math.Sqrt2 / 2
		}
		switch b.Type {
		case "lowshelf":
			st = append(st, filter.LowShelf(rate, b.Hz, q, b.GainDB))
		case "highshelf":
			st = append(st, filter.HighShelf(rate, b.Hz, q, b.GainDB))
		case "peak":
			st = append(st, filter.Peaking(rate, b.Hz, q, b.GainDB))
		default:
			return nil, fmt.Errorf("master: unknown EQ band type %q", b.Type)
		}
	}
	return st, nil
}

// Reference is the integrated loudness (LUFS) the compressor stage sees.
const Reference = -20.0

// Processor runs a preset's gain, filters and compressor over a stream, in place.
type Processor struct {
	pre  float32
	eq   *filter.Chain
	comp *dynamics.Compressor
}

// NewProcessor returns the streaming first stage of p with a gain of
// preGainDB applied first (Reference minus the input's integrated
// loudness; 0 to skip).
func NewProcessor(p Preset, rate, channels int, preGainDB float64) (*Processor, error) {
	st, err := p.stages(rate)
	if err != nil {
		return nil, err
	}
	pr := &Processor{pre: float32(dsp.Amp(preGainDB))}
	if len(st) > 0 {
		pr.eq = filter.NewChain(channels, st...)
	}
	if p.Compressor != nil {
		pr.comp = dynamics.NewCompressor(rate, channels, *p.Compressor)
	}
	return pr, nil
}

// Process filters and compresses interleaved samples in place.
func (pr *Processor) Process(samples []float32) {
	dsp.Gain(samples, pr.pre)
	if pr.eq != nil {
		pr.eq.Process(samples)
	}
	if pr.comp != nil {
		pr.comp.Process(samples)
	}
}

// Report describes a mastering run.
type Report struct {
	Preset   string           `json:"preset"`
	Loudness *loudness.Report `json:"loudness,omitempty"` // when a loudness target applied
}

// Apply runs preset p over b and returns a new buffer (b is not modified).
// If target is non-nil it replaces the preset's own loudness step (e.g.
// loudness.ACX for an audiobook).
func Apply(b dsp.Buffer, p Preset, target *loudness.Target) (dsp.Buffer, Report, error) {
	r := Report{Preset: p.ID}
	if err := b.Check(); err != nil {
		return b, r, err
	}
	var pre float64
	if p.Compressor != nil {
		if in := loudness.Integrated(b); !math.IsInf(in, -1) {
			pre = Reference - in
		}
	}
	pr, err := NewProcessor(p, b.Rate, b.Channels, pre)
	if err != nil {
		return b, r, err
	}
	out := b.Clone()
	pr.Process(out.Samples)
	t := target
	if t == nil && p.Loudness != 0 {
		t = &loudness.Target{ID: p.ID, Integrated: p.Loudness, MinIntegrated: p.Loudness - 1, MaxIntegrated: p.Loudness + 1, TruePeak: p.TruePeak}
	}
	if t != nil {
		var lr loudness.Report
		out, lr = loudness.Normalize(out, *t, loudness.NormalizeOptions{})
		r.Loudness = &lr
	}
	return out, r, nil
}
