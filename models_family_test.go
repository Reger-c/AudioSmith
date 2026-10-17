package main

// The family apps' models in the app (models_family.go): Settings →
// Storage → Erase everything with the models keeps what Dicta uses, and
// Models → Clean up lists the removed models it keeps.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/i18n"
)

var testDicta = hub.Family{Installed: func(bid string) bool { return bid == "dev.hehehai.dicta" }}

// fakeHubRepo lays out a repo in a Hugging Face cache: a snapshot with
// one file linked to a blob of size bytes.
func fakeHubRepo(t *testing.T, cache, repo string, size int) {
	t.Helper()
	dir := hub.RepoDir(cache, repo)
	snap := filepath.Join(dir, "snapshots", strings.Repeat("1", 40))
	for _, d := range []string{filepath.Join(dir, "blobs"), snap} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "blobs", "b10b"), make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../blobs/b10b", filepath.Join(snap, "model.safetensors")); err != nil {
		t.Fatal(err)
	}
}

// TestEraseKeepsFamilyModels: erasing everything with the models deletes
// the catalogue's repos except those Dicta uses while it is installed
// (Qwen3-ASR, and Whisper's tokenizer repo); files outside the
// catalogue stay.
func TestEraseKeepsFamilyModels(t *testing.T) {
	repos := catalogRepos()
	for _, r := range []string{"mlx-community/Kokoro-82M-bf16", "mlx-community/Qwen3-ASR-0.6B-4bit", "openai/whisper-large-v3", "beshkenadze/kitten-tts-g2p"} {
		if !slices.Contains(repos, r) {
			t.Fatalf("catalogRepos lacks %s", r)
		}
	}
	cache := t.TempDir()
	for _, r := range []string{"mlx-community/Kokoro-82M-bf16", "mlx-community/Qwen3-ASR-0.6B-4bit", "openai/whisper-large-v3", "someone/else"} {
		fakeHubRepo(t, cache, r, 100)
	}
	freed, kept, err := hub.RemoveLeftovers(cache, repos, testDicta)
	if err != nil || freed != 100 || len(kept) != 2 {
		t.Fatalf("freed %d, kept %+v, %v", freed, kept, err)
	}
	for r, want := range map[string]bool{"mlx-community/Kokoro-82M-bf16": false, "mlx-community/Qwen3-ASR-0.6B-4bit": true, "openai/whisper-large-v3": true, "someone/else": true} {
		if _, err := os.Stat(hub.RepoDir(cache, r)); (err == nil) != want {
			t.Errorf("%s: there %v, want %v", r, err == nil, want)
		}
	}
}

// TestRemovedKeptListed: Clean up's confirmation lists a removed model
// Dicta uses as kept, and does not count it in what it frees.
func TestRemovedKeptListed(t *testing.T) {
	i18n.SetLocale("en")
	a := &app{}
	a.removedLeft = []hub.Leftover{
		{Repo: "mlx-community/gemma-4-e4b-it-4bit", Bytes: 5000, UsedBy: []string{"Dicta"}},
		{Repo: "mlx-community/parakeet-tdt-0.6b-v3", Bytes: 300},
	}
	if n := a.removedBytes(); n != 300 {
		t.Errorf("bytes %d", n)
	}
	names, bytes, kept := a.removedOnDisk()
	if len(names) != 1 || bytes[0] != 300 || !strings.Contains(names[0], "Parakeet") {
		t.Errorf("to clean %v %v", names, bytes)
	}
	if !slices.Equal(kept, []string{"Gemma 4 E4B · Kept: used by Dicta"}) {
		t.Errorf("kept %q", kept)
	}
	i18n.SetLocale("zh-Hans")
	defer i18n.SetLocale("en")
	if _, _, kept := a.removedOnDisk(); !slices.Equal(kept, []string{"Gemma 4 E4B · 已保留：Dicta 在使用"}) {
		t.Errorf("zh kept %q", kept)
	}
}

// TestSharedText: Delete of a model Dicta uses only says why its files
// stay (an OK-only dialog; phase 0 removes nothing).
func TestSharedText(t *testing.T) {
	i18n.SetLocale("en")
	if got := sharedText([]string{"Dicta"}, 712_000_000); got != "Dicta also uses this model, so its files (712 MB) stay on this Mac." {
		t.Errorf("en %q", got)
	}
	i18n.SetLocale("zh-Hans")
	defer i18n.SetLocale("en")
	if got := sharedText([]string{"Dicta", "VoxtNew"}, 712_000_000); got != "Dicta、VoxtNew 也在使用这个模型，文件（712 MB）会保留在这台 Mac 上。" {
		t.Errorf("zh %q", got)
	}
}
