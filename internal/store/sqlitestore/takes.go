package sqlitestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/hehehai/AudioSmith/internal/core"
)

// takeRepo stores takes: the recipe as JSON (the source of truth, in the
// MVP's history.json shape) plus the columns queries filter on. The
// relation columns (voice, project) and starred win over the JSON when
// reading, since foreign keys may clear them.
type takeRepo struct {
	db  *sqlite.DB
	dir string
}

func (r *takeRepo) AudioDir() string { return r.dir }

func (r *takeRepo) AudioPath(t core.Take) string { return filepath.Join(r.dir, t.File) }

const takeUpsert = `INSERT INTO takes (id, created_at, file, text, model, voice_id, project_id, job_id, starred,
  sample_rate, audio_seconds, gen_seconds, recipe)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  created_at = excluded.created_at, file = excluded.file, text = excluded.text, model = excluded.model,
  voice_id = excluded.voice_id, project_id = excluded.project_id, job_id = excluded.job_id,
  starred = excluded.starred, sample_rate = excluded.sample_rate, audio_seconds = excluded.audio_seconds,
  gen_seconds = excluded.gen_seconds, recipe = excluded.recipe`

func takeStatement(t core.Take) (sqlite.Statement, error) {
	if err := validID(t.ID); err != nil {
		return sqlite.Statement{}, err
	}
	if t.File == "" || filepath.Base(t.File) != t.File {
		return sqlite.Statement{}, fmt.Errorf("take %s: file %q: %w", t.ID, t.File, core.ErrInvalid)
	}
	recipe, err := json.Marshal(t)
	if err != nil {
		return sqlite.Statement{}, err
	}
	return sqlite.Statement{SQL: takeUpsert, Args: []any{
		t.ID, t.CreatedAt.UnixMilli(), t.File, t.Text, t.Model, nullable(t.VoiceID), nullable(t.ProjectID),
		nullable(t.JobID), t.Starred, t.SampleRate, t.AudioSeconds, t.GenSeconds, string(recipe),
	}}, nil
}

func (r *takeRepo) Put(ctx context.Context, t core.Take) error {
	st, err := takeStatement(t)
	if err != nil {
		return err
	}
	_, err = r.db.Execute(ctx, st.SQL, st.Args...)
	return wrap("take "+t.ID, err)
}

const takeColumns = `t.recipe, t.voice_id, t.project_id, t.starred`

func scanTake(row []any) (core.Take, error) {
	var t core.Take
	if err := json.Unmarshal(asBytes(row[0]), &t); err != nil {
		return t, err
	}
	t.VoiceID, t.ProjectID, t.Starred = asString(row[1]), asString(row[2]), asInt(row[3]) != 0
	return t, nil
}

func (r *takeRepo) Get(ctx context.Context, id string) (core.Take, error) {
	rows, err := r.db.Query(ctx, `SELECT `+takeColumns+` FROM takes t WHERE t.id = ?`, id)
	if err != nil {
		return core.Take{}, wrap("take "+id, err)
	}
	if len(rows.Values) == 0 {
		return core.Take{}, fmt.Errorf("take %s: %w", id, core.ErrNotFound)
	}
	return scanTake(rows.Values[0])
}

func (r *takeRepo) List(ctx context.Context, q core.TakeQuery) ([]core.Take, error) {
	var where []string
	var args []any
	if s := strings.TrimSpace(q.Search); s != "" {
		if useFTS(s) {
			where = append(where, `t.seq IN (SELECT rowid FROM takes_fts WHERE takes_fts MATCH ?)`)
			args = append(args, ftsPhrase(s))
		} else {
			where = append(where, `t.text LIKE ? ESCAPE '\'`)
			args = append(args, likePattern(s))
		}
	}
	if q.Model != "" {
		where, args = append(where, `t.model = ?`), append(args, q.Model)
	}
	if q.VoiceID != "" {
		where, args = append(where, `t.voice_id = ?`), append(args, q.VoiceID)
	}
	if q.ProjectID != "" {
		where, args = append(where, `t.project_id = ?`), append(args, q.ProjectID)
	}
	if q.Starred {
		where = append(where, `t.starred = 1`)
	}
	sql := `SELECT ` + takeColumns + ` FROM takes t`
	if len(where) > 0 {
		sql += ` WHERE ` + strings.Join(where, ` AND `)
	}
	sql += ` ORDER BY t.created_at DESC, t.id DESC`
	sql, args = page(sql, args, q.Limit, q.Offset)
	rows, err := r.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, wrap("takes", err)
	}
	out := make([]core.Take, 0, len(rows.Values))
	for _, row := range rows.Values {
		t, err := scanTake(row)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// page adds LIMIT/OFFSET; limit 0 means no limit.
func page(sql string, args []any, limit, offset int) (string, []any) {
	if limit <= 0 && offset <= 0 {
		return sql, args
	}
	if limit <= 0 {
		limit = -1
	}
	return sql + ` LIMIT ? OFFSET ?`, append(args, limit, max(offset, 0))
}

// Delete removes the row, then the audio file (a missing file is fine).
func (r *takeRepo) Delete(ctx context.Context, id string) error {
	rows, err := r.db.Query(ctx, `DELETE FROM takes WHERE id = ? RETURNING file`, id)
	if err != nil {
		return wrap("take "+id, err)
	}
	if len(rows.Values) == 0 {
		return fmt.Errorf("take %s: %w", id, core.ErrNotFound)
	}
	file := asString(rows.Values[0][0])
	if file != "" && filepath.Base(file) == file {
		if err := os.Remove(filepath.Join(r.dir, file)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("take %s: removing audio: %w", id, err)
		}
	}
	return nil
}
