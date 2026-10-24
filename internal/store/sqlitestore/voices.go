package sqlitestore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/store"
)

// --- voices -------------------------------------------------------------

// voiceRepo stores voice profiles: core.Voice as JSON plus filter columns
// (migration 2 adds search, the text searched besides the name, and origin).
type voiceRepo struct {
	db  *sqlite.DB
	dir string
}

func (r *voiceRepo) VoiceDir() string { return r.dir }

// voiceSearch is what a voice is found by besides its name.
func voiceSearch(v core.Voice) string {
	parts := []string{v.Style, v.RefText, v.Design, v.Language}
	parts = append(parts, v.Tags...)
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n")
}

func (r *voiceRepo) Put(ctx context.Context, v core.Voice) error {
	if err := validID(v.ID); err != nil {
		return err
	}
	profile, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = r.db.Execute(ctx, `INSERT INTO voices (id, name, kind, model, locked, consent, created_at, updated_at, profile, search, origin)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET name = excluded.name, kind = excluded.kind, model = excluded.model,
  locked = excluded.locked, consent = excluded.consent, created_at = excluded.created_at,
  updated_at = excluded.updated_at, profile = excluded.profile, search = excluded.search, origin = excluded.origin`,
		v.ID, v.Name, string(v.Kind), v.Model, v.Locked, v.Verified(), v.CreatedAt.UnixMilli(), v.UpdatedAt.UnixMilli(),
		string(profile), voiceSearch(v), v.Origin)
	return wrap("voice "+v.ID, err)
}

func scanVoice(row []any) (core.Voice, error) {
	var v core.Voice
	err := json.Unmarshal(asBytes(row[0]), &v)
	return v, err
}

func (r *voiceRepo) Get(ctx context.Context, id string) (core.Voice, error) {
	rows, err := r.db.Query(ctx, `SELECT profile FROM voices WHERE id = ?`, id)
	if err != nil {
		return core.Voice{}, wrap("voice "+id, err)
	}
	if len(rows.Values) == 0 {
		return core.Voice{}, fmt.Errorf("voice %s: %w", id, core.ErrNotFound)
	}
	return scanVoice(rows.Values[0])
}

