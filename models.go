package main

// modelPreset is a TTS checkpoint AudioSmith offers by name. Any other
// Hugging Face repo that mlx-audio-swift loads works through "Custom".
type modelPreset struct {
	Label string // shown in the model picker
	Repo  string // Hugging Face repo id
	About string
	Size  string // rough download size

	Voices       []string // suggested voices; the first is the default
	Languages    []string // suggested language values; "" means auto
	Speed        bool     // honors the speed slider (Kokoro)
	Sampling     bool     // honors temperature / top-p / max tokens
	RefAudio     bool     // can clone a voice from reference audio
	VoiceIsStyle bool     // "voice" is a free-form speaker / style prompt
}

const customModelLabel = "Custom Hugging Face repo…"

var modelPresets = []modelPreset{
	{
		Label: "Kokoro 82M",
		Repo:  "mlx-community/Kokoro-82M-bf16",
		About: "Fast, light, 54 voices in 9 languages. Language follows the voice prefix (af_ = US English, zf_ = Chinese…).",
		Size:  "≈ 390 MB",
		Voices: []string{
			"af_heart", "af_bella", "af_nicole", "af_sarah", "af_sky", "am_adam", "am_michael", "am_puck",
			"bf_emma", "bf_isabella", "bm_george", "bm_lewis",
			"zf_xiaobei", "zf_xiaoni", "zf_xiaoxiao", "zf_xiaoyi", "zm_yunjian", "zm_yunxi",
			"jf_alpha", "jm_kumo", "ef_dora", "ff_siwis", "if_sara", "pf_dora", "hf_alpha",
		},
		Speed: true,
	},
	{
		Label:        "Qwen3-TTS 0.6B CustomVoice",
		Repo:         "mlx-community/Qwen3-TTS-12Hz-0.6B-CustomVoice-8bit",
		About:        "Chinese and English with named speakers. The voice field also takes a style, e.g. \"Vivian, calm and warm\".",
		Size:         "≈ 2 GB",
		Voices:       []string{"Vivian", "Serena", "Uncle_Fu", "Dylan", "Eric", "Ryan", "Aiden"},
		Languages:    []string{"Chinese", "English", "Japanese", "Korean", "German", "French", "Spanish", "Italian", "Portuguese", "Russian"},
		Sampling:     true,
		VoiceIsStyle: true,
	},
	{
		Label:     "Qwen3-TTS 0.6B Base (voice clone)",
		Repo:      "mlx-community/Qwen3-TTS-12Hz-0.6B-Base-8bit",
		About:     "Clones the voice of a reference clip; give its transcript too for best results.",
		Size:      "≈ 2 GB",
		Languages: []string{"Chinese", "English", "Japanese", "Korean", "German", "French", "Spanish", "Italian", "Portuguese", "Russian"},
		Sampling:  true,
		RefAudio:  true,
	},
	{
		Label:    "Marvis TTS 250M",
		Repo:     "Marvis-AI/marvis-tts-250m-v0.2-MLX-8bit",
		About:    "English conversational voices; clones a voice from reference audio.",
		Size:     "≈ 670 MB",
		Voices:   []string{"conversational_a", "conversational_b"},
		Sampling: true,
		RefAudio: true,
	},
	{
		Label:    "Soprano 80M",
		Repo:     "mlx-community/Soprano-80M-bf16",
		About:    "Tiny English model, very fast.",
		Size:     "≈ 220 MB",
		Sampling: true,
	},
}

func presetLabels() []string {
	labels := make([]string, 0, len(modelPresets)+1)
	for _, p := range modelPresets {
		labels = append(labels, p.Label)
	}
	return append(labels, customModelLabel)
}

func presetByLabel(label string) (modelPreset, bool) {
	for _, p := range modelPresets {
		if p.Label == label {
			return p, true
		}
	}
	return modelPreset{}, false
}

func presetByRepo(repo string) (modelPreset, bool) {
	for _, p := range modelPresets {
		if p.Repo == repo {
			return p, true
		}
	}
	return modelPreset{}, false
}

// customPreset describes an unknown repo: every control is offered, since
// we cannot tell which ones the model reads.
func customPreset(repo string) modelPreset {
	return modelPreset{
		Label:    customModelLabel,
		Repo:     repo,
		About:    "Any TTS repo mlx-audio-swift supports (it reads model_type from config.json).",
		Sampling: true,
		RefAudio: true,
		Speed:    true,
	}
}
