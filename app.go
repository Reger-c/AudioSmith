package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
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
	clip         *clip // what the result becomes, for "synthesize"
}

// app is AudioSmith's state. Every field is read and written on the main
// thread: engine events reach it through post.
type app struct {
	win     *mygo.Window // nil in tests
	eng     backend
	lib     *library
	pl      player
	logPath string

	page        string // sidebar selection: "studio", "history" or "engine"
	historyList ui.ListState

	// The studio's form.
	text          string
	modelLabel    string
	customRepo    string
	voice         string
	language      string
	speed         float64
	modelDefaults bool // leave temperature / top-p / max tokens to the model
	temperature   float64
	topP          float64
	maxTokens     float64
	refAudio      string
	refText       string
	autoplay      bool

	// The engine.
	engState    engineState
	engVersion  string
	engPID      int
	engError    string
	engTail     string
	loadedModel string

	job        *job
	jobError   string
	lastClipID string
	toasts     []string // shown by the next build of the view
}

func newApp(eng backend, lib *library) *app {
	a := &app{
		eng:           eng,
		lib:           lib,
		page:          "studio",
		modelLabel:    modelPresets[0].Label,
		voice:         modelPresets[0].Voices[0],
		speed:         1,
		modelDefaults: true,
		temperature:   0.7,
		topP:          0.9,
		maxTokens:     1200,
		autoplay:      true,
		text:          "Hello from AudioSmith. This voice was generated on your Mac with MLX.",
	}
	if len(lib.clips) > 0 {
		a.lastClipID = lib.clips[0].ID
	}
	return a
}

// post runs fn on the main thread and redraws; directly in tests.
func (a *app) post(fn func()) {
	if a.win == nil {
		fn()
		return
	}
	a.win.Update(fn)
}

func (a *app) toast(format string, args ...any) {
	a.toasts = append(a.toasts, fmt.Sprintf(format, args...))
}

// preset is the model the form has chosen.
func (a *app) preset() modelPreset {
	if p, ok := presetByLabel(a.modelLabel); ok {
		return p
	}
	return customPreset(strings.TrimSpace(a.customRepo))
}

// modelChanged resets the voice and language to the new model's defaults.
func (a *app) modelChanged() {
	p := a.preset()
	a.voice, a.language = "", ""
	if len(p.Voices) > 0 {
		a.voice = p.Voices[0]
	}
	if len(p.Languages) > 0 {
		a.language = p.Languages[0]
	}
}

func (a *app) canGenerate() bool {
	return a.engState == engineReady && a.job == nil &&
		strings.TrimSpace(a.text) != "" && a.preset().Repo != ""
}

func (a *app) generate() {
	if !a.canGenerate() {
		return
	}
	p := a.preset()
	now := time.Now()
	c := &clip{
		ID:        newClipID(now),
		CreatedAt: now,
		Text:      strings.TrimSpace(a.text),
		Model:     p.Repo,
		Voice:     strings.TrimSpace(a.voice),
		Language:  strings.TrimSpace(a.language),
	}
	c.File = c.ID + ".wav"
	req := engineRequest{
		Cmd:      "synthesize",
		Model:    c.Model,
		Text:     c.Text,
		Voice:    c.Voice,
		Language: c.Language,
		Output:   a.lib.path(c),
	}
	if p.Speed {
		c.Speed = a.speed
		req.Speed = &c.Speed
	}
	if p.Sampling && !a.modelDefaults {
		c.Temperature, c.TopP, c.MaxTokens = a.temperature, a.topP, int(a.maxTokens)
		req.Temperature, req.TopP, req.MaxTokens = &c.Temperature, &c.TopP, c.MaxTokens
	}
	if p.RefAudio && strings.TrimSpace(a.refAudio) != "" {
		c.RefAudio, c.RefText = strings.TrimSpace(a.refAudio), strings.TrimSpace(a.refText)
		req.RefAudio, req.RefText = c.RefAudio, c.RefText
	}
	id, err := a.eng.Send(req)
	if err != nil {
		a.jobError = err.Error()
		return
	}
	status := "Starting…"
	if a.loadedModel != c.Model {
		status = "Loading model…"
	}
	a.jobError = ""
	a.job = &job{id: id, kind: "synthesize", model: c.Model, started: now, status: status, fraction: -1, clip: c}
}

// preload loads the chosen model without generating, so the first take is quick.
func (a *app) preload() {
	repo := a.preset().Repo
	if a.engState != engineReady || a.job != nil || repo == "" || repo == a.loadedModel {
		return
	}
	id, err := a.eng.Send(engineRequest{Cmd: "load", Model: repo})
	if err != nil {
		a.jobError = err.Error()
		return
	}
	a.jobError = ""
	a.job = &job{id: id, kind: "load", model: repo, started: time.Now(), status: "Loading model…", fraction: -1}
}

