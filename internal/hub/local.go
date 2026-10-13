package hub

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hehehai/AudioSmith/internal/catalog"
)

// weightExts are the extensions of model weights: a snapshot without one
// is not a usable model, whatever its ref says (a tool may have fetched
// config.json alone).
var weightExts = []string{".safetensors", ".bin", ".npz", ".gguf", ".onnx", ".pt", ".pth", ".mlmodelc", ".mlpackage"}

// commitOf is the snapshot e uses on disk: its pinned commit, else what
// its ref (refs/main) points at; "" when unknown.
func commitOf(repoDir string, e catalog.Entry) string {
	rev := revisionOf(e)
	if isCommit(rev) {
		return rev
	}
	data, err := os.ReadFile(filepath.Join(repoDir, "refs", filepath.FromSlash(rev)))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// Status reports what of e is on disk, from the files alone (no network).
func (c *Client) Status(ctx context.Context, e catalog.Entry) (Local, error) {
	return Status(c.opts.CacheDir, e)
}

// Status is Client.Status for a cache directory. An entry whose
// auxiliary repos are not all there is Partial, and its Missing lists
// them as "<repo>:".
func Status(cacheDir string, e catalog.Entry) (Local, error) {
	l, err := statusOne(cacheDir, e, true)
	if err != nil {
		return l, err
	}
	for _, a := range e.Aux {
		al, err := statusOne(cacheDir, auxEntry(e, a), false)
		if err != nil {
			return l, err
		}
		l.Bytes += al.Bytes
		if al.State != Downloaded {
			l.Missing = append(l.Missing, a.Repo+":")
			if l.State == Downloaded || al.State == Partial {
				l.State = Partial
			}
		}
	}
	return l, nil
}

// statusOne is the status of one repo; weights asks for a weights file
// when e lists no files.
func statusOne(cacheDir string, e catalog.Entry, weights bool) (Local, error) {
	repoDir := RepoDir(cacheDir, e.Repo)
	var l Local
	if _, err := os.Stat(repoDir); os.IsNotExist(err) {
		l.State = Missing
		return l, nil
	}
	incomplete := false
	if entries, err := os.ReadDir(filepath.Join(repoDir, "blobs")); err == nil {
		for _, de := range entries {
			if strings.Contains(de.Name(), ".incomplete") {
				incomplete = true
				if info, err := de.Info(); err == nil {
					l.Bytes += info.Size()
				}
			}
		}
	}
	commit := commitOf(repoDir, e)
	snap := filepath.Join(repoDir, "snapshots", commit)
	if commit == "" || !isDir(snap) {
		if incomplete || nonEmpty(filepath.Join(repoDir, "blobs")) || nonEmpty(filepath.Join(repoDir, "snapshots")) {
			l.State = Partial
		} else {
			l.State = Missing
		}
		return l, nil
	}
	l.Snapshot = snap
	hasWeights := false
	present := map[string]bool{}
	err := filepath.WalkDir(snap, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(snap, p)
		rel = filepath.ToSlash(rel)
		st, err := os.Stat(p) // follows the link to the blob
		if err != nil {
			l.Missing = append(l.Missing, rel)
			return nil
		}
		present[rel] = true
		l.Bytes += st.Size()
		if slices.Contains(weightExts, strings.ToLower(filepath.Ext(rel))) {
			hasWeights = true
		}
		return nil
	})
	if err != nil {
		return l, err
	}
	for _, f := range e.Files {
		if !present[f.Path] && !slices.Contains(l.Missing, f.Path) {
			l.Missing = append(l.Missing, f.Path)
		}
	}
	switch {
	case len(l.Missing) > 0 || incomplete || weights && len(e.Files) == 0 && !hasWeights:
		l.State = Partial
	default:
		l.State = Downloaded
	}
	return l, nil
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func nonEmpty(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}

// Verify hashes every file of e's snapshot against the HF API's listing
// (or e.Files when they carry hashes) and returns those missing or bad;
// bad blobs are removed, so that Download fetches them again. Files of
// auxiliary repos are named "<repo>:<path>".
func (c *Client) Verify(ctx context.Context, e catalog.Entry) ([]string, error) {
	defer c.lockRepo(e.Repo)()
	bad, err := c.verifyOne(ctx, e)
	if err != nil {
		return nil, err
	}
	for _, a := range e.Aux {
		if commitOf(RepoDir(c.opts.CacheDir, a.Repo), auxEntry(e, a)) == "" {
			bad = append(bad, a.Repo+":")
			continue
		}
		unlock := c.lockRepo(a.Repo)
		ab, err := c.verifyOne(ctx, auxEntry(e, a))
		unlock()
		if err != nil {
			return nil, err
		}
		for _, p := range ab {
			bad = append(bad, a.Repo+":"+p)
		}
	}
	return bad, nil
}

func (c *Client) verifyOne(ctx context.Context, e catalog.Entry) ([]string, error) {
	repoDir := RepoDir(c.opts.CacheDir, e.Repo)
	commit := commitOf(repoDir, e)
	if commit == "" {
		return nil, fmt.Errorf("%s is not downloaded", e.Repo)
	}
	var files []remoteFile
	if hashed := len(e.Files) > 0 && !slices.ContainsFunc(e.Files, func(f catalog.File) bool { return f.SHA256 == "" }); hashed {
		for _, f := range e.Files {
			files = append(files, remoteFile{File: f, blob: f.SHA256, lfs: true})
		}
	} else {
		pinned := e
		pinned.Revision = commit
		p, err := c.plan(ctx, pinned)
		if p == nil {
			return nil, err
		}
		files = p.files
	}
	var bad []string
	snap := filepath.Join(repoDir, "snapshots", commit)
	for _, f := range files {
		link := filepath.Join(snap, filepath.FromSlash(f.Path))
		blob, err := filepath.EvalSymlinks(link)
		if err != nil {
			bad = append(bad, f.Path)
			continue
		}
		ok, err := checkBlob(ctx, blob, f)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			bad = append(bad, f.Path)
			continue
		}
		if !ok {
			bad = append(bad, f.Path)
			os.Remove(blob)
		}
	}
	return bad, nil
}

