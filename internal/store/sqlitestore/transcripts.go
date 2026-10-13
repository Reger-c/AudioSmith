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
	"github.com/hehehai/AudioSmith/internal/store"
)

// transcriptRepo stores transcripts (migration 003): the JSON document
// plus the columns lists and search use.
type transcriptRepo struct {
	db  *sqlite.DB
	dir string
}

// Transcripts is the transcript repository; its audio lives in
// <dir>/transcripts.
func (s *Store) Transcripts() store.Transcripts {
	return &transcriptRepo{db: s.db, dir: filepath.Join(s.dir, "transcripts")}
}

func (r *transcriptRepo) AudioDir() string { return r.dir }

func (r *transcriptRepo) AudioPath(t core.Transcript) string {
	if t.Audio == "" {
		return ""
	}
	return filepath.Join(r.dir, t.Audio)
}

const transcriptUpsert = `INSERT INTO transcripts (id, created_at, updated_at, source, title, language, duration, audio, text, brief, doc)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  created_at = excluded.created_at, updated_at = excluded.updated_at, source = excluded.source,
  title = excluded.title, language = excluded.language, duration = excluded.duration, audio = excluded.audio,
  text = excluded.text, brief = excluded.brief, doc = excluded.doc`

// brief is t without word timings.
func brief(t core.Transcript) core.Transcript {
	segs := make([]core.TranscriptSegment, len(t.Segments))
	for i, s := range t.Segments {
		s.Words = nil
		segs[i] = s
	}
	t.Segments = segs
	return t
}

func (r *transcriptRepo) Put(ctx context.Context, t core.Transcript) error {
	if err := validID(t.ID); err != nil {
		return err
	}
	if t.Audio != "" && filepath.Base(t.Audio) != t.Audio {
		return fmt.Errorf("transcript %s: audio %q: %w", t.ID, t.Audio, core.ErrInvalid)
	}
	doc, err := json.Marshal(t)
	if err != nil {
		return err
	}
	short, err := json.Marshal(brief(t))
	if err != nil {
		return err
	}
	search := t.Text()
	for _, extra := range []string{t.Clean, t.Raw} {
		if extra != "" && extra != search {
			search += "\n" + extra
		}
	}
	_, err = r.db.Execute(ctx, transcriptUpsert, t.ID, t.CreatedAt.UnixMilli(), t.UpdatedAt.UnixMilli(), string(t.Source),
		t.Title, t.Language, t.Duration, t.Audio, search, string(short), string(doc))
	return wrap("transcript "+t.ID, err)
}

func (r *transcriptRepo) Get(ctx context.Context, id string) (core.Transcript, error) {
	rows, err := r.db.Query(ctx, `SELECT doc FROM transcripts WHERE id = ?`, id)
	if err != nil {
		return core.Transcript{}, wrap("transcript "+id, err)
	}
	if len(rows.Values) == 0 {
		return core.Transcript{}, fmt.Errorf("transcript %s: %w", id, core.ErrNotFound)
	}
	var t core.Transcript
	err = json.Unmarshal(asBytes(rows.Values[0][0]), &t)
	return t, err
}

func (r *transcriptRepo) List(ctx context.Context, q core.TranscriptQuery) ([]core.Transcript, error) {
	var where []string
	var args []any
	if s := strings.TrimSpace(q.Search); s != "" {
		if useFTS(s) {
			where = append(where, `t.seq IN (SELECT rowid FROM transcripts_fts WHERE transcripts_fts MATCH ?)`)
			args = append(args, ftsPhrase(s))
		} else {
			p := likePattern(s)
			where = append(where, `(t.text LIKE ? ESCAPE '\' OR t.title LIKE ? ESCAPE '\' OR t.language LIKE ? ESCAPE '\')`)
			args = append(args, p, p, p)
		}
	}
	if q.Source != "" {
		where, args = append(where, `t.source = ?`), append(args, string(q.Source))
	}
	sql := `SELECT t.brief FROM transcripts t`
	if len(where) > 0 {
		sql += ` WHERE ` + strings.Join(where, ` AND `)
	}
	sql += ` ORDER BY t.created_at DESC, t.id DESC`
	sql, args = page(sql, args, q.Limit, q.Offset)
	rows, err := r.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, wrap("transcripts", err)
	}
	out := make([]core.Transcript, 0, len(rows.Values))
	for _, row := range rows.Values {
		var t core.Transcript
		if err := json.Unmarshal(asBytes(row[0]), &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// Delete removes the row, then the audio file (a missing file is fine).
func (r *transcriptRepo) Delete(ctx context.Context, id string) error {
	rows, err := r.db.Query(ctx, `DELETE FROM transcripts WHERE id = ? RETURNING audio`, id)
	if err != nil {
		return wrap("transcript "+id, err)
	}
	if len(rows.Values) == 0 {
		return fmt.Errorf("transcript %s: %w", id, core.ErrNotFound)
	}
	if file := asString(rows.Values[0][0]); file != "" && filepath.Base(file) == file {
		if err := os.Remove(filepath.Join(r.dir, file)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("transcript %s: removing audio: %w", id, err)
		}
	}
	return nil
}
