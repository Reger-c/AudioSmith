package engine

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
)

// TestRealIdleUnloadListen (#109) runs the listen worker with dictation's
// Qwen3-ASR 0.6B: its cold load, a warm and a cold transcription (the
// first dictation after the idle limit unloaded the model), and the
// memory before and after the supervisor unloads it for being idle.
// AUDIOSMITH_E2E=1, macOS, the model in HF_HUB_CACHE:
//
//	AUDIOSMITH_E2E=1 HF_HUB_CACHE=… go test -run TestRealIdleUnloadListen -v ./internal/engine
func TestRealIdleUnloadListen(t *testing.T) {
	if os.Getenv("AUDIOSMITH_E2E") == "" {
		t.Skip("set AUDIOSMITH_E2E=1 to run the real engine")
	}
	bin := Find(filepath.Join("..", "..", "resources"))
	if bin == "" {
		t.Fatal("no engine: run scripts/build-engine.sh")
	}
	const asr = "mlx-community/Qwen3-ASR-0.6B-4bit"
	cache := linkedCache(t, asr)
	if m, _ := filepath.Glob(filepath.Join(cache, "models--mlx-community--Qwen3-ASR-0.6B-4bit", "snapshots", "*", "config.json")); len(m) == 0 {
		t.Skip("Qwen3-ASR 0.6B is not downloaded")
	}
	dir := t.TempDir()
	speech := filepath.Join(dir, "speech.wav")
	if out, err := exec.Command("say", "-o", speech, "--data-format=LEI16@16000", "Send the draft to Anna before the meeting tomorrow morning.").CombinedOutput(); err != nil {
		t.Fatalf("say: %v %s", err, out)
	}
	const idle = 5 * time.Second
	sup := NewSupervisor(Config{Bin: bin, Bus: core.NewBus(), Roles: []proto.Role{proto.RoleListen}, CacheDir: cache, Offline: true,
		LogDir: dir, StatsEvery: 250 * time.Millisecond, IdleUnload: idle, Logf: t.Logf})
	if err := sup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer sup.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	mem := func(what string) {
		raw, err := sup.Call(ctx, proto.RoleListen, "", proto.MethodStats, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		var st proto.StatsResult
		json.Unmarshal(raw, &st)
		rss := 0
		for _, w := range sup.Status() {
			if w.Role == proto.RoleListen && w.PID > 0 {
				out, _ := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(w.PID)).Output()
				rss, _ = strconv.Atoi(strings.TrimSpace(string(out)))
			}
		}
		t.Logf("%-28s models %d, MLX active %4d MB, cache %4d MB, process RSS %4d MB", what, len(st.Models), st.Memory.Active>>20, st.Memory.Cache>>20, rss>>10)
	}
	transcribe := func(id string) (time.Duration, bool) {
		loading := false
		start := time.Now()
		raw, err := sup.Call(ctx, proto.RoleListen, id, proto.MethodSTTTranscribe, proto.TranscribeParams{Model: asr, Audio: speech, Language: "en"},
			func(m proto.Message) {
				if strings.Contains(string(m.Params), `"loading"`) {
					loading = true
				}
			})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.ToLower(string(raw)), "anna") {
			t.Errorf("transcript %s", raw)
		}
		return time.Since(start), loading
	}

	mem("before")
	start := time.Now()
	raw, err := sup.Call(ctx, proto.RoleListen, "load1", proto.MethodModelsLoad, proto.LoadParams{Model: asr, Task: "stt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var lr proto.LoadResult
	json.Unmarshal(raw, &lr)
	t.Logf("cold load (first in the process): %.2f s (engine %.2f s)", time.Since(start).Seconds(), lr.Seconds)
	mem("loaded")
	d, _ := transcribe("warm1")
	t.Logf("warm transcription: %.2f s", d.Seconds())
	d, _ = transcribe("warm2")
	t.Logf("warm transcription: %.2f s", d.Seconds())
	mem("after transcribing")

	// The idle limit unloads it; the engine clears MLX's cache.
	gone := time.Now()
	for {
		raw, err := sup.Call(ctx, proto.RoleListen, "", proto.MethodModelsLoaded, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		var res proto.LoadedResult
		json.Unmarshal(raw, &res)
		if len(res.Models) == 0 {
			break
		}
		if time.Since(gone) > idle+30*time.Second {
			t.Fatalf("not unloaded after %s: %+v", time.Since(gone), res.Models)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("unloaded %.1f s after the last use (limit %s)", time.Since(gone).Seconds(), idle)
	mem("unloaded")

	// The first dictation after it: loads, then transcribes.
	d, loading := transcribe("cold1")
	t.Logf("cold transcription (load + transcribe): %.2f s, loading stage %v", d.Seconds(), loading)
	mem("reloaded")
	d, _ = transcribe("warm3")
	t.Logf("warm transcription: %.2f s", d.Seconds())

	// Held (a dictation that records): not unloaded past the limit.
	release := sup.Hold(proto.RoleListen)
	time.Sleep(idle + 2*time.Second)
	raw, _ = sup.Call(ctx, proto.RoleListen, "", proto.MethodModelsLoaded, nil, nil)
	var held proto.LoadedResult
	json.Unmarshal(raw, &held)
	if len(held.Models) != 1 {
		t.Errorf("unloaded while held: %+v", held.Models)
	}
	release()
}
