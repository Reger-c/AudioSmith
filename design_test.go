package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	vd "github.com/hehehai/AudioSmith/internal/voicedesign"
)

// voiceDesignRepo is a Qwen3-TTS VoiceDesign model, not in the built-in
// catalogue yet (thread E adds it): tests add it with withDesignCatalog.
const voiceDesignRepo = "mlx-community/Qwen3-TTS-12Hz-1.7B-VoiceDesign-8bit"

// withDesignCatalog makes the voice pages offer a Qwen3-TTS VoiceDesign
// model too.
func withDesignCatalog(t *testing.T) {
	t.Helper()
	c := *catalog.Builtin()
	c.Entries = append(append([]catalog.Entry(nil), c.Entries...), catalog.Entry{
		ID: "qwen3-tts-1.7b-voicedesign-8bit", Label: "Qwen3-TTS 1.7B VoiceDesign", Family: "qwen3_tts", Repo: voiceDesignRepo,
		Tasks: []catalog.Task{catalog.TaskTTS}, About: "x", Capabilities: []catalog.Capability{catalog.CapDesign, catalog.CapLanguage, catalog.CapSampling},
		Languages: []catalog.Language{{Code: "en", Value: "English"}, {Code: "zh", Value: "Chinese"}},
		Licence:   catalog.Licence{ID: "apache-2.0", Name: "Apache 2.0", Commercial: true, Verified: true},
	})
	saved := voiceCatalog
	voiceCatalog = func() *catalog.Catalog { return &c }
	t.Cleanup(func() { voiceCatalog = saved })
}

func TestDesignPage(t *testing.T) {
	a, eng, _ := newVoiceTestApp(t)
	tt := ui.NewTester(a.view, 1200, 1800)
	d := &a.design
	a.navigate("/design")
	tt.Frame()
	if !tt.HasText("Describe the voice") || !tt.HasText("Sent to OmniVoice (non-commercial)") && !strings.Contains(strings.Join(tt.Texts(), "|"), "Sent to OmniVoice") {
		t.Fatalf("design page: %q", tt.Texts())
	}

	// The description maps to attributes, shown as chips.
	d.description = "a deep elderly British man, very calm, purple"
	tt.Frame()
	if d.attrs.Gender != "male" || d.attrs.Age != "elderly" || d.attrs.Accent != "british" || d.attrs.Pitch == "" {
		t.Fatalf("parsed %+v", d.attrs)
	}
	texts := strings.Join(tt.Texts(), "|")
	if !strings.Contains(texts, "British → English accent: British") || !strings.Contains(texts, "Couldn't map: purple") {
		t.Errorf("chips: %q", texts)
	}
	// A pick holds while the description keeps its word on that category…
	d.pick(vd.Age, "middle_aged")
	d.description = "a deep elderly British man, very calm, slightly raspy"
	tt.Frame()
	if d.attrs.Age != "middle_aged" || !d.attrs.HasStyle("raspy") {
		t.Errorf("pick lost: %+v", d.attrs)
	}
	// …and goes when the description changes it.
	d.description = "a deep young British man, very calm, slightly raspy"
	tt.Frame()
	if d.attrs.Age != "young_adult" {
		t.Errorf("description ignored: %+v", d.attrs)
	}
	// A dialect clears the accent, with a note.
	a.designCleared(d.pick(vd.Dialect, "sichuan"))
	tt.Frame()
	if d.attrs.Accent != "" || !tt.HasText("Cleared English accent: British: it can't go with your pick.") {
		t.Errorf("dialect: %+v %q", d.attrs, tt.Texts())
	}
	// Reset to description drops the picks.
	tt.Click("Reset to description")
	if d.attrs.Dialect != "" || d.attrs.Accent != "british" {
		t.Errorf("reset: %+v", d.attrs)
	}

	// OmniVoice can't say "calm" or "raspy": the page says so.
	tt.Frame()
	if !strings.Contains(strings.Join(tt.Texts(), "|"), "can't express") {
		t.Errorf("dropped note missing: %q", tt.Texts())
	}

	// Generate (after the licence).
	d.seed = 777
	d.script = "Good evening."
	tt.Frame()
	tt.Click("Generate preview")
	tt.Frame()
	if !a.licenceOpen {
		t.Fatal("no licence dialog for OmniVoice")
	}
	acceptLicences(t, a)
	a.licenceOpen = false
	tt.Frame()
	tt.Click("Generate preview")
	req := eng.last(t)
	if req.Model != omni || req.Seed == nil || *req.Seed != 777 || !strings.Contains(req.Voice, "british accent") || req.Text != "Good evening." {
		t.Fatalf("request %+v", req.SynthesizeParams)
	}
	writeTestWAV(t, req.Output.Path, 24000, 1.5)
	seed := int64(777)
	eng.succeed(req.ID, proto.SynthesizeResult{Output: req.Output.Path, SampleRate: 24000, AudioSeconds: 1.5, Seconds: 1, Seed: &seed})
	tt.Frame()
	if j := a.voiceJob("design", ""); j == nil || j.take == nil || !tt.HasText("Latest take") {
		t.Fatalf("preview: %+v %q", j, tt.Texts())
	}

	// Save: the preview becomes the voice's sample.
	d.saveName = "Old Gent"
	tt.Frame()
	if err := tt.Click("Save as voice"); err != nil {
		t.Fatal(err)
	}
	v := a.findVoice(d.linked)
	if v == nil || v.Kind != core.VoiceDesigned || v.Seed != 777 || v.Sample != "sample.wav" || v.CreatedWith != omni ||
		v.Design != d.description || v.SampleText != "Good evening." {
		t.Fatalf("saved %+v", v)
	}
	var attrs vd.Attributes
	json.Unmarshal(v.DesignAttrs, &attrs)
	if attrs.Accent != "british" || attrs.Gender != "male" {
		t.Errorf("attrs %+v", attrs)
	}
	tt.Frame()
	if !tt.HasText("Design · Old Gent") || !tt.HasText("Update voice") {
		t.Errorf("linked: %q", tt.Texts())
	}
	// Generating while linked records the voice.
	tt.Click("Generate preview")
	if req := eng.last(t); req.Cmd != "synthesize" {
		t.Fatal(req)
	}
	// A new seed unlinks the draft.
	tt.Click("New seed")
	tt.Frame()
	if d.linked != "" || tt.HasText("Design · Old Gent") {
		t.Error("still linked after a new seed")
	}
	// Open it again from its editor.
	a.openVoiceEditor(v.ID)
	tt.Frame()
	tt.Click("Open in Design")
	if a.router.Path() != "/design" || d.linked != v.ID || int64(d.seed) != 777 {
		t.Errorf("open in design: %s %v", d.linked, d.seed)
	}

	// Starting points replace the recipe and drop the description.
	tt.Frame()
	tt.Click("Narrator")
	if d.description != "" || d.attrs.Age != "middle_aged" || d.preset != "narrator" {
		t.Errorf("preset: %+v", d)
	}
}

