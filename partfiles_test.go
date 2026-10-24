package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Temporary files of writes cut short are cleaned at start, not those of
// writes in progress (family audit A13).
func TestCleanPartFiles(t *testing.T) {
	dir := t.TempDir()
	refs := filepath.Join(dir, "references")
	os.MkdirAll(refs, 0o755)
	old := filepath.Join(refs, ".rec-01JX.wav.6029.part")
	young := filepath.Join(refs, ".rec-01JY.wav.7.part")
	keep := filepath.Join(refs, "rec-01JX.wav")
	for _, p := range []string{old, young, keep} {
		os.WriteFile(p, []byte("x"), 0o644)
	}
	past := time.Now().Add(-2 * time.Hour)
	os.Chtimes(old, past, past)
	os.Chtimes(keep, past, past)
	// #101 L3: deeper down too (the engine's writes in dubs/<id>/renders/<lang>/, the cache).
	deep := filepath.Join(dir, "dubs", "01JZ", "renders", "es", ".line-3.wav.88.part")
	cache := t.TempDir()
	chunk := filepath.Join(cache, "chunks", "ab", ".c.wav.9.part")
	for _, p := range []string{deep, chunk} {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o644)
		os.Chtimes(p, past, past)
	}
	if n := cleanPartFiles(time.Hour, dir, cache); n != 3 {
		t.Fatalf("removed %d", n)
	}
	for p, want := range map[string]bool{old: false, young: true, keep: true, deep: false, chunk: false} {
		if _, err := os.Stat(p); (err == nil) != want {
			t.Errorf("%s: there %v, want %v", filepath.Base(p), err == nil, want)
		}
	}
}
