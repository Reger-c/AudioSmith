package catalog

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestBuiltin pins the MVP presets still offered, now data: the pickers
// must offer them in order, then OmniVoice (M0), then what M1 added; the
// seven speech models thread #43 kept.
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
	}
	labels := c.Labels(TaskTTS)
	// The MVP presets, then OmniVoice (M0), then what M1 added, then Custom.
	if len(labels) < len(want)+2 || labels[len(want)] != "OmniVoice" || labels[len(labels)-1] != CustomLabel {
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
	if got := c.ForTask(TaskTTS); len(got) != 7 {
		t.Errorf("%d speech models offered, want 7", len(got))
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
// pinned engine), non-commercial and gated behind acceptance (the
// services and the engine executors check it), offered since the engine
// runs it end to end (TestModelLifecycle on the Mac).
func TestOmniVoice(t *testing.T) {
	c := Builtin()
	e, ok := c.ByID("omnivoice-bf16")
	if !ok || e.Repo != "mlx-community/OmniVoice-bf16" || e.Hidden || e.Licence.Commercial || !e.Licence.RequiresAcceptance || e.Licence.ID != "cc-by-nc-4.0" {
		t.Fatalf("%+v", e)
	}
	if !e.Has(CapClone) || !e.Has(CapDesign) || !e.Has(CapDuration) || !e.Has(CapSpeed) {
		t.Errorf("capabilities %v", e.Capabilities)
	}
	if !slices.Contains(c.Labels(TaskTTS), e.Label) {
		t.Error("OmniVoice is not in the picker")
	}
	for _, x := range c.Entries {
		if strings.Contains(strings.ToLower(x.Repo), "omnivoice") && x.Quantization != "bf16" {
			t.Errorf("%s: only OmniVoice-bf16 loads", x.Repo)
		}
	}
	// Hidden entries stay out of pickers.
	h := *c
	h.Entries = slices.Clone(c.Entries)
	for i := range h.Entries {
		if h.Entries[i].ID == e.ID {
			h.Entries[i].Hidden = true
		}
	}
	if slices.Contains(h.Labels(TaskTTS), e.Label) || slices.ContainsFunc(h.ForTask(TaskTTS), func(x Entry) bool { return x.ID == e.ID }) {
		t.Error("a hidden entry is offered")
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
		"dup repo":   `{"version":1,"entries":[{"id":"a","label":"A","name":"Aa","family":"f","repo":"r","tasks":["tts"],"licence":{"id":"x","name":"X","commercial":true}},{"id":"b","label":"B","name":"Bb","family":"f","repo":"r","tasks":["tts"],"licence":{"id":"x","name":"X","commercial":true}}]}`,
		"task":       `{"version":1,"entries":[{"id":"a","label":"A","name":"Aa","family":"f","repo":"r","tasks":["sing"],"licence":{"id":"x","name":"X","commercial":true}}]}`,
		"capability": `{"version":1,"entries":[{"id":"a","label":"A","name":"Aa","family":"f","repo":"r","tasks":["tts"],"capabilities":["fly"],"licence":{"id":"x","name":"X","commercial":true}}]}`,
		"licence":    `{"version":1,"entries":[{"id":"a","label":"A","name":"Aa","family":"f","repo":"r","tasks":["tts"]}]}`,
		"aux":        `{"version":1,"entries":[{"id":"a","label":"A","name":"Aa","family":"f","repo":"r","tasks":["tts"],"aux":[{"repo":"r"}],"licence":{"id":"x","name":"X","commercial":true}}]}`,
		"no name":    `{"version":1,"entries":[{"id":"a","label":"A","family":"f","repo":"r","tasks":["tts"],"licence":{"id":"x","name":"X","commercial":true}}]}`,
		"dup name":   `{"version":1,"entries":[{"id":"a","label":"A","name":"Same","family":"f","repo":"r","tasks":["tts"],"licence":{"id":"x","name":"X","commercial":true}},{"id":"b","label":"B","name":"same","family":"f","repo":"r2","tasks":["tts"],"licence":{"id":"x","name":"X","commercial":true}}]}`,
		"tag":        `{"version":1,"entries":[{"id":"a","label":"A","name":"Aa","tags":["shiny"],"family":"f","repo":"r","tasks":["tts"],"licence":{"id":"x","name":"X","commercial":true}}]}`,
		"computed":   `{"version":1,"entries":[{"id":"a","label":"A","name":"Aa","tags":["recommended"],"family":"f","repo":"r","tasks":["tts"],"licence":{"id":"x","name":"X","commercial":true}}]}`,
		"repeated":   `{"version":1,"entries":[{"id":"a","label":"A","name":"Aa","tags":["fast","fast"],"family":"f","repo":"r","tasks":["tts"],"licence":{"id":"x","name":"X","commercial":true}}]}`,
		"nc gate":    `{"version":1,"entries":[{"id":"a","label":"A","name":"Aa","family":"f","repo":"r","tasks":["tts"],"licence":{"id":"cc-by-nc-4.0","name":"CC BY-NC 4.0","commercial":false}}]}`,
	} {
		if _, err := Parse([]byte(data)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	ok := `{"version":1,"entries":[{"id":"a","label":"A","name":"Aa","family":"f","repo":"r","tasks":["tts"],"licence":{"id":"cc-by-nc-4.0","name":"CC BY-NC 4.0","commercial":false,"requiresAcceptance":true}}]}`
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
	if got := c.DisplayName("mlx-community/Kokoro-82M-bf16"); got != "Fast English voices" {
		t.Error(got)
	}
	if got := c.TechnicalName("mlx-community/Kokoro-82M-bf16"); got != "Kokoro 82M" {
		t.Error(got)
	}
	if got := c.DisplayName("someone/My-TTS"); got != "My-TTS" {
		t.Error(got)
	}
	if got := ShortRepo("plain"); got != "plain" {
		t.Error(got)
	}
}

// Every entry and auxiliary repo pins a commit.
func TestRevisionsArePinned(t *testing.T) {
	commit := regexp.MustCompile(`^[0-9a-f]{40}$`)
	for _, e := range Builtin().Entries {
		if !commit.MatchString(e.Revision) {
			t.Errorf("%s: revision %q is not a commit", e.ID, e.Revision)
		}
		for _, a := range e.Aux {
			if !commit.MatchString(a.Revision) {
				t.Errorf("%s: aux %s: revision %q is not a commit", e.ID, a.Repo, a.Revision)
			}
		}
	}
}

func TestUnknownLanguageCode(t *testing.T) {
	c := *Builtin()
	c.Entries = slices.Clone(c.Entries)
	e := c.Entries[0]
	e.Languages = []Language{{Code: "xx-nonsense"}}
	c.Entries[0] = e
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "xx-nonsense") {
		t.Errorf("validate: %v", err)
	}
}

func TestM1Entries(t *testing.T) {
	c := Builtin()
	for id, task := range map[string]Task{DefaultSTT: TaskSTT, DefaultAlign: TaskAlign, DefaultVAD: TaskVAD, DefaultEnhance: TaskEnhance, DefaultDictation: TaskSTT, DefaultSeparate: TaskSeparate, DefaultDiarize: TaskDiarize, DefaultLLM: TaskLLM} {
		e, ok := c.ByID(id)
		if !ok || !slices.Contains(e.Tasks, task) || e.SampleRate == 0 && task != TaskLLM {
			t.Errorf("default %s: %+v", id, e)
		}
	}
	if e, _ := c.ByID(DefaultAlign); !e.Has(CapWordTimes) {
		t.Error("the aligner times words")
	}
	if e, _ := c.ByID("kokoro-82m-bf16"); e.MaxChars == 0 {
		t.Error("kokoro has a per-request limit")
	}
	if n := TextLength("Hi 你好"); n != 9 {
		t.Errorf("TextLength = %d", n)
	}
}

// TestNeedsReference: every speech model kept speaks without a
// reference: its own voices (Kokoro, CustomVoice), a default voice
// (Qwen3-TTS Base, Chatterbox Turbo's conds.safetensors), or a design
// (OmniVoice, VoiceDesign). The flag stays for models that only clone
// (MOSS-TTS-Nano was one); it needs the clone capability.
func TestNeedsReference(t *testing.T) {
	var got []string
	for _, e := range Builtin().Entries {
		if e.NeedsReference {
			got = append(got, e.ID)
		}
	}
	if len(got) != 0 {
		t.Errorf("needsReference: %q", got)
	}
	c := Catalog{Version: 1, Entries: []Entry{{ID: "x", Label: "X", Name: "Xx", Family: "x", Repo: "o/x", Tasks: []Task{TaskTTS},
		NeedsReference: true, Licence: Licence{ID: "mit", Name: "MIT", Commercial: true}}}}
	if err := c.Validate(); err == nil {
		t.Error("needsReference without clone validated")
	}
	c.Entries[0].Capabilities = []Capability{CapClone}
	if err := c.Validate(); err != nil {
		t.Error(err)
	}
}

// TestPresets: presets.json (thread #41) names live speech models, every
// workflow recommends 1–3 per language, and new users get what main
// decided on 2026-10-15.
func TestPresets(t *testing.T) {
	c := Builtin()
	p := BuiltinPresets()
	if err := p.Validate(c); err != nil {
		t.Fatal(err)
	}
	ids := func(es []Entry) []string {
		var out []string
		for _, e := range es {
			out = append(out, e.ID)
		}
		return out
	}
	for _, w := range []struct {
		w    Workflow
		lang string
		want []string
	}{
		{WorkflowStudio, "zh-Hans", []string{"qwen3-tts-0.6b-customvoice-8bit", "qwen3-tts-1.7b-base-8bit"}},
		{WorkflowStudio, "en", []string{"kokoro-82m-bf16", "qwen3-tts-1.7b-base-8bit", "chatterbox-turbo-8bit"}},
		{WorkflowStudio, "ja", []string{"kokoro-82m-bf16", "qwen3-tts-1.7b-base-8bit", "chatterbox-turbo-8bit"}},
		{WorkflowAudiobook, "zh", []string{"qwen3-tts-0.6b-base-8bit", "qwen3-tts-1.7b-base-8bit"}},
		{WorkflowDub, "en", []string{"qwen3-tts-0.6b-base-8bit", "qwen3-tts-1.7b-base-8bit"}},
		{WorkflowDesign, "zh", []string{"qwen3-tts-1.7b-voicedesign-4bit"}},
	} {
		if got := ids(c.Recommended(w.w, w.lang)); !slices.Equal(got, w.want) {
			t.Errorf("Recommended(%s, %s) = %v, want %v", w.w, w.lang, got, w.want)
		}
	}
	for _, d := range []struct {
		w         Workflow
		lang      string
		haveVoice bool
		want      string
	}{
		{WorkflowStudio, "zh-Hans", false, "qwen3-tts-0.6b-customvoice-8bit"},
		{WorkflowStudio, "zh-Hans", true, "qwen3-tts-1.7b-base-8bit"},
		{WorkflowStudio, "en", true, "kokoro-82m-bf16"},
		{WorkflowAudiobook, "zh", true, "qwen3-tts-0.6b-base-8bit"},
		{WorkflowAudiobook, "zh", false, "qwen3-tts-0.6b-customvoice-8bit"},
		{WorkflowDub, "zh", false, "qwen3-tts-0.6b-base-8bit"},
		{WorkflowStory, "en", false, "kokoro-82m-bf16"},
	} {
		if e, ok := c.DefaultModel(d.w, d.lang, d.haveVoice); !ok || e.ID != d.want {
			t.Errorf("DefaultModel(%s, %s, %v) = %s", d.w, d.lang, d.haveVoice, e.ID)
		}
	}
	cv, _ := PresetFor("qwen3-tts-0.6b-customvoice-8bit")
	if pp := cv.ParamsFor(WorkflowStudio); pp.Temperature == nil || *pp.Temperature != 0.7 || pp.TopP == nil || *pp.TopP != 0.8 {
		t.Errorf("CustomVoice params %+v", pp)
	}
	if cv.VoiceFor("zh-Hans") != "Serena" {
		t.Errorf("CustomVoice voice %q", cv.VoiceFor("zh-Hans"))
	}
	ov, _ := PresetFor("omnivoice-bf16")
	if pp := ov.ParamsFor(WorkflowStudio); *pp.Guidance != 1.5 || *pp.Steps != 32 {
		t.Errorf("OmniVoice params %+v", pp)
	}
	if pp := ov.ParamsFor(WorkflowDub); *pp.Steps != 16 || *pp.Guidance != 1.5 {
		t.Errorf("OmniVoice dub params %+v", pp)
	}
}

// TestRemoved: the six speech models thread #43 removed are gone from
// the entries and the pickers, and every name they had (id, repo, short
// repo, label, any case) maps to its replacement; a cloning request of
// Marvis, Soprano or Pocket goes to Chatterbox Turbo. Kokoro's Chinese
// speakers stay hidden.
func TestRemoved(t *testing.T) {
	c := Builtin()
	want := []struct{ id, repo, by, cloneBy string }{
		{"kitten-tts-mini-0.8", "mlx-community/kitten-tts-mini-0.8", "kokoro-82m-bf16", "kokoro-82m-bf16"},
		{"moss-tts-nano-100m", "mlx-community/MOSS-TTS-Nano-100M", "qwen3-tts-0.6b-base-8bit", "qwen3-tts-0.6b-base-8bit"},
		{"chatterbox-8bit", "mlx-community/chatterbox-8bit", "chatterbox-turbo-8bit", "chatterbox-turbo-8bit"},
		{"marvis-tts-250m-v0.2-8bit", "Marvis-AI/marvis-tts-250m-v0.2-MLX-8bit", "kokoro-82m-bf16", "chatterbox-turbo-8bit"},
		{"soprano-80m-bf16", "mlx-community/Soprano-80M-bf16", "kokoro-82m-bf16", "chatterbox-turbo-8bit"},
		{"pocket-tts", "mlx-community/pocket-tts", "kokoro-82m-bf16", "chatterbox-turbo-8bit"},
	}
	if len(c.Removed) != len(want) {
		t.Fatalf("%d removed", len(c.Removed))
	}
	for _, w := range want {
		if _, ok := c.ByID(w.id); ok {
			t.Errorf("%s is still an entry", w.id)
		}
		r, ok := c.RemovedModel(w.id)
		if !ok || r.Repo != w.repo {
			t.Fatalf("%s: %+v", w.id, r)
		}
		for _, ref := range []string{w.id, w.repo, strings.ToUpper(w.repo), ShortRepo(w.repo), r.Label, " " + r.Label + " "} {
			if e, rr, ok := c.Replace(ref, false); !ok || e.ID != w.by || rr.ID != w.id {
				t.Errorf("Replace(%q) = %s", ref, e.ID)
			}
			if e, _, ok := c.Replace(ref, true); !ok || e.ID != w.cloneBy {
				t.Errorf("Replace(%q, clone) = %s", ref, e.ID)
			}
		}
		if slices.Contains(c.Labels(TaskTTS), r.Label) {
			t.Errorf("%s is still offered", w.id)
		}
		if c.DisplayName(w.repo) != r.Label {
			t.Errorf("DisplayName(%s) = %q", w.repo, c.DisplayName(w.repo))
		}
	}
	for _, ref := range []string{"", "kokoro-82m-bf16", "mlx-community/Kokoro-82M-bf16", "someone/else"} {
		if _, _, ok := c.Replace(ref, false); ok {
			t.Errorf("Replace(%q) maps", ref)
		}
	}
	// Repos shared with kept models stay: Kokoro's G2P (Kitten's too) and
	// S3TokenizerV2 (Chatterbox Turbo's).
	pocket, _ := c.RemovedModel("pocket-tts")
	if !slices.Equal(pocket.Repos(), []string{"mlx-community/pocket-tts", "kyutai/moshiko-pytorch-bf16"}) {
		t.Errorf("pocket repos %v", pocket.Repos())
	}
	for _, r := range c.Removed {
		for _, repo := range r.Repos() {
			if c.UsesRepo(repo) {
				t.Errorf("%s: %s is still used", r.ID, repo)
			}
		}
	}
	if !c.UsesRepo("beshkenadze/kitten-tts-g2p") || !c.UsesRepo("mlx-community/S3TokenizerV2") {
		t.Error("shared aux repos")
	}
	k, _ := c.ByID("kokoro-82m-bf16")
	if slices.ContainsFunc(k.VoiceIDs(), func(v string) bool { return strings.HasPrefix(v, "z") }) || !slices.Contains(k.AllVoiceIDs(), "zf_xiaobei") || len(k.VoiceIDs()) != 19 {
		t.Errorf("kokoro voices %v", k.VoiceIDs())
	}
	for name, edit := range map[string]func(*Catalog){
		"replaced by a removed model":   func(b *Catalog) { b.Removed[0].ReplacedBy = "moss-tts-nano-100m" },
		"clone replacement can't clone": func(b *Catalog) { b.Removed[3].CloneReplacedBy = "kokoro-82m-bf16" },
		"no replacement":                func(b *Catalog) { b.Removed[0].ReplacedBy = "" },
		"an entry's repo":               func(b *Catalog) { b.Removed[0].Repo = "mlx-community/Kokoro-82M-bf16" },
		"a shared aux repo":             func(b *Catalog) { b.Removed[0].Aux = []string{"beshkenadze/kitten-tts-g2p"} },
	} {
		bad := *c
		bad.Removed = slices.Clone(c.Removed)
		edit(&bad)
		if bad.Validate() == nil {
			t.Errorf("%s validates", name)
		}
	}
}
