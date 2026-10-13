package engine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindEngine(t *testing.T) {
	t.Setenv("AUDIOSMITH_ENGINE", "")
	if got := Find(t.TempDir()); got != "" {
		t.Errorf("found %q in an empty directory", got)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "res", "engine"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "res", "engine", "audiosmith-engine"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	got := Find("res")
	if !filepath.IsAbs(got) || filepath.Base(got) != "audiosmith-engine" {
		t.Fatalf("Find = %q", got)
	}
	t.Setenv("AUDIOSMITH_ENGINE", "bin/engine")
	if got := Find("res"); got != filepath.Join(dir, "bin", "engine") {
		t.Errorf("with AUDIOSMITH_ENGINE: %q", got)
	}
}
