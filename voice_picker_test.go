package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	myaudio "github.com/egoist/mygo/audio"
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/services"
)

// TestVoicePreviewSources: each kind of voice resolves to its preview.
func TestVoicePreviewSources(t *testing.T) {
	a, _, _ := newTestApp(t)
	cv, kok := entry(t, "qwen3-tts-0.6b-customvoice-8bit"), entry(t, "kokoro-82m-bf16")
	// Every visible built-in speaker has its bundled clip, but Kokoro's
	// Japanese ones (unintelligible) and the hidden Chinese ones.
	for _, e := range []catalog.Entry{cv, kok} {
		for _, v := range e.Voices {
			p := speakerPreview(e, v.ID)
			want := !v.Hidden && !strings.HasPrefix(v.Language, "ja")
			if p.ok() != want {
				t.Errorf("%s %s: preview %v, want %v", e.ID, v.ID, p.ok(), want)
			}
			if !p.ok() {
				continue
			}
			// A bundled Ogg Vorbis clip of 2–6 s, which the app decodes.
			d, format, err := myaudio.NewDecoder(bytes.NewReader(p.data))
			if err != nil || format != "vorbis" || p.path != "" {
				t.Errorf("%s: %q %v", v.ID, format, err)
				continue
			}
			if secs := float64(d.Len()) / float64(d.SampleRate()); secs < 2 || secs > 6 || d.Channels() != 1 {
				t.Errorf("%s: %.1f s, %d channels", v.ID, secs, d.Channels())
			}
		}
	}
	if speakerPreview(cv, "Nobody").ok() || speakerPreview(cv, "../kokoro-af_heart").ok() || speakerPreview(catalog.Entry{}, "Vivian").ok() {
		t.Error("an unknown speaker has a preview")
	}
	// Saved voices: the sample, else the reference; a built-in speaker's
	// voice its clip; a description alone nothing.
	cloned := addVoice(t, a, core.Voice{Name: "Clone", Kind: core.VoiceCloned}, 8)
	if p := a.savedVoicePreview(&cloned); !p.ok() || p.path != a.voicePath(&cloned, cloned.RefAudio) || p.data != nil {
		t.Errorf("cloned: %+v", p)
	}
	sample := filepath.Join(t.TempDir(), "sample.wav")
	if err := writeSpeech(sample, 3, []span{{0.2, 2.8}}); err != nil {
		t.Fatal(err)
	}
	designed, err := a.lib.svc.Voices.SaveFiles(t.Context(), core.Voice{Name: "Designed", Kind: core.VoiceDesigned, Design: "warm"}, core.VoiceFiles{Sample: sample})
	if err != nil {
		t.Fatal(err)
	}
	if p := a.savedVoicePreview(&designed); !p.ok() || p.path != a.voicePath(&designed, designed.Sample) {
		t.Errorf("designed: %+v", p)
	}
	bare := addVoice(t, a, core.Voice{Name: "Words", Kind: core.VoiceDesigned, Design: "a calm narrator"}, 0)
	if a.savedVoicePreview(&bare).ok() {
		t.Error("a description-only voice has a preview")
	}
	heart := addVoice(t, a, core.Voice{Name: "Heart", Kind: core.VoiceBuiltin, Model: kok.Repo, Speaker: "af_heart"}, 0)
	if p := a.savedVoicePreview(&heart); !p.ok() || p.id != speakerPreview(kok, "af_heart").id {
		t.Errorf("built-in voice: %+v", p)
	}
	if a.savedVoicePreview(nil).ok() {
		t.Error("nil voice")
	}
}

// pickerHarness renders one voice picker over Vivian, Serena (bundled
// clips), a saved voice (its file) and a voice without a preview.
func pickerHarness(t *testing.T) (*app, *ui.Tester, *string, []voiceOption) {
	a, _, _ := newTestApp(t)
	cv := entry(t, "qwen3-tts-0.6b-customvoice-8bit")
	v := addVoice(t, a, core.Voice{Name: "Clone", Kind: core.VoiceCloned}, 8)
	opts := []voiceOption{
		{value: "", label: "Default voice"},
		{value: services.SpeakerRef + "Vivian", label: "Vivian", group: "Model voices", preview: speakerPreview(cv, "Vivian")},
		{value: services.SpeakerRef + "Serena", label: "Serena", group: "Model voices", preview: speakerPreview(cv, "Serena")},
		{value: v.ID, label: "Clone", group: "My voices", voice: &v, preview: a.savedVoicePreview(&v)},
	}
	cur := new(string)
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Padding(10).Children(func() {
			if v, ok := a.voicePicker(c, "test-voice", *cur, opts, voicePickerOpts{label: "Voice"}); ok {
				*cur = v
			}
		})
	}, 500, 600)
	pinTester(tt)
	t.Cleanup(a.pl.Stop)
	return a, tt, cur, opts
}

