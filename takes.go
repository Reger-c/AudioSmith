package main

// The take library as the UI sees it: a snapshot of the library's takes,
// kept current by the services' bus events (never re-read on the main
// thread), their waveforms (peaks computed in the background and cached
// on disk) and the streaming player.

import (
	"cmp"
	"context"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/audio"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/store"
	"github.com/hehehai/AudioSmith/internal/store/sqlitestore"
)

// peakCount is how many peaks a waveform draws.
const peakCount = 160

// library is the domain behind the take pages: the SQLite store, the bus
// and the services over them. main opens it once (openTakeStore); other
// areas reach the store's documents and licences, and the bus, through it.
type library struct {
	store *sqlitestore.Store
	bus   *core.Bus
	svc   *services.Services
	peaks audio.PeakCache
	// peakSlots bounds how many waveforms decode at once.
	peakSlots chan struct{}
}

// openLibrary opens the store in dataDir (migrating it, and importing the
// MVP's history.json the first time) and the services over it. Peaks are
// cached in cacheDir/peaks.
func openLibrary(dataDir, cacheDir string) (*library, error) {
	ctx := context.Background()
	st, err := sqlitestore.Open(ctx, dataDir)
	if err != nil {
		return nil, err
	}
	if n, err := st.ImportHistoryJSON(ctx); err != nil {
		log.Printf("history.json: %v", err) // tried again at the next start
	} else if n > 0 {
		log.Printf("history.json: imported %d takes", n)
	}
	bus := core.NewBus()
	return &library{
		store:     st,
		bus:       bus,
		svc:       services.New(services.Config{Store: st, Bus: bus}),
		peaks:     audio.PeakCache{Dir: filepath.Join(cacheDir, "peaks"), N: peakCount},
		peakSlots: make(chan struct{}, 2),
	}, nil
}

// close writes pending drafts, waits for takes being saved and closes the store.
func (l *library) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := l.svc.Close(ctx); err != nil {
		log.Printf("library: %v", err)
	}
	if err := l.store.Close(); err != nil {
		log.Printf("library: %v", err)
	}
}

// openTakeStore opens the library in the app's data directory and closes
// it when the app quits. Without its database the app cannot keep takes,
// so failing to open it (a database from a newer AudioSmith, a missing
// SQLite library) ends the app with the reason.
func openTakeStore(dataDir string) *library {
	cacheDir, err := mygo.App.Path(mygo.PathCache)
	if err != nil {
		cacheDir = filepath.Join(dataDir, "cache")
	}
	lib, err := openLibrary(dataDir, cacheDir)
	if err != nil {
		log.Fatalf("opening the library in %s: %v", dataDir, err)
	}
	mygo.App.OnQuit(lib.close)
	return lib
}

// takesState is the take library's view state.
type takesState struct {
	lib *library
	// takeStore is the store's take repository, for its pure AudioDir and
	// AudioPath (the Engine page shows the directory).
	takeStore    store.Takes
	takes        []core.Take          // snapshot, newest first
	peaks        map[string][]float32 // waveform by take id; empty when there is none
	peaksPending map[string]bool      // waveforms being computed
	pl           audio.Player
	lastClipID   string // the Studio's "Latest take"
	historyList  ui.ListState
}

// openTakes connects the take pages to the library: it subscribes to take
// events (once, here) and loads the first snapshot and the Studio's draft.
func (a *app) openTakes(lib *library) {
	a.lib = lib
	a.takeStore = lib.store.Takes()
	a.peaks = map[string][]float32{}
	a.peaksPending = map[string]bool{}
	lib.bus.Subscribe(func(ev core.Event) {
		// On the publisher's goroutine: hand the event to the main thread.
		switch ev := ev.(type) {
		case core.TakeCreated:
			a.post(func() { a.putTake(ev.Take) })
		case core.TakeUpdated:
			a.post(func() { a.putTake(ev.Take) })
		case core.TakeDeleted:
			a.post(func() { a.takeDeleted(ev.ID) })
		}
	}, "take.")
	a.loadTakes()
	a.restoreStudioDraft()
}

// background runs fn on a goroutine; in tests (no window) it runs fn at
// once, so views see its results without waiting.
func (a *app) background(fn func()) {
	if a.win == nil {
		fn()
		return
	}
	go fn()
}

