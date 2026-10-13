package hub

import (
	"path/filepath"
	"testing"
)

func TestCacheDir(t *testing.T) {
	t.Setenv("HF_HUB_CACHE", "")
	t.Setenv("HF_HOME", "/data/hf")
	if got := CacheDir(); got != filepath.Join("/data/hf", "hub") {
		t.Errorf("HF_HOME: %q", got)
	}
	t.Setenv("HF_HUB_CACHE", "/cache")
	if got := CacheDir(); got != "/cache" {
		t.Errorf("HF_HUB_CACHE: %q", got)
	}
	if got := RepoDir("/cache", "mlx-community/Kokoro-82M-bf16"); got != "/cache/models--mlx-community--Kokoro-82M-bf16" {
		t.Errorf("RepoDir %q", got)
	}
}
