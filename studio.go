package main

// The Studio's form and its actions. The form autosaves as a draft
// (core.DraftService, key "studio") and comes back at the next start.
// Until generation runs on jobs (integration step I1: TakeService.Generate)
// generate builds the take's recipe and the engine request and hands
// both to the engine link (enginelink.go).

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/i18n"
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

	// savedDraft is the form as last handed to the draft service (or restored).
	savedDraft studioDraft
}

func newStudioState() studioState {
	first := catalog.Builtin().ForTask(catalog.TaskTTS)[0]
	s := studioState{
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
	s.savedDraft = s.toDraft()
	return s
}

// studioDraft is the Studio form as saved between runs.
type studioDraft struct {
	Version       int     `json:"version"`
	Text          string  `json:"text"`
	ModelLabel    string  `json:"modelLabel"`
	CustomRepo    string  `json:"customRepo,omitempty"`
	Voice         string  `json:"voice"`
	Language      string  `json:"language"`
	Speed         float64 `json:"speed"`
	ModelDefaults bool    `json:"modelDefaults"`
	Temperature   float64 `json:"temperature"`
	TopP          float64 `json:"topP"`
	MaxTokens     float64 `json:"maxTokens"`
	RefAudio      string  `json:"refAudio,omitempty"`
	RefText       string  `json:"refText,omitempty"`
	Autoplay      bool    `json:"autoplay"`
}

const studioDraftKey = "studio"

func (s *studioState) toDraft() studioDraft {
	return studioDraft{Version: 1, Text: s.text, ModelLabel: s.modelLabel, CustomRepo: s.customRepo,
		Voice: s.voice, Language: s.language, Speed: s.speed, ModelDefaults: s.modelDefaults,
		Temperature: s.temperature, TopP: s.topP, MaxTokens: s.maxTokens,
		RefAudio: s.refAudio, RefText: s.refText, Autoplay: s.autoplay}
}

// applyDraft restores a draft, ignoring a model the catalogue no longer has.
func (s *studioState) applyDraft(d studioDraft) {
	if d.ModelLabel != catalog.CustomLabel && !slices.Contains(catalog.Builtin().Labels(catalog.TaskTTS), d.ModelLabel) {
		return
	}
	s.text, s.modelLabel, s.customRepo = d.Text, d.ModelLabel, d.CustomRepo
	s.voice, s.language = d.Voice, d.Language
	if d.Speed > 0 {
		s.speed = d.Speed
	}
	s.modelDefaults = d.ModelDefaults
	if d.Temperature > 0 || d.TopP > 0 || d.MaxTokens > 0 {
		s.temperature, s.topP, s.maxTokens = d.Temperature, d.TopP, d.MaxTokens
	}
	s.refAudio, s.refText, s.autoplay = d.RefAudio, d.RefText, d.Autoplay
	s.savedDraft = s.toDraft()
}

// autosaveStudio hands the form to the draft service when it changed
// since the last call. The service only remembers it here; it writes
// after a pause in editing, on its own goroutine.
func (a *app) autosaveStudio() {
	if d := a.toDraft(); d != a.savedDraft {
		a.savedDraft = d
		a.lib.svc.Drafts.Save(studioDraftKey, d)
	}
}

// restoreStudioDraft loads the saved form in the background and applies
// it unless the form was edited meanwhile.
func (a *app) restoreStudioDraft() {
	drafts := a.lib.svc.Drafts
	a.background(func() {
		var d studioDraft
		ok, err := drafts.Load(context.Background(), studioDraftKey, &d)
		if err != nil || !ok {
			return
		}
		a.post(func() {
			if a.toDraft() == a.savedDraft {
				a.applyDraft(d)
			}
		})
	})
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
		ID:        core.NewID(now),
		CreatedAt: now,
		Text:      strings.TrimSpace(a.text),
		Model:     p.Repo,
		Voice:     strings.TrimSpace(a.voice),
		Language:  strings.TrimSpace(a.language),
	}
	t.File = t.ID + ".wav"
	req := proto.SynthesizeParams{
		Model:    t.Model,
		Text:     t.Text,
		Voice:    t.Voice,
		Language: t.Language,
		Output:   proto.Output{Path: a.takeStore.AudioPath(*t)},
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
	a.showPage("studio")
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
