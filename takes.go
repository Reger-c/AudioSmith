package main

// The take library as the UI sees it: a snapshot of the store's takes,
// their waveforms and the player. Thread A moves the store calls behind
// core.TakeService (off the UI thread, results via the bus), caches peaks
// in the background and streams playback.

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/audio"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/store"
	"github.com/hehehai/AudioSmith/internal/store/historyjson"
)

// takesState is the take library's view state.
type takesState struct {
	takeStore   store.Takes
	takes       []core.Take          // snapshot of takeStore, newest first
	peaks       map[string][]float32 // waveform by take id, loaded on demand
	pl          audio.Player
	lastClipID  string // the Studio's "Latest take"
	historyList ui.ListState
}

// openTakeStore opens the take library in the app's data directory.
func openTakeStore(dataDir string) store.Takes {
	lib, err := historyjson.Open(dataDir)
	if err != nil {
		log.Printf("history: %v", err)
	}
	return lib
}

func (a *app) openTakes(s store.Takes) {
	a.takeStore = s
	a.peaks = map[string][]float32{}
	a.reloadTakes()
	if len(a.takes) > 0 {
		a.lastClipID = a.takes[0].ID
	}
}

// reloadTakes refreshes the snapshot after a change.
func (a *app) reloadTakes() {
	takes, err := a.takeStore.List(context.Background(), core.TakeQuery{})
	if err != nil {
		a.toast(i18n.T("Could not read the history: %v", err))
		return
	}
	a.takes = takes
}

func (a *app) findTake(id string) *core.Take {
	for i := range a.takes {
		if a.takes[i].ID == id {
			return &a.takes[i]
		}
	}
	return nil
}

func (a *app) lastClip() *core.Take {
	if a.lastClipID == "" {
		return nil
	}
	return a.findTake(a.lastClipID)
}

// takeFinished saves a take the engine has written, shows it as the latest
// and plays it when autoplay is on.
func (a *app) takeFinished(t *core.Take) {
	if err := a.takeStore.Put(context.Background(), *t); err != nil {
		a.toast(i18n.T("Could not save the history: %v", err))
	}
	a.reloadTakes()
	a.lastClipID = t.ID
	if a.autoplay {
		if err := a.pl.Toggle(t.ID, a.takeStore.AudioPath(*t)); err != nil {
			a.toast(i18n.T("Could not play: %v", err))
		}
	}
}

func (a *app) play(t *core.Take) {
	if err := a.pl.Toggle(t.ID, a.takeStore.AudioPath(*t)); err != nil {
		a.toast(i18n.T("Could not play: %v", err))
	}
}

func (a *app) deleteClip(t *core.Take) {
	id := t.ID
	if a.pl.Current() == id {
		a.pl.Stop()
	}
	if err := a.takeStore.Delete(context.Background(), id); err != nil {
		a.toast(i18n.T("Could not delete: %v", err))
	}
	a.reloadTakes()
	delete(a.peaks, id)
	if a.lastClipID == id {
		a.lastClipID = ""
	}
}

// export asks where to save a copy of the take; the dialog blocks, so it
// runs on a goroutine of its own.
func (a *app) export(t *core.Take) {
	src := a.takeStore.AudioPath(*t)
	name := exportName(t)
	go func() {
		dst, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
			Parent:      a.win,
			Title:       i18n.T("Export audio"),
			DefaultPath: name,
			Filters:     []mygo.FileFilter{{Name: i18n.T("WAV audio"), Extensions: []string{"wav"}}},
		})
		if err != nil || dst == "" {
			return
		}
		err = copyFile(src, dst)
		a.post(func() {
			if err != nil {
				a.toast(i18n.T("Export failed: %v", err))
			} else {
				a.toast(i18n.T("Exported %s", filepath.Base(dst)))
			}
		})
	}()
}

func exportName(t *core.Take) string {
	words := strings.Fields(t.Text)
	if len(words) > 6 {
		words = words[:6]
	}
	base := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) {
			return '-'
		}
		return r
	}, strings.Join(words, " "))
	if base == "" {
		base = "AudioSmith"
	}
	if len([]rune(base)) > 40 {
		base = string([]rune(base)[:40])
	}
	return base + ".wav"
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}
