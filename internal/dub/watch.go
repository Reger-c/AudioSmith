package dub

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Watch folders: the batch queue takes new videos dropped into a folder.
// Every 5 s (WatchInterval) the folder is listed (not its subfolders); a
// file counts once two scans in a row see the same size and modification
// time (it is not being copied any more). Files there when watching
// starts are ignored. The tracker is the pure part: the app scans and
// enqueues.

// WatchInterval is how often a watched folder is scanned.
const WatchInterval = 5 * time.Second

// MediaExts are the extensions batch dubbing and watch folders take.
var MediaExts = []string{"mp4", "mov", "m4v", "mkv", "webm", "avi", "mp3", "wav", "m4a", "aac", "flac", "ogg", "opus", "aiff", "aif", "caf"}

// VideoExts are the video extensions among MediaExts.
var VideoExts = []string{"mp4", "mov", "m4v", "mkv", "webm", "avi"}

// IsMedia reports whether path has one of exts (case-insensitive).
func IsMedia(path string, exts []string) bool {
	return slices.Contains(exts, strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")))
}

// FileState is a file as a scan saw it.
type FileState struct {
	Size    int64
	ModTime time.Time
}

// Watcher tracks one folder. Its state persists (Seen, Pending), so a
// restart neither re-adds files nor forgets ones being copied.
type Watcher struct {
	Dir  string   `json:"dir"`
	Exts []string `json:"exts,omitempty"` // nil = MediaExts
	// Seen are files handled (enqueued, or there at the start), by name.
	Seen map[string]bool `json:"seen"`
	// Pending are files seen once, by name.
	Pending map[string]FileState `json:"pending,omitempty"`
}

// NewWatcher starts watching dir: files in it now (listing) are seen.
func NewWatcher(dir string, exts []string, listing map[string]FileState) *Watcher {
	w := &Watcher{Dir: dir, Exts: exts, Seen: map[string]bool{}, Pending: map[string]FileState{}}
	for name := range listing {
		w.Seen[name] = true
	}
	return w
}

// Scan takes a listing of the folder and returns the names of files now
// stable and new, in name order; they are seen from now on.
func (w *Watcher) Scan(listing map[string]FileState) []string {
	if w.Seen == nil {
		w.Seen = map[string]bool{}
	}
	if w.Pending == nil {
		w.Pending = map[string]FileState{}
	}
	exts := w.Exts
	if exts == nil {
		exts = MediaExts
	}
	var ready []string
	for name, st := range listing {
		if w.Seen[name] || !IsMedia(name, exts) || strings.HasPrefix(name, ".") {
			continue
		}
		prev, ok := w.Pending[name]
		if ok && prev.Size == st.Size && prev.ModTime.Equal(st.ModTime) && st.Size > 0 {
			ready = append(ready, name)
			w.Seen[name] = true
			delete(w.Pending, name)
			continue
		}
		w.Pending[name] = st
	}
	// Files that went away are forgotten.
	for name := range w.Pending {
		if _, ok := listing[name]; !ok {
			delete(w.Pending, name)
		}
	}
	slices.Sort(ready)
	return ready
}

// List lists dir's regular files (not subfolders, not hidden ones).
func List(dir string) (map[string]FileState, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := map[string]FileState{}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		fi, err := e.Info()
		if err != nil || fi.Mode()&fs.ModeType != 0 {
			continue
		}
		out[e.Name()] = FileState{Size: fi.Size(), ModTime: fi.ModTime()}
	}
	return out, nil
}
