package sqlitestore

import (
	"context"
	"strings"
	"time"

	"github.com/hehehai/AudioSmith/internal/jobs"
)

// PruneJobs removes the finished jobs (succeeded, failed, cancelled) that
// finished before t, with their dependencies and events, as DeleteJobs
// does, but keeps two kinds whose record AudioSmith still reads: jobs of
// the kinds keep (the model downloads, which tell it what it downloaded)
// and jobs whose params name a "model" that is not in known (a custom
// repo it used: family_app.go's usedRepos). It returns how many it
// removed. Housekeeping at startup (review #102: the table was never
// cleaned).
func (s *Store) PruneJobs(ctx context.Context, finishedBefore time.Time, keep, known []string) (int64, error) {
	args := []any{string(jobs.StateSucceeded), string(jobs.StateFailed), string(jobs.StateCancelled), finishedBefore.UnixMilli()}
	sql := `DELETE FROM jobs WHERE state IN (?, ?, ?) AND finished_at IS NOT NULL AND finished_at < ?`
	if len(keep) > 0 {
		sql += ` AND kind NOT IN (` + marks(len(keep)) + `)`
		for _, k := range keep {
			args = append(args, k)
		}
	}
	// A model the params name, '' for none (or params that are no JSON
	// object, which name none).
	model := `COALESCE(CASE WHEN json_valid(params) AND json_type(params) = 'object' THEN json_extract(params, '$.model') END, '')`
	sql += ` AND (` + model + ` = ''`
	if len(known) > 0 {
		sql += ` OR ` + model + ` IN (` + marks(len(known)) + `)`
		for _, m := range known {
			args = append(args, m)
		}
	}
	sql += `)`
	res, err := s.db.Execute(ctx, sql, args...)
	if err != nil {
		return 0, wrap("jobs", err)
	}
	return res.Changes, nil
}

// marks is n placeholders, comma-separated.
func marks(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}
