package sqlitestore

import (
	"slices"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
)

// Migration 2 keeps the voices of a version-1 database and indexes them
// for the new search.
func TestMigration2Voices(t *testing.T) {
	dir := t.TempDir()
	saved := migrations
	t.Cleanup(func() { migrations = saved })
	migrations = saved[:1]
	s := openTest(t, dir)
	now := time.Now()
	// As M0 wrote a profile: consent was a bool.
	if _, err := s.db.Execute(ctx, `INSERT INTO voices (id, name, kind, model, locked, consent, created_at, updated_at, profile)
VALUES ('v1', 'Old Narrator', 'cloned', '', 0, 1, ?, ?, '{"id":"v1","name":"Old Narrator","kind":"cloned","model":"","consent":true,"createdAt":"2026-10-01T00:00:00Z","updatedAt":"2026-10-01T00:00:00Z"}')`,
		now.UnixMilli(), now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	s.Close()

	migrations = saved
	s = openTest(t, dir)
	l, err := s.Voices().List(ctx, core.VoiceQuery{Search: "narrator"})
	if err != nil || len(l) != 1 || !l[0].Verified() {
		t.Fatalf("after migration: %v %+v", err, l)
	}
	// New rows are found by style and transcript too.
	v := core.Voice{ID: "v2", Name: "Second", Kind: core.VoiceCloned, Style: "whispery", RefText: "Once upon a time",
		Origin: "gallery:f-x", CreatedAt: now, UpdatedAt: now}
	if err := s.Voices().Put(ctx, v); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]int{"whisper": 1, "upon a": 1, "Sec": 1, "o": 2, "zzz": 0} {
		if l, _ := s.Voices().List(ctx, core.VoiceQuery{Search: q}); len(l) != want {
			t.Errorf("search %q: %d, want %d", q, len(l), want)
		}
	}
	v.Style = "booming"
	s.Voices().Put(ctx, v)
	if l, _ := s.Voices().List(ctx, core.VoiceQuery{Search: "whisper"}); len(l) != 0 {
		t.Error("index not updated")
	}
	if l, _ := s.Voices().List(ctx, core.VoiceQuery{Origin: "gallery:f-x"}); len(l) != 1 || l[0].ID != "v2" {
		t.Errorf("origin %+v", l)
	}
}

func TestVoiceUsage(t *testing.T) {
	s := openTest(t, t.TempDir())
	now := time.Now()
	s.Voices().Put(ctx, core.Voice{ID: "v", Name: "V", Kind: core.VoiceCloned, CreatedAt: now, UpdatedAt: now})
	if u, err := s.Voices().Usage(ctx, "v"); err != nil || u.Takes != 0 || u.LatestTake != "" || len(u.Projects) != 0 {
		t.Fatalf("%+v %v", u, err)
	}
	for i, id := range []string{"a", "b"} {
		tk := core.Take{ID: id, CreatedAt: now.Add(time.Duration(i) * time.Second), File: id + ".wav", Text: "x", Model: "m", VoiceID: "v"}
		if err := s.Takes().Put(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}
	s.Projects().Put(ctx, core.Project{ID: "p", Kind: core.ProjectStory, Title: "Night", CreatedAt: now, UpdatedAt: now})
	for i, seg := range []string{"s1", "s2"} {
		if _, err := s.db.Execute(ctx, `INSERT INTO segments (id, project_id, ord, text, voice_id) VALUES (?, 'p', ?, 'x', 'v')`, seg, i); err != nil {
			t.Fatal(err)
		}
	}
	u, err := s.Voices().Usage(ctx, "v")
	if err != nil || u.Takes != 2 || u.LatestTake != "b" || u.Segments != 2 ||
		!slices.Equal(u.Projects, []core.VoiceProjectUsage{{ID: "p", Title: "Night", Kind: core.ProjectStory, Segments: 2}}) {
		t.Fatalf("%+v %v", u, err)
	}
}
