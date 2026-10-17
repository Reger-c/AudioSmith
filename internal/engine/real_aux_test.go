package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/jobs"
)

// TestRealEngineAuxPaths: Kokoro's G2P (an auxiliary repo the engine
// finds by id in mlx-audio/) loads from the snapshot the app pinned
// (auxPaths), though the cache also holds another commit of the repo
// whose ref is main (another ActNow app's) and an engine copy made from
// that one. It downloads Kokoro (about 0.5 GB with its G2P repos) into
// AUDIOSMITH_E2E_HUB, a throwaway folder (default: a temporary one).
// AUDIOSMITH_E2E=1, macOS:
//
//	AUDIOSMITH_E2E=1 AUDIOSMITH_E2E_HUB=/tmp/x/hub go test -run TestRealEngineAuxPaths -v ./internal/engine
func TestRealEngineAuxPaths(t *testing.T) {
	if os.Getenv("AUDIOSMITH_E2E") == "" {
		t.Skip("set AUDIOSMITH_E2E=1 to run the real engine")
	}
	bin := Find(filepath.Join("..", "..", "resources"))
	if bin == "" {
		t.Fatal("no engine: run scripts/build-engine.sh")
	}
	cache := os.Getenv("AUDIOSMITH_E2E_HUB")
	if cache == "" {
		cache = t.TempDir()
	}
	home, _ := os.UserHomeDir()
	if filepath.Clean(cache) == filepath.Join(home, ".cache", "huggingface", "hub") {
		t.Fatal("AUDIOSMITH_E2E_HUB must be a throwaway folder")
	}
	e, ok := catalog.Builtin().ByID("kokoro-82m-bf16")
	if !ok {
		t.Fatal("no Kokoro in the catalogue")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	if err := hub.New(hub.Options{CacheDir: cache}).Download(ctx, e, nil); err != nil {
		t.Fatal(err)
	}
	l, _ := hub.Status(cache, e)
	aux := hub.AuxSnapshots(cache, e)
	const g2p = "beshkenadze/kitten-tts-g2p"
	if l.State != hub.Downloaded || aux[g2p] == "" {
		t.Fatalf("Kokoro %+v, aux %v", l, aux)
	}
	// Another commit of the G2P repo, named by refs/main, and the engine's
	// copy of it: files of the same names that are not weights.
	other := filepath.Join(hub.RepoDir(cache, g2p), "snapshots", strings.Repeat("f", 40))
	stale := filepath.Join(cache, "mlx-audio", strings.ReplaceAll(g2p, "/", "_"))
	names, _ := os.ReadDir(aux[g2p])
	for _, dir := range []string{other, stale} {
		os.MkdirAll(dir, 0o755)
		for _, n := range names {
			os.Remove(filepath.Join(dir, n.Name()))
			os.WriteFile(filepath.Join(dir, n.Name()), []byte("another commit, not these weights"), 0o644)
		}
	}
	os.WriteFile(filepath.Join(stale, "config.json"), []byte("{}"), 0o644)
	os.WriteFile(filepath.Join(hub.RepoDir(cache, g2p), "refs", "main"), []byte(filepath.Base(other)), 0o644)

	dir := t.TempDir()
	sup := NewSupervisor(Config{Bin: bin, Bus: core.NewBus(), Roles: []proto.Role{proto.RoleGPU}, CacheDir: cache, Offline: true,
		LogDir: dir, StatsEvery: -1, Logf: t.Logf})
	sup.Start(ctx)
	defer sup.Stop()
	ex := executor(sup, proto.RoleGPU, proto.MethodTTSSynthesize, ExecutorOptions{
		ModelPath: func(context.Context, string) (string, error) { return l.Snapshot, nil },
		AuxPaths:  func(context.Context, string) map[string]string { return aux },
	})
	if _, err := runJob(ctx, ex, proto.SynthesizeParams{Model: e.Repo, Text: "Hello from the pinned phonemizer.", Voice: "af_heart",
		Output: proto.Output{Path: filepath.Join(dir, "k.wav")}}); err != nil {
		t.Fatalf("kokoro with auxPaths: %v", err)
	}
	for _, n := range names {
		if strings.HasPrefix(n.Name(), ".") {
			continue // hidden files (.gitattributes) are not linked
		}
		target, err := os.Readlink(filepath.Join(stale, n.Name()))
		if err != nil || target != filepath.Join(aux[g2p], n.Name()) {
			t.Errorf("%s: %q %v, want a link to the pinned snapshot", n.Name(), target, err)
		}
	}
	// A pinned snapshot that is not there: a clear error.
	ex = executor(sup, proto.RoleGPU, proto.MethodTTSSynthesize, ExecutorOptions{
		AuxPaths: func(context.Context, string) map[string]string {
			return map[string]string{g2p: filepath.Join(dir, "gone")}
		},
	})
	_, err := runJob(ctx, ex, proto.SynthesizeParams{Model: e.Repo, ModelPath: l.Snapshot, Text: "Hi.", Voice: "af_heart",
		Output: proto.Output{Path: filepath.Join(dir, "k2.wav")}})
	var je *jobs.Error
	if !errors.As(err, &je) || je.Code != proto.CodeModelNotDownloaded {
		t.Errorf("missing aux snapshot: %v", err)
	}
}
