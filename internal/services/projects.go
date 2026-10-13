package services

// The project library (M3): longform projects saved with their summary and
// segments, rename, the export records, and the search over every kind
// of record the Projects page lists.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/store"
)

// Library is the project library beyond core.ProjectService.
type Library struct {
	projects *Projects
	store    store.Store
	bus      *core.Bus
	now      func() time.Time
}

func newLibrary(cfg Config, p *Projects) *Library {
	return &Library{projects: p, store: cfg.Store, bus: cfg.Bus, now: cfg.Now}
}

// ProjectSummary is what the library shows of a project without reading
// its body (store.Projects.PutSummary).
type ProjectSummary struct {
	Model    string  `json:"model,omitempty"`
	Chapters int     `json:"chapters,omitempty"`
	Lines    int     `json:"lines,omitempty"`
	Words    int     `json:"words,omitempty"`
	Chars    int     `json:"chars,omitempty"`
	Seconds  float64 `json:"seconds,omitempty"` // estimated runtime
	Cast     int     `json:"cast,omitempty"`
	Render   string  `json:"render,omitempty"` // the last render
}

// SummaryOf decodes a library item's summary (store.LibraryItem.Detail of
// a project).
func SummaryOf(detail string) ProjectSummary {
	var s ProjectSummary
	json.Unmarshal([]byte(detail), &s)
	return s
}

// SaveDoc saves a Stories or Audiobook project: p's ID "" makes a new one
// (Save as). The body is doc; the summary and the segments (a story's
// lines, an audiobook's voices) follow, so the library and "where this
// voice has been used" see them.
func (l *Library) SaveDoc(ctx context.Context, p core.Project, doc LongformDoc) (core.Project, error) {
	doc.Version = LongformVersion
	if doc.Kind == "" {
		doc.Kind = p.Kind
	}
	p.Kind = doc.Kind
	if strings.TrimSpace(p.Title) == "" {
		p.Title = doc.Title
	}
	body, err := json.Marshal(doc)
	if err != nil {
		return p, err
	}
	p.Body = body
	saved, err := l.projects.Save(ctx, p)
	if err != nil {
		return saved, err
	}
	sum, segs := summarizeDoc(&doc)
	data, _ := json.Marshal(sum)
	if err := l.store.Projects().PutSummary(ctx, saved.ID, data, segs); err != nil {
		return saved, err
	}
	return saved, nil
}

// summarizeDoc is a document's summary and segments.
func summarizeDoc(d *LongformDoc) (ProjectSummary, []store.Segment) {
	sum := ProjectSummary{Model: d.Model, Cast: len(d.Cast), Render: d.Render}
	var segs []store.Segment
	voiceID := func(ref string) string {
		if ref == "" || strings.HasPrefix(ref, SpeakerRef) {
			return ""
		}
		return ref
	}
	script := d.Script
	if d.Kind == core.ProjectStory {
		script = StoryScript(d.Lines)
		cast := map[string]CastMember{}
		for _, m := range d.Cast {
			cast[m.ID] = m
		}
		for i, ln := range d.Lines {
			if ln.Chapter {
				continue
			}
			sum.Lines++
			ref, who := d.Voice, ""
			if m, ok := cast[ln.Character]; ok {
				who = m.Name
				if m.Voice != "" {
					ref = m.Voice
				}
			}
			if ln.Voice != "" {
				ref = ln.Voice
			}
			segs = append(segs, store.Segment{ID: ln.ID, Ord: float64(i), Text: ln.Text, Speaker: who, VoiceID: voiceID(ref)})
		}
	} else {
		sum.Cast = len(VoiceNames(d.Script))
		segs = append(segs, store.Segment{ID: "narrator", Ord: 0, Text: d.Title, VoiceID: voiceID(d.Voice)})
		for i, n := range VoiceNames(d.Script) {
			segs = append(segs, store.Segment{ID: "cast" + fmt.Sprint(i), Ord: float64(i + 1), Text: n, Speaker: n, VoiceID: voiceID(d.VoiceCast[n])})
		}
	}
	st := StatsOf(script, d.Speed)
	sum.Chapters, sum.Words, sum.Chars, sum.Seconds = st.Chapters, st.Words, st.Chars, st.Seconds
	return sum, segs
}

// LoadDoc reads a Stories or Audiobook project's document.
func (l *Library) LoadDoc(ctx context.Context, id string) (core.Project, LongformDoc, error) {
	p, err := l.projects.Get(ctx, id)
	if err != nil {
		return p, LongformDoc{}, err
	}
	var d LongformDoc
	if err := json.Unmarshal(p.Body, &d); err != nil {
		return p, d, fmt.Errorf("project %s: %w: %w", id, core.ErrInvalid, err)
	}
	if d.Kind == "" {
		d.Kind = p.Kind
	}
	if d.Title == "" {
		d.Title = p.Title
	}
	return p, d, nil
}

// Rename changes a project's title (and its document's).
func (l *Library) Rename(ctx context.Context, id, title string) (core.Project, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return core.Project{}, fmt.Errorf("rename: no title: %w", core.ErrInvalid)
	}
	p, err := l.projects.Get(ctx, id)
	if err != nil {
		return p, err
	}
	p.Title = title
	if p.Kind == core.ProjectStory || p.Kind == core.ProjectAudiobook {
		var d LongformDoc
		if json.Unmarshal(p.Body, &d) == nil {
			d.Title = title
			return l.SaveDoc(ctx, p, d)
		}
	}
	return l.projects.Save(ctx, p)
}

// Search lists the library (projects, finished renders, voices, takes,
// exports), newest first.
func (l *Library) Search(ctx context.Context, q store.LibraryQuery) ([]store.LibraryItem, error) {
	return l.store.Library().Search(ctx, q)
}

// Counts counts the library's categories for a search.
func (l *Library) Counts(ctx context.Context, search string) (map[string]int, error) {
	return l.store.Library().Counts(ctx, search)
}

// RecordExport records that a file was exported; ID, time and size are
// filled in.
func (l *Library) RecordExport(ctx context.Context, e core.Export) (core.Export, error) {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = l.now()
	}
	if e.ID == "" {
		e.ID = core.NewID(e.CreatedAt)
	}
	if e.Bytes == 0 {
		if fi, err := os.Stat(e.Path); err == nil {
			e.Bytes = fi.Size()
		}
	}
	if e.Title == "" {
		e.Title = baseName(e.Path)
	}
	if err := l.store.Exports().Put(ctx, e); err != nil {
		return e, err
	}
	l.bus.Publish(core.ExportChanged{Export: e})
	return e, nil
}

// Export returns one export record.
func (l *Library) Export(ctx context.Context, id string) (core.Export, error) {
	return l.store.Exports().Get(ctx, id)
}

// DeleteExport deletes an export record; the file stays.
func (l *Library) DeleteExport(ctx context.Context, id string) error {
	e, err := l.store.Exports().Get(ctx, id)
	if err != nil {
		return err
	}
	if err := l.store.Exports().Delete(ctx, id); err != nil {
		return err
	}
	l.bus.Publish(core.ExportChanged{Export: e, Deleted: true})
	return nil
}

func baseName(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[i+1:]
	}
	return path
}
