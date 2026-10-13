package main

// The Studio's form and its actions. Thread A moves generation onto
// core.TakeService (a job per take) and drafts; until then generate builds
// the take's recipe and the protocol-1 request and hands both to the
// engine link (enginelink.go).

import (
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/store/historyjson"
)

// studioState is the Studio form.
type studioState struct {
	text          string
	modelLabel    string // a catalog label, or catalog.CustomLabel
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
}

func newStudioState() studioState {
	first := catalog.Builtin().ForTask(catalog.TaskTTS)[0]
	return studioState{
		modelLabel:    first.Label,
		voice:         first.VoiceIDs()[0],
		speed:         1,
		modelDefaults: true,
		temperature:   0.7,
		topP:          0.9,
		maxTokens:     1200,
		autoplay:      true,
		text:          "Hello from AudioSmith. This voice was generated on your Mac with MLX.",
	}
}

// preset is the catalogue entry the form has chosen.
func (a *app) preset() catalog.Entry {
	if e, ok := catalog.Builtin().ByLabel(a.modelLabel); ok {
		return e
	}
	return catalog.Custom(strings.TrimSpace(a.customRepo))
}

// modelChanged resets the voice and language to the new model's defaults.
func (a *app) modelChanged() {
	p := a.preset()
	a.voice, a.language = "", ""
	if v := p.VoiceIDs(); len(v) > 0 {
		a.voice = v[0]
	}
	if l := p.LanguageOptions(); len(l) > 0 {
		a.language = l[0]
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
	t := &core.Take{
		ID:        historyjson.NewTakeID(now),
		CreatedAt: now,
		Text:      strings.TrimSpace(a.text),
		Model:     p.Repo,
		Voice:     strings.TrimSpace(a.voice),
		Language:  strings.TrimSpace(a.language),
	}
	t.File = t.ID + ".wav"
	req := engine.Request{
		Cmd:      "synthesize",
		Model:    t.Model,
		Text:     t.Text,
		Voice:    t.Voice,
		Language: t.Language,
		Output:   a.takeStore.AudioPath(*t),
	}
	if p.Has(catalog.CapSpeed) {
		t.Speed = a.speed
		req.Speed = &t.Speed
	}
	if p.Has(catalog.CapSampling) && !a.modelDefaults {
		t.Temperature, t.TopP, t.MaxTokens = a.temperature, a.topP, int(a.maxTokens)
		req.Temperature, req.TopP, req.MaxTokens = &t.Temperature, &t.TopP, t.MaxTokens
	}
	if p.Has(catalog.CapClone) && strings.TrimSpace(a.refAudio) != "" {
		t.RefAudio, t.RefText = strings.TrimSpace(a.refAudio), strings.TrimSpace(a.refText)
		req.RefAudio, req.RefText = t.RefAudio, t.RefText
	}
	a.startSynthesis(t, req)
}

// reuse copies a take's recipe back into the form.
func (a *app) reuse(t *core.Take) {
	a.text = t.Text
	if p, ok := catalog.Builtin().ByRepo(t.Model); ok {
		a.modelLabel = p.Label
	} else {
		a.modelLabel, a.customRepo = catalog.CustomLabel, t.Model
	}
	a.voice, a.language = t.Voice, t.Language
	if t.Speed > 0 {
		a.speed = t.Speed
	}
	a.modelDefaults = t.Temperature == 0 && t.TopP == 0 && t.MaxTokens == 0
	if !a.modelDefaults {
		a.temperature, a.topP, a.maxTokens = t.Temperature, t.TopP, float64(t.MaxTokens)
	}
	a.refAudio, a.refText = t.RefAudio, t.RefText
	a.page = "studio"
}

func (a *app) chooseRefAudio() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent:  a.win,
			Title:   i18n.T("Reference audio"),
			Filters: []mygo.FileFilter{{Name: i18n.T("Audio"), Extensions: []string{"wav", "mp3", "m4a", "aac", "flac", "aiff", "caf"}}},
		})
		if err != nil || len(paths) == 0 {
			return
		}
		a.post(func() { a.refAudio = paths[0] })
	}()
}
