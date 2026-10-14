package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
)

// TestRealEngineLoadsInPlace: models given by their snapshot path load
// without a copy in <cache>/mlx-audio (Qwen3-ASR and the forced aligner,
// whose loaders write a tokenizer.json beside the weights, get a folder of
// links; the snapshot itself is left as it was), and a full copy left by
// an older engine is removed (Kokoro) or replaced by links (Qwen3-ASR).
// The engine works in a temporary model folder whose repos link to the
// real HF cache, so the real cache is not touched. It needs Kokoro,
// Qwen3-ASR 0.6B and the forced aligner downloaded. AUDIOSMITH_E2E=1, macOS.
func TestRealEngineLoadsInPlace(t *testing.T) {
	if os.Getenv("AUDIOSMITH_E2E") == "" {
		t.Skip("set AUDIOSMITH_E2E=1 to run the real engine")
	}
	bin := Find(filepath.Join("..", "..", "resources"))
	if bin == "" {
		t.Fatal("no engine: run scripts/build-engine.sh")
	}
	real := os.Getenv("HF_HUB_CACHE")
	if real == "" {
		home, _ := os.UserHomeDir()
		real = filepath.Join(home, ".cache", "huggingface", "hub")
	}
	const (
		kokoro = "mlx-community/Kokoro-82M-bf16"
		asr    = "mlx-community/Qwen3-ASR-0.6B-4bit"
		align  = "mlx-community/Qwen3-ForcedAligner-0.6B-4bit"
	)
	repoDir := func(cache, repo string) string {
		return filepath.Join(cache, "models--"+strings.ReplaceAll(repo, "/", "--"))
	}
	flat := func(cache, repo string) string {
		return filepath.Join(cache, "mlx-audio", strings.ReplaceAll(repo, "/", "_"))
	}
	snapshot := func(repo string) string {
		snaps, _ := filepath.Glob(filepath.Join(repoDir(real, repo), "snapshots", "*", "config.json"))
		if len(snaps) == 0 {
			t.Skipf("%s is not downloaded", repo)
		}
		return filepath.Dir(snaps[0])
	}
	cache := t.TempDir()
	snaps := map[string]string{}
	for _, repo := range []string{kokoro, asr, align} {
		snaps[repo] = snapshot(repo)
		if err := os.Symlink(repoDir(real, repo), repoDir(cache, repo)); err != nil {
			t.Fatal(err)
		}
	}
	// Kokoro's phonemizers are repos it resolves by id: link the engine's copies.
	os.MkdirAll(filepath.Join(cache, "mlx-audio"), 0o755)
	aux, _ := filepath.Glob(filepath.Join(real, "mlx-audio", "beshkenadze_*"))
	for _, d := range aux {
		os.Symlink(d, filepath.Join(cache, "mlx-audio", filepath.Base(d)))
	}
	// Copies an older engine left: Kokoro's whole, Qwen3-ASR's weights.
	for repo, name := range map[string]string{kokoro: "kokoro-v1_0.safetensors", asr: "model.safetensors"} {
		os.MkdirAll(flat(cache, repo), 0o755)
		os.WriteFile(filepath.Join(flat(cache, repo), name), []byte("an old copy"), 0o644)
		os.WriteFile(filepath.Join(flat(cache, repo), "config.json"), []byte("{}"), 0o644)
	}
	listing := func(dir string) []string {
		es, _ := os.ReadDir(dir)
		var out []string
		for _, e := range es {
			out = append(out, e.Name())
		}
		return out
	}
	before := map[string][]string{}
	for repo, s := range snaps {
		before[repo] = listing(s)
	}

	dir := t.TempDir()
	sup := NewSupervisor(Config{Bin: bin, Bus: core.NewBus(), Roles: []proto.Role{proto.RoleGPU}, CacheDir: cache, Offline: true,
		LogDir: dir, StatsEvery: -1, Logf: t.Logf})
	sup.Start(context.Background())
	defer sup.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	wav := filepath.Join(dir, "hello.wav")
	aiff := filepath.Join(dir, "hello.aiff")
	if out, err := exec.Command("say", "-v", "Samantha", "-o", aiff, "Hello from the engine test.").CombinedOutput(); err != nil {
		t.Fatalf("say: %v %s", err, out)
	}
	if out, err := exec.Command("afconvert", "-f", "WAVE", "-d", "LEI16@16000", "-c", "1", aiff, wav).CombinedOutput(); err != nil {
		t.Fatalf("afconvert: %v %s", err, out)
	}
	if _, err := runJob(ctx, Executor(sup, proto.RoleGPU, proto.MethodTTSSynthesize), proto.SynthesizeParams{Model: kokoro, ModelPath: snaps[kokoro],
		Text: "Hello.", Voice: "af_heart", Output: proto.Output{Path: filepath.Join(dir, "k.wav")}}); err != nil {
		t.Fatalf("kokoro: %v", err)
	}
	var tr proto.TranscribeResult
	res, err := runJob(ctx, Executor(sup, proto.RoleGPU, proto.MethodSTTTranscribe), proto.TranscribeParams{Model: asr, ModelPath: snaps[asr], Audio: wav})
	if err != nil {
		t.Fatalf("qwen3-asr: %v", err)
	}
	decodeInto(t, res, &tr)
	if !strings.Contains(strings.ToLower(tr.Text), "engine test") {
		t.Errorf("qwen3-asr heard %q", tr.Text)
	}
	var ar proto.AlignResult
	res, err = runJob(ctx, Executor(sup, proto.RoleGPU, proto.MethodSTTAlign), proto.AlignParams{Model: align, ModelPath: snaps[align], Audio: wav,
		Text: "Hello from the engine test.", Language: "en"})
	if err != nil {
		t.Fatalf("aligner: %v", err)
	}
	decodeInto(t, res, &ar)
	if len(ar.Words) != 5 {
		t.Errorf("aligned %+v", ar.Words)
	}

	if _, err := os.Stat(flat(cache, kokoro)); !os.IsNotExist(err) {
		t.Errorf("Kokoro's old copy is still there: %v", listing(flat(cache, kokoro)))
	}
	for _, repo := range []string{asr, align} {
		d := flat(cache, repo)
		for _, name := range listing(d) {
			fi, err := os.Lstat(filepath.Join(d, name))
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode()&os.ModeSymlink == 0 && fi.Size() > 64<<20 {
				t.Errorf("%s: %s is a copy (%d bytes)", repo, name, fi.Size())
			}
			if fi.Mode()&os.ModeSymlink == 0 && name != "tokenizer.json" {
				t.Errorf("%s: %s is not a link", repo, name)
			}
		}
		if after := listing(snaps[repo]); !slices.Equal(after, before[repo]) {
			t.Errorf("%s: the snapshot changed: %v → %v", repo, before[repo], after)
		}
		t.Logf("%s: %v", repo, listing(d))
	}
}

// linkedCache is a temporary HF cache whose repos link to the real
// cache's (and the engine's phonemizer copies too), so an engine that
// copies or writes into its cache does it there, not in the user's.
func linkedCache(t *testing.T, repos ...string) string {
	t.Helper()
	real := os.Getenv("HF_HUB_CACHE")
	if real == "" {
		home, _ := os.UserHomeDir()
		real = filepath.Join(home, ".cache", "huggingface", "hub")
	}
	cache := t.TempDir()
	for _, repo := range repos {
		d := "models--" + strings.ReplaceAll(repo, "/", "--")
		os.Symlink(filepath.Join(real, d), filepath.Join(cache, d))
	}
	os.MkdirAll(filepath.Join(cache, "mlx-audio"), 0o755)
	aux, _ := filepath.Glob(filepath.Join(real, "mlx-audio", "beshkenadze_*"))
	for _, d := range aux {
		os.Symlink(d, filepath.Join(cache, "mlx-audio", filepath.Base(d)))
	}
	return cache
}
