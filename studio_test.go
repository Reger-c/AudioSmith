package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
)

// kokoro is the first catalogue entry, which the Studio starts on.
var kokoro = catalog.Builtin().Entries[0].Repo

func TestGenerateTake(t *testing.T) {
	a, eng, tt := newTestApp(t)
	a.text = "Testing one two three."
	a.voice = "zf_xiaobei"
	a.speed = 1.25
	tt.Frame()

	if err := tt.Click("Generate"); err != nil {
		t.Fatal(err)
	}
	req := eng.last(t)
	if req.Cmd != "synthesize" || req.Model != kokoro || req.Voice != "zf_xiaobei" ||
		req.Text != "Testing one two three." || req.Speed == nil || *req.Speed != 1.25 {
		t.Fatalf("request %+v", req)
	}
	if req.Temperature != nil || req.TopP != nil {
		t.Errorf("Kokoro takes no sampling settings, got %+v", req)
	}
	if !strings.HasSuffix(req.Output.Path, ".wav") || !strings.HasPrefix(req.Output.Path, a.takeStore.AudioDir()) {
		t.Errorf("output %q", req.Output.Path)
	}
	if a.job == nil {
		t.Fatal("no job after Generate")
	}

	// A second click while it runs does nothing.
	tt.Frame()
	tt.Click("Generating…")
	if len(eng.sent) != 1 {
		t.Errorf("sent %d requests while busy", len(eng.sent))
	}

	eng.progress(req.ID, "loading", -1, 0)
	eng.progress(req.ID, "generating", 0.5, 0.4)
	tt.Frame()
	if !tt.HasText("Generating…") {
		t.Errorf("status not shown: %q", tt.Texts())
	}

	writeTestWAV(t, req.Output.Path, 24000, 0.8)
	eng.succeed(req.ID, proto.SynthesizeResult{Output: req.Output.Path, SampleRate: 24000, AudioSeconds: 0.8, Seconds: 0.2})
	tt.Frame()
	if a.job != nil {
		t.Fatal("job still running after done")
	}
	if len(a.takes) != 1 || a.lastClip() == nil {
		t.Fatalf("clips %v", a.takes)
	}
	cl := a.takes[0]
	if cl.RTF() != 4 || cl.Voice != "zf_xiaobei" {
		t.Errorf("clip %+v", cl)
	}
	if !tt.HasText("Latest take") || !tt.HasText("Play") {
		t.Errorf("latest take not shown: %q", tt.Texts())
	}
	if len(a.peaks[cl.ID]) != 160 {
		t.Errorf("waveform has %d peaks", len(a.peaks[cl.ID]))
	}
	if a.loadedModel != req.Model {
		t.Errorf("loaded model %q", a.loadedModel)
	}

	// The take is in the library (it survives a restart: TestDraftAndLibrarySurviveRestart).
	takes, err := a.lib.svc.Takes.List(context.Background(), core.TakeQuery{})
	if err != nil || len(takes) != 1 || takes[0].Text != "Testing one two three." || takes[0].SampleRate != 24000 {
		t.Fatalf("stored takes %v, %v", takes, err)
	}
	if _, err := os.Stat(filepath.Join(a.lib.peaks.Dir, cl.ID+".f32")); err != nil {
		t.Errorf("peaks not cached: %v", err)
	}
}

func TestShortcutGenerates(t *testing.T) {
	a, eng, tt := newTestApp(t)
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyEnter)
	if len(eng.sent) != 1 || eng.sent[0].Cmd != "synthesize" {
		t.Fatalf("⌘↩ sent %+v", eng.sent)
	}
	_ = a
}

func TestCannotGenerateWithoutEngineOrText(t *testing.T) {
	a, eng, tt := newTestApp(t)
	a.text = "   "
	tt.Frame()
	tt.Click("Generate")
	a.text = "Hello"
	a.engState = engineStarting
	tt.Frame()
	tt.Click("Generate")
	if len(eng.sent) != 0 {
		t.Fatalf("sent %+v", eng.sent)
	}
}

func TestModelSwitchShowsItsControls(t *testing.T) {
	a, eng, tt := newTestApp(t)
	tt.Frame()
	if !tt.HasText("Speed  1.00×") {
		t.Errorf("Kokoro shows no speed: %q", tt.Texts())
	}

	a.modelLabel = "Qwen3-TTS 0.6B Base (voice clone)"
	a.modelChanged()
	tt.Frame()
	if a.voice != "" || a.language != "Chinese" {
		t.Errorf("voice %q language %q after switching", a.voice, a.language)
	}
	if tt.HasText("Speed  1.00×") || !tt.HasText("Voice cloning") || !tt.HasText("Use the model's sampling defaults") {
		t.Errorf("controls for Qwen Base: %q", tt.Texts())
	}

	// Sampling settings go out only when the defaults are off.
	tt.Click("Use the model's sampling defaults")
	a.temperature = 0.5
	a.refAudio, a.refText = "/tmp/ref.wav", "Reference words"
	tt.Frame()
	tt.Click("Generate")
	req := eng.last(t)
	if req.Temperature == nil || *req.Temperature != 0.5 || req.MaxTokens != 1200 || req.Speed != nil ||
		req.RefAudio != "/tmp/ref.wav" || req.RefText != "Reference words" || req.Language != "Chinese" {
		t.Fatalf("request %+v", req)
	}
}

// TestDraftAndLibrarySurviveRestart: the form autosaves, and the next run
// starts with it and with the takes.
func TestDraftAndLibrarySurviveRestart(t *testing.T) {
	dir := t.TempDir()
	lib := openTestLibrary(t, dir)
	a, _, tt := newTestAppWith(t, lib)
	addTake(t, a, core.Take{ID: "T1", CreatedAt: time.Now(), Text: "Kept", Model: kokoro}, 0.2)
	a.text = "A script in progress"
	a.modelLabel = "Qwen3-TTS 0.6B Base (voice clone)"
	a.modelChanged()
	a.refText = "ref words"
	tt.Frame()
	tt.Frame() // unchanged: nothing more to save
	if err := lib.svc.Drafts.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	lib.close()

	b, _, tt2 := newTestAppWith(t, openTestLibrary(t, dir))
	if b.text != "A script in progress" || b.modelLabel != "Qwen3-TTS 0.6B Base (voice clone)" ||
		b.refText != "ref words" || b.language != "Chinese" || b.autoplay {
		t.Errorf("restored form %+v", b.toDraft())
	}
	tt2.Frame()
	if len(b.takes) != 1 || b.lastClip() == nil || !tt2.HasText("Latest take") {
		t.Errorf("takes after restart %v", b.takes)
	}
}
