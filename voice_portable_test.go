package main

import (
	"strings"
	"testing"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/services"
)

// TestPortableSpeaker: a built-in speaker made portable is said by its
// model (the portable line, evened out) and saved as a cloned voice
// "<name> (approximate)" that the models that clone can speak with.
func TestPortableSpeaker(t *testing.T) {
	a, eng, tt := newTestApp(t)
	cv := entry(t, "qwen3-tts-0.6b-customvoice-8bit")
	a.makeSpeakerPortable(cv, "Serena")
	r := eng.last(t)
	p := renderOf(t, r)
	if p.Take.Model != cv.Repo || p.Take.Voice != "Serena" || !strings.Contains(p.Take.Text, "clon") || p.Take.Render == nil || p.Take.Render.Master != services.MasterDefault {
		t.Fatalf("render %+v", p.Take)
	}
	finishRender(t, eng, r, 10)
	tt.Frame()
	var v *core.Voice
	for i := range a.voices {
		if isPortable(&a.voices[i]) {
			v = &a.voices[i]
		}
	}
	if v == nil || v.Name != "Serena (approximate)" || v.Kind != core.VoiceCloned || v.RefAudio == "" || v.RefText != services.PortableLine("en") {
		t.Fatalf("portable voice %+v", v)
	}
	base := entry(t, "qwen3-tts-1.7b-base-8bit")
	if ok, why := voiceUsable(v, base); !ok {
		t.Errorf("not usable on 1.7B Base: %s", why)
	}
	if voiceKindLabel(v) != "Portable copy (approximate clone)" {
		t.Errorf("kind %q", voiceKindLabel(v))
	}
}

// TestPortableHints: a saved built-in voice on a model that clones says
// how to use it there, and the Studio offers Make portable; a designed
// voice with a sample becomes portable without rendering.
func TestPortableHints(t *testing.T) {
	a, eng, tt := newTestApp(t)
	kok := entry(t, "kokoro-82m-bf16")
	v := addVoice(t, a, core.Voice{Name: "Heart", Kind: core.VoiceBuiltin, Model: kok.Repo, Speaker: "af_heart", Language: "en"}, 0)
	base := entry(t, "qwen3-tts-1.7b-base-8bit")
	if ok, why := voiceUsable(&v, base); ok || !strings.Contains(why, "portable copy") {
		t.Errorf("usable %v: %q", ok, why)
	}
	a.selectModel(base)
	a.studioVoice = v.ID
	tt.Frame()
	if err := tt.Click("Make portable"); err != nil {
		t.Fatalf("%v: %q", err, tt.Texts())
	}
	if p := renderOf(t, eng.last(t)); p.Take.Model != kok.Repo || p.Take.Voice != "af_heart" {
		t.Errorf("render %+v", p.Take)
	}
	_ = catalog.Builtin
}
