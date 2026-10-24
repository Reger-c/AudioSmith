package main

import (
	"context"
	famodels "github.com/hehehai/actnowkit/models"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/audio"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// TestModelLifecycle drives the product path of a model on a Mac, through
// the app's state: download it from Hugging Face with the Go downloader
// (system proxy, mirror race) into an empty model folder, cancel half way
// and resume, accept its licence when it needs one, generate with the
// engine started offline — run under sandbox-exec with the network denied
// when it is there, so loading from the local snapshot is proven — and
// delete it. It needs the network and AUDIOSMITH_E2E_MODELS, a comma-
// separated list of catalogue ids, in order:
//
//	AUDIOSMITH_E2E=1 AUDIOSMITH_E2E_MODELS=kokoro-82m-bf16,omnivoice-bf16 go test -run TestModelLifecycle -v -timeout 30m .
//
// OmniVoice designs a voice with a target duration, then clones the
// first take.
func TestModelLifecycle(t *testing.T) {
	ids := os.Getenv("AUDIOSMITH_E2E_MODELS")
	if os.Getenv("AUDIOSMITH_E2E") == "" || ids == "" {
		t.Skip("set AUDIOSMITH_E2E=1 and AUDIOSMITH_E2E_MODELS to download real models")
	}
	bin := engine.Find("../resources")
	if bin == "" {
		t.Fatal("no engine: run scripts/build-engine.sh")
	}
	logDir := t.TempDir()
	bin = offlineEngine(t, bin)

	lib := openTestLibrary(t)
	models := t.TempDir()
	lib.settings.Update(context.Background(), func(s *settings.Settings) { s.Models.CacheDir = models })
	h := &engineHost{bin: bin, logDir: logDir}
	a := newApp(h, lib)
	a.autoplay = false
	events := make(chan func(), 4096)
	a.enginePost = func(fn func()) { events <- fn }
	a.postQueue = a.enginePost
	h.sup = engine.NewSupervisor(engine.Config{Bin: bin, Bus: a.bus, LogDir: logDir, Offline: true, CacheDir: models,
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
				t.Fatalf("timed out waiting for %s (job %+v, error %q)", what, a.job, a.jobError)
			}
		}
	}
	a.startEngine()
	waitFor("ready", time.Minute, func() bool { return a.engState == engineReady })
	t.Logf("downloads go through %q", hub.DescribeProxy(""))

	var firstTake *core.Take
	for _, id := range strings.Split(ids, ",") {
		e := entry(t, id)
		m := a.model(e)

		// Download, cancel at about a third, resume.
		start := time.Now()
		a.download(e)
		if a.licenceOpen {
			a.acceptLicence(e, a.licenceThen)
			a.licenceOpen, a.licenceThen = false, nil
		}
		waitFor("download started", time.Minute, func() bool { return m.downloading && m.progress.TotalBytes > 0 })
		waitFor("a third", 10*time.Minute, func() bool { return m.progress.Bytes > m.progress.TotalBytes/3 || !m.downloading })
		if m.downloading {
			a.cancelDownload(e)
			waitFor("cancelled", time.Minute, func() bool { return !m.downloading })
			waitFor("partial", 10*time.Second, func() bool { return m.known && m.local.State == hub.Partial })
			t.Logf("%s: cancelled with %s on disk", e.Label, mb(m.local.Bytes))
			a.download(e)
		}
		waitFor("downloaded", 20*time.Minute, func() bool { return !m.downloading && m.known && m.local.State == hub.Downloaded })
		if m.err != "" {
			t.Fatalf("%s: %s", e.Label, m.err)
		}
		t.Logf("%s: downloaded %s in %.0f s (with the pause), snapshot %s", e.Label, mb(m.local.Bytes), time.Since(start).Seconds(), m.local.Snapshot)

		// Generate, offline.
		a.selectModel(e)
		a.text = "AudioSmith downloaded this model, and the engine loaded it from disk."
		if e.Family == "omnivoice" {
			a.voice, a.duration = "female, young adult, british accent", 4
		}
		take := func(what string) *core.Take {
			t.Helper()
			a.generate()
			if a.job == nil {
				t.Fatalf("%s: generate did not start: %q", what, a.jobError)
			}
			waitFor(what, 10*time.Minute, func() bool { return a.job == nil && a.awaitTake == "" })
			if a.jobError != "" {
				t.Fatalf("%s: %s", what, a.jobError)
			}
			c := a.lastClip()
			if c == nil || c.Model != e.Repo || c.AudioSeconds < 0.5 {
				t.Fatalf("%s: take %+v", what, c)
			}
			if _, err := audio.Peaks(a.takeStore.AudioPath(*c), 50); err != nil {
				t.Fatalf("%s: audio unreadable: %v", what, err)
			}
			t.Logf("%s: %.2f s of audio in %.2f s, seed %d, %s", what, c.AudioSeconds, c.GenSeconds, c.Seed, a.takeStore.AudioPath(*c))
			cp := *c
			return &cp
		}
		c := take(e.Label)
		if e.Family == "omnivoice" {
			if c.AudioSeconds < 3.8 || c.AudioSeconds > 4.2 {
				t.Errorf("duration 4 s gave %.2f s", c.AudioSeconds)
			}
			if firstTake != nil {
				a.refAudio, a.refText = a.takeStore.AudioPath(*firstTake), firstTake.Text
				a.voice, a.duration = "", 0
				a.text = "Now in a cloned voice."
				take(e.Label + " clone")
				a.refAudio, a.refText = "", ""
			}
		}
		if firstTake == nil {
			firstTake = c
		}

		// Delete: unload first (a loaded model cannot be deleted).
		a.unload()
		waitFor("unloaded", time.Minute, func() bool { return a.loadedModel == "" })
		a.deleteModel(e, famodels.ThisApp)
		waitFor("deleted", time.Minute, func() bool { return m.local.State == hub.Missing })
		if _, err := os.Stat(hub.EngineCopy(models, e.Repo)); err == nil {
			t.Errorf("%s: the engine's copy is left", e.Label)
		}
		t.Logf("%s: deleted", e.Label)
	}
}
