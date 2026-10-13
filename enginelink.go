package main

// The link between the UI and the engine: the process, its state, and the
// one job it runs (protocol 1). Thread B replaces it with the protocol-2
// supervisor and the job scheduler: the UI then shows jobs.Updated events
// instead of engine events, and startSynthesis becomes a job submission.

import (
	"path/filepath"
	"time"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/i18n"
)

type engineState int

const (
	engineStarting engineState = iota
	engineReady
	engineFailed
)

// job is the request the engine is working on: one at a time.
type job struct {
	id           string
	kind         string // "synthesize" or "load"
	model        string
	started      time.Time
	status       string
	fraction     float64 // 0–1, or -1 when unknown
	audioSeconds float64
	take         *core.Take // what the result becomes, for "synthesize"
}

// engineLink is the engine's view state.
type engineLink struct {
	eng     engine.Backend
	logPath string

	engState    engineState
	engVersion  string
	engPID      int
	engError    string
	engTail     string
	loadedModel string

	job      *job
	jobError string
}

// newEngineProcess finds the engine among the app's resources.
func newEngineProcess(resources, logDir string) *engine.Process {
	return &engine.Process{
		Bin:     engine.Find(resources),
		LogPath: filepath.Join(logDir, "engine.log"),
	}
}

// connectEngine routes the process's events to the main thread.
func (a *app) connectEngine(p *engine.Process) {
	a.logPath = p.LogPath
	p.OnEvent = func(ev engine.Event) { a.post(func() { a.handleEvent(ev) }) }
	p.OnExit = func(err error, tail string) { a.post(func() { a.engineExited(err, tail) }) }
}

// startSynthesis sends a synthesize request whose result becomes take t.
func (a *app) startSynthesis(t *core.Take, req engine.Request) {
	id, err := a.eng.Send(req)
	if err != nil {
		a.jobError = err.Error()
		return
	}
	status := i18n.T("Starting…")
	if a.loadedModel != t.Model {
		status = i18n.T("Loading model…")
	}
	a.jobError = ""
	a.job = &job{id: id, kind: "synthesize", model: t.Model, started: t.CreatedAt, status: status, fraction: -1, take: t}
}

// preload loads the chosen model without generating, so the first take is quick.
func (a *app) preload() {
	repo := a.preset().Repo
	if a.engState != engineReady || a.job != nil || repo == "" || repo == a.loadedModel {
		return
	}
	id, err := a.eng.Send(engine.Request{Cmd: "load", Model: repo})
	if err != nil {
		a.jobError = err.Error()
		return
	}
	a.jobError = ""
	a.job = &job{id: id, kind: "load", model: repo, started: time.Now(), status: i18n.T("Loading model…"), fraction: -1}
}

func (a *app) cancel() {
	if a.job == nil {
		return
	}
	a.job.status = i18n.T("Cancelling…")
	a.eng.Send(engine.Request{Cmd: "cancel"})
}

func (a *app) unload() {
	if a.job != nil || a.loadedModel == "" {
		return
	}
	a.eng.Send(engine.Request{Cmd: "unload"})
}

func (a *app) restartEngine() {
	a.job = nil
	a.loadedModel = ""
	a.engState, a.engError, a.engTail = engineStarting, "", ""
	if err := a.eng.Restart(); err != nil {
		a.engState, a.engError = engineFailed, err.Error()
	}
}

// handleEvent applies an engine event; it runs on the main thread.
func (a *app) handleEvent(ev engine.Event) {
	mine := a.job != nil && ev.ID == a.job.id
	switch ev.Event {
	case "ready":
		a.engState, a.engVersion, a.engPID, a.engError = engineReady, ev.Version, ev.PID, ""
	case "status":
		if mine {
			a.job.status = ev.Message
		}
	case "loaded":
		a.loadedModel = ev.Model
		if mine && a.job.kind == "load" {
			a.toast(i18n.T("Loaded %s in %.1f s", catalog.ShortRepo(ev.Model), ev.Seconds))
			a.job = nil
		}
	case "progress":
		if mine {
			if ev.Fraction != nil {
				a.job.fraction = *ev.Fraction
			}
			if ev.AudioSeconds > 0 {
				a.job.audioSeconds = ev.AudioSeconds
			}
		}
	case "done":
		if !mine || a.job.take == nil {
			return
		}
		t := a.job.take
		a.job = nil
		t.SampleRate, t.AudioSeconds, t.GenSeconds = ev.SampleRate, ev.AudioSeconds, ev.Seconds
		if ev.Output != "" {
			t.File = filepath.Base(ev.Output)
		}
		a.takeFinished(t)
	case "cancelled":
		if mine {
			a.job = nil
			a.toast(i18n.T("Cancelled"))
		}
	case "unloaded":
		a.loadedModel = ""
		a.toast(i18n.T("Model unloaded"))
	case "error":
		if mine || (a.job != nil && ev.ID == "") {
			a.job = nil
			a.jobError = ev.Message
		} else {
			a.toast(i18n.T("Engine: %s", ev.Message))
		}
	}
}

// engineExited reports the engine process ending without being asked to.
func (a *app) engineExited(err error, tail string) {
	a.engState = engineFailed
	a.engError = i18n.T("The engine stopped")
	if err != nil {
		a.engError += ": " + err.Error()
	}
	a.engTail = tail
	a.loadedModel = ""
	if a.job != nil {
		a.jobError = a.engError
		a.job = nil
	}
}
