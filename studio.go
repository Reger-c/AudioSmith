package main

// The Studio's form and its actions. The form autosaves as a draft
// (core.DraftService, key "studio") and comes back at the next start.
// generate builds the take's recipe (text, model, voice, parameters and
// the output options of the render pipeline) and hands it to the engine
// link (startTake), which queues a take.render job through TakeService.
//
// Its parts:
//
//	studio_view.go     the page: composer, generate bar, job status, inspector
//	studio_editor.go   the script editor (markup highlighting, diagnostics, import)
//	studio_controls.go the insert menu, language picker, Audio quality and Voice controls popovers
//	studio_output.go   the latest take: details, Check audio, Save as…, drag out
//	studio_preview.go  the streaming preview and the render's chunk progress
//	studio_voice.go    the voice and cloning section (seam for the voice library)
//	studio_notices.go  translations of render and markup findings

import (
	"context"
	"crypto/rand"
	"math/big"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// Bit depths of the Audio quality popover, by index.
var bitDepths = []int{16, 24, 32}

// studioState is the Studio form.
type studioState struct {
	text          string
	modelLabel    string // a catalog label, or catalog.CustomLabel
	customRepo    string
	voice         string
	language      string // the value the model's language parameter takes; "" = auto
	speed         float64
	modelDefaults bool // leave temperature / top-p / max tokens to the model
	temperature   float64
	topP          float64
	maxTokens     float64
	refAudio      string
	refText       string
	duration      float64 // target seconds (models with CapDuration); 0 = natural
	// autoplay mirrors settings.Studio.Autoplay; the checkbox changes the setting.
	autoplay bool

	// Audio quality.
	steps     float64 // diffusion steps (models with Steps); 0 = the model's
	bitsIndex int     // into bitDepths
	evenOut   bool    // master with services.MasterDefault
	normalize bool    // read numbers, dates… as words
	// Voice controls: a pinned seed makes takes repeatable.
	seedPinned bool
	seed       float64
	// recentLangs are the languages chosen last, newest first (codes).
	recentLangs [4]string

	script      scriptEditor
	composer    composerState
	pron        pronState // Settings → Pronunciation
	renderState           // the render the Studio follows: chunk progress, preview

	// savedDraft is the form as last handed to the draft service (or restored).
	savedDraft studioDraft
}

// newStudioState is the form of a fresh start: the settings' default
// model and autoplay (the draft, restored next, may change the model).
func newStudioState(prefs settings.Settings) studioState {
	first := catalog.Builtin().ForTask(catalog.TaskTTS)[0]
	if e, ok := catalog.Builtin().ByID(prefs.Studio.Model); ok && !e.Hidden && slices.Contains(e.Tasks, catalog.TaskTTS) {
		first = e
	}
	s := studioState{
		modelLabel:    first.Label,
		speed:         1,
		modelDefaults: true,
		temperature:   0.7,
		topP:          0.9,
		maxTokens:     1200,
		autoplay:      prefs.Studio.Autoplay,
		evenOut:       true,
		normalize:     true,
		text:          "Hello from AudioSmith. This voice was generated on your Mac with MLX.",
	}
	if v := first.VoiceIDs(); len(v) > 0 {
		s.voice = v[0]
	}
	if l := first.LanguageOptions(); len(l) > 0 {
		s.language = l[0]
	}
	s.script = newScriptEditor()
	s.savedDraft = s.toDraft()
	return s
}

// studioDraft is the Studio form as saved between runs. Version 2 added
// the output options, the seed and the recent languages.
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
	Duration      float64 `json:"duration,omitempty"`

	Steps       float64   `json:"steps,omitempty"`
	Bits        int       `json:"bits,omitempty"`
	RawVolume   bool      `json:"rawVolume,omitempty"`   // Even out volume off
	NoNormalize bool      `json:"noNormalize,omitempty"` // normalisation off
	SeedPinned  bool      `json:"seedPinned,omitempty"`
	Seed        float64   `json:"seed,omitempty"`
	Recent      [4]string `json:"recentLanguages"`
}

const studioDraftKey = "studio"

