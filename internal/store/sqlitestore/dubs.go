package sqlitestore

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/store"
)

// dubRepo stores dub projects (migration 005).
type dubRepo struct {
	db  *sqlite.DB
	dir string
}

// Dubs is the dub project repository; project files live in <dir>/dubs.
func (s *Store) Dubs() store.Dubs {
	return &dubRepo{db: s.db, dir: filepath.Join(s.dir, "dubs")}
}

func (r *dubRepo) Dir() string { return r.dir }

const dubUpsert = `INSERT INTO dubs (id, created_at, updated_at, batch, title, source, languages, phase, duration, doc)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  created_at = excluded.created_at, updated_at = excluded.updated_at, batch = excluded.batch,
  title = excluded.title, source = excluded.source, languages = excluded.languages,
  phase = excluded.phase, duration = excluded.duration, doc = excluded.doc`

func (r *dubRepo) Put(ctx context.Context, d store.DubRecord) error {
	if err := validID(d.ID); err != nil {
		return err
	}
	if len(d.Doc) == 0 || !json.Valid(d.Doc) {
		return fmt.Errorf("dub %s: document is not JSON: %w", d.ID, core.ErrInvalid)
	}
	_, err := r.db.Execute(ctx, dubUpsert, d.ID, d.CreatedAt.UnixMilli(), d.UpdatedAt.UnixMilli(), d.Batch, d.Title, d.Source,
		strings.Join(d.Languages, ","), d.Phase, d.Duration, string(d.Doc))
	return wrap("dub "+d.ID, err)
}

const dubColumns = `id, created_at, updated_at, batch, title, source, languages, phase, duration`

func scanDub(row []any) store.DubRecord {
	d := store.DubRecord{
		ID: asString(row[0]), CreatedAt: fromMS(row[1]), UpdatedAt: fromMS(row[2]), Batch: asString(row[3]),
		Title: asString(row[4]), Source: asString(row[5]), Phase: asString(row[7]),
	}
	if l := asString(row[6]); l != "" {
		d.Languages = strings.Split(l, ",")
	}
	switch v := row[8].(type) {
	case float64:
		d.Duration = v
	case int64:
		d.Duration = float64(v)
	}
	if len(row) > 9 {
		d.Doc = json.RawMessage(asBytes(row[9]))
	}
	return d
}

func (r *dubRepo) Get(ctx context.Context, id string) (store.DubRecord, error) {
	rows, err := r.db.Query(ctx, `SELECT `+dubColumns+`, doc FROM dubs WHERE id = ?`, id)
	if err != nil {
		return store.DubRecord{}, wrap("dub "+id, err)
	}
	if len(rows.Values) == 0 {
		return store.DubRecord{}, fmt.Errorf("dub %s: %w", id, core.ErrNotFound)
	}
	return scanDub(rows.Values[0]), nil
}

func (r *dubRepo) List(ctx context.Context, q store.DubQuery) ([]store.DubRecord, error) {
	sql := `SELECT ` + dubColumns + ` FROM dubs`
	var args []any
	if !q.AllBatches {
		sql += ` WHERE batch = ?`
		args = append(args, q.Batch)
	}
	sql += ` ORDER BY updated_at DESC, id DESC`
	sql, args = page(sql, args, q.Limit, q.Offset)
	rows, err := r.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, wrap("dubs", err)
	}
	out := make([]store.DubRecord, 0, len(rows.Values))
	for _, row := range rows.Values {
		out = append(out, scanDub(row))
	}
	return out, nil
}

func (r *dubRepo) Delete(ctx context.Context, id string) error {
	rows, err := r.db.Query(ctx, `DELETE FROM dubs WHERE id = ? RETURNING id`, id)
	if err != nil {
		return wrap("dub "+id, err)
	}
	if len(rows.Values) == 0 {
		return fmt.Errorf("dub %s: %w", id, core.ErrNotFound)
	}
	return nil
}
