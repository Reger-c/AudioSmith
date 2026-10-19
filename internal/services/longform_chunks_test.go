package services

import (
	"os"
	"path/filepath"
	"testing"
)

// The longform chunks live in the data folder, not in Caches, which
// cleaners empty (family audit A7); those of the old place move over once.
func TestLongformChunksInDataFolder(t *testing.T) {
	data, cache := t.TempDir(), t.TempDir()
	old := filepath.Join(cache, "longform", "chunks")
	os.MkdirAll(old, 0o755)
	os.WriteFile(filepath.Join(old, "k-1.wav"), []byte("chunk"), 0o644)
	r := &renderer{cacheDir: cache, dataDir: data}
	dir := r.longformChunkDir()
	if dir != filepath.Join(data, "longform-chunks") {
		t.Fatalf("dir %s", dir)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "k-1.wav")); err != nil || string(b) != "chunk" {
		t.Fatalf("not moved: %q %v", b, err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("the old folder stayed")
	}
	if (&renderer{cacheDir: cache}).longformChunkDir() != old {
		t.Error("without a data folder: the cache's")
	}
}
