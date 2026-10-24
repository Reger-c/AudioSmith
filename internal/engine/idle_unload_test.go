package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/engine/proto"
)

// loadedOn lists the resident models of role's fake engine.
func loadedOn(t *testing.T, s Supervisor, role proto.Role) []string {
	t.Helper()
	raw, err := s.Call(context.Background(), role, "", proto.MethodModelsLoaded, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var res proto.LoadedResult
	json.Unmarshal(raw, &res)
	var ids []string
	for _, m := range res.Models {
		ids = append(ids, m.Model)
	}
	return ids
}

func waitLoadedOn(t *testing.T, s Supervisor, role proto.Role, want int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		got := loadedOn(t, s, role)
		if len(got) == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: loaded %v, want %d models", role, got, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// #109: the idle limit unloads the listen worker's dictation model too,
// and none of a held worker's models (a dictation recording, a live
// call) until it is released.
func TestIdleUnloadEveryWorkerButHeld(t *testing.T) {
	s, w := newTestSup(t, "", func(c *Config) {
		c.OnDemand = []proto.Role{proto.RoleListen}
		c.IdleUnload = 300 * time.Millisecond
	})
	w.until(t, "ready", ready)
	ctx := context.Background()
	const asr = "mlx-community/Qwen3-ASR-0.6B-8bit"
	if _, err := s.Call(ctx, proto.RoleGPU, "L1", proto.MethodModelsLoad, proto.LoadParams{Model: "a/one"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Call(ctx, proto.RoleListen, "L2", proto.MethodModelsLoad, proto.LoadParams{Model: asr, Task: "stt"}, nil); err != nil {
		t.Fatal(err)
	}
	waitLoadedOn(t, s, proto.RoleListen, 1, 5*time.Second)

	release := s.Hold(proto.RoleListen)
	waitLoadedOn(t, s, proto.RoleGPU, 0, 5*time.Second) // gpu: unloaded after the limit
	time.Sleep(500 * time.Millisecond)                   // well over the limit
	if got := loadedOn(t, s, proto.RoleListen); len(got) != 1 {
		t.Fatalf("a held worker's model was unloaded: %v", got)
	}
	release()
	release() // once
	waitLoadedOn(t, s, proto.RoleListen, 0, 5*time.Second)
}

// #109: a model a running job uses is never unloaded, whatever the idle
// limit; once the job ends the limit counts from its last use.
func TestIdleUnloadKeepsModelsInUse(t *testing.T) {
	s, w := newTestSup(t, "", func(c *Config) { c.IdleUnload = 100 * time.Millisecond })
	w.until(t, "ready", ready)
	notify, started := jobStarted()
	done := make(chan error, 1)
	go func() {
		_, err := s.Call(context.Background(), proto.RoleGPU, "J1", proto.MethodTTSSynthesize, synth("slow", false), notify)
		done <- err
	}()
	started(t)
	time.Sleep(600 * time.Millisecond) // six idle limits while the job runs
	if got := loadedModels(t, s); len(got) != 1 {
		t.Fatalf("the job's model was unloaded: %v", got)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	waitLoaded(t, s, 0)
}

// #109: the supervisor unloads only if the model is still as idle as its
// last engine.stats said: one a job took since stays.
func TestUnloadSkipsAModelUsedSince(t *testing.T) {
	sup, w := newTestSup(t, "", func(c *Config) { c.StatsEvery = -1 })
	w.until(t, "ready", ready)
	if _, err := sup.Call(context.Background(), proto.RoleGPU, "L1", proto.MethodModelsLoad, proto.LoadParams{Model: "a/one"}, nil); err != nil {
		t.Fatal(err)
	}
	s := sup.(*supervisor)
	stale := candidate{proto.RoleGPU, proto.LoadedModel{Model: "a/one", IdleSeconds: 3600}} // used just now
	if s.unload(stale, "test") {
		t.Fatal("unloaded a model used since its stats")
	}
	if got := loadedModels(t, sup); len(got) != 1 {
		t.Fatalf("loaded %v", got)
	}
	time.Sleep(50 * time.Millisecond)
	if !s.unload(candidate{proto.RoleGPU, proto.LoadedModel{Model: "a/one", IdleSeconds: 0.04}}, "test") {
		t.Fatal("an idle model was kept")
	}
	waitLoaded(t, sup, 0)
}
