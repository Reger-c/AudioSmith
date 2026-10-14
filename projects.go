package main

// The project library (/projects, M3): one list of everything the app has
// made — Stories and Audiobook projects and their finished renders, voices,
// transcripts, takes and export records — with a filter per kind (with
// counts), full-text search (the store's FTS indexes), list or cards,
// inline rename, open (asking before replacing an unsaved draft), Show in
// Finder, delete (several at once, with a retry for those that failed)
// and "How it was made" for renders.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/store"
)

// libCategories are the filters, in order.
var libCategories = []string{store.LibAll, store.LibDub, store.LibStory, store.LibAudiobook, store.LibVoice, store.LibTranscript, store.LibTake, store.LibExport}

// libPageSize is how many items the page reads at a time.
const libPageSize = 200

// projectsState is the Projects page's state.
type projectsState struct {
	query    string
	category int // into libCategories
	cards    int // 0 list, 1 cards
	items    []store.LibraryItem
	counts   map[string]int
	limit    int
	loaded   string // query|category|limit the items are of
	loading  bool
	dirty    bool // something changed: read again when shown
	list     ui.ListState
	grid     ui.GridState

	selected   map[string]bool // item keys
	errors     map[string]string
	renaming   string // item key
	renameText string
	deleteOpen bool
	details    map[string]bool
	asked      map[string]bool // renders read for their details
}

// libKey is an item's key: source and id.
func libKey(it store.LibraryItem) string { return it.Source + ":" + it.ID }

// openProjects subscribes the library to the changes it lists.
func (a *app) openProjects(lib *library) {
	a.projects.selected, a.projects.errors, a.projects.details = map[string]bool{}, map[string]string{}, map[string]bool{}
	a.projects.asked = map[string]bool{}
	a.projects.limit = libPageSize
	a.projects.dirty = true
	mark := func() { a.post(func() { a.projects.dirty = true }) }
	core.On(lib.bus, func(core.TakeCreated) { mark() })
	core.On(lib.bus, func(core.TakeDeleted) { mark() })
	core.On(lib.bus, func(core.VoiceChanged) { mark() })
	core.On(lib.bus, func(core.ExportChanged) { mark() })
}

// refreshLibrary reads the items and counts when the query changed or
// something in the library did.
func (a *app) refreshLibrary() {
	s := &a.projects
	cat := libCategories[min(max(s.category, 0), len(libCategories)-1)]
	key := strings.TrimSpace(s.query) + "|" + cat + "|" + fmt.Sprint(s.limit)
	if (key == s.loaded && !s.dirty) || s.loading {
		return
	}
	s.loading, s.dirty = true, false
	lib := a.lib.svc.Library
	q := store.LibraryQuery{Category: cat, Search: strings.TrimSpace(s.query), Limit: s.limit}
	a.background(func() {
		ctx := context.Background()
		items, err := lib.Search(ctx, q)
		var counts map[string]int
		if err == nil {
			counts, err = lib.Counts(ctx, q.Search)
		}
		a.post(func() {
			s.loading = false
			if err != nil {
				a.toast(i18n.T("Could not read the library: %v", err))
				return
			}
			s.items, s.counts, s.loaded = items, counts, key
			// Keep only selections still shown.
			for k := range s.selected {
				if !s.has(k) {
					delete(s.selected, k)
				}
			}
		})
	})
}

func (s *projectsState) has(key string) bool {
	for _, it := range s.items {
		if libKey(it) == key {
			return true
		}
	}
	return false
}

// categoryLabel names a filter.
func categoryLabel(cat string) string {
	switch cat {
	case store.LibDub:
		return i18n.T("Dubbing projects")
	case store.LibStory:
		return i18n.T("Stories")
	case store.LibAudiobook:
		return i18n.T("Audiobooks")
	case store.LibVoice:
		return i18n.T("Voices")
	case store.LibTranscript:
		return i18n.T("Transcripts")
	case store.LibTake:
		return i18n.T("History")
	case store.LibExport:
		return i18n.T("Exports")
	}
	return i18n.T("All")
}

// itemKind names what an item is.
func itemKind(it store.LibraryItem) string {
	switch it.Source {
	case "render":
		if it.Category == store.LibAudiobook {
			return i18n.T("Audiobook render")
		}
		return i18n.T("Story render")
	case "project":
		switch core.ProjectKind(it.Category) {
		case core.ProjectStory:
			return i18n.T("Story")
		case core.ProjectAudiobook:
			return i18n.T("Audiobook")
		case core.ProjectDub:
			return i18n.T("Dubbing project")
		case core.ProjectTranscript:
			return i18n.T("Transcript")
		}
		return i18n.T("Project")
	case "voice":
		return i18n.T("Voice")
	case "take":
		return i18n.T("Take")
	case "export":
		return i18n.T("Export")
	}
	return it.Source
}

