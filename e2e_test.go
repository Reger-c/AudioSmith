package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/audio"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/engine"
)

// TestEndToEnd drives the real audiosmith-engine through the app's state:
// start, preload, generate twice (English, Chinese), cancel nothing, quit.
// It needs macOS on Apple silicon, a built engine and the network on first
// run (the model downloads), so it runs only with AUDIOSMITH_E2E=1:
//
//	AUDIOSMITH_E2E=1 go test -run TestEndToEnd -v .
func TestEndToEnd(t *testing.T) {
	if os.Getenv("AUDIOSMITH_E2E") == "" {
		t.Skip("set AUDIOSMITH_E2E=1 to run the real engine")
	}
	enginePath := engine.Find("resources")
	if enginePath == "" {
		t.Fatal("no engine: run scripts/build-engine.sh")
	}
	lib := openTestLibrary(t)
	events := make(chan func(), 64)
	eng := &engine.Process{Bin: enginePath, LogPath: filepath.Join(t.TempDir(), "engine.log")}
	a := newApp(eng, lib)
	a.autoplay = false
	// Events arrive on the engine's goroutine; apply them here, in order,
	// as the window's main thread would.
	eng.OnEvent = func(ev engine.Event) { events <- func() { a.handleEvent(ev) } }
	eng.OnExit = func(err error, tail string) { events <- func() { a.engineExited(err, tail) } }
	defer eng.Stop()

	waitFor := func(what string, timeout time.Duration, done func() bool) {
		t.Helper()
		deadline := time.After(timeout)
		for !done() {
			if a.engState == engineFailed {
				t.Fatalf("engine failed: %s\n%s", a.engError, a.engTail)
			}
			select {
			case fn := <-events:
				fn()
			case <-deadline:
				t.Fatalf("timed out waiting for %s (job %+v, error %q, engine %q)", what, a.job, a.jobError, a.engError)
			}
		}
	}

	a.restartEngine()
	waitFor("ready", 30*time.Second, func() bool { return a.engState == engineReady })
	t.Logf("engine %s pid %d", a.engVersion, a.engPID)

	a.preload()
	waitFor("load", 10*time.Minute, func() bool { return a.job == nil })
	if a.loadedModel != catalog.Builtin().Entries[0].Repo || a.jobError != "" {
		t.Fatalf("loaded %q, error %q", a.loadedModel, a.jobError)
	}

	for i, tc := range []struct{ text, voice string }{
		{"AudioSmith end to end test.", "af_heart"},
		{"这是端到端测试。", "zf_xiaoxiao"},
	} {
		a.text, a.voice = tc.text, tc.voice
		a.generate()
		if a.job == nil {
			t.Fatalf("generate %d did not start: %q", i, a.jobError)
		}
		waitFor("take", 5*time.Minute, func() bool { return a.job == nil })
		if a.jobError != "" {
			t.Fatalf("take %d: %s", i, a.jobError)
		}
		c := a.lastClip()
		if c == nil || c.Voice != tc.voice || c.AudioSeconds < 0.5 || c.SampleRate != 24000 {
			t.Fatalf("take %d: %+v", i, c)
		}
		peaks, err := audio.Peaks(a.takeStore.AudioPath(*c), 50)
		if err != nil || len(peaks) != 50 {
			t.Fatalf("take %d audio unreadable: %v", i, err)
		}
		t.Logf("take %d: %.2f s of audio in %.2f s (%.1f× realtime)", i, c.AudioSeconds, c.GenSeconds, c.RTF())
	}
	if len(a.takes) != 2 {
		t.Fatalf("history has %d takes", len(a.takes))
	}

	a.unload()
	waitFor("unload", 30*time.Second, func() bool { return a.loadedModel == "" })
}