// TestVoicePickerPreview: ▶ plays, ■ stops, another ▶ takes over,
// closing the list or picking stops it; the row still picks.
func TestVoicePickerPreview(t *testing.T) {
	a, tt, cur, opts := pickerHarness(t)
	viv, ser, mine := opts[1].preview.id, opts[2].preview.id, opts[3].preview.id
	click := func(s string) {
		t.Helper()
		if err := tt.Click(s); err != nil {
			t.Fatalf("%v (texts %q)", err, tt.Texts())
		}
		tt.Frame()
	}
	if tt.HasText("Vivian") {
		t.Fatal("the list shows while closed")
	}
	click("Voice")
	if !a.voicePickers.open["test-voice"] || !tt.HasText("Vivian") || !tt.HasText("Model voices") {
		t.Fatalf("not open: %q", tt.Texts())
	}
	if _, ok := tt.Find("Hear Default voice"); ok {
		t.Error("a row without a preview has ▶")
	}
	click("Hear Vivian")
	if !a.pl.Playing(viv) || *cur != "" || !a.voicePickers.open["test-voice"] {
		t.Fatalf("▶ Vivian: playing %q, cur %q", a.pl.Current(), *cur)
	}
	click("Hear Serena") // another row's ▶ stops the first
	if a.pl.Playing(viv) || !a.pl.Playing(ser) {
		t.Fatalf("▶ Serena: playing %q", a.pl.Current())
	}
	click("Stop the preview") // ■
	if a.pl.Playing(ser) || a.pl.Current() != "" {
		t.Fatalf("■: playing %q", a.pl.Current())
	}
	click("Hear Clone") // a saved voice plays its file
	if !a.pl.Playing(mine) {
		t.Fatalf("▶ Clone: playing %q", a.pl.Current())
	}
	tt.Key(0, ui.KeyEscape) // closing stops it
	tt.Frame()
	if a.voicePickers.open["test-voice"] || a.pl.Current() != "" {
		t.Fatalf("Escape: open %v, playing %q", a.voicePickers.open["test-voice"], a.pl.Current())
	}
	// Picking a row picks it, closes the list and stops the preview.
	click("Voice")
	click("Hear Vivian")
	click("Serena")
	if *cur != services.SpeakerRef+"Serena" || a.voicePickers.open["test-voice"] || a.pl.Current() != "" {
		t.Fatalf("pick: cur %q, open %v, playing %q", *cur, a.voicePickers.open["test-voice"], a.pl.Current())
	}
	// Opened by the app (snapshots), closed by the app: stopped too.
	a.voicePickerOpen("test-voice", true)
	tt.Frame()
	click("Hear Vivian")
	a.voicePickerOpen("test-voice", false)
	tt.Frame()
	if a.pl.Current() != "" {
		t.Errorf("closed by the app: playing %q", a.pl.Current())
	}
	// The main player's audio is not the picker's to stop.
	a.voicePickerOpen("test-voice", true)
	tt.Frame()
	a.pl.PlayPreview("take:T1", opts[1].preview.data, "", 0)
	a.voicePickerOpen("test-voice", false)
	tt.Frame()
	if a.pl.Current() != "take:T1" {
		t.Errorf("closing stopped another player's audio: %q", a.pl.Current())
	}
}

// TestVoicePickerKeyboard: the arrows open the list and move, P plays
// the highlighted row, Enter picks.
func TestVoicePickerKeyboard(t *testing.T) {
	a, tt, cur, opts := pickerHarness(t)
	tt.Frame()
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	if !tt.Focused("Voice") {
		t.Fatalf("trigger not focused")
	}
	tt.Key(0, ui.KeyDown) // opens on the chosen row (Default voice)
	tt.Frame()
	if !a.voicePickers.open["test-voice"] || a.voicePickers.highlight["test-voice"] != 0 {
		t.Fatalf("Down: open %v, highlight %d", a.voicePickers.open["test-voice"], a.voicePickers.highlight["test-voice"])
	}
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	tt.Key(0, ui.KeyP)
	tt.Frame()
	if !a.pl.Playing(opts[1].preview.id) {
		t.Fatalf("P: playing %q", a.pl.Current())
	}
	tt.Key(0, ui.KeyP) // again: stops
	tt.Frame()
	if a.pl.Current() != "" {
		t.Fatalf("P again: playing %q", a.pl.Current())
	}
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if *cur != services.SpeakerRef+"Serena" || a.voicePickers.open["test-voice"] {
		t.Fatalf("Enter: cur %q, open %v", *cur, a.voicePickers.open["test-voice"])
	}
	tt.Key(0, ui.KeyUp) // reopens on Serena; Home, End
	tt.Key(0, ui.KeyEnd)
	tt.Frame()
	if a.voicePickers.highlight["test-voice"] != len(opts)-1 {
		t.Errorf("End: highlight %d", a.voicePickers.highlight["test-voice"])
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.voicePickers.open["test-voice"] || *cur != services.SpeakerRef+"Serena" {
		t.Errorf("Escape: open %v cur %q", a.voicePickers.open["test-voice"], *cur)
	}
}
