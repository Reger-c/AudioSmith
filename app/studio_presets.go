package main

// The Studio's recommended settings (the catalogue's presets, thread
// #41): picking a model fills the form with its tuned parameters and the
// user's changes for that model (settings.Studio.Overrides); the form's
// 0 means "recommended", which the services fill in (FillDefaults). A new
// user gets the model the presets choose for their language, which
// follows their saved voices until they choose a model themselves.

import (
	"slices"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// loadPreset fills the form for model e: its default voice for the
// interface's language, the language left to detection, and the user's
// changes ov (0 = recommended).
func (s *studioState) loadPreset(e catalog.Entry, ov settings.ModelParams) {
	pr, hasPreset := catalog.PresetFor(e.ID)
	s.voice, s.language = "", ""
	ids := e.VoiceIDsIn(i18n.Locale())
	if v := pr.VoiceFor(i18n.Locale()); v != "" && slices.Contains(ids, v) {
		s.voice = v
	} else if len(ids) > 0 {
		s.voice = ids[0]
	}
	if l := pr.Defaults.Language; l != "" && l != "auto" {
		s.language = l
	} else if !hasPreset {
		// No preset: the model's first language, as before (a model
		// without "auto").
		if l := e.LanguageOptions(); len(l) > 0 {
			s.language = l[0]
		}
	}
	s.temperature, s.topP = ov.Temperature, ov.TopP
	s.steps, s.guidance = float64(ov.Steps), ov.Guidance
	s.speed = 1
	if ov.Speed > 0 {
		s.speed = ov.Speed
	}
}

// recommended are the form's model's recommended parameters.
func (a *app) recommended() catalog.Params {
	pr, _ := catalog.PresetFor(a.preset().ID)
	return pr.ParamsFor(catalog.WorkflowStudio)
}

// studioOverride is what the form changes from the model's
// recommendation (a value equal to the recommended one is no change).
func (a *app) studioOverride() settings.ModelParams {
	rec := a.recommended()
	diff := func(v float64, r *float64) float64 {
		if r != nil && v == *r {
			return 0
		}
		return v
	}
	o := settings.ModelParams{
		Temperature: diff(a.temperature, rec.Temperature), TopP: diff(a.topP, rec.TopP),
		Guidance: diff(a.guidance, rec.Guidance),
	}
	if a.steps > 0 && (rec.Steps == nil || int(a.steps) != *rec.Steps) {
		o.Steps = int(a.steps)
	}
	if a.speed > 0 && a.speed != 1 {
		o.Speed = a.speed
	}
	return o
}

// syncOverrides keeps the settings' changes for the form's model in step
// with the form (autosaveStudio calls it after each frame's edits).
func (a *app) syncOverrides() {
	p := a.preset()
	if p.ID == "" || p.ID == "custom" {
		return
	}
	o := a.studioOverride()
	if o == a.prefs.Studio.Overrides[p.ID] {
		return
	}
	id := p.ID
	a.updateSettings(func(s *settings.Settings) {
		if o == (settings.ModelParams{}) {
			delete(s.Studio.Overrides, id)
			return
		}
		if s.Studio.Overrides == nil {
			s.Studio.Overrides = map[string]settings.ModelParams{}
		}
		s.Studio.Overrides[id] = o
	})
}

// studioChanges names what the form changes from the recommendation:
// the model's parameters, a pinned seed, a target duration, the output
// options turned off ("已改 N 项").
func (a *app) studioChanges() []string {
	p := a.preset()
	o := a.studioOverride()
	var out []string
	add := func(on bool, name string) {
		if on {
			out = append(out, name)
		}
	}
	add(o.Speed != 0 && p.Has(catalog.CapSpeed), i18n.T("Speed"))
	add(o.Temperature != 0 && p.Has(catalog.CapSampling), i18n.T("Temperature"))
	add(o.TopP != 0 && p.Has(catalog.CapSampling), i18n.T("Top P"))
	add(o.Steps != 0 && a.traits().Steps, i18n.T("Steps"))
	add(o.Guidance != 0 && p.Has(catalog.CapGuidance), i18n.T("Prompt guidance"))
	add(a.duration > 0 && p.Has(catalog.CapDuration), i18n.T("Duration"))
	add(a.seedPinned, i18n.T("Seed"))
	add(!a.evenOut, i18n.T("Even out volume"))
	add(!a.normalize, i18n.T("Read numbers as words"))
	return out
}

// restoreRecommended clears the form's changes: the model's recommended
// parameters, a free seed, the natural length, the output options on.
func (a *app) restoreRecommended() {
	a.temperature, a.topP, a.steps, a.guidance = 0, 0, 0, 0
	a.speed, a.duration, a.seedPinned = 1, 0, false
	a.evenOut, a.normalize = true, true
}

// shown is a parameter as a control shows it: the form's value, else the
// recommended one, else the model's usual one (def).
func shown(v float64, rec *float64, def float64) float64 {
	switch {
	case v > 0:
		return v
	case rec != nil:
		return *rec
	}
	return def
}

// shownSteps is the form's steps as the controls show them.
func (a *app) shownSteps() float64 {
	if a.steps > 0 {
		return a.steps
	}
	if r := a.recommended().Steps; r != nil {
		return float64(*r)
	}
	return services.StepsBalanced
}

// startAutoModel is called once the draft was looked for: without a
// draft and without a model in the settings the user is new, and gets
// the presets' model for the interface's language.
func (a *app) startAutoModel(hadDraft bool) {
	if hadDraft || a.prefs.Studio.Model != "" {
		return
	}
	a.studioAuto = true
	a.autoStudioModel()
}

// autoStudioModel picks a new user's Studio model: by the interface's
// language and whether they have saved voices (Chinese: Qwen3-TTS
// CustomVoice, then 1.7B Base speaking their newest voice once they have
// one; English: Kokoro). The settings are not written: it stays
// automatic until the user picks a model.
func (a *app) autoStudioModel() {
	if !a.studioAuto {
		return
	}
	saved := a.savedVoices()
	e, ok := catalog.Builtin().DefaultModel(catalog.WorkflowStudio, i18n.Locale(), len(saved) > 0)
	if !ok {
		return
	}
	if a.modelLabel != e.Label {
		a.modelLabel = e.Label
		a.needDownload = nil
		a.loadPreset(e, a.prefs.Studio.Overrides[e.ID])
	}
	if a.studioVoice == "" {
		for _, v := range saved {
			if ok, _ := voiceUsable(v, e); ok && v.Kind != core.VoiceBuiltin {
				a.studioVoice = v.ID
				break
			}
		}
	}
}

// savedVoices are the library's voices the user made (not built-in
// speakers), newest first.
func (a *app) savedVoices() []*core.Voice {
	var out []*core.Voice
	for i := range a.voices {
		if a.voices[i].Kind != core.VoiceBuiltin {
			out = append(out, &a.voices[i])
		}
	}
	slices.SortStableFunc(out, func(x, y *core.Voice) int { return y.CreatedAt.Compare(x.CreatedAt) })
	return out
}
