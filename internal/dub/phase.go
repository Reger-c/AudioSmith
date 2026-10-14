package dub

import (
	"slices"
	"strings"
	"time"
)

// Phase summarises where a project is, for lists and the stepper.
type Phase string

const (
	PhaseNew         Phase = "new"         // no source yet
	PhaseReady       Phase = "ready"       // a source, nothing run
	PhasePreparing   Phase = "preparing"   // fetching, decoding, separating, transcribing
	PhaseTranscribed Phase = "transcribed" // a transcript, no translation yet
	PhaseTranslating Phase = "translating"
	PhaseTranslated  Phase = "translated" // every target translated
	PhaseGenerating  Phase = "generating"
	PhaseDone        Phase = "done" // every target has a track and nothing changed since
	PhaseFailed      Phase = "failed"
)

// The stages before the transcript, in order.
var prepStages = []Stage{StageFetch, StageIngest, StageSeparate, StageTranscribe, StageReferences}

// StageFetch downloads a URL's video (yt-dlp).
const StageFetch Stage = "fetch"

// Run is the run the user asked for last: what Resume continues after an
// interruption. Nil once it finished or was cancelled.
type Run struct {
	Through Stage     `json:"through"`
	Langs   []string  `json:"langs,omitempty"`
	At      time.Time `json:"at"`
	// Batch: no checkpoints (the batch queue).
	Batch bool `json:"batch,omitempty"`
}

// Phase is where p is.
func (p *Project) Phase() Phase {
	if p.Source.Path == "" && p.Source.URL == "" {
		return PhaseNew
	}
	running, failed := false, false
	for _, st := range p.Stages {
		switch st.Status {
		case StatusRunning:
			running = true
		case StatusFailed:
			failed = true
		}
	}
	stageRunning := func(s Stage) bool {
		for k, st := range p.Stages {
			if st.Status == StatusRunning && (k == string(s) || strings.HasPrefix(k, string(s)+":")) {
				return true
			}
		}
		return false
	}
	for _, s := range prepStages {
		if stageRunning(s) {
			return PhasePreparing
		}
	}
	switch {
	case stageRunning(StageTranslate):
		return PhaseTranslating
	case stageRunning(StageSynthesize), stageRunning(StageMix), stageRunning(StageQC):
		return PhaseGenerating
	case running:
		return PhasePreparing
	case failed:
		return PhaseFailed
	}
	if len(p.Segments) == 0 {
		return PhaseReady
	}
	if p.TracksReady() {
		return PhaseDone
	}
	if p.AllTranslated() {
		return PhaseTranslated
	}
	return PhaseTranscribed
}

// AllTranslated reports whether every target has every line.
func (p *Project) AllTranslated() bool {
	if len(p.Targets) == 0 {
		return false
	}
	for _, l := range p.Targets {
		done, total := p.Translated(l)
		if total == 0 || done < total {
			return false
		}
	}
	return true
}

// TracksReady reports whether every target has a mixed track.
func (p *Project) TracksReady() bool {
	if len(p.Targets) == 0 {
		return false
	}
	for _, l := range p.Targets {
		if p.Tracks[l].File == "" {
			return false
		}
	}
	return true
}

// Failed lists the keys of failed stages, in order.
func (p *Project) Failed() []string {
	var out []string
	for k, st := range p.Stages {
		if st.Status == StatusFailed {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}