func (a *app) cancel() {
	if a.job == nil {
		return
	}
	a.job.status = "Cancelling…"
	a.eng.Send(engineRequest{Cmd: "cancel"})
}

func (a *app) unload() {
	if a.job != nil || a.loadedModel == "" {
		return
	}
	a.eng.Send(engineRequest{Cmd: "unload"})
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
func (a *app) handleEvent(ev engineEvent) {
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
			a.toast("Loaded %s in %.1f s", shortRepo(ev.Model), ev.Seconds)
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
		if !mine || a.job.clip == nil {
			return
		}
		c := a.job.clip
		a.job = nil
		c.SampleRate, c.AudioSeconds, c.GenSeconds = ev.SampleRate, ev.AudioSeconds, ev.Seconds
		if ev.Output != "" {
			c.File = filepath.Base(ev.Output)
		}
		if err := a.lib.add(c); err != nil {
			a.toast("Could not save the history: %v", err)
		}
		a.lastClipID = c.ID
		if a.autoplay {
			if err := a.pl.toggle(c.ID, a.lib.path(c)); err != nil {
				a.toast("Could not play: %v", err)
			}
		}
	case "cancelled":
		if mine {
			a.job = nil
			a.toast("Cancelled")
		}
	case "unloaded":
		a.loadedModel = ""
		a.toast("Model unloaded")
	case "error":
		if mine || (a.job != nil && ev.ID == "") {
			a.job = nil
			a.jobError = ev.Message
		} else {
			a.toast("Engine: %s", ev.Message)
		}
	}
}

// engineExited reports the engine process ending without being asked to.
func (a *app) engineExited(err error, tail string) {
	a.engState = engineFailed
	a.engError = "The engine stopped"
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

func (a *app) lastClip() *clip {
	if a.lastClipID == "" {
		return nil
	}
	return a.lib.find(a.lastClipID)
}

func (a *app) play(c *clip) {
	if err := a.pl.toggle(c.ID, a.lib.path(c)); err != nil {
		a.toast("Could not play: %v", err)
	}
}

func (a *app) deleteClip(c *clip) {
	if a.pl.clipID == c.ID {
		a.pl.stop()
	}
	if err := a.lib.remove(c.ID); err != nil {
		a.toast("Could not delete: %v", err)
	}
	if a.lastClipID == c.ID {
		a.lastClipID = ""
	}
}

// reuse copies a clip's settings back into the form.
func (a *app) reuse(c *clip) {
	a.text = c.Text
	if p, ok := presetByRepo(c.Model); ok {
		a.modelLabel = p.Label
	} else {
		a.modelLabel, a.customRepo = customModelLabel, c.Model
	}
	a.voice, a.language = c.Voice, c.Language
	if c.Speed > 0 {
		a.speed = c.Speed
	}
	a.modelDefaults = c.Temperature == 0 && c.TopP == 0 && c.MaxTokens == 0
	if !a.modelDefaults {
		a.temperature, a.topP, a.maxTokens = c.Temperature, c.TopP, float64(c.MaxTokens)
	}
	a.refAudio, a.refText = c.RefAudio, c.RefText
	a.page = "studio"
}

// export asks where to save a copy of the clip; the dialog blocks, so it
// runs on a goroutine of its own.
func (a *app) export(c *clip) {
	src := a.lib.path(c)
	name := exportName(c)
	go func() {
		dst, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
			Parent:      a.win,
			Title:       "Export audio",
			DefaultPath: name,
			Filters:     []mygo.FileFilter{{Name: "WAV audio", Extensions: []string{"wav"}}},
		})
		if err != nil || dst == "" {
			return
		}
		err = copyFile(src, dst)
		a.post(func() {
			if err != nil {
				a.toast("Export failed: %v", err)
			} else {
				a.toast("Exported %s", filepath.Base(dst))
			}
		})
	}()
}

func (a *app) chooseRefAudio() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent:  a.win,
			Title:   "Reference audio",
			Filters: []mygo.FileFilter{{Name: "Audio", Extensions: []string{"wav", "mp3", "m4a", "aac", "flac", "aiff", "caf"}}},
		})
		if err != nil || len(paths) == 0 {
			return
		}
		a.post(func() { a.refAudio = paths[0] })
	}()
}

func exportName(c *clip) string {
	words := strings.Fields(c.Text)
	if len(words) > 6 {
		words = words[:6]
	}
	base := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) {
			return '-'
		}
		return r
	}, strings.Join(words, " "))
	if base == "" {
		base = "AudioSmith"
	}
	if len([]rune(base)) > 40 {
		base = string([]rune(base)[:40])
	}
	return base + ".wav"
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

// shortRepo is a repo id without its owner.
func shortRepo(repo string) string {
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		return repo[i+1:]
	}
	return repo
}

func modelLabelOf(repo string) string {
	if p, ok := presetByRepo(repo); ok {
		return p.Label
	}
	return shortRepo(repo)
}