// loadTakes reads the snapshot in the background.
func (a *app) loadTakes() {
	svc := a.lib.svc.Takes
	a.background(func() {
		takes, err := svc.List(context.Background(), core.TakeQuery{})
		a.post(func() {
			if err != nil {
				a.toast(i18n.T("Could not read the history: %v", err))
				return
			}
			// Keep takes that events added while the list was read.
			for _, t := range a.takes {
				if !slices.ContainsFunc(takes, func(x core.Take) bool { return x.ID == t.ID }) {
					takes = append(takes, t)
				}
			}
			sortTakes(takes)
			a.takes = takes
			if a.lastClipID == "" && len(takes) > 0 {
				a.lastClipID = takes[0].ID
			}
		})
	})
}

func sortTakes(takes []core.Take) {
	slices.SortStableFunc(takes, func(x, y core.Take) int {
		if c := y.CreatedAt.Compare(x.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(y.ID, x.ID)
	})
}

// putTake adds or replaces a take in the snapshot.
func (a *app) putTake(t core.Take) {
	if i := slices.IndexFunc(a.takes, func(x core.Take) bool { return x.ID == t.ID }); i >= 0 {
		a.takes[i] = t
		return
	}
	a.takes = append(a.takes, t)
	sortTakes(a.takes)
	if a.lastClipID == "" {
		a.lastClipID = t.ID
	}
}

// takeDeleted drops a take from the snapshot and its cached waveform.
func (a *app) takeDeleted(id string) {
	a.takes = slices.DeleteFunc(a.takes, func(t core.Take) bool { return t.ID == id })
	if a.pl.Current() == id {
		a.pl.Stop()
	}
	delete(a.peaks, id)
	if a.lastClipID == id {
		a.lastClipID = ""
	}
	peaks := a.lib.peaks
	a.background(func() { peaks.Remove(id) })
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

// takeFinished is called when the engine has written a take: it shows it
// as the latest at once, plays it when autoplay is on, and saves it in
// the background (TakeCreated then confirms it).
func (a *app) takeFinished(t *core.Take) {
	take := *t
	a.putTake(take)
	a.lastClipID = take.ID
	if a.autoplay {
		if err := a.pl.Toggle(take.ID, a.takeStore.AudioPath(take)); err != nil {
			a.toast(i18n.T("Could not play: %v", err))
		}
	}
	svc := a.lib.svc.Takes
	a.background(func() {
		if _, err := svc.Add(context.Background(), take); err != nil {
			a.post(func() { a.toast(i18n.T("Could not save the history: %v", err)) })
		}
	})
}

func (a *app) play(t *core.Take) {
	if err := a.pl.Toggle(t.ID, a.takeStore.AudioPath(*t)); err != nil {
		a.toast(i18n.T("Could not play: %v", err))
	}
}

// deleteClip deletes a take in the background; TakeDeleted updates the views.
func (a *app) deleteClip(t *core.Take) {
	id := t.ID
	if a.pl.Current() == id {
		a.pl.Stop()
	}
	svc := a.lib.svc.Takes
	a.background(func() {
		if err := svc.Delete(context.Background(), id); err != nil {
			a.post(func() { a.toast(i18n.T("Could not delete: %v", err)) })
		}
	})
}

// requestPeaks computes a take's waveform in the background, once at a
// time per take; the view draws a placeholder until it arrives.
func (a *app) requestPeaks(t *core.Take) {
	id := t.ID
	if a.peaksPending[id] {
		return
	}
	a.peaksPending[id] = true
	lib, path := a.lib, a.takeStore.AudioPath(*t)
	a.background(func() {
		lib.peakSlots <- struct{}{}
		p, err := lib.peaks.Get(id, path)
		<-lib.peakSlots
		if err != nil {
			p = []float32{} // drawn as a flat line
		}
		a.post(func() {
			delete(a.peaksPending, id)
			if a.findTake(id) != nil {
				a.peaks[id] = p
			}
		})
	})
}

// export asks where to save a copy of the take; the dialog blocks, so it
// runs on a goroutine of its own, as does the copy.
func (a *app) export(t *core.Take) {
	id, name := t.ID, exportName(t)
	svc := a.lib.svc.Takes
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
		err = svc.Export(context.Background(), id, dst)
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