func (s *studioState) toDraft() studioDraft {
	return studioDraft{Version: 2, Text: s.text, ModelLabel: s.modelLabel, CustomRepo: s.customRepo,
		Voice: s.voice, Language: s.language, Speed: s.speed, ModelDefaults: s.modelDefaults,
		Temperature: s.temperature, TopP: s.topP, MaxTokens: s.maxTokens,
		RefAudio: s.refAudio, RefText: s.refText, Duration: s.duration,
		Steps: s.steps, Bits: bitDepths[min(max(s.bitsIndex, 0), len(bitDepths)-1)], RawVolume: !s.evenOut,
		NoNormalize: !s.normalize, SeedPinned: s.seedPinned, Seed: s.seed, Recent: s.recentLangs}
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
	s.refAudio, s.refText, s.duration = d.RefAudio, d.RefText, d.Duration
	s.steps, s.evenOut, s.normalize = d.Steps, !d.RawVolume, !d.NoNormalize
	s.bitsIndex = max(slices.Index(bitDepths, d.Bits), 0)
	s.seedPinned, s.seed, s.recentLangs = d.SeedPinned, d.Seed, d.Recent
	s.script.reset(s.text)
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

// traits are the render traits of the form's model.
func (a *app) traits() services.Traits { return modelInfo(a.preset().Repo).Traits }

// selectModel makes e the Studio's model, as its Model select does (the
// ⌘E switcher and the palette call it). The settings remember it as the
// default of the next fresh start.
func (a *app) selectModel(e catalog.Entry) {
	if a.modelLabel == e.Label {
		return
	}
	a.modelLabel = e.Label
	a.modelChanged()
}

// modelChanged resets the voice and language to the new model's defaults
// and remembers the model in the settings.
func (a *app) modelChanged() {
	p := a.preset()
	a.needDownload = nil
	if p.ID != "" && p.ID != "custom" && p.ID != a.prefs.Studio.Model {
		id := p.ID
		a.updateSettings(func(s *settings.Settings) { s.Studio.Model = id })
	}
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

// generate makes a take of the form: TakeService checks the licence and
// that the model is downloaded, and queues the job (startTake).
func (a *app) generate() {
	if !a.canGenerate() {
		return
	}
	a.startRender()
	a.startTake(a.recipe(time.Now()))
}

// recipe is the take the form describes, with a new id: only what the
// model takes is set, so its defaults apply to the rest.
func (a *app) recipe(now time.Time) core.Take {
	p := a.preset()
	tr := a.traits()
	t := core.Take{
		ID:       core.NewID(now),
		Text:     strings.TrimSpace(a.text),
		Model:    p.Repo,
		Voice:    strings.TrimSpace(a.voice),
		Language: strings.TrimSpace(a.language),
		Render: &core.TakeRender{
			Bits:        bitDepths[min(max(a.bitsIndex, 0), len(bitDepths)-1)],
			NoNormalize: !a.normalize,
		},
	}
	if t.Render.Bits == 16 {
		t.Render.Bits = 0
	}
	if a.evenOut {
		t.Render.Master = services.MasterDefault
	}
	if a.seedPinned && a.seed > 0 {
		t.Seed, t.Render.SeedPinned = int64(a.seed), true
	}
	if p.Has(catalog.CapSpeed) {
		t.Speed = a.speed
	}
	if p.Has(catalog.CapSampling) && !a.modelDefaults {
		t.Temperature, t.TopP, t.MaxTokens = a.temperature, a.topP, int(a.maxTokens)
	}
	if p.Has(catalog.CapClone) && strings.TrimSpace(a.refAudio) != "" && !a.studioUsesSavedVoice(p) {
		t.RefAudio, t.RefText = strings.TrimSpace(a.refAudio), strings.TrimSpace(a.refText)
	}
	// A voice from the library (voices.go): its reference, design, seed,
	// style, and the encoded prompt the engine prefers to the reference.
	a.applyStudioVoice(&t, p)
	t.VoicePrompt = a.studioVoicePrompt(p)
	if p.Has(catalog.CapDuration) && a.duration > 0 {
		t.Duration = a.duration
	}
	if tr.Steps && a.steps > 0 {
		t.Steps = int(a.steps)
	}
	return t
}

// reuse copies a take's whole recipe back into the form: text, model,
// voice, language, parameters, seed (pinned) and output options.
func (a *app) reuse(t *core.Take) {
	a.text = t.Text
	a.script.set(t.Text)
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
	// The library voice it was spoken with, if it still exists.
	a.studioVoice, a.studioClip = "", nil
	if v := a.findVoice(t.VoiceID); v != nil {
		a.studioVoice = v.ID
		a.refAudio, a.refText = "", ""
	}
	a.duration, a.steps = t.Duration, float64(t.Steps)
	a.seedPinned, a.seed = t.Seed != 0, float64(t.Seed)
	if r := t.Render; r != nil {
		a.bitsIndex = max(slices.Index(bitDepths, r.Bits), 0)
		a.evenOut = r.Master != "" && r.Master != "raw"
		a.normalize = !r.NoNormalize
	} else {
		a.bitsIndex, a.evenOut, a.normalize = 0, false, true
	}
	a.showPage("studio")
}

// newSeed pins a new random seed.
func (a *app) newSeed() {
	n, _ := rand.Int(rand.Reader, big.NewInt(1<<31-2))
	a.seed, a.seedPinned = float64(n.Int64()+1), true
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

// snapped rounds a slider's value to a step, so labels and requests show
// 1.05 rather than 1.0473; the slider moves smoothly in between.
func snapped(e ui.Element, v *float64, step float64) ui.Element {
	if e.Changed() {
		*v = float64(int(*v/step+0.5)) * step
	}
	return e
}
