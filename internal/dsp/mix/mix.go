// Package mix places clips on a timeline and sums them, sample-accurately,
// with per-track and per-clip gain, fades and pan, and ducking (background
// tracks lowered under speech). A Mixer renders any range on demand, so
// playback and export stream; Mix renders everything.
package mix

import (
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/resample"
)

// Clip is audio placed on a track.
type Clip struct {
	Audio   dsp.Buffer // resampled/remixed to the timeline's format when needed
	Offset  int        // first frame on the timeline (may be negative: starts cut)
	From    int        // first frame of Audio used (0 = start)
	To      int        // end frame of Audio used (0 = the end)
	GainDB  float64
	FadeIn  float64 // seconds
	FadeOut float64 // seconds
	Pan     float64 // −1 left … 1 right (balance: centre leaves both channels at unity; stereo timelines only)
}

// Duck lowers a track while other tracks (the key) have clips.
type Duck struct {
	Keys     []int   // indexes of the tracks whose clips are speech
	DepthDB  float64 // gain while ducked; default −12 dB
	Attack   float64 // ramp down before speech starts, s; default 0.15
	Release  float64 // ramp up after speech ends, s; default 0.4
	MergeGap float64 // speech regions closer than this merge (no pumping), s; default 0.6
}

func (d Duck) norm() Duck {
	if d.DepthDB == 0 {
		d.DepthDB = -12
	}
	if d.Attack <= 0 {
		d.Attack = 0.15
	}
	if d.Release <= 0 {
		d.Release = 0.4
	}
	if d.MergeGap <= 0 {
		d.MergeGap = 0.6
	}
	return d
}

// Track is a lane of clips.
type Track struct {
	Name   string
	GainDB float64
	Mute   bool
	Clips  []Clip
	Duck   *Duck // nil = never ducked
}

// Timeline is the whole arrangement.
type Timeline struct {
	Rate     int
	Channels int
	Tracks   []Track
}

type prepared struct {
	samples   []float32 // timeline format, already gain/fade/pan applied
	start     int       // timeline frame
	end       int
	track     int
	trackGain float32
}

// Mixer renders a prepared timeline.
type Mixer struct {
	rate, ch int
	clips    []prepared // sorted by start
	maxLen   int        // longest clip, for the search window
	length   int
	ducks    map[int]*Envelope
}

// ErrFormat reports an invalid timeline format.
var ErrFormat = errors.New("mix: invalid timeline format")

// NewMixer prepares t: converts clips to the timeline format and bakes clip
// gain, fades and pan. The timeline's clips are not modified.
func NewMixer(t Timeline) (*Mixer, error) {
	if t.Rate <= 0 || t.Channels <= 0 {
		return nil, ErrFormat
	}
	m := &Mixer{rate: t.Rate, ch: t.Channels, ducks: map[int]*Envelope{}}
	for ti, tr := range t.Tracks {
		if tr.Mute {
			continue
		}
		tg := float32(dsp.Amp(tr.GainDB))
		for ci, c := range tr.Clips {
			if err := c.Audio.Check(); err != nil {
				return nil, fmt.Errorf("mix: track %d clip %d: %w", ti, ci, err)
			}
			a := c.Audio
			to := c.To
			if to <= 0 || to > a.Frames() {
				to = a.Frames()
			}
			a = a.Slice(c.From, to).Clone()
			if a.Rate != t.Rate {
				a = resample.Buffer(a, t.Rate, resample.Good)
			}
			a = dsp.Remix(a, t.Channels)
			if a.Frames() == 0 {
				continue
			}
			dsp.Gain(a.Samples, float32(dsp.Amp(c.GainDB)))
			dsp.FadeIn(a, dsp.FramesAt(c.FadeIn, t.Rate), dsp.EqualPower)
			dsp.FadeOut(a, dsp.FramesAt(c.FadeOut, t.Rate), dsp.EqualPower)
			if c.Pan != 0 && t.Channels == 2 {
				p := min(max(c.Pan, -1), 1)
				gl, gr := float32(min(1, 1-p)), float32(min(1, 1+p))
				for i := 0; i < len(a.Samples); i += 2 {
					a.Samples[i] *= gl
					a.Samples[i+1] *= gr
				}
			}
			p := prepared{samples: a.Samples, start: c.Offset, end: c.Offset + a.Frames(), track: ti, trackGain: tg}
			m.clips = append(m.clips, p)
			m.maxLen = max(m.maxLen, p.end-p.start)
			m.length = max(m.length, p.end)
		}
	}
	slices.SortStableFunc(m.clips, func(a, b prepared) int { return a.start - b.start })
	for ti, tr := range t.Tracks {
		if tr.Duck == nil || tr.Mute {
			continue
		}
		d := tr.Duck.norm()
		var regions []Span
		for _, k := range d.Keys {
			if k < 0 || k >= len(t.Tracks) || k == ti || t.Tracks[k].Mute {
				continue
			}
			for _, c := range m.clips {
				if c.track == k {
					regions = append(regions, Span{c.start, c.end})
				}
			}
		}
		m.ducks[ti] = NewEnvelope(t.Rate, regions, d)
	}
	return m, nil
}

// Length returns the frames up to the end of the last clip.
func (m *Mixer) Length() int { return m.length }

// Render mixes frames [from, from+len(dst)/channels) into dst (overwriting it).
func (m *Mixer) Render(dst []float32, from int) {
	clear(dst)
	n := len(dst) / m.ch
	to := from + n
	// Clips starting before from−maxLen cannot reach from.
	lo := sort.Search(len(m.clips), func(i int) bool { return m.clips[i].start > from-m.maxLen-1 })
	var gain []float32
	for _, c := range m.clips[lo:] {
		if c.start >= to {
			break
		}
		if c.end <= from {
			continue
		}
		s, e := max(c.start, from), min(c.end, to)
		src := c.samples[(s-c.start)*m.ch : (e-c.start)*m.ch]
		out := dst[(s-from)*m.ch : (e-from)*m.ch]
		if env := m.ducks[c.track]; env != nil {
			if cap(gain) < e-s {
				gain = make([]float32, e-s)
			}
			g := gain[:e-s]
			env.Gains(g, s)
			for i := range g {
				gi := g[i] * c.trackGain
				for k := range m.ch {
					out[i*m.ch+k] += src[i*m.ch+k] * gi
				}
			}
			continue
		}
		g := c.trackGain
		for i, v := range src {
			out[i] += v * g
		}
	}
}

// Mix renders the whole timeline (from frame 0 to Length).
func Mix(t Timeline) (dsp.Buffer, error) {
	m, err := NewMixer(t)
	if err != nil {
		return dsp.Buffer{}, err
	}
	out := dsp.New(t.Rate, t.Channels, m.Length())
	m.Render(out.Samples, 0)
	return out, nil
}
