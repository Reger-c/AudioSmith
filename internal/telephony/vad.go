package telephony

import (
	"math"
	"time"

	"github.com/hehehai/AudioSmith/internal/dsp"
)

// EndpointOptions tune the endpointer; zero values take the defaults.
type EndpointOptions struct {
	// Start is how much speech starts an utterance (and barges in on the
	// agent); default 200 ms.
	Start time.Duration
	// End is the silence that ends it; default 700 ms.
	End time.Duration
	// PreRoll is kept before the start, so the first syllable is not cut;
	// default 300 ms.
	PreRoll time.Duration
	// Max cuts an utterance that goes on; default 30 s.
	Max time.Duration
	// MarginDB: speech is this far above the noise floor; default 10 dB.
	MarginDB float64
	// MinDB: and at least this loud (dBFS RMS of a 20 ms frame); default −45.
	MinDB float64
}

func (o EndpointOptions) norm() EndpointOptions {
	if o.Start <= 0 {
		o.Start = 200 * time.Millisecond
	}
	if o.End <= 0 {
		o.End = 700 * time.Millisecond
	}
	if o.PreRoll <= 0 {
		o.PreRoll = 300 * time.Millisecond
	}
	if o.Max <= 0 {
		o.Max = 30 * time.Second
	}
	if o.MarginDB == 0 {
		o.MarginDB = 10
	}
	if o.MinDB == 0 {
		o.MinDB = -45
	}
	return o
}

// EndpointKind is what the endpointer found.
type EndpointKind int

const (
	// SpeechStarted: the caller started speaking (barge-in point).
	SpeechStarted EndpointKind = iota + 1
	// SpeechEnded: an utterance is complete; Audio holds it (16 kHz).
	SpeechEnded
)

// Endpoint is one finding.
type Endpoint struct {
	Kind  EndpointKind
	Audio []float32
}

// Endpointer finds utterances in a stream of 16 kHz audio by frame
// energy against an adaptive noise floor. Not goroutine-safe.
type Endpointer struct {
	o       EndpointOptions
	frame   int // samples per 20 ms frame
	pending []float32
	floor   float64
	started bool // the floor has a first value
	inside  bool
	onset   int // consecutive speech frames before the start (one gap allowed)
	gap     int
	silence int // consecutive silent frames inside
	pre     [][]float32
	utter   []float32
}

// NewEndpointer returns an endpointer for ListenRate audio.
func NewEndpointer(o EndpointOptions) *Endpointer {
	return &Endpointer{o: o.norm(), frame: ListenRate * FrameMillis / 1000}
}

func (e *Endpointer) frames(d time.Duration) int {
	return max(int(d/(FrameMillis*time.Millisecond)), 1)
}

// Speaking reports whether an utterance is in progress.
func (e *Endpointer) Speaking() bool { return e.inside }

// Reset forgets the utterance in progress (the noise floor stays).
func (e *Endpointer) Reset() {
	e.inside, e.onset, e.gap, e.silence = false, 0, 0, 0
	e.utter, e.pre = nil, nil
}

// Push adds audio and returns what it completes.
func (e *Endpointer) Push(pcm []float32) []Endpoint {
	var out []Endpoint
	e.pending = append(e.pending, pcm...)
	for len(e.pending) >= e.frame {
		f := append([]float32(nil), e.pending[:e.frame]...)
		e.pending = e.pending[e.frame:]
		if ep, ok := e.step(f); ok {
			out = append(out, ep)
		}
	}
	return out
}

func (e *Endpointer) step(f []float32) (Endpoint, bool) {
	db := dsp.DB(dsp.RMS(f) + 1e-9)
	if !e.started {
		// A phone line starts quiet; the floor adapts from there.
		e.floor, e.started = math.Min(math.Max(db, -80), -60), true
	}
	speech := db > math.Max(e.floor+e.o.MarginDB, e.o.MinDB)
	if !e.inside {
		// The floor follows quiet frames down quickly and creeps up
		// slowly (1 dB/s), so a steady noise becomes the floor.
		if db < e.floor {
			e.floor = 0.7*e.floor + 0.3*db
		} else if !speech {
			e.floor += 0.02
		}
		e.floor = math.Min(math.Max(e.floor, -80), -20)
		e.pre = append(e.pre, f)
		if n := e.frames(e.o.PreRoll); len(e.pre) > n {
			e.pre = e.pre[len(e.pre)-n:]
		}
		switch {
		case speech:
			e.onset++
			e.gap = 0
		case e.onset > 0 && e.gap == 0:
			e.gap = 1 // one quiet frame inside a word is allowed
		default:
			e.onset, e.gap = 0, 0
		}
		if e.onset < e.frames(e.o.Start) {
			return Endpoint{}, false
		}
		e.inside, e.silence = true, 0
		for _, p := range e.pre {
			e.utter = append(e.utter, p...)
		}
		e.pre = nil
		return Endpoint{Kind: SpeechStarted}, true
	}
	e.utter = append(e.utter, f...)
	if speech {
		e.silence = 0
	} else {
		e.silence++
	}
	if e.silence >= e.frames(e.o.End) || len(e.utter) >= e.frames(e.o.Max)*e.frame {
		// Keep 200 ms of the trailing silence.
		trim := max(e.silence-e.frames(200*time.Millisecond), 0) * e.frame
		audio := e.utter[:len(e.utter)-min(trim, len(e.utter))]
		e.Reset()
		return Endpoint{Kind: SpeechEnded, Audio: audio}, true
	}
	return Endpoint{}, false
}
