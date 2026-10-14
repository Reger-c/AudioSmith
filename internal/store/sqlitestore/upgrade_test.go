package sqlitestore

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/store"
)

// TestUpgradeFromMigration1 is the v0.2 upgrade path: an MVP data folder
// (history.json + clips) opened by an M0 build (migration 1 only), with a
// voice, a project with segments, a licence, a document and a finished
// job, then opened by this build (every migration). Nothing is lost, the
// file is backed up first, search works on the old rows, and the tables
// of the later migrations take new rows.
func TestUpgradeFromMigration1(t *testing.T) {
	dir := t.TempDir()
	writeMVP(t, dir)
	saved := migrations
	t.Cleanup(func() { migrations = saved })

	// As the M0 build left it.
	migrations = saved[:1]
	s := openTest(t, dir)
	if n, err := s.ImportHistoryJSON(ctx); n != 2 || err != nil {
		t.Fatalf("M0 import: %d %v", n, err)
	}
	now := time.Now()
	ms := now.UnixMilli()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO voices (id, name, kind, model, locked, consent, created_at, updated_at, profile)
VALUES ('v1', 'Old Narrator', 'cloned', '', 0, 1, ?, ?, '{"id":"v1","name":"Old Narrator","kind":"cloned","model":"","consent":true,"refText":"Once upon a time","createdAt":"2026-10-01T00:00:00Z","updatedAt":"2026-10-01T00:00:00Z"}')`, []any{ms, ms}},
		{`INSERT INTO projects (id, kind, title, created_at, updated_at, body) VALUES ('p1', 'story', 'Old Story', ?, ?, '{}')`, []any{ms, ms}},
		{`INSERT INTO segments (id, project_id, ord, text, voice_id) VALUES ('s1', 'p1', 0, 'First line', 'v1')`, nil},
		{`INSERT INTO licences (licence, model, accepted_at) VALUES ('cc-by-nc-4.0', 'mlx-community/OmniVoice-bf16', ?)`, []any{ms}},
	} {
		if _, err := s.db.Execute(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
	if err := s.Documents().Put(ctx, "settings:app", []byte(`{"version":1,"studio":{"keepTakes":50}}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Documents().Put(ctx, "draft:studio", []byte(`{"text":"unsaved script"}`)); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// This build.
	migrations = saved
	s = openTest(t, dir)
	if v, _ := userVersion(ctx, s.db); v != Version() || Version() < 7 {
		t.Fatalf("user_version %d, want %d (≥ 7)", v, Version())
	}
	if _, err := os.Stat(filepath.Join(dir, FileName+".bak-1")); err != nil {
		t.Errorf("no backup of the version-1 database: %v", err)
	}
	takes, err := s.Takes().List(ctx, core.TakeQuery{})
	if err != nil || len(takes) != 2 {
		t.Fatalf("takes %v %+v", err, takes)
	}
	if l, _ := s.Takes().List(ctx, core.TakeQuery{Search: "旧的"}); len(l) != 1 {
		t.Error("old takes are not searchable")
	}
	if _, err := os.Stat(s.Takes().AudioPath(takes[0])); err != nil {
		t.Errorf("take audio: %v", err)
	}
	if n, _ := s.ImportHistoryJSON(ctx); n != 0 {
		t.Errorf("history.json imported again: %d", n)
	}
	vs, err := s.Voices().List(ctx, core.VoiceQuery{Search: "narrator"})
	if err != nil || len(vs) != 1 || vs[0].RefText != "Once upon a time" {
		t.Errorf("voices %v %+v", err, vs)
	}
	if u, err := s.Voices().Usage(ctx, "v1"); err != nil || u.Segments != 1 {
		t.Errorf("voice usage %+v %v", u, err)
	}
	if p, err := s.Projects().Get(ctx, "p1"); err != nil || p.Title != "Old Story" {
		t.Errorf("project %+v %v", p, err)
	}
	if ls, err := s.Licences().List(ctx); err != nil || len(ls) != 1 {
		t.Errorf("licences %+v %v", ls, err)
	}
	for k, want := range map[string]string{"settings:app": `{"version":1,"studio":{"keepTakes":50}}`, "draft:studio": `{"text":"unsaved script"}`} {
		if doc, ok, _ := s.Documents().Get(ctx, k); !ok || string(doc) != want {
			t.Errorf("document %s: %s", k, doc)
		}
	}
	// The later migrations' tables work.
	if err := s.Transcripts().Put(ctx, core.Transcript{ID: "t1", CreatedAt: now, UpdatedAt: now, Title: "new", Mode: "fast", Model: "m",
		Segments: []core.TranscriptSegment{{Text: "hello there"}}}); err != nil {
		t.Errorf("transcripts: %v", err)
	}
	if err := s.Dubs().Put(ctx, store.DubRecord{ID: "d1", CreatedAt: now, UpdatedAt: now, Title: "dub", Doc: []byte(`{}`)}); err != nil {
		t.Errorf("dubs: %v", err)
	}
	if err := s.Workflows().PutDoc(ctx, store.WorkflowRecord{ID: "w1", Name: "wf", CreatedAt: now, UpdatedAt: now, Doc: []byte(`{}`)}); err != nil {
		t.Errorf("workflows: %v", err)
	}
	if _, err := s.Calls().List(ctx, store.CallQuery{Limit: 10}); err != nil {
		t.Errorf("calls: %v", err)
	}
	s.Close()
	s = openTest(t, dir)
	if takes, _ := s.Takes().List(ctx, core.TakeQuery{}); len(takes) != 2 {
		t.Errorf("after reopening: %d takes", len(takes))
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "*.bak-*")); len(matches) != 1 {
		t.Errorf("backups %v", matches)
	}
}