// List filters by kind, model (a voice for any model, model "", matches
// every model) and origin, and searches names, styles, transcripts,
// descriptions and tags; sorted by name.
func (r *voiceRepo) List(ctx context.Context, q core.VoiceQuery) ([]core.Voice, error) {
	var where []string
	var args []any
	if s := strings.TrimSpace(q.Search); s != "" {
		if useFTS(s) {
			where, args = append(where, `seq IN (SELECT rowid FROM voices_fts WHERE voices_fts MATCH ?)`), append(args, ftsPhrase(s))
		} else {
			p := likePattern(s)
			where, args = append(where, `(name LIKE ? ESCAPE '\' OR search LIKE ? ESCAPE '\')`), append(args, p, p)
		}
	}
	if q.Kind != "" {
		where, args = append(where, `kind = ?`), append(args, string(q.Kind))
	}
	if q.Model != "" {
		where, args = append(where, `(model = ? OR model = '')`), append(args, q.Model)
	}
	if q.Origin != "" {
		where, args = append(where, `origin = ?`), append(args, q.Origin)
	}
	sql := `SELECT profile FROM voices`
	if len(where) > 0 {
		sql += ` WHERE ` + strings.Join(where, ` AND `)
	}
	rows, err := r.db.Query(ctx, sql+` ORDER BY name COLLATE NOCASE, id`, args...)
	if err != nil {
		return nil, wrap("voices", err)
	}
	out := make([]core.Voice, 0, len(rows.Values))
	for _, row := range rows.Values {
		v, err := scanVoice(row)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// Delete removes the profile and its blob directory. Takes and segments
// that used it keep their audio and lose the link (ON DELETE SET NULL).
func (r *voiceRepo) Delete(ctx context.Context, id string) error {
	if err := validID(id); err != nil {
		return err
	}
	res, err := r.db.Execute(ctx, `DELETE FROM voices WHERE id = ?`, id)
	if err != nil {
		return wrap("voice "+id, err)
	}
	if res.Changes == 0 {
		return fmt.Errorf("voice %s: %w", id, core.ErrNotFound)
	}
	return os.RemoveAll(filepath.Join(r.dir, id))
}

// Usage counts the voice's takes and the project segments that use it.
func (r *voiceRepo) Usage(ctx context.Context, id string) (core.VoiceUsage, error) {
	var u core.VoiceUsage
	rows, err := r.db.Query(ctx, `SELECT COUNT(*), (SELECT id FROM takes WHERE voice_id = ? ORDER BY created_at DESC, id DESC LIMIT 1)
FROM takes WHERE voice_id = ?`, id, id)
	if err != nil {
		return u, wrap("voice usage "+id, err)
	}
	if len(rows.Values) == 1 {
		u.Takes = int(asInt(rows.Values[0][0]))
		u.LatestTake = asString(rows.Values[0][1])
	}
	rows, err = r.db.Query(ctx, `SELECT p.id, p.title, p.kind, COUNT(*) FROM segments s JOIN projects p ON p.id = s.project_id
WHERE s.voice_id = ? GROUP BY p.id ORDER BY p.updated_at DESC, p.id`, id)
	if err != nil {
		return u, wrap("voice usage "+id, err)
	}
	for _, row := range rows.Values {
		pu := core.VoiceProjectUsage{ID: asString(row[0]), Title: asString(row[1]), Kind: core.ProjectKind(asString(row[2])), Segments: int(asInt(row[3]))}
		u.Projects = append(u.Projects, pu)
		u.Segments += pu.Segments
	}
	return u, nil
}

// Links lists the takes and segments whose voice_id is id.
func (r *voiceRepo) Links(ctx context.Context, id string) (store.VoiceLinks, error) {
	var l store.VoiceLinks
	for _, q := range []struct {
		sql string
		out *[]string
	}{{`SELECT id FROM takes WHERE voice_id = ? ORDER BY id`, &l.Takes}, {`SELECT id FROM segments WHERE voice_id = ? ORDER BY id`, &l.Segments}} {
		rows, err := r.db.Query(ctx, q.sql, id)
		if err != nil {
			return l, wrap("voice links "+id, err)
		}
		for _, row := range rows.Values {
			*q.out = append(*q.out, asString(row[0]))
		}
	}
	return l, nil
}

// Relink sets voice_id back to id on the takes and segments of l that
// have none now.
func (r *voiceRepo) Relink(ctx context.Context, id string, l store.VoiceLinks) error {
	for _, q := range []struct {
		table string
		ids   []string
	}{{"takes", l.Takes}, {"segments", l.Segments}} {
		for _, x := range q.ids {
			if _, err := r.db.Execute(ctx, `UPDATE `+q.table+` SET voice_id = ? WHERE id = ? AND voice_id IS NULL`, id, x); err != nil {
				return wrap("voice relink "+id, err)
			}
		}
	}
	return nil
}

// --- favorites ----------------------------------------------------------

type favoriteRepo struct{ db *sqlite.DB }

// List returns a scope's keys, oldest first.
func (r *favoriteRepo) List(ctx context.Context, scope string) ([]string, error) {
	rows, err := r.db.Query(ctx, `SELECT key FROM favorites WHERE scope = ? ORDER BY created_at, key`, scope)
	if err != nil {
		return nil, wrap("favorites "+scope, err)
	}
	out := make([]string, 0, len(rows.Values))
	for _, row := range rows.Values {
		out = append(out, asString(row[0]))
	}
	return out, nil
}

func (r *favoriteRepo) Set(ctx context.Context, scope, key string, on bool) error {
	if scope == "" || key == "" {
		return fmt.Errorf("favorite %q/%q: %w", scope, key, core.ErrInvalid)
	}
	var err error
	if on {
		_, err = r.db.Execute(ctx, `INSERT INTO favorites (scope, key, created_at) VALUES (?, ?, ?) ON CONFLICT(scope, key) DO NOTHING`,
			scope, key, time.Now().UnixMilli())
	} else {
		_, err = r.db.Execute(ctx, `DELETE FROM favorites WHERE scope = ? AND key = ?`, scope, key)
	}
	return wrap("favorite "+key, err)
}
