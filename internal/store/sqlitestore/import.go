package sqlitestore

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/hehehai/AudioSmith/internal/store/historyjson"
)

// HistoryImportKey is the document that records the history.json import.
const HistoryImportKey = "migration:historyjson"

// HistoryImport is the HistoryImportKey document.
type HistoryImport struct {
	ImportedAt time.Time `json:"importedAt"`
	Takes      int       `json:"takes"`  // takes imported
	Source     string    `json:"source"` // the history.json read
}

// ImportHistoryJSON brings the MVP's take library into the store, once:
// the takes of <dir>/history.json whose audio still exists are added (in
// one transaction, with the HistoryImportKey marker), and history.json is
// left in place. The audio stays where it is, so dir must be the store's
// own directory (both use <dir>/clips). It returns how many takes it
// imported: 0 when the import was done before or there is no history.
func (s *Store) ImportHistoryJSON(ctx context.Context) (int, error) {
	if _, done, err := s.documents.Get(ctx, HistoryImportKey); err != nil || done {
		return 0, err
	}
	takes, err := historyjson.Read(s.dir)
	if err != nil {
		// Leave the marker unset: the next start tries again.
		return 0, fmt.Errorf("sqlitestore: reading history.json: %w", err)
	}
	stmts := make([]sqlite.Statement, 0, len(takes)+1)
	for _, t := range takes {
		// MVP takes have no relations; drop any, as they would point nowhere.
		t.VoiceID, t.ProjectID = "", ""
		st, err := takeStatement(t)
		if err != nil {
			return 0, fmt.Errorf("sqlitestore: importing history.json: %w", err)
		}
		// A take already in the store wins over its history.json copy.
		st.SQL = takeInsertIgnore
		stmts = append(stmts, st)
	}
	now := time.Now()
	marker, err := json.Marshal(HistoryImport{ImportedAt: now.UTC(), Takes: len(takes), Source: filepath.Join(s.dir, "history.json")})
	if err != nil {
		return 0, err
	}
	stmts = append(stmts, documentStatement(HistoryImportKey, marker, now))
	if _, err := s.db.Transaction(ctx, stmts); err != nil {
		return 0, wrap("sqlitestore: importing history.json", err)
	}
	return len(takes), nil
}

const takeInsertIgnore = `INSERT INTO takes (id, created_at, file, text, model, voice_id, project_id, job_id, starred,
  sample_rate, audio_seconds, gen_seconds, recipe)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO NOTHING`
