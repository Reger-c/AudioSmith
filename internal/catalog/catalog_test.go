package catalog

import (
	"slices"
	"strings"
	"testing"
)

// TestBuiltin pins the five MVP presets, now data: the pickers must offer
// exactly what the hard-coded presets offered.
func TestBuiltin(t *testing.T) {
	c := Builtin()
	want := []struct {
		label, repo, size string
		voices, languages []string
		speed, sampling   bool
		clone, style      bool
	}{
		{"Kokoro 82M", "mlx-community/Kokoro-82M-bf16", "≈ 481 MB", nil, nil, true, false, false, false},
		{"Qwen3-TTS 0.6B CustomVoice", "mlx-community/Qwen3-TTS-12Hz-0.6B-CustomVoice-8bit", "≈ 2 GB",
			[]string{"Vivian", "Serena", "Uncle_Fu", "Dylan", "Eric", "Ryan", "Aiden"},
			[]string{"Chinese", "English", "Japanese", "Korean", "German", "French", "Spanish", "Italian", "Portuguese", "Russian"},
			false, true, false, true},
		{"Qwen3-TTS 0.6B Base (voice clone)", "mlx-community/Qwen3-TTS-12Hz-0.6B-Base-8bit", "≈ 2 GB", nil,
			[]string{"Chinese", "English", "Japanese", "Korean", "German", "French", "Spanish", "Italian", "Portuguese", "Russian"},
			false, true, true, false},
		{"Marvis TTS 250M", "Marvis-AI/marvis-tts-250m-v0.2-MLX-8bit", "≈ 672 MB", []string{"conversational_a", "conversational_b"}, nil, false, true, true, false},
		{"Soprano 80M", "mlx-community/Soprano-80M-bf16", "≈ 221 MB", nil, nil, false, true, false, false},
	}
	labels := c.Labels(TaskTTS)
	if len(labels) != len(want)+1 || labels[len(labels)-1] != CustomLabel {
		t.Fatalf("labels %q", labels)
	}
	for i, w := range want {
		e, ok := c.ByLabel(w.label)
		if !ok || labels[i] != w.label || e.Repo != w.repo || HumanSize(e.Size) != w.size {
			t.Errorf("%s: %+v (size %q)", w.label, e, HumanSize(e.Size))
			continue
		}
		if w.voices != nil && !slices.Equal(e.VoiceIDs(), w.voices) {
			t.Errorf("%s voices %q", w.label, e.VoiceIDs())
		}
		if !slices.Equal(e.LanguageOptions(), w.languages) {
			t.Errorf("%s languages %q", w.label, e.LanguageOptions())
		}
		if e.Has(CapSpeed) != w.speed || e.Has(CapSampling) != w.sampling || e.Has(CapClone) != w.clone || e.Has(CapVoiceStyle) != w.style {
			t.Errorf("%s capabilities %v", w.label, e.Capabilities)
		}
		if r, ok := c.ByRepo(w.repo); !ok || r.ID != e.ID {
			t.Errorf("ByRepo(%s)", w.repo)
		}
		if id, ok := c.ByID(e.ID); !ok || id.Repo != w.repo {
			t.Errorf("ByID(%s)", e.ID)
		}
	}
	if k, _ := c.ByLabel("Kokoro 82M"); len(k.Voices) != 25 || k.VoiceIDs()[0] != "af_heart" || len(k.Aux) != 2 {
		t.Errorf("kokoro voices %d aux %v", len(k.Voices), k.Aux)
	}
	for _, e := range c.Entries {
		if !e.Licence.Verified {
			t.Errorf("%s: licence not verified against the upstream model card", e.ID)
		}
	}
}

