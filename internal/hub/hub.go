// Package hub owns model files on disk: it finds the Hugging Face cache,
// reports which catalogue entries are downloaded, and (thread C, M0)
// downloads, verifies and deletes them, so engines only ever load local
// files (engine.hello offline:true).
//
// Files go in the huggingface_hub cache layout, shared with the Python
// tools and swift-huggingface:
//
//	<cache>/models--<owner>--<name>/blobs/<sha256 or etag>
//	<cache>/models--<owner>--<name>/snapshots/<revision>/<path> → ../../blobs/…
//	<cache>/models--<owner>--<name>/refs/main  (the revision "main" points at)
//
// Imports: catalog (events satisfy core.Event structurally). Downloads run
// as jobs.LaneNet jobs of kind "model.download"; the Downloader does the
// work for their executor.
package hub

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/hehehai/AudioSmith/internal/catalog"
)

// CacheDir is where models live: the same rules as huggingface_hub
// ($HF_HUB_CACHE, else $HF_HOME/hub, else ~/.cache/huggingface/hub).
// Settings may override it later (settings.Models.CacheDir).
func CacheDir() string {
	if d := os.Getenv("HF_HUB_CACHE"); d != "" {
		return d
	}
	if d := os.Getenv("HF_HOME"); d != "" {
		return filepath.Join(d, "hub")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "huggingface", "hub")
}

// RepoDir is the cache directory of a repo id ("owner/name").
func RepoDir(cacheDir, repo string) string {
	return filepath.Join(cacheDir, "models--"+strings.ReplaceAll(repo, "/", "--"))
}

// LocalState is how much of an entry is on disk.
type LocalState string

const (
	Missing    LocalState = "missing"    // nothing
	Partial    LocalState = "partial"    // some files, or interrupted downloads (resumable)
	Downloaded LocalState = "downloaded" // every file present (and verified, when hashes are known)
)

// Local describes an entry on disk.
type Local struct {
	State    LocalState
	Snapshot string // snapshots/<revision> directory, for proto.LoadParams.Path; "" when Missing
	Bytes    int64  // on disk (blobs this entry references)
	Missing  []string
}

// Progress of a download. Published as DownloadProgress and reported as
// jobs.Progress by the download job.
type Progress struct {
	Repo       string
	File       string // current file
	Bytes      int64  // done, all files
	TotalBytes int64
	Mirror     string // endpoint in use (after racing)
}

// DownloadProgress is published on the core.Bus while downloading (at
// most ~4×/s per entry). Topic "model.download".
type DownloadProgress struct{ Progress Progress }

func (DownloadProgress) Topic() string { return "model.download" }

// Options configure a Downloader.
type Options struct {
	CacheDir string
	// Endpoints are raced on the first request; the fastest healthy one
	// wins ("https://huggingface.co", "https://hf-mirror.com"). Empty =
	// $HF_ENDPOINT, else huggingface.co.
	Endpoints []string
	// Token returns the HF token (from the Keychain; $HF_TOKEN in dev) for
	// gated repos; nil or "" = anonymous.
	Token func() string
	// Parallel is the number of files (and ranges of big files) fetched at
	// once. 0 = 4.
	Parallel int
}

// Downloader manages model files. Goroutine-safe; methods may block on I/O.
type Downloader interface {
	// Status reports what of e is on disk. Cheap enough for a page load,
	// but still I/O: not from a build pass.
	Status(ctx context.Context, e catalog.Entry) (Local, error)
	// Plan lists the files to fetch (from the HF API when e.Files is
	// empty) and checks free disk space; it errors before downloading
	// anything when space is short or the repo is gated without a token.
	Plan(ctx context.Context, e catalog.Entry) (Plan, error)
	// Download fetches what Plan lists, resuming partial files, verifying
	// sha256 of LFS files, and writing refs/ last so a snapshot is never
	// half there. progress is called often; ctx cancels (partial files
	// stay for resuming).
	Download(ctx context.Context, e catalog.Entry, progress func(Progress)) error
	// Verify re-hashes the files and returns the bad or missing ones; a
	// later Download repairs them.
	Verify(ctx context.Context, e catalog.Entry) ([]string, error)
	// Delete removes the entry's snapshot and the blobs no other snapshot uses.
	Delete(ctx context.Context, e catalog.Entry) error
}

// Plan is what a download will do.
type Plan struct {
	Revision  string // resolved commit
	Files     []catalog.File
	Bytes     int64 // still to download
	FreeBytes int64 // on the cache volume
}
