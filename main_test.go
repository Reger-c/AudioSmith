package main

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// fakeEngine records requests instead of running audiosmith-engine.
type fakeEngine struct {
	sent     []engineRequest
	restarts int
}

func (f *fakeEngine) Send(req engineRequest) (string, error) {
	if req.ID == "" {
		req.ID = string(rune('a' + len(f.sent)))
	}
	f.sent = append(f.sent, req)
	return req.ID, nil
}
func (f *fakeEngine) Restart() error { f.restarts++; return nil }
func (f *fakeEngine) Stop()          {}
func (f *fakeEngine) Path() string   { return "/fake/audiosmith-engine" }

func (f *fakeEngine) last(t *testing.T) engineRequest {
	t.Helper()
	if len(f.sent) == 0 {
		t.Fatal("nothing was sent to the engine")
	}
	return f.sent[len(f.sent)-1]
}

func newTestApp(t *testing.T) (*app, *fakeEngine, *ui.Tester) {
	t.Helper()
	lib, err := loadLibrary(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	eng := &fakeEngine{}
	a := newApp(eng, lib)
	a.autoplay = false
	a.handleEvent(engineEvent{Event: "ready", Version: "test", PID: 42})
	return a, eng, ui.NewTester(a.view, 1100, 720)
}

// writeTestWAV writes a mono 16-bit sine, as the engine would write a take.
func writeTestWAV(t *testing.T, path string, rate int, seconds float64) {
	t.Helper()
	n := int(float64(rate) * seconds)
	data := make([]byte, 44+2*n)
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(36+2*n))
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], 1)
	binary.LittleEndian.PutUint32(data[24:], uint32(rate))
	binary.LittleEndian.PutUint32(data[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(data[32:], 2)
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], uint32(2*n))
	for i := range n {
		// A tone that swells, so the waveform has a shape.
		v := math.Sin(2*math.Pi*220*float64(i)/float64(rate)) * float64(i) / float64(n)
		binary.LittleEndian.PutUint16(data[44+2*i:], uint16(int16(v*20000)))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

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
	if req.Cmd != "synthesize" || req.Model != modelPresets[0].Repo || req.Voice != "zf_xiaobei" ||
		req.Text != "Testing one two three." || req.Speed == nil || *req.Speed != 1.25 {
		t.Fatalf("request %+v", req)
	}
	if req.Temperature != nil || req.TopP != nil {
		t.Errorf("Kokoro takes no sampling settings, got %+v", req)
	}
	if !strings.HasSuffix(req.Output, ".wav") || !strings.HasPrefix(req.Output, a.lib.clipsDir()) {
		t.Errorf("output %q", req.Output)
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

	a.handleEvent(engineEvent{ID: req.ID, Event: "loaded", Model: req.Model, SampleRate: 24000})
	a.handleEvent(engineEvent{ID: req.ID, Event: "status", Message: "Generating…"})
	half := 0.5
	a.handleEvent(engineEvent{ID: req.ID, Event: "progress", Fraction: &half, AudioSeconds: 0.4})
	tt.Frame()
	if !tt.HasText("Generating…") {
		t.Errorf("status not shown: %q", tt.Texts())
	}

	writeTestWAV(t, req.Output, 24000, 0.8)
	a.handleEvent(engineEvent{ID: req.ID, Event: "done", Output: req.Output, SampleRate: 24000, AudioSeconds: 0.8, Seconds: 0.2})
	tt.Frame()
	if a.job != nil {
		t.Fatal("job still running after done")
	}
	if len(a.lib.clips) != 1 || a.lastClip() == nil {
		t.Fatalf("clips %v", a.lib.clips)
	}
	cl := a.lib.clips[0]
	if cl.RTF() != 4 || cl.Voice != "zf_xiaobei" {
		t.Errorf("clip %+v", cl)
	}
	if !tt.HasText("Latest take") || !tt.HasText("Play") {
		t.Errorf("latest take not shown: %q", tt.Texts())
	}
	if len(cl.peaks) != 160 {
		t.Errorf("waveform has %d peaks", len(cl.peaks))
	}
	if a.loadedModel != req.Model {
		t.Errorf("loaded model %q", a.loadedModel)
	}

	// The history survives a restart.
	lib, err := loadLibrary(a.lib.dir)
	if err != nil || len(lib.clips) != 1 || lib.clips[0].Text != "Testing one two three." {
		t.Fatalf("reloaded history %v, %v", lib.clips, err)
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

func TestErrorsAndCancel(t *testing.T) {
	a, eng, tt := newTestApp(t)
	tt.Frame()
	tt.Click("Generate")
	id := eng.last(t).ID

	a.handleEvent(engineEvent{ID: id, Event: "error", Message: "Unsupported model type: nil"})
	tt.Frame()
	if a.job != nil || !tt.HasText("Unsupported model type: nil") {
		t.Fatalf("error not shown: %q", tt.Texts())
	}

	tt.Click("Generate")
	id = eng.last(t).ID
	tt.Frame()
	if err := tt.Click("Cancel"); err != nil {
		t.Fatal(err)
	}
	if eng.last(t).Cmd != "cancel" {
		t.Fatalf("Cancel sent %+v", eng.last(t))
	}
	a.handleEvent(engineEvent{ID: id, Event: "cancelled"})
	if a.job != nil {
		t.Fatal("job survived cancellation")
	}
}

func TestEngineCrash(t *testing.T) {
	a, eng, tt := newTestApp(t)
	tt.Frame()
	tt.Click("Generate")
	a.engineExited(os.ErrClosed, "fatal: Failed to load the default metallib")
	a.page = "engine"
	tt.Frame()
	if a.job != nil || a.engState != engineFailed {
		t.Fatalf("state %v job %v", a.engState, a.job)
	}
	if !tt.HasText("Engine stopped") || !tt.HasText("fatal: Failed to load the default metallib") {
		t.Errorf("crash not shown: %q", tt.Texts())
	}
	if err := tt.Click("Restart engine"); err != nil {
		t.Fatal(err)
	}
	if eng.restarts != 1 || a.engState != engineStarting {
		t.Errorf("restarts %d state %v", eng.restarts, a.engState)
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

func TestPreloadAndUnload(t *testing.T) {
	a, eng, tt := newTestApp(t)
	tt.Frame()
	if err := tt.Click("Preload"); err != nil {
		t.Fatal(err)
	}
	req := eng.last(t)
	if req.Cmd != "load" || req.Model != modelPresets[0].Repo {
		t.Fatalf("Preload sent %+v", req)
	}
	a.handleEvent(engineEvent{ID: req.ID, Event: "loaded", Model: req.Model, Seconds: 1.5})
	tt.Frame()
	if a.job != nil || !tt.HasText("Loaded") {
		t.Fatalf("after load: job %v texts %q", a.job, tt.Texts())
	}
	tt.Click("Unload")
	if eng.last(t).Cmd != "unload" {
		t.Fatalf("Unload sent %+v", eng.last(t))
	}
	a.handleEvent(engineEvent{ID: eng.last(t).ID, Event: "unloaded"})
	if a.loadedModel != "" {
		t.Error("model still loaded")
	}
}

func TestHistoryReuseAndDelete(t *testing.T) {
	a, _, tt := newTestApp(t)
	for i, text := range []string{"First take", "Second take"} {
		c := &clip{
			ID: newClipID(time.Now().Add(time.Duration(i) * time.Second)), CreatedAt: time.Now().Add(time.Duration(i) * time.Second),
			Text: text, Model: "mlx-community/Qwen3-TTS-12Hz-0.6B-CustomVoice-8bit", Voice: "Vivian, cheerful",
			Language: "English", Temperature: 0.4, TopP: 0.8, MaxTokens: 900, AudioSeconds: 1,
		}
		c.File = c.ID + ".wav"
		writeTestWAV(t, a.lib.path(c), 24000, 0.3)
		if err := a.lib.add(c); err != nil {
			t.Fatal(err)
		}
	}
	a.page = "history"
	tt.Frame()
	if !tt.HasText("2 takes") || !tt.HasText("First take") || !tt.HasText("Second take") {
		t.Fatalf("history: %q", tt.Texts())
	}

	second := a.lib.clips[0]
	a.reuse(second)
	if a.page != "studio" || a.text != "Second take" || a.modelLabel != "Qwen3-TTS 0.6B CustomVoice" ||
		a.voice != "Vivian, cheerful" || a.modelDefaults || a.temperature != 0.4 || a.maxTokens != 900 {
		t.Errorf("reuse: %+v", a)
	}

	a.page = "history"
	tt.Frame()
	path := a.lib.path(second)
	a.deleteClip(second)
	tt.Frame()
	if len(a.lib.clips) != 1 || a.lib.clips[0].Text != "First take" {
		t.Fatalf("after delete: %v", a.lib.clips)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("audio of a deleted take remains: %v", err)
	}
}

func TestEmptyHistory(t *testing.T) {
	a, _, tt := newTestApp(t)
	a.page = "history"
	tt.Frame()
	if !tt.HasText("No takes yet") {
		t.Errorf("texts %q", tt.Texts())
	}
}

func TestPeaksAndNames(t *testing.T) {
	p := peaksOf([]float32{0, 0.5, 0.25, 1, 0, 0}, 3)
	if len(p) != 3 || p[0] != 0.5 || p[1] != 1 || p[2] != 0 {
		t.Errorf("peaks %v", p)
	}
	if got := exportName(&clip{Text: "What: a/test? of the export name, really long"}); got != "What- a-test- of the export name,.wav" {
		t.Errorf("export name %q", got)
	}
	if got := exportName(&clip{Text: "你好，世界"}); got != "你好，世界.wav" {
		t.Errorf("export name %q", got)
	}
}
