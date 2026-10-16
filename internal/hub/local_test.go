package hub

// The local cache: status of other tools' downloads, engine copies.

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/hehehai/AudioSmith/internal/catalog"
)

func TestStatusOfOtherToolsCache(t *testing.T) {
	// A snapshot with only config.json (a tool fetched metadata) is not a
	// model yet.
	dir := t.TempDir()
	e := catalog.Entry{Repo: "a/b"}
	repo := RepoDir(dir, "a/b")
	os.MkdirAll(filepath.Join(repo, "blobs"), 0o755)
	os.MkdirAll(filepath.Join(repo, "snapshots", testCommit), 0o755)
	os.MkdirAll(filepath.Join(repo, "refs"), 0o755)
	os.WriteFile(filepath.Join(repo, "refs", "main"), []byte(testCommit), 0o644)
	os.WriteFile(filepath.Join(repo, "blobs", "abc"), []byte("{}"), 0o644)
	os.Symlink("../../blobs/abc", filepath.Join(repo, "snapshots", testCommit, "config.json"))
	if l, _ := Status(dir, e); l.State != Partial {
		t.Errorf("config only: %+v", l)
	}
	os.WriteFile(filepath.Join(repo, "blobs", "w"), []byte("weights"), 0o644)
	os.Symlink("../../blobs/w", filepath.Join(repo, "snapshots", testCommit, "model.safetensors"))
	if l, _ := Status(dir, e); l.State != Downloaded || l.Bytes != 9 {
		t.Errorf("with weights: %+v", l)
	}
}

func TestCleanEngineCopies(t *testing.T) {
	dir := t.TempDir()
	if got, err := CleanEngineCopies(dir); err != nil || got != nil {
		t.Fatalf("no dir: %v %v", got, err)
	}
	copies := EngineCopiesDir(dir)
	os.MkdirAll(filepath.Join(copies, "mlx-community_Empty"), 0o755)
	os.MkdirAll(filepath.Join(copies, "mlx-community_Full"), 0o755)
	os.WriteFile(filepath.Join(copies, "mlx-community_Full", "config.json"), []byte("{}"), 0o644)
	got, err := CleanEngineCopies(dir)
	if err != nil || !slices.Equal(got, []string{"mlx-community_Empty"}) {
		t.Fatalf("removed %v %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(copies, "mlx-community_Full", "config.json")); err != nil {
		t.Error("removed a full copy")
	}
}