// itemIcon is an item's icon.
func itemIcon(it store.LibraryItem) *ui.SVG {
	switch it.Source {
	case "voice":
		return voicesIcon
	case "take":
		return historyIcon
	case "export":
		return exportIcon
	case "render":
		return renderIcon
	}
	if it.Category == store.LibAudiobook {
		return audiobookIcon
	}
	return storiesIcon
}

// itemMeta is an item's second line.
func (a *app) itemMeta(it store.LibraryItem) string {
	parts := []string{itemKind(it)}
	switch it.Source {
	case "project":
		s := services.SummaryOf(it.Detail)
		if s.Chapters > 1 {
			parts = append(parts, i18n.N(s.Chapters, "%d chapter", "%d chapters", s.Chapters))
		}
		if s.Lines > 0 {
			parts = append(parts, i18n.N(s.Lines, "%d line", "%d lines", s.Lines))
		}
		if s.Words > 0 {
			parts = append(parts, i18n.N(s.Words, "%d word", "%d words", s.Words))
		}
		if s.Chars > 0 {
			parts = append(parts, i18n.N(s.Chars, "%d character", "%d characters", s.Chars))
		}
	case "render":
		if r := a.lfRenders[it.ID]; r != nil {
			parts = append(parts, a.renderRecipe(r))
		}
	case "voice":
		parts = append(parts, voiceKindText(core.VoiceKind(it.Detail)))
	case "take":
		parts = append(parts, modelName(it.Detail))
	case "export":
		parts = append(parts, it.Detail)
	}
	parts = append(parts, i18n.DateTime(it.Updated))
	return strings.Join(parts, " · ")
}

// renderRecipe is a render's one-line recipe: voices · speed · engine · length.
func (a *app) renderRecipe(r *lfRender) string {
	s := r.body.Summary
	var parts []string
	if v := a.voiceNames(s.Voices); v != "" {
		parts = append(parts, v)
	}
	parts = append(parts, speedRange(s.MinSpeed, s.MaxSpeed), modelName(s.Model))
	if r.rec.AudioSeconds > 0 {
		parts = append(parts, lfClock(time.Duration(r.rec.AudioSeconds*float64(time.Second))))
	}
	return strings.Join(parts, " · ")
}

// voiceNames names voice references (VoiceIDs or speakers).
func (a *app) voiceNames(refs []string) string {
	var out []string
	for _, ref := range refs {
		out = append(out, a.lfVoiceLabel(ref))
	}
	return strings.Join(out, ", ")
}

// speedRange is "1.00×" or "0.90–1.10×".
func speedRange(lo, hi float64) string {
	if lo <= 0 {
		lo = 1
	}
	if hi <= 0 {
		hi = lo
	}
	if lo == hi {
		return fmt.Sprintf("%.2f×", lo)
	}
	return fmt.Sprintf("%.2f–%.2f×", lo, hi)
}

// renderFor is a render's state for an item: from the renders followed,
// or read in the background.
func (a *app) renderFor(id string) *lfRender {
	if r := a.lfRenders[id]; r != nil {
		return r
	}
	if a.projects.asked[id] {
		return nil
	}
	a.projects.asked[id] = true
	svc := a.lib.svc.Longform
	a.background(func() {
		r, _, err := svc.Get(context.Background(), id)
		if err != nil {
			return
		}
		a.post(func() { a.putRender(r) })
	})
	return nil
}

// openItem opens an item: a project in its page, a voice in its editor,
// a take in the Studio, a render's page.
func (a *app) openItem(it store.LibraryItem) {
	switch it.Source {
	case "project":
		kind := core.ProjectKind(it.Category)
		if kind != core.ProjectStory && kind != core.ProjectAudiobook {
			a.toast(i18n.T("Opening this kind of project comes later."))
			return
		}
		a.askOpenProject(it.ID, kind, it.Title)
	case "voice":
		a.openVoiceEditor(it.ID)
	case "take":
		if t := a.findTake(it.ID); t != nil {
			a.reuse(t)
		}
	case "render":
		if r := a.lfRenders[it.ID]; r != nil {
			p := a.lfPageOf(r.rec.Kind)
			p.render = r.rec.ID
			a.navigate("/" + lfRoute(r.rec.Kind))
		}
	}
}