// TestOmniVoice: the bf16 checkpoint only (the quantized ones crash the
// pinned engine), non-commercial and gated behind acceptance, and hidden
// from pickers until the generate path checks licences.
func TestOmniVoice(t *testing.T) {
	c := Builtin()
	e, ok := c.ByID("omnivoice-bf16")
	if !ok || e.Repo != "mlx-community/OmniVoice-bf16" || !e.Hidden || e.Licence.Commercial || !e.Licence.RequiresAcceptance || e.Licence.ID != "cc-by-nc-4.0" {
		t.Fatalf("%+v", e)
	}
	if !e.Has(CapClone) || !e.Has(CapDesign) || !e.Has(CapDuration) {
		t.Errorf("capabilities %v", e.Capabilities)
	}
	for _, x := range c.ForTask(TaskTTS) {
		if x.ID == e.ID {
			t.Error("a hidden entry is offered")
		}
	}
	if slices.Contains(c.Labels(TaskTTS), e.Label) {
		t.Error("a hidden entry is in the picker")
	}
	for _, x := range c.Entries {
		if strings.Contains(strings.ToLower(x.Repo), "omnivoice") && x.Quantization != "bf16" {
			t.Errorf("%s: only OmniVoice-bf16 loads", x.Repo)
		}
	}
}

func TestCustom(t *testing.T) {
	e := Custom("me/model")
	if e.Repo != "me/model" || !e.Has(CapSpeed) || !e.Has(CapSampling) || !e.Has(CapClone) || e.Has(CapVoiceStyle) {
		t.Errorf("%+v", e)
	}
	if len(e.LanguageOptions()) != 0 || len(e.VoiceIDs()) != 0 {
		t.Errorf("custom offers no suggestions: %+v", e)
	}
}

func TestValidate(t *testing.T) {
	for name, data := range map[string]string{
		"version":    `{"version":0,"entries":[]}`,
		"dup repo":   `{"version":1,"entries":[{"id":"a","label":"A","family":"f","repo":"r","tasks":["tts"],"licence":{"id":"x","name":"X","commercial":true}},{"id":"b","label":"B","family":"f","repo":"r","tasks":["tts"],"licence":{"id":"x","name":"X","commercial":true}}]}`,
		"task":       `{"version":1,"entries":[{"id":"a","label":"A","family":"f","repo":"r","tasks":["sing"],"licence":{"id":"x","name":"X","commercial":true}}]}`,
		"capability": `{"version":1,"entries":[{"id":"a","label":"A","family":"f","repo":"r","tasks":["tts"],"capabilities":["fly"],"licence":{"id":"x","name":"X","commercial":true}}]}`,
		"licence":    `{"version":1,"entries":[{"id":"a","label":"A","family":"f","repo":"r","tasks":["tts"]}]}`,
		"aux":        `{"version":1,"entries":[{"id":"a","label":"A","family":"f","repo":"r","tasks":["tts"],"aux":[{"repo":"r"}],"licence":{"id":"x","name":"X","commercial":true}}]}`,
		"nc gate":    `{"version":1,"entries":[{"id":"a","label":"A","family":"f","repo":"r","tasks":["tts"],"licence":{"id":"cc-by-nc-4.0","name":"CC BY-NC 4.0","commercial":false}}]}`,
	} {
		if _, err := Parse([]byte(data)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	ok := `{"version":1,"entries":[{"id":"a","label":"A","family":"f","repo":"r","tasks":["tts"],"licence":{"id":"cc-by-nc-4.0","name":"CC BY-NC 4.0","commercial":false,"requiresAcceptance":true}}]}`
	if _, err := Parse([]byte(ok)); err != nil {
		t.Error(err)
	}
}

func TestHumanSize(t *testing.T) {
	for in, want := range map[int64]string{0: "", 220e6: "≈ 220 MB", 1_500_000_000: "≈ 1.5 GB", 2e9: "≈ 2 GB", 999_600_000: "≈ 1000 MB"} {
		if got := HumanSize(in); got != want {
			t.Errorf("HumanSize(%d) = %q, want %q", in, got, want)
		}
	}
	if !strings.HasPrefix(HumanSize(12e9), "≈ 12") {
		t.Error(HumanSize(12e9))
	}
}

func TestNames(t *testing.T) {
	c := Builtin()
	if got := c.DisplayName("mlx-community/Kokoro-82M-bf16"); got != "Kokoro 82M" {
		t.Error(got)
	}
	if got := c.DisplayName("someone/My-TTS"); got != "My-TTS" {
		t.Error(got)
	}
	if got := ShortRepo("plain"); got != "plain" {
		t.Error(got)
	}
}
