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

// The Projects page lists workflows (M6) as items of source "workflow",
// whose detail is the last run's state.
func init() {
	libParts = append(libParts, libPart{
		sql:      `SELECT 'workflow' AS src, id, 'workflow' AS cat, name AS title, state AS detail, updated_at AS at FROM workflows`,
		fts:      `seq IN (SELECT rowid FROM workflows_fts WHERE workflows_fts MATCH ?)`,
		like:     `(name LIKE ? ESCAPE '\' OR summary LIKE ? ESCAPE '\')`,
		likeArgs: 2,
		category: func(c string) bool { return c == "" || c == store.LibWorkflow },
	})
}

// workflowRepo stores workflows (migration 006).
type workflowRepo struct {
	db  *sqlite.DB
	dir string
}

// Workflows is the workflow repository; run files live in <dir>/workflows.
func (s *Store) Workflows() store.Workflows {
	return &workflowRepo{db: s.db, dir: filepath.Join(s.dir, "workflows")}
}

func (r *workflowRepo) Dir() string { return r.dir }

const workflowUpsert = `INSERT INTO workflows (id, created_at, updated_at, name, summary, doc)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  created_at = excluded.created_at, updated_at = excluded.updated_at, name = excluded.name,
  summary = excluded.summary, doc = excluded.doc`

func (r *workflowRepo) PutDoc(ctx context.Context, w store.WorkflowRecord) error {
	if err := validID(w.ID); err != nil {
		return err
	}
	if len(w.Doc) == 0 || !json.Valid(w.Doc) {
		return fmt.Errorf("workflow %s: document is not JSON: %w", w.ID, core.ErrInvalid)
	}
	_, err := r.db.Execute(ctx, workflowUpsert, w.ID, w.CreatedAt.UnixMilli(), w.UpdatedAt.UnixMilli(), w.Name, w.Summary, string(w.Doc))
	return wrap("workflow "+w.ID, err)
}

func (r *workflowRepo) PutRun(ctx context.Context, w store.WorkflowRecord) error {
	run := ""
	if len(w.Run) > 0 {
		if !json.Valid(w.Run) {
			return fmt.Errorf("workflow %s: run is not JSON: %w", w.ID, core.ErrInvalid)
		}
		run = string(w.Run)
	}
	res, err := r.db.Execute(ctx, `UPDATE workflows SET state = ?, items = ?, done = ?, run = ? WHERE id = ?`, w.State, w.Items, w.Done, run, w.ID)
	if err != nil {
		return wrap("workflow "+w.ID, err)
	}
	if res.Changes == 0 {
		return fmt.Errorf("workflow %s: %w", w.ID, core.ErrNotFound)
	}
	return nil
}

const workflowColumns = `id, created_at, updated_at, name, summary, state, items, done`

func scanWorkflow(row []any) store.WorkflowRecord {
	w := store.WorkflowRecord{ID: asString(row[0]), CreatedAt: fromMS(row[1]), UpdatedAt: fromMS(row[2]), Name: asString(row[3]),
		Summary: asString(row[4]), State: asString(row[5]), Items: int(asInt(row[6])), Done: int(asInt(row[7]))}
	if len(row) > 9 {
		w.Doc = json.RawMessage(asBytes(row[8]))
		if run := asBytes(row[9]); len(run) > 0 {
			w.Run = json.RawMessage(run)
		}
	}
	return w
}

func (r *workflowRepo) Get(ctx context.Context, id string) (store.WorkflowRecord, error) {
	rows, err := r.db.Query(ctx, `SELECT `+workflowColumns+`, doc, run FROM workflows WHERE id = ?`, id)
	if err != nil {
		return store.WorkflowRecord{}, wrap("workflow "+id, err)
	}
	if len(rows.Values) == 0 {
		return store.WorkflowRecord{}, fmt.Errorf("workflow %s: %w", id, core.ErrNotFound)
	}
	return scanWorkflow(rows.Values[0]), nil
}

func (r *workflowRepo) List(ctx context.Context) ([]store.WorkflowRecord, error) {
	rows, err := r.db.Query(ctx, `SELECT `+workflowColumns+` FROM workflows ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, wrap("workflows", err)
	}
	out := make([]store.WorkflowRecord, 0, len(rows.Values))
	for _, row := range rows.Values {
		out = append(out, scanWorkflow(row))
	}
	return out, nil
}

func (r *workflowRepo) Delete(ctx context.Context, id string) error {
	res, err := r.db.Execute(ctx, `DELETE FROM workflows WHERE id = ?`, id)
	if err != nil {
		return wrap("workflow "+id, err)
	}
	if res.Changes == 0 {
		return fmt.Errorf("workflow %s: %w", id, core.ErrNotFound)
	}
	return nil
}
