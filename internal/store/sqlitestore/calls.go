package sqlitestore

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/store"
)

// The Projects page lists phone calls (M6b) as items of source "call",
// whose detail is "<outcome>|<status>".
func init() {
	libParts = append(libParts, libPart{
		sql:      `SELECT 'call' AS src, id, 'call' AS cat, title, outcome || '|' || status AS detail, updated_at AS at FROM calls`,
		fts:      `seq IN (SELECT rowid FROM calls_fts WHERE calls_fts MATCH ?)`,
		like:     `(title LIKE ? ESCAPE '\' OR summary LIKE ? ESCAPE '\')`,
		likeArgs: 2,
		category: func(c string) bool { return c == "" || c == store.LibCall },
	})
}

// callRepo stores phone calls (migration 007).
type callRepo struct {
	db  *sqlite.DB
	dir string
}

// Calls is the call repository; recordings live in <dir>/calls.
func (s *Store) Calls() store.Calls {
	return &callRepo{db: s.db, dir: filepath.Join(s.dir, "calls")}
}

func (r *callRepo) Dir() string { return r.dir }

const callUpsert = `INSERT INTO calls (id, created_at, updated_at, direction, number, title, status, outcome, summary, provider_sid, duration, doc)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  created_at = excluded.created_at, updated_at = excluded.updated_at, direction = excluded.direction,
  number = excluded.number, title = excluded.title, status = excluded.status, outcome = excluded.outcome,
  summary = excluded.summary, provider_sid = excluded.provider_sid, duration = excluded.duration, doc = excluded.doc`

func (r *callRepo) Put(ctx context.Context, c store.CallRecord) error {
	if err := validID(c.ID); err != nil {
		return err
	}
	if len(c.Doc) == 0 || !json.Valid(c.Doc) {
		return fmt.Errorf("call %s: document is not JSON: %w", c.ID, core.ErrInvalid)
	}
	_, err := r.db.Execute(ctx, callUpsert, c.ID, c.CreatedAt.UnixMilli(), c.UpdatedAt.UnixMilli(), c.Direction, c.Number, c.Title,
		c.Status, c.Outcome, c.Summary, c.ProviderSID, c.Duration, string(c.Doc))
	return wrap("call "+c.ID, err)
}

const callColumns = `id, created_at, updated_at, direction, number, title, status, outcome, summary, provider_sid, duration`

func scanCall(row []any) store.CallRecord {
	c := store.CallRecord{ID: asString(row[0]), CreatedAt: fromMS(row[1]), UpdatedAt: fromMS(row[2]), Direction: asString(row[3]),
		Number: asString(row[4]), Title: asString(row[5]), Status: asString(row[6]), Outcome: asString(row[7]),
		Summary: asString(row[8]), ProviderSID: asString(row[9]), Duration: asFloat(row[10])}
	if len(row) > 11 {
		c.Doc = json.RawMessage(asBytes(row[11]))
	}
	return c
}

func (r *callRepo) get(ctx context.Context, where, arg, what string) (store.CallRecord, error) {
	rows, err := r.db.Query(ctx, `SELECT `+callColumns+`, doc FROM calls WHERE `+where+` ORDER BY created_at DESC LIMIT 1`, arg)
	if err != nil {
		return store.CallRecord{}, wrap("call "+what, err)
	}
	if len(rows.Values) == 0 {
		return store.CallRecord{}, fmt.Errorf("call %s: %w", what, core.ErrNotFound)
	}
	return scanCall(rows.Values[0]), nil
}

func (r *callRepo) Get(ctx context.Context, id string) (store.CallRecord, error) {
	return r.get(ctx, `id = ?`, id, id)
}

func (r *callRepo) BySID(ctx context.Context, sid string) (store.CallRecord, error) {
	if sid == "" {
		return store.CallRecord{}, fmt.Errorf("call without a SID: %w", core.ErrNotFound)
	}
	return r.get(ctx, `provider_sid = ?`, sid, sid)
}

func (r *callRepo) List(ctx context.Context, q store.CallQuery) ([]store.CallRecord, error) {
	sql := `SELECT ` + callColumns + ` FROM calls ORDER BY created_at DESC, id DESC`
	var args []any
	if q.Limit > 0 {
		sql += ` LIMIT ?`
		args = append(args, q.Limit)
	}
	rows, err := r.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, wrap("calls", err)
	}
	out := make([]store.CallRecord, 0, len(rows.Values))
	for _, row := range rows.Values {
		out = append(out, scanCall(row))
	}
	return out, nil
}

func (r *callRepo) Delete(ctx context.Context, id string) error {
	res, err := r.db.Execute(ctx, `DELETE FROM calls WHERE id = ?`, id)
	if err != nil {
		return wrap("call "+id, err)
	}
	if res.Changes == 0 {
		return fmt.Errorf("call %s: %w", id, core.ErrNotFound)
	}
	return nil
}