// recordTakeExport records that a take was saved to dst (from any
// goroutine): the Projects page lists it under Exports.
func (a *app) recordTakeExport(id, dst string) {
	lib := a.lib.svc.Library
	title := id
	a.post(func() {
		if t := a.findTake(id); t != nil {
			title = strings.Join(strings.Fields(t.Text), " ")
			if r := []rune(title); len(r) > 80 {
				title = string(r[:80]) + "…"
			}
		}
		a.background(func() {
			if _, err := lib.RecordExport(context.Background(), core.Export{Kind: "take", SourceID: id, Title: title, Path: dst, Format: "wav"}); err != nil {
				a.post(func() { a.toast(i18n.T("Could not record the export: %v", err)) })
			}
		})
	})
}

// recordVoiceExport records that a voice was exported to dst.
func (a *app) recordVoiceExport(id, dst string) {
	lib := a.lib.svc.Library
	a.post(func() {
		title := id
		if v := a.findVoice(id); v != nil {
			title = v.Name
		}
		a.background(func() {
			lib.RecordExport(context.Background(), core.Export{Kind: "voice", SourceID: id, Title: title, Path: dst, Format: "asvoice"})
		})
	})
}

// renameItem renames a project.
func (a *app) renameItem(it store.LibraryItem, title string) {
	lib := a.lib.svc.Library
	a.background(func() {
		_, err := lib.Rename(context.Background(), it.ID, title)
		a.post(func() {
			if err != nil {
				a.toast(i18n.T("Could not rename: %v", err))
			}
			a.projects.dirty = true
		})
	})
}

// deleteItems deletes items one after the other; those that fail stay
// selected with their error.
func (a *app) deleteItems(items []store.LibraryItem) {
	svc := a.lib.svc
	for _, it := range items {
		if it.Source == "take" && a.pl.Current() == it.ID {
			a.pl.Stop()
		}
	}
	a.background(func() {
		errs := map[string]string{}
		done := 0
		for _, it := range items {
			ctx := context.Background()
			var err error
			switch it.Source {
			case "project":
				err = svc.Projects.Delete(ctx, it.ID)
			case "render":
				err = svc.Longform.Delete(ctx, it.ID)
			case "voice":
				err = svc.Voices.Delete(ctx, it.ID)
			case "take":
				err = svc.Takes.Delete(ctx, it.ID)
			case "export":
				err = svc.Library.DeleteExport(ctx, it.ID)
			}
			if err != nil && !errors.Is(err, core.ErrNotFound) {
				errs[libKey(it)] = err.Error()
				continue
			}
			done++
		}
		a.post(func() {
			s := &a.projects
			s.errors = errs
			for _, it := range items {
				if _, failed := errs[libKey(it)]; !failed {
					delete(s.selected, libKey(it))
				}
			}
			s.dirty = true
			if len(errs) == 0 {
				s.deleteOpen = false
			}
			a.toast(i18n.N(done, "Deleted: %d", "Deleted: %d", done))
		})
	})
}

var (
	storiesIcon   = icon(`<path d="M3 3.5h4.5a1.5 1.5 0 0 1 1.5 1.5v8a1.5 1.5 0 0 0-1.5-1.5H3zM13 3.5H9.5"/><path d="M5 6.5h2M5 8.5h2M11 6.5h1M11 8.5h1"/><path d="M9 5a1.5 1.5 0 0 1 1.5-1.5H13v8h-2.5A1.5 1.5 0 0 0 9 13"/>`)
	audiobookIcon = icon(`<path d="M3 4.5a1 1 0 0 1 1-1h4v10H4a1 1 0 0 1-1-1z"/><path d="M8 3.5h4a1 1 0 0 1 1 1V8"/><path d="M10 13.5v-2a2 2 0 0 1 4 0v2"/><rect x="9.5" y="11.5" width="1.2" height="2.5" rx=".5"/><rect x="13.3" y="11.5" width="1.2" height="2.5" rx=".5"/>`)
	projectsIcon  = icon(`<rect x="2" y="3" width="12" height="10" rx="1.5"/><path d="M2 6h12M5 9h6M5 11h4"/>`)
	exportIcon    = icon(`<path d="M8 2v8M5 5l3-3 3 3"/><path d="M3 9v3.5A1.5 1.5 0 0 0 4.5 14h7a1.5 1.5 0 0 0 1.5-1.5V9"/>`)
	renderIcon    = icon(`<circle cx="8" cy="8" r="6"/><path d="M6.5 5.5v5l4-2.5z"/>`)
)
