package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/hub"
)

// TestWhisperPrompt checks that the engine gives Whisper the request's
// prompt (engine patch 0005, thread #47): a sentence with names Whisper
// misspells is transcribed without and with the names as prompt; with
// them it spells them, and the prompt is not copied into the text. Uses
// Whisper from the HF cache (skips when it is not downloaded).
//
//	AUDIOSMITH_E2E=1 go test -run TestWhisperPrompt -v .
func TestWhisperPrompt(t *testing.T) {
	if os.Getenv("AUDIOSMITH_E2E") == "" {
		t.Skip("set AUDIOSMITH_E2E=1 to run the real engine")
	}
	bin := engine.Find("resources")
	if bin == "" {
		t.Fatal("no engine: run scripts/build-engine.sh")
	}
	ctx := context.Background()
	e, _ := catalog.Builtin().ByID("whisper-large-v3-turbo-4bit")
	l, err := hub.New(hub.Options{CacheDir: hub.CacheDir()}).Status(ctx, e)
	if err != nil || l.State != hub.Downloaded {
		t.Skipf("%s is not downloaded (%v)", e.Repo, err)
	}
	dir := t.TempDir()
	aiff, wav := filepath.Join(dir, "names.aiff"), filepath.Join(dir, "names.wav")
	text := "Siobhan Nguyen and Aoife Ní Bhriain reviewed the Kokoro release with Zhuge Ming."
	if out, err := exec.Command("say", "-v", "Samantha", "-o", aiff, text).CombinedOutput(); err != nil {
		t.Fatalf("say: %v %s", err, out)
	}
	if out, err := exec.Command("afconvert", "-f", "WAVE", "-d", "LEI16@16000", "-c", "1", aiff, wav).CombinedOutput(); err != nil {
		t.Fatalf("afconvert: %v %s", err, out)
	}
	sup := engine.NewSupervisor(engine.Config{Bin: bin, Bus: core.NewBus(), Roles: []proto.Role{proto.RoleListen}, LogDir: dir, Offline: true,
		CacheDir: hub.CacheDir(), StatsEvery: -1, Logf: t.Logf})
	sup.Start(ctx)
	defer sup.Stop()
	deadline := time.Now().Add(2 * time.Minute)
	for sup.Status()[0].State != engine.WorkerReady {
		if time.Now().After(deadline) {
			t.Fatalf("listen worker not ready: %+v", sup.Status())
		}
		time.Sleep(20 * time.Millisecond)
	}
	transcribe := func(prompt string) string {
		raw, err := sup.Call(ctx, proto.RoleListen, core.NewID(time.Now()), proto.MethodSTTTranscribe,
			proto.TranscribeParams{Model: e.Repo, ModelPath: l.Snapshot, Audio: wav, Language: "en", Prompt: prompt}, nil)
		if err != nil {
			t.Fatal(err)
		}
		var r proto.TranscribeResult
		json.Unmarshal(raw, &r)
		return r.Text
	}
	const vocab = "Siobhan, Nguyen, Aoife, Kokoro, Zhuge Ming"
	plain, prompted := transcribe(""), transcribe(vocab)
	t.Logf("without prompt: %q", plain)
	t.Logf("with prompt:    %q", prompted)
	hits := func(s string) int {
		n := 0
		for _, w := range strings.Split(vocab, ", ") {
			if strings.Contains(strings.ToLower(s), strings.ToLower(w)) {
				n++
			}
		}
		return n
	}
	if hits(prompted) <= hits(plain) {
		t.Errorf("the prompt did not help: %d names without, %d with", hits(plain), hits(prompted))
	}
	if strings.Contains(prompted, vocab) {
		t.Errorf("the prompt was copied into the text")
	}
}
