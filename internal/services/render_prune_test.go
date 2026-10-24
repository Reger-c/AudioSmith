package services

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestPruneCacheAtMostEveryMinute: the chunk cache is walked and kept
// under its cap, at most once a minute (review #102: every render, a
// dub's every line, walked it all).
func TestPruneCacheAtMostEveryMinute(t *testing.T) {
	r := &renderer{cacheDir: t.TempDir(), cacheMax: 1000}
	dir := r.chunkDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(n int) {
		t.Helper()
		for i := range n {
			p := filepath.Join(dir, time.Now().Format("150405.000000000")+"-"+string(rune('a'+i))+".wav")
			if err := os.WriteFile(p, make([]byte, 400), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	count := func() int {
		es, _ := os.ReadDir(dir)
		return len(es)
	}
	write(5)
	r.pruneCache()
	if n := count(); n > 2 {
		t.Fatalf("%d chunks of 400 bytes left under a cap of 1000", n)
	}
	write(5)
	r.pruneCache()
	if n := count(); n < 5 {
		t.Fatalf("walked again at once: %d chunks left", n)
	}
	r.pruneAt = time.Now().Add(-pruneEvery)
	r.pruneCache()
	if n := count(); n > 2 {
		t.Fatalf("not walked a minute later: %d chunks left", n)
	}
}
