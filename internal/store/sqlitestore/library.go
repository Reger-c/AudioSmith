package sqlitestore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/hehehai/AudioSmith/internal/core"
)

// --- projects -----------------------------------------------------------

type projectRepo struct{ db *sqlite.DB }

func (r *projectRepo) Put(ctx context.Context, p core.Project) error {
	if err := validID(p.ID); err != nil {
		return err
	}
	body := string(p.Body)
	if len(p.Body) == 0 {
		body = "{}"
	} else if !json.Valid(p.Body) {
		return fmt.Errorf("project %s: body is not JSON: %w", p.ID, core.ErrInvalid)
	}
	_, err := r.db.Execute(ctx, `INSERT INTO projects (id, kind, title, created_at, updated_at, body)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET kind = excluded.kind, title = excluded.title,
  created_at = excluded.created_at, updated_at = excluded.updated_at, body = excluded.body`,
		p.ID, string(p.Kind), p.Title, p.CreatedAt.UnixMilli(), p.UpdatedAt.UnixMilli(), body)
	return wrap("project "+p.ID, err)
}

const projectColumns = `id, kind, title, created_at, updated_at, body`

func scanProject(row []any) core.Project {
	return core.Project{
		ID:        asString(row[0]),
		Kind:      core.ProjectKind(asString(row[1])),
		Title:     asString(row[2]),
		CreatedAt: fromMS(row[3]),
		UpdatedAt: fromMS(row[4]),
		Body:      json.RawMessage(asBytes(row[5])),
	}
}

func (r *projectRepo) Get(ctx context.Context, id string) (core.Project, error) {
	rows, err := r.db.Query(ctx, `SELECT `+projectColumns+` FROM projects WHERE id = ?`, id)
	if err != nil {
		return core.Project{}, wrap("project "+id, err)
	}
	if len(rows.Values) == 0 {
		return core.Project{}, fmt.Errorf("project %s: %w", id, core.ErrNotFound)
	}
	return scanProject(rows.Values[0]), nil
}

// List returns projects of kind ("" = all) whose title matches search,
// most recently updated first.
func (r *projectRepo) List(ctx context.Context, kind core.ProjectKind, search string) ([]core.Project, error) {
	var where []string
	var args []any
	if s := strings.TrimSpace(search); s != "" {
		if useFTS(s) {
			where, args = append(where, `seq IN (SELECT rowid FROM projects_fts WHERE projects_fts MATCH ?)`), append(args, ftsPhrase(s))
		} else {
			where, args = append(where, `title LIKE ? ESCAPE '\'`), append(args, likePattern(s))
		}
	}
	if kind != "" {
		where, args = append(where, `kind = ?`), append(args, string(kind))
	}
	sql := `SELECT ` + projectColumns + ` FROM projects`
	if len(where) > 0 {
		sql += ` WHERE ` + strings.Join(where, ` AND `)
	}
	rows, err := r.db.Query(ctx, sql+` ORDER BY updated_at DESC, id DESC`, args...)
	if err != nil {
		return nil, wrap("projects", err)
	}
	out := make([]core.Project, 0, len(rows.Values))
	for _, row := range rows.Values {
		out = append(out, scanProject(row))
	}
	return out, nil
}

func (r *projectRepo) Delete(ctx context.Context, id string) error {
	res, err := r.db.Execute(ctx, `DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return wrap("project "+id, err)
	}
	if res.Changes == 0 {
		return fmt.Errorf("project %s: %w", id, core.ErrNotFound)
	}
	return nil
}

// --- licences -----------------------------------------------------------

type licenceRepo struct{ db *sqlite.DB }

func (r *licenceRepo) Put(ctx context.Context, a core.LicenceAcceptance) error {
	if a.Licence == "" {
		return fmt.Errorf("licence acceptance without a licence: %w", core.ErrInvalid)
	}
	_, err := r.db.Execute(ctx, `INSERT INTO licences (licence, model, accepted_at) VALUES (?, ?, ?)
ON CONFLICT(licence, model) DO UPDATE SET accepted_at = excluded.accepted_at`, a.Licence, a.Model, a.AcceptedAt.UnixMilli())
	return wrap("licence "+a.Licence, err)
}

// Delete revokes an acceptance; revoking one not on record is a no-op.
func (r *licenceRepo) Delete(ctx context.Context, licence, model string) error {
	_, err := r.db.Execute(ctx, `DELETE FROM licences WHERE licence = ? AND model = ?`, licence, model)
	return wrap("licence "+licence, err)
}

func (r *licenceRepo) List(ctx context.Context) ([]core.LicenceAcceptance, error) {
	rows, err := r.db.Query(ctx, `SELECT licence, model, accepted_at FROM licences ORDER BY licence, model`)
	if err != nil {
		return nil, wrap("licences", err)
	}
	out := make([]core.LicenceAcceptance, 0, len(rows.Values))
	for _, row := range rows.Values {
		out = append(out, core.LicenceAcceptance{Licence: asString(row[0]), Model: asString(row[1]), AcceptedAt: fromMS(row[2])})
	}
	return out, nil
}

// --- documents ----------------------------------------------------------

type documentRepo struct{ db *sqlite.DB }

func (r *documentRepo) Get(ctx context.Context, key string) ([]byte, bool, error) {
	rows, err := r.db.Query(ctx, `SELECT doc FROM documents WHERE key = ?`, key)
	if err != nil {
		return nil, false, wrap("document "+key, err)
	}
	if len(rows.Values) == 0 {
		return nil, false, nil
	}
	return asBytes(rows.Values[0][0]), true, nil
}

func documentStatement(key string, doc []byte, now time.Time) sqlite.Statement {
	return sqlite.Statement{SQL: `INSERT INTO documents (key, doc, updated_at) VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET doc = excluded.doc, updated_at = excluded.updated_at`, Args: []any{key, string(doc), now.UnixMilli()}}
}

// Put stores a JSON document under key.
func (r *documentRepo) Put(ctx context.Context, key string, doc []byte) error {
	if key == "" || !json.Valid(doc) {
		return fmt.Errorf("document %q: %w", key, core.ErrInvalid)
	}
	st := documentStatement(key, doc, time.Now())
	_, err := r.db.Execute(ctx, st.SQL, st.Args...)
	return wrap("document "+key, err)
}

// Delete removes a document; deleting a missing one is a no-op.
func (r *documentRepo) Delete(ctx context.Context, key string) error {
	_, err := r.db.Execute(ctx, `DELETE FROM documents WHERE key = ?`, key)
	return wrap("document "+key, err)
}
