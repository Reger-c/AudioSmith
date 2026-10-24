package services

// Deleting a voice through a trash (#108): Trash moves the voice's files
// to <data>/trash/voices/<id>/ with its profile and the takes and segments
// that used it, so that Restore can undo it for a little while (the
// window's Undo); Purge then removes them for good, and PurgeTrash, at
// start, what an earlier run left. Delete (the Local API, MCP, the
// Projects page) goes through the trash too and empties its entry at once.
//
// Crash safety: the trash entry (voice.json) is written first, the files
// moved second, the profile deleted last. An entry whose voice is still
// in the library (the app stopped between those steps) gets its files
// back at the next start instead of losing them.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/store"
)

// trashEntry is what voice.json in a trash entry holds.
type trashEntry struct {
	Voice     core.Voice       `json:"voice"`
	Links     store.VoiceLinks `json:"links"`
	DeletedAt time.Time        `json:"deletedAt"`
}

// trashDir is <data>/trash/voices, beside the voices' folder.
func (s *Voices) trashDir() string {
	return filepath.Join(filepath.Dir(s.store.VoiceDir()), "trash", "voices")
}

func (s *Voices) trashEntry(id string) string { return filepath.Join(s.trashDir(), id) }

// Delete deletes a voice for good, its files too. Takes made with it
// keep their audio.
func (s *Voices) Delete(ctx context.Context, id string) error {
	return s.remove(ctx, id, false)
}

// Trash deletes a voice whose files wait in the trash until Purge, so
// that Restore can undo it (the window's Undo).
func (s *Voices) Trash(ctx context.Context, id string) error {
	return s.remove(ctx, id, true)
}

func (s *Voices) remove(ctx context.Context, id string, keep bool) error {
	s.mu.Lock()
	v, links, err := s.trash(ctx, id)
	if err == nil && !keep {
		os.RemoveAll(s.trashEntry(id))
	}
	s.mu.Unlock()
	logDelete(ctx, "voice", id, v.Name, err, "kind", string(v.Kind), "takes", len(links.Takes), "segments", len(links.Segments), "undo", keep)
	if err != nil {
		return err
	}
	s.bus.Publish(core.VoiceChanged{Voice: v, Deleted: true})
	return nil
}

// trash moves voice id to the trash and deletes its profile. Caller
// holds mu.
func (s *Voices) trash(ctx context.Context, id string) (core.Voice, store.VoiceLinks, error) {
	if filepath.Base(id) != id || id == "" || id == "." || id == ".." {
		return core.Voice{}, store.VoiceLinks{}, fmt.Errorf("voice %q: %w", id, core.ErrInvalid)
	}
	v, err := s.store.Get(ctx, id)
	if err != nil {
		return v, store.VoiceLinks{}, err
	}
	links, err := s.store.Links(ctx, id)
	if err != nil {
		return v, links, err
	}
	entry := s.trashEntry(id)
	os.RemoveAll(entry) // an older delete of the same id, restored since
	data, _ := json.Marshal(trashEntry{Voice: v, Links: links, DeletedAt: s.now()})
	if err := os.MkdirAll(entry, 0o755); err != nil {
		return v, links, err
	}
	if err := writeFileAtomic(filepath.Join(entry, "voice.json"), data); err != nil {
		os.RemoveAll(entry)
		return v, links, err
	}
	files := filepath.Join(entry, "files")
	moved := false
	if _, err := os.Stat(s.dir(id)); err == nil {
		if err := os.Rename(s.dir(id), files); err != nil {
			os.RemoveAll(entry)
			return v, links, err
		}
		moved = true
	}
	if err := s.store.Delete(ctx, id); err != nil {
		if moved {
			os.Rename(files, s.dir(id))
		}
		os.RemoveAll(entry)
		return v, links, err
	}
	return v, links, nil
}

// ErrNotInTrash: Restore found no deleted voice of that id (purged, or
// never deleted).
var ErrNotInTrash = errors.New("the deleted voice is no longer kept")

// Restore undoes Delete while the voice is in the trash: its profile,
// its files and its links to takes and segments come back.
func (s *Voices) Restore(ctx context.Context, id string) (core.Voice, error) {
	s.mu.Lock()
	v, err := s.restore(ctx, id)
	s.mu.Unlock()
	logOp(ctx, "voice.restore", id, v.Name, err)
	if err != nil {
		return v, err
	}
	s.bus.Publish(core.VoiceChanged{Voice: v})
	return v, nil
}

func (s *Voices) restore(ctx context.Context, id string) (core.Voice, error) {
	if filepath.Base(id) != id || id == "" {
		return core.Voice{}, fmt.Errorf("voice %q: %w", id, core.ErrInvalid)
	}
	entry := s.trashEntry(id)
	data, err := os.ReadFile(filepath.Join(entry, "voice.json"))
	if err != nil {
		return core.Voice{}, fmt.Errorf("voice %s: %w", id, ErrNotInTrash)
	}
	var e trashEntry
	if err := json.Unmarshal(data, &e); err != nil || e.Voice.ID != id {
		return core.Voice{}, fmt.Errorf("voice %s: %w", id, ErrNotInTrash)
	}
	if _, err := s.store.Get(ctx, id); err == nil {
		return e.Voice, fmt.Errorf("voice %s is in the library already: %w", id, core.ErrInvalid)
	}
	files := filepath.Join(entry, "files")
	moved := false
	if _, err := os.Stat(files); err == nil {
		if err := os.MkdirAll(s.store.VoiceDir(), 0o755); err != nil {
			return e.Voice, err
		}
		if err := os.Rename(files, s.dir(id)); err != nil {
			return e.Voice, err
		}
		moved = true
	}
	if err := s.store.Put(ctx, e.Voice); err != nil {
		if moved {
			os.Rename(s.dir(id), files)
		}
		return e.Voice, err
	}
	if err := s.store.Relink(ctx, id, e.Links); err != nil {
		// The voice is back; only some takes lost their link to it.
		logOp(ctx, "voice.relink", id, e.Voice.Name, err)
	}
	os.RemoveAll(entry)
	return e.Voice, nil
}

// Purge removes a deleted voice's files for good: the undo is over.
func (s *Voices) Purge(id string) {
	if filepath.Base(id) != id || id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	os.RemoveAll(s.trashEntry(id))
}

// PurgeTrash empties the trash at start: what an earlier run deleted
// is gone for good, and the files of a delete it did not finish (its
// voice is still in the library) go back in place.
func (s *Voices) PurgeTrash(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ents, err := os.ReadDir(s.trashDir())
	if err != nil {
		return
	}
	purged := 0
	for _, e := range ents {
		id, entry := e.Name(), s.trashEntry(e.Name())
		if _, err := s.store.Get(ctx, id); err == nil {
			files := filepath.Join(entry, "files")
			if _, err := os.Stat(s.dir(id)); os.IsNotExist(err) {
				if _, err := os.Stat(files); err == nil {
					os.Rename(files, s.dir(id))
					logOp(ctx, "voice.trash.recover", id, "", nil)
				}
			}
		} else {
			purged++
		}
		os.RemoveAll(entry)
	}
	if purged > 0 {
		logOp(ctx, "voice.trash.purge", "", "", nil, "voices", purged)
	}
}
