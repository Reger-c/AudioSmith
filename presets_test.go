package main

import (
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
)

// TestPresetDefaults: a take that sets no sampling gets the model's
// tuned parameters (thread #41): Qwen3-TTS 0.7 / 0.8, OmniVoice guidance
// 1.5 and 32 steps (16 for dubbing); what the recipe sets wins; models
// without a preset (Kokoro's sampling, Marvis) get nothing.
func TestPresetDefaults(t *testing.T) {
	a, eng, _ := newTestApp(t)
	acceptLicences(t, a)
	gen := func(id string, edit func(*core.Take)) core.Take {
		t.Helper()
		a.job, a.jobError = nil, ""
		a.selectModel(entry(t, id))
		a.text = "Hello there."
		a.modelDefaults = true
		r := a.recipe(time.Now())
		if edit != nil {
			edit(&r)
		}
		a.startTake(r)
		req := eng.last(t)
		eng.cancelled(req.ID)
		sp := req.SynthesizeParams
		var got core.Take
		if sp.Temperature != nil {
			got.Temperature = *sp.Temperature
		}
		if sp.TopP != nil {
			got.TopP = *sp.TopP
		}
		if sp.Guidance != nil {
			got.Guidance = *sp.Guidance
		}
		got.Steps = sp.Steps
		return got
	}
	if g := gen("qwen3-tts-0.6b-customvoice-8bit", nil); g.Temperature != 0.7 || g.TopP != 0.8 {
		t.Errorf("CustomVoice %+v", g)
	}
	if g := gen("qwen3-tts-1.7b-base-8bit", func(r *core.Take) { r.Temperature = 0.95 }); g.Temperature != 0.95 || g.TopP != 0.8 {
		t.Errorf("1.7B Base with a temperature %+v", g)
	}
	if g := gen("omnivoice-bf16", nil); g.Guidance != 1.5 || g.Steps != 32 {
		t.Errorf("OmniVoice %+v", g)
	}
	if g := gen("marvis-tts-250m-v0.2-8bit", nil); g.Temperature != 0 || g.TopP != 0 {
		t.Errorf("Marvis %+v", g)
	}
	info := modelInfo(entry(t, "omnivoice-bf16").Repo)
	var dubTake core.Take
	info.FillDefaults(&dubTake, "dub")
	if dubTake.Steps != 16 || dubTake.Guidance != 1.5 {
		t.Errorf("OmniVoice dub %+v", dubTake)
	}
}
