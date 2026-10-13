package sqlitestore

import (
	"errors"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/store"
)

func TestRendersAndExports(t *testing.T) {
	s := openTest(t, t.TempDir())
	now := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	p := core.Project{ID: "p1", Kind: core.ProjectAudiobook, Title: "The Lighthouse", CreatedAt: now, UpdatedAt: now}
	if err := s.Projects().Put(ctx, p); err != nil {
		t.Fatal(err)
	}
	r := core.Render{ID: "r1", ProjectID: "p1", Kind: core.ProjectAudiobook, Title: "The Lighthouse", State: core.RenderRunning,
		CreatedAt: now, UpdatedAt: now, Body: []byte(`{"chapters":2}`)}
	if err := s.Renders().Put(ctx, r); err != nil {
		t.Fatal(err)
	}
	a := core.Render{ID: "r2", ProjectID: "nope", Kind: core.ProjectStory, Title: "Line", State: core.RenderDone, Audition: true,
		CreatedAt: now.Add(time.Minute), UpdatedAt: now}
	if err := s.Renders().Put(ctx, a); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Renders().Get(ctx, "r2"); err != nil || got.ProjectID != "" || !got.Audition {
		t.Fatalf("an unknown project is none: %+v %v", got, err)
	}
	l, err := s.Renders().List(ctx, store.RenderQuery{})
	if err != nil || len(l) != 1 || l[0].ID != "r1" || string(l[0].Body) != `{"chapters":2}` {
		t.Fatalf("auditions are not listed: %+v %v", l, err)
	}
	l, _ = s.Renders().List(ctx, store.RenderQuery{Auditions: true, States: []core.RenderState{core.RenderDone}})
	if len(l) != 1 || l[0].ID != "r2" {
		t.Fatalf("by state: %+v", l)
	}
	// Deleting the project keeps its renders.
	if err := s.Projects().Delete(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Renders().Get(ctx, "r1"); err != nil || got.ProjectID != "" {
		t.Fatalf("after the project went: %+v %v", got, err)
	}
	if err := s.Renders().Delete(ctx, "r1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Renders().Delete(ctx, "r1"); !errors.Is(err, core.ErrNotFound) {
		t.Fatal(err)
	}

	e := core.Export{ID: "e1", CreatedAt: now, Kind: "book", SourceID: "r1", Title: "The Lighthouse", Path: "/Users/me/Books/lighthouse.m4b", Format: "m4b", Bytes: 42}
	if err := s.Exports().Put(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := s.Exports().Put(ctx, core.Export{ID: "e2", Path: ""}); !errors.Is(err, core.ErrInvalid) {
		t.Fatal(err)
	}
	for q, want := range map[string]int{"": 1, "Books": 1, "m4": 1, "light": 1, "zzz": 0} {
		got, err := s.Exports().List(ctx, q, 0)
		if err != nil || len(got) != want {
			t.Errorf("exports %q: %d %v", q, len(got), err)
		}
	}
	if got, _ := s.Exports().Get(ctx, "e1"); !got.CreatedAt.Equal(e.CreatedAt) || got.Path != e.Path || got.Bytes != 42 || got.SourceID != "r1" {
		t.Errorf("round trip: %+v", got)
	}
}

func TestSegmentsCountAsVoiceUsage(t *testing.T) {
	s := openTest(t, t.TempDir())
	now := time.Now()
	s.Voices().Put(ctx, core.Voice{ID: "v1", Name: "Mara", Kind: core.VoiceCloned, CreatedAt: now, UpdatedAt: now})
	s.Projects().Put(ctx, core.Project{ID: "p1", Kind: core.ProjectStory, Title: "Story", CreatedAt: now, UpdatedAt: now})
	segs := []store.Segment{{ID: "a", Ord: 1, Text: "Hello", Speaker: "Mara", VoiceID: "v1"}, {Ord: 2, Text: "Hi", VoiceID: "gone"}, {Ord: 3, Text: "Yes", VoiceID: "v1"}}
	if err := s.Projects().PutSummary(ctx, "p1", []byte(`{"lines":3}`), segs); err != nil {
		t.Fatal(err)
	}
	u, err := s.Voices().Usage(ctx, "v1")
	if err != nil || u.Segments != 2 || len(u.Projects) != 1 || u.Projects[0].Title != "Story" {
		t.Fatalf("%+v %v", u, err)
	}
	// Replaced, not added.
	s.Projects().PutSummary(ctx, "p1", nil, segs[:1])
	if u, _ := s.Voices().Usage(ctx, "v1"); u.Segments != 1 {
		t.Fatalf("%+v", u)
	}
	if err := s.Projects().PutSummary(ctx, "nope", nil, nil); !errors.Is(err, core.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestLibrarySearchAndCounts(t *testing.T) {
	s := openTest(t, t.TempDir())
	at := func(m int) time.Time { return time.Date(2026, 10, 13, 9, m, 0, 0, time.UTC) }
	s.Projects().Put(ctx, core.Project{ID: "p1", Kind: core.ProjectStory, Title: "Mara and the lighthouse", CreatedAt: at(1), UpdatedAt: at(1)})
	s.Projects().Put(ctx, core.Project{ID: "p2", Kind: core.ProjectAudiobook, Title: "Night train", CreatedAt: at(2), UpdatedAt: at(2)})
	s.Projects().PutSummary(ctx, "p2", []byte(`{"chapters":12}`), nil)
	s.Voices().Put(ctx, core.Voice{ID: "v1", Name: "Mara", Kind: core.VoiceCloned, CreatedAt: at(3), UpdatedAt: at(3)})
	tk := take("t1", at(4), "The lighthouse keeper spoke.")
	s.Takes().Put(ctx, tk)
	s.Renders().Put(ctx, core.Render{ID: "r1", ProjectID: "p2", Kind: core.ProjectAudiobook, Title: "Night train", State: core.RenderDone, CreatedAt: at(5), UpdatedAt: at(5)})
	s.Renders().Put(ctx, core.Render{ID: "r2", ProjectID: "p2", Kind: core.ProjectAudiobook, Title: "Night train", State: core.RenderFailed, CreatedAt: at(6), UpdatedAt: at(6)})
	s.Exports().Put(ctx, core.Export{ID: "e1", CreatedAt: at(7), Kind: "take", SourceID: "t1", Title: "Lighthouse take", Path: "/tmp/lighthouse.wav"})

	lib := s.Library()
	all, err := lib.Search(ctx, store.LibraryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, it := range all {
		order = append(order, it.Source+":"+it.ID+":"+it.Category)
	}
	want := []string{"export:e1:export", "render:r1:audiobook", "take:t1:take", "voice:v1:voice", "project:p2:audiobook", "project:p1:story"}
	if len(order) != len(want) {
		t.Fatalf("%v", order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("%v, want %v", order, want)
		}
	}
	if all[4].Detail != `{"chapters":12}` {
		t.Errorf("summary: %q", all[4].Detail)
	}
	got, _ := lib.Search(ctx, store.LibraryQuery{Search: "lighthouse"})
	if len(got) != 3 { // the story, the take, the export
		t.Fatalf("search: %+v", got)
	}
	got, _ = lib.Search(ctx, store.LibraryQuery{Category: store.LibAudiobook})
	if len(got) != 2 {
		t.Fatalf("audiobooks: %+v", got)
	}
	got, _ = lib.Search(ctx, store.LibraryQuery{Search: "Ma", Category: store.LibVoice})
	if len(got) != 1 || got[0].ID != "v1" {
		t.Fatalf("short search: %+v", got)
	}
	got, _ = lib.Search(ctx, store.LibraryQuery{Limit: 2, Offset: 1})
	if len(got) != 2 || got[0].ID != "r1" {
		t.Fatalf("page: %+v", got)
	}
	n, err := lib.Counts(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]int{"": 6, "story": 1, "audiobook": 2, "voice": 1, "take": 1, "export": 1, "dub": 0} {
		if n[k] != v {
			t.Errorf("count %q = %d, want %d (%v)", k, n[k], v, n)
		}
	}
	if n, _ := lib.Counts(ctx, "lighthouse"); n[""] != 3 || n["story"] != 1 {
		t.Errorf("counts of a search: %v", n)
	}
}
