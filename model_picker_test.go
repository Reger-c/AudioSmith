package main

import (
	"strings"
	"testing"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/dub"
)

// TestModelPicker: the Studio's picker lists the recommended models for
// the interface's language first, with their "best for" and hints; the
// others come with "Show all models"; picking one applies it; retired
// models are not offered.
func TestModelPicker(t *testing.T) {
	a, _, tt := newTestApp(t)
	tt.Frame()
	a.pickerOpen = map[string]bool{"model": true}
	tt.Frame()
	texts := strings.Join(tt.Texts(), "|")
	for _, want := range []string{"Recommended", "English: fastest and most natural built-in voices", "Speed ●●● · Quality ●●●", "Chatterbox Turbo", "Show all models (7)"} {
		if !strings.Contains(texts, want) {
			t.Errorf("picker lacks %q: %s", want, texts)
		}
	}
	if strings.Contains(texts, "Marvis TTS 250M") {
		t.Error("the others show before Show all models")
	}
	tt.Click("Show all models (7)")
	tt.Frame()
	texts = strings.Join(tt.Texts(), "|")
	for _, l := range []string{"Marvis TTS 250M", "OmniVoice", "Custom Hugging Face repo…"} {
		if !strings.Contains(texts, l) {
			t.Errorf("all models lack %q", l)
		}
	}
	for _, id := range []string{"kitten-tts-mini-0.8", "moss-tts-nano-100m", "chatterbox-8bit"} {
		if strings.Contains(texts, entry(t, id).Label+"|") {
			t.Errorf("retired %s offered", id)
		}
	}
	if err := tt.Click("Qwen3-TTS 1.7B Base (voice clone)"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.preset().ID != "qwen3-tts-1.7b-base-8bit" || a.pickerOpen["model"] {
		t.Errorf("picked %s, open %v", a.preset().ID, a.pickerOpen["model"])
	}
}

// TestModelPickerDub: the dub's picker greys out the models that can't
// clone, with the reason.
func TestModelPickerDub(t *testing.T) {
	d := newDubTest(t, 3)
	a, tt := d.a, d.tt
	a.dubSourceFile(d.src)
	a.dubStep = dubStepGenerate
	a.dubSide.set, a.dubSide.open = true, true
	a.pickerOpen = map[string]bool{"gen-model": true}
	a.pickerAll = map[string]bool{"gen-model": true}
	tt.Frame()
	tt.Frame()
	texts := strings.Join(tt.Texts(), "|")
	if !strings.Contains(texts, "Can't clone: the dub won't sound like the speakers") || !strings.Contains(texts, "Long narration and dubbing with a saved voice, about real time") {
		t.Errorf("dub picker: %s", texts)
	}
	if err := tt.Click("Kokoro 82M"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.dub.Settings.Model == entry(t, "kokoro-82m-bf16").Repo {
		t.Error("a model that can't clone was picked")
	}
	_ = dub.TimingSmart
}

// TestRetiredModelHint: a page on a retired model says what replaces it,
// and Switch moves to it.
func TestRetiredModelHint(t *testing.T) {
	a, _, tt := newTestApp(t)
	a.modelLabel = entry(t, "kitten-tts-mini-0.8").Label
	a.modelChanged()
	tt.Frame()
	if !tt.HasText("KittenTTS mini is retired: it misread too much in our tests. Kokoro 82M is better.") {
		t.Fatalf("no hint: %q", tt.Texts())
	}
	if err := tt.Click("Switch"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.preset().ID != "kokoro-82m-bf16" {
		t.Errorf("switched to %s", a.preset().ID)
	}
	_ = catalog.Builtin
}