func TestDesignModels(t *testing.T) {
	withDesignCatalog(t)
	a, _, _ := newVoiceTestApp(t)
	tt := ui.NewTester(a.view, 1200, 1800)
	d := &a.design
	if ms := designModels(); len(ms) != 2 || ms[1].Repo != voiceDesignRepo {
		t.Fatalf("design models %v", ms)
	}
	a.navigate("/design")
	d.model = "Qwen3-TTS 1.7B VoiceDesign"
	d.description = "a cheerful young woman, bright voice"
	tt.Frame()
	texts := strings.Join(tt.Texts(), "|")
	if !strings.Contains(texts, "This model reads your description as written") || !strings.Contains(texts, "a cheerful young woman, bright voice") {
		t.Errorf("freeform: %q", texts)
	}
	// It isn't downloaded here: the error offers the download.
	tt.Click("Generate preview")
	tt.Frame()
	j := a.voiceJob("design", "")
	if j == nil || j.needDownload == nil || !tt.HasText("Download") {
		t.Fatalf("not downloaded: %+v %q", j, tt.Texts())
	}
	// No model that designs: the page says where to get one.
	voiceCatalog = func() *catalog.Catalog {
		c := *catalog.Builtin()
		c.Entries = c.Entries[:1]
		return &c
	}
	tt.Frame()
	if !tt.HasText("No model here can design voices. Download one that can, such as OmniVoice, on the Models page.") {
		t.Errorf("no models: %q", tt.Texts())
	}
}

func TestDesignSampleOnDemand(t *testing.T) {
	a, eng, _ := newVoiceTestApp(t)
	acceptLicences(t, a)
	tt := ui.NewTester(a.view, 1200, 1800)
	v := addVoice(t, a, core.Voice{Name: "Kid", Kind: core.VoiceDesigned, Language: "zh",
		DesignAttrs: designAttrs(vd.Attributes{Age: "child", Dialect: "sichuan"}), Seed: 5}, 0)
	a.navigate("/voices")
	tt.Frame()
	a.playVoice(a.findVoice(v.ID))
	req := eng.last(t)
	if req.Model != omni || req.Text != sampleLine("zh") || !strings.Contains(req.Voice, "四川话") || *req.Seed != 5 {
		t.Fatalf("sample request %+v", req.SynthesizeParams)
	}
	writeTestWAV(t, req.Output.Path, 24000, 2)
	eng.succeed(req.ID, proto.SynthesizeResult{Output: req.Output.Path, SampleRate: 24000, AudioSeconds: 2, Seconds: 1})
	got := a.findVoice(v.ID)
	if got.Sample != "sample.wav" || got.SampleText != sampleLine("zh") || got.CreatedWith != omni {
		t.Fatalf("sample %+v", got)
	}
	// With a sample, a model that only clones can speak with it.
	base, _ := catalog.Builtin().ByRepo(qwenBase)
	if ok, _ := voiceUsable(got, base); !ok {
		t.Error("sample not usable for cloning")
	}
	tk := core.Take{Model: qwenBase}
	a.fillVoice(&tk, got, base)
	if tk.RefAudio != a.voicePath(got, "sample.wav") || tk.RefText != sampleLine("zh") {
		t.Errorf("clone of the sample %+v", tk)
	}
}
