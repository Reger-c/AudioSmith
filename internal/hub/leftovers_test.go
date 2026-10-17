package hub

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/hehehai/AudioSmith/internal/catalog"
)

// fakeRepo lays out a Hugging Face cache repo: a blob of size bytes and a
// snapshot linking to it.
func fakeRepo(t *testing.T, cacheDir, repo string, size int) {
	t.Helper()
	dir := RepoDir(cacheDir, repo)
	blob := filepath.Join(dir, "blobs", "abc123")
	snap := filepath.Join(dir, "snapshots", "0123456789abcdef0123456789abcdef01234567")
	for _, d := range []string{filepath.Dir(blob), snap, filepath.Join(dir, "refs")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(blob, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../blobs/abc123", filepath.Join(snap, "model.safetensors")); err != nil {
		t.Fatal(err)
	}
}

// TestLeftovers: the removed models' cleanup (thread #43) in a temporary
// HOME's Hugging Face cache measures and deletes exactly the removed
// repos' directories and engine copies: kept models, the auxiliary repos
// kept models share (Kokoro's G2P, Turbo's S3 tokenizer), look-alike
// names and other folders stay.
func TestLeftovers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HF_HUB_CACHE", "")
	t.Setenv("HF_HOME", "")
	cache := CacheDir()
	if cache != filepath.Join(home, ".cache", "huggingface", "hub") {
		t.Fatalf("cache %s", cache)
	}
	var removed []string
	for _, r := range catalog.Builtin().Removed {
		removed = append(removed, r.Repos()...)
	}
	kept := []string{"mlx-community/Kokoro-82M-bf16", "beshkenadze/kitten-tts-g2p", "mlx-community/S3TokenizerV2",
		"mlx-community/chatterbox-turbo-8bit", "mlx-community/pocket-tts-extra", "kyutai/moshiko-pytorch-q8"}
	for _, repo := range kept {
		fakeRepo(t, cache, repo, 10)
	}
	// Kitten and Pocket are on disk, Pocket's tokenizer too, and the
	// engine copied Marvis; the others never were downloaded.
	fakeRepo(t, cache, "mlx-community/kitten-tts-mini-0.8", 1000)
	fakeRepo(t, cache, "mlx-community/pocket-tts", 2000)
	fakeRepo(t, cache, "kyutai/moshiko-pytorch-bf16", 300)
	marvis := EngineCopy(cache, "Marvis-AI/marvis-tts-250m-v0.2-MLX-8bit")
	os.MkdirAll(marvis, 0o755)
	os.WriteFile(filepath.Join(marvis, "model.safetensors"), make([]byte, 40), 0o644)
	other := filepath.Join(home, "notes.txt")
	os.WriteFile(other, []byte("mine"), 0o644)

	ls, err := Leftovers(cache, removed, noFamily)
	if err != nil {
		t.Fatal(err)
	}
	var repos []string
	var total int64
	for _, l := range ls {
		repos = append(repos, l.Repo)
		total += l.Bytes
	}
	slices.Sort(repos)
	if !slices.Equal(repos, []string{"Marvis-AI/marvis-tts-250m-v0.2-MLX-8bit", "kyutai/moshiko-pytorch-bf16", "mlx-community/kitten-tts-mini-0.8", "mlx-community/pocket-tts"}) {
		t.Fatalf("leftovers %v", repos)
	}
	if total != 3340 { // blobs once (links not followed) + the engine copy
		t.Errorf("total %d", total)
	}
	freed, stayed, err := RemoveLeftovers(cache, removed, noFamily)
	if err != nil || freed != total || len(stayed) != 0 {
		t.Fatalf("freed %d, kept %v, %v", freed, stayed, err)
	}
	if ls, _ := Leftovers(cache, removed, noFamily); len(ls) != 0 {
		t.Errorf("left %v", ls)
	}
	for _, repo := range kept {
		if _, err := os.Stat(filepath.Join(RepoDir(cache, repo), "blobs", "abc123")); err != nil {
			t.Errorf("%s: %v", repo, err)
		}
	}
	if _, err := os.Stat(EngineCopiesDir(cache)); err != nil {
		t.Error("the engine copies folder went")
	}
	if _, err := os.Stat(other); err != nil {
		t.Error(err)
	}
	for _, bad := range []string{"", "..", "../x", "a/../../b", "a/b/c", "/abs", "a"} {
		if _, _, err := RemoveLeftovers(cache, []string{bad}, noFamily); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