// Delete removes e's snapshot, the refs pointing at it, and the blobs no
// other snapshot links to (partial downloads included). The repo
// directory goes when no snapshot is left. Auxiliary repos stay: they are
// small and other entries may share them.
func (c *Client) Delete(ctx context.Context, e catalog.Entry) error {
	defer c.lockRepo(e.Repo)()
	return Delete(c.opts.CacheDir, e)
}

// Delete is Client.Delete for a cache directory.
func Delete(cacheDir string, e catalog.Entry) error {
	repoDir := RepoDir(cacheDir, e.Repo)
	if _, err := os.Stat(repoDir); os.IsNotExist(err) {
		return nil
	}
	snaps := filepath.Join(repoDir, "snapshots")
	if commit := commitOf(repoDir, e); commit != "" {
		if err := os.RemoveAll(filepath.Join(snaps, commit)); err != nil {
			return err
		}
		os.RemoveAll(filepath.Join(snaps, commit+".incomplete"))
		refs := filepath.Join(repoDir, "refs")
		filepath.WalkDir(refs, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				if data, err := os.ReadFile(p); err == nil && strings.TrimSpace(string(data)) == commit {
					os.Remove(p)
				}
			}
			return nil
		})
	}
	// Blobs the remaining snapshots use.
	used := map[string]bool{}
	filepath.WalkDir(snaps, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type()&fs.ModeSymlink != 0 {
			if target, err := os.Readlink(p); err == nil {
				used[filepath.Base(target)] = true
			}
		}
		return nil
	})
	if !nonEmpty(snaps) {
		return os.RemoveAll(repoDir)
	}
	blobs := filepath.Join(repoDir, "blobs")
	entries, _ := os.ReadDir(blobs)
	for _, de := range entries {
		name := de.Name()
		base, _, _ := strings.Cut(name, ".incomplete")
		if !used[base] {
			if err := os.Remove(filepath.Join(blobs, name)); err != nil {
				return err
			}
		}
	}
	return nil
}

// EngineCopiesDir is where mlx-audio-swift keeps its own copies of the
// snapshots it loads: <cache>/mlx-audio/<owner>_<name>/.
func EngineCopiesDir(cacheDir string) string { return filepath.Join(cacheDir, "mlx-audio") }

// CleanEngineCopies removes the empty directories mlx-audio-swift leaves
// in EngineCopiesDir after a failed load. Left there, the next load of
// that repo finds an "incomplete" model and deletes the repo's snapshot
// from the hub cache as well (research/m0-spikes.md §2). The supervisor
// calls it before starting an engine. It returns what it removed.
func CleanEngineCopies(cacheDir string) ([]string, error) {
	dir := EngineCopiesDir(cacheDir)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, de := range entries {
		p := filepath.Join(dir, de.Name())
		if !de.IsDir() || nonEmpty(p) {
			continue
		}
		if err := os.Remove(p); err == nil {
			removed = append(removed, de.Name())
		}
	}
	return removed, nil
}
