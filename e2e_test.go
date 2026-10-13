package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/audio"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/i18n"
)

// TestEndToEnd drives the real audiosmith-engine through the app's state,
// the supervisor and the job scheduler: start, preload, generate twice
// (English, Chinese), cancel a long take, unload, quit. It needs macOS on
// Apple silicon, a built engine and the network on first run (the model
// downloads), so it runs only with AUDIOSMITH_E2E=1:
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
	h := &engineHost{bin: enginePath, logDir: t.TempDir()}
	a := newApp(h, lib)
	a.autoplay = false
	// Engine events arrive on other goroutines; apply them here, in order,
	// as the window's main thread would.
	events := make(chan func(), 4096)
	a.enginePost = func(fn func()) { events <- fn }
	a.postQueue = a.enginePost
	h.sup = engine.NewSupervisor(engine.Config{Bin: enginePath, Bus: a.bus, LogDir: h.logDir,
		Log: func(e core.LogEntry) { t.Logf("%s %s: %s", e.Level, e.Source, e.Message) }, Logf: t.Logf})
	a.connectEngine(h)
	defer h.Stop()

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

	a.startEngine()
	waitFor("ready", time.Minute, func() bool { return a.engState == engineReady })
	w := a.workers[0]
	t.Logf("engine %s pid %d on %s, families %v", a.engVersion, a.engPID, w.Hello.Device.Name, w.Hello.Families)
	if w.Hello.Protocol != proto.Version || w.Hello.Concurrency != 1 {
		t.Fatalf("hello %+v", w.Hello)
	}

	kokoro := catalog.Builtin().Entries[0].Repo
	a.preload()
	waitFor("load", 10*time.Minute, func() bool { return a.job == nil })
	if a.loadedModel != kokoro || a.jobError != "" {
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
		if c == nil || c.Voice != tc.voice || c.AudioSeconds < 0.5 || c.SampleRate != 24000 || c.Seed == 0 {
			t.Fatalf("take %d: %+v", i, c)
		}
		peaks, err := audio.Peaks(a.takeStore.AudioPath(*c), 50)
		if err != nil || len(peaks) != 50 {
			t.Fatalf("take %d audio unreadable: %v", i, err)
		}
		t.Logf("take %d: %.2f s of audio in %.2f s (%.1f× realtime), seed %d", i, c.AudioSeconds, c.GenSeconds, c.RTF(), c.Seed)
	}
	if len(a.takes) != 2 {
		t.Fatalf("history has %d takes", len(a.takes))
	}
	waitFor("stats", 30*time.Second, func() bool { return len(a.loaded) > 0 })

	// Cancel a long take by id: the job ends cancelled, the engine stays up.
	// Kokoro takes at most ~510 phonemes at once.
	long := strings.Repeat("This sentence makes the take long enough to cancel it. ", 7)
	a.text, a.voice = long, "af_heart"
	a.generate()
	waitFor("generating", time.Minute, func() bool {
		return a.job != nil && a.job.id != "" && a.job.status == i18n.T("Generating…")
	})
	time.Sleep(1500 * time.Millisecond) // into the computation
	started := time.Now()
	a.cancel()
	waitFor("cancelled", 30*time.Second, func() bool { return a.job == nil })
	t.Logf("cancelled in %.2f s", time.Since(started).Seconds())
	if a.jobError != "" || len(a.takes) != 2 || a.engState != engineReady || a.workers[0].Restarts != 0 {
		t.Fatalf("after cancel: error %q, %d takes, engine %v, %+v", a.jobError, len(a.takes), a.engState, a.workers[0])
	}

	a.unload()
	waitFor("unload", 30*time.Second, func() bool { return a.loadedModel == "" })
}
