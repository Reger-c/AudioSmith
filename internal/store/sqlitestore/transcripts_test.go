package sqlitestore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
)

func TestTranscripts(t *testing.T) {
	dir := t.TempDir()
	saved := migrations
	t.Cleanup(func() { migrations = saved })
	// A version-2 database migrates to 3.
	migrations = saved[:2]
	s := openTest(t, dir)
	s.Close()
	migrations = saved
	s = openTest(t, dir)
	r := s.Transcripts()

	at := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	prob := 0.9
	a := core.Transcript{ID: "t1", CreatedAt: at, UpdatedAt: at, Title: "interview.m4a", Source: core.SourceFile, Mode: "accurate",
		Model: "m", Language: "en", Duration: 12, Audio: "t1.wav", Raw: "Hello there. General Kenobi.",
		Segments: []core.TranscriptSegment{
			{Start: 0, End: 1.2, Text: "Hello there.", Words: []core.TranscriptWord{{Text: "Hello", Start: 0, End: 0.5, Prob: &prob}, {Text: "there.", Start: 0.6, End: 1.2}}},
			{Start: 1.5, End: 3, Text: "General Kenobi."},
		}}
	b := core.Transcript{ID: "t2", CreatedAt: at.Add(time.Minute), UpdatedAt: at, Source: core.SourceDictation, Mode: "fast", Model: "m",
		Language: "zh", Segments: []core.TranscriptSegment{{Start: 0, End: 2, Text: "你好世界"}}, Clean: "你好，世界。", App: "Notes"}
	os.MkdirAll(r.AudioDir(), 0o755)
	os.WriteFile(r.AudioPath(a), []byte("RIFF"), 0o644)
	for _, x := range []core.Transcript{a, b} {
		if err := r.Put(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	got, err := r.Get(ctx, "t1")
	if err != nil || len(got.Segments) != 2 || len(got.Segments[0].Words) != 2 || *got.Segments[0].Words[0].Prob != 0.9 || !got.CreatedAt.Equal(at) {
		t.Fatalf("get %+v %v", got, err)
	}
	l, err := r.List(ctx, core.TranscriptQuery{})
	if err != nil || len(l) != 2 || l[0].ID != "t2" || l[1].Segments[0].Words != nil || l[1].Text() != "Hello there. General Kenobi." {
		t.Fatalf("list %+v %v", l, err)
	}
	for q, want := range map[string]int{"kenobi": 1, "世界": 1, "，世界": 1, "intervi": 1, "zh": 1, "en": 1, "e": 1, "nothing": 0} {
		if l, err := r.List(ctx, core.TranscriptQuery{Search: q}); err != nil || len(l) != want {
			t.Errorf("search %q: %d %v, want %d", q, len(l), err, want)
		}
	}
	if l, _ := r.List(ctx, core.TranscriptQuery{Source: core.SourceDictation}); len(l) != 1 || l[0].App != "Notes" {
		t.Errorf("by source %+v", l)
	}
	if l, _ := r.List(ctx, core.TranscriptQuery{Limit: 1, Offset: 1}); len(l) != 1 || l[0].ID != "t1" {
		t.Errorf("page %+v", l)
	}
	// Edits re-index.
	a.Segments[1].Text, a.Segments[1].Edited = "General Grievous.", true
	r.Put(ctx, a)
	if l, _ := r.List(ctx, core.TranscriptQuery{Search: "grievous"}); len(l) != 1 {
		t.Error("edit not indexed")
	}
	// Delete takes the audio.
	if err := r.Delete(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.AudioDir(), "t1.wav")); !os.IsNotExist(err) {
		t.Error("audio kept")
	}
	if _, err := r.Get(ctx, "t1"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("deleted: %v", err)
	}
	if err := r.Delete(ctx, "t1"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("delete twice: %v", err)
	}
	if err := r.Put(ctx, core.Transcript{ID: "x", Audio: "../x.wav"}); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("escaping audio: %v", err)
	}
}
