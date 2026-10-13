package mix

import (
	"math"
	"slices"
	"sort"

	"github.com/hehehai/AudioSmith/internal/dsp"
)

// Span is a frame range [Start, End).
type Span struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// Envelope is a ducking gain curve: unity, ramping (equal-power-ish raised
// cosine) down to the depth over Attack before each region, holding through
// it, and back up over Release after.
type Envelope struct {
	regions         []Span // merged, sorted
	depth           float64
	attack, release int
}

// NewEnvelope builds the curve for regions (any order, may overlap).
func NewEnvelope(rate int, regions []Span, d Duck) *Envelope {
	d = d.norm()
	rs := slices.Clone(regions)
	slices.SortFunc(rs, func(a, b Span) int { return a.Start - b.Start })
	gap := dsp.FramesAt(d.MergeGap, rate)
	var merged []Span
	for _, r := range rs {
		if r.End <= r.Start {
			continue
		}
		if n := len(merged); n > 0 && r.Start-merged[n-1].End <= gap {
			merged[n-1].End = max(merged[n-1].End, r.End)
			continue
		}
		merged = append(merged, r)
	}
	return &Envelope{regions: merged, depth: dsp.Amp(d.DepthDB),
		attack: max(dsp.FramesAt(d.Attack, rate), 1), release: max(dsp.FramesAt(d.Release, rate), 1)}
}

// Regions returns the merged regions.
func (e *Envelope) Regions() []Span { return e.regions }

func (e *Envelope) ramp(x float64) float64 {
	// x in [0,1]: 0 = unity, 1 = fully ducked; smooth (raised cosine).
	s := 0.5 - 0.5*math.Cos(math.Pi*min(max(x, 0), 1))
	return 1 + (e.depth-1)*s
}

// At returns the gain at frame f.
func (e *Envelope) At(f int) float64 {
	// The regions that can affect f: the last one starting before f+attack
	// and its predecessor (ramps may overlap when regions are close).
	i := sort.Search(len(e.regions), func(i int) bool { return e.regions[i].Start-e.attack > f })
	g := 1.0
	for j := max(i-2, 0); j < min(i+1, len(e.regions)); j++ {
		r := e.regions[j]
		var v float64
		switch {
		case f < r.Start-e.attack:
			v = 1
		case f < r.Start:
			v = e.ramp(float64(f-(r.Start-e.attack)) / float64(e.attack))
		case f < r.End:
			v = e.depth
		case f < r.End+e.release:
			v = e.ramp(1 - float64(f-r.End)/float64(e.release))
		default:
			v = 1
		}
		g = min(g, v)
	}
	return g
}

// Gains fills g with the gains of frames from, from+1, ….
func (e *Envelope) Gains(g []float32, from int) {
	to := from + len(g)
	// Regions whose ramps reach [from, to); none means unity throughout.
	i := sort.Search(len(e.regions), func(i int) bool { return e.regions[i].End+e.release > from })
	if i == len(e.regions) || e.regions[i].Start-e.attack >= to {
		for k := range g {
			g[k] = 1
		}
		return
	}
	for i := range g {
		g[i] = float32(e.At(from + i))
	}
}

// Apply multiplies b by the envelope in place (frame 0 of b = frame 0 of the curve).
func (e *Envelope) Apply(b dsp.Buffer) {
	for i := range b.Frames() {
		g := float32(e.At(i))
		if g == 1 {
			continue
		}
		for c := range b.Channels {
			b.Samples[i*b.Channels+c] *= g
		}
	}
}

// SpeechRegions finds where key is active: 50 ms windows whose RMS reaches
// thresholdDB (dBFS; default −40 if 0), in key's frames.
func SpeechRegions(key dsp.Buffer, thresholdDB float64) []Span {
	if thresholdDB == 0 {
		thresholdDB = -40
	}
	thr := math.Pow(10, thresholdDB/10)
	w := max(dsp.FramesAt(0.05, key.Rate), 1)
	var out []Span
	n := key.Frames()
	for s := 0; s < n; s += w {
		e := min(s+w, n)
		var acc float64
		for _, v := range key.Samples[s*key.Channels : e*key.Channels] {
			acc += float64(v) * float64(v)
		}
		if acc/float64((e-s)*key.Channels) < thr {
			continue
		}
		if k := len(out); k > 0 && out[k-1].End == s {
			out[k-1].End = e
		} else {
			out = append(out, Span{s, e})
		}
	}
	return out
}

// Sidechain returns a copy of bg ducked wherever key (aligned at frame 0, same
// rate) has sound above thresholdDB, with d's depth and timing.
func Sidechain(bg, key dsp.Buffer, thresholdDB float64, d Duck) dsp.Buffer {
	out := bg.Clone()
	NewEnvelope(bg.Rate, SpeechRegions(key, thresholdDB), d).Apply(out)
	return out
}
