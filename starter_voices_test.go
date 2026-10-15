package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/services"
)

// useStarter makes the bundled starter voices one voice file: a cloned
// voice "温晴" exported from another library.
func useStarter(t *testing.T) {
	t.Helper()
	b, _, _ := newTestApp(t)
	v := addVoice(t, b, core.Voice{Name: "温晴", Kind: core.VoiceCloned, Language: "zh"}, 8)
	file := filepath.Join(t.TempDir(), "wenqing.asvoice")
	if err := b.lib.svc.Voices.Export(context.Background(), v.ID, file, core.VoiceExport{IncludeReference: true, App: "AudioSmith"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	saved := starterFiles
	starterFiles = func() map[string][]byte { return map[string][]byte{"starter-zh-narrator.asvoice": data} }
	t.Cleanup(func() { starterFiles = saved })
}

// TestStarterVoices: a bundled starter voice is imported into a new
// library once; deleted, it stays deleted.
func TestStarterVoices(t *testing.T) {
	useStarter(t)
	a, _, tt := newTestApp(t)
	tt.Frame()
	v := a.starterVoice()
	if v == nil || v.Name != "温晴" || len(a.prefs.Setup.Starters) != 1 {
		t.Fatalf("starter %+v (settings %v)", v, a.prefs.Setup.Starters)
	}
	a.importStarterVoices()
	if n := len(a.savedVoices()); n != 1 {
		t.Errorf("imported twice: %d voices", n)
	}
	if err := a.lib.svc.Voices.Delete(context.Background(), v.ID); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	a.importStarterVoices()
	tt.Frame()
	if a.starterVoice() != nil || len(a.savedVoices()) != 0 {
		t.Errorf("deleted starter came back: %v", a.savedVoices())
	}
}

// TestNewUserLongform: a new user's Stories and Audiobook start on the
// presets' models — Chinese audiobook: Qwen3-TTS 0.6B Base narrated by
// the starter voice (CustomVoice + Serena without one); English: Kokoro +
// af_heart — until the user picks a model or writes.
func TestNewUserLongform(t *testing.T) {
	t.Cleanup(func() { i18n.SetLocale("en") })
	a, _, tt := newTestApp(t)
	tt.Frame()
	if e := entryFor(a.lfBook.doc.Model); e.ID != "kokoro-82m-bf16" || a.lfBook.doc.Voice != services.SpeakerRef+"af_heart" || !a.lfBook.auto {
		t.Fatalf("English audiobook: %s %q", e.ID, a.lfBook.doc.Voice)
	}
	i18n.SetLocale("zh-Hans")
	a.autoLfModels()
	if e := entryFor(a.lfBook.doc.Model); e.ID != "qwen3-tts-0.6b-customvoice-8bit" || a.lfBook.doc.Voice != services.SpeakerRef+"Serena" {
		t.Fatalf("Chinese audiobook without voices: %s %q", e.ID, a.lfBook.doc.Voice)
	}

	useStarter(t)
	b, _, tt2 := newTestApp(t)
	tt2.Frame()
	i18n.SetLocale("zh-Hans")
	b.autoLfModels()
	st := b.starterVoice()
	if st == nil {
		t.Fatal("no starter")
	}
	if e := entryFor(b.lfBook.doc.Model); e.ID != "qwen3-tts-0.6b-base-8bit" || b.lfBook.doc.Voice != st.ID {
		t.Errorf("Chinese audiobook with the starter: %s %q", e.ID, b.lfBook.doc.Voice)
	}
	// The story: 1.7B Base with a saved voice.
	if e := entryFor(b.lfStories.doc.Model); e.ID != "qwen3-tts-1.7b-base-8bit" {
		t.Errorf("Chinese story with a voice: %s", e.ID)
	}
	// Writing ends the automatic choice.
	b.lfBook.doc.Script = "# 第一章\n\n很久以前。"
	b.lfBook.doc.Model = catalog.Builtin().Recommended(catalog.WorkflowAudiobook, "zh")[1].Repo
	addVoice(t, b, core.Voice{Name: "我的", Kind: core.VoiceCloned, Language: "zh"}, 8)
	tt2.Frame()
	if e := entryFor(b.lfBook.doc.Model); e.ID != "qwen3-tts-1.7b-base-8bit" {
		t.Errorf("a written page changed model: %s", e.ID)
	}
}
