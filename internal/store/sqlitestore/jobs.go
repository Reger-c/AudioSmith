package sqlitestore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/jobs"
)

// jobRepo implements jobs.Store on the jobs and job_deps tables. State
// changes are logged to job_events by triggers (see the migration).
type jobRepo struct{ db *sqlite.DB }

var (
	_ jobs.Store     = (*jobRepo)(nil)
	_ jobs.JobLoader = (*jobRepo)(nil)
)

// nonTerminal are the states Filter{States: nil} selects.
var nonTerminal = []jobs.State{jobs.StateWaiting, jobs.StateQueued, jobs.StateRunning, jobs.StateInterrupted}

func jsonOrNil(v any, empty bool) (any, error) {
	if empty {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// SaveJob upserts the whole job, and its dependencies, in one transaction.
func (r *jobRepo) SaveJob(ctx context.Context, j jobs.Job) error {
	if j.ID == "" || j.Kind == "" {
		return fmt.Errorf("job %q: id and kind are required: %w", j.ID, core.ErrInvalid)
	}
	params := string(j.Params)
	if len(j.Params) == 0 {
		params = "null"
	} else if !json.Valid(j.Params) {
		return fmt.Errorf("job %s: params are not JSON: %w", j.ID, core.ErrInvalid)
	}
	progress, err := jsonOrNil(j.Progress, j.Progress == jobs.Progress{})
	if err != nil {
		return err
	}
	var result any
	if len(j.Result) > 0 {
		if !json.Valid(j.Result) {
			return fmt.Errorf("job %s: result is not JSON: %w", j.ID, core.ErrInvalid)
		}
		result = string(j.Result)
	}
	jerr, err := jsonOrNil(j.Err, j.Err == nil)
	if err != nil {
		return err
	}
	stmts := []sqlite.Statement{{SQL: `INSERT INTO jobs (id, kind, lane, priority, state, owner, title, attempt,
  max_attempts, idempotent, params, progress, result, error, created_at, started_at, finished_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET kind = excluded.kind, lane = excluded.lane, priority = excluded.priority,
  state = excluded.state, owner = excluded.owner, title = excluded.title, attempt = excluded.attempt,
  max_attempts = excluded.max_attempts, idempotent = excluded.idempotent, params = excluded.params,
  progress = excluded.progress, result = excluded.result, error = excluded.error,
  created_at = excluded.created_at, started_at = excluded.started_at, finished_at = excluded.finished_at`,
		Args: []any{j.ID, j.Kind, string(j.Lane), int64(j.Priority), string(j.State), j.Owner, j.Title, j.Attempt,
			max(j.MaxAttempts, 1), j.Idempotent, params, progress, result, jerr,
			j.Created.UnixMilli(), ms(j.Started), ms(j.Finished)}},
		{SQL: `DELETE FROM job_deps WHERE job_id = ?`, Args: []any{j.ID}},
	}
	for _, after := range j.After {
		stmts = append(stmts, sqlite.Statement{SQL: `INSERT OR IGNORE INTO job_deps (job_id, after_id) VALUES (?, ?)`, Args: []any{j.ID, after}})
	}
	_, err = r.db.Transaction(ctx, stmts)
	return wrap("job "+j.ID, err)
}

const jobColumns = `j.id, j.kind, j.lane, j.priority, j.state, j.owner, j.title, j.attempt, j.max_attempts,
  j.idempotent, j.params, j.progress, j.result, j.error, j.created_at, j.started_at, j.finished_at,
  (SELECT json_group_array(d.after_id ORDER BY d.rowid) FROM job_deps d WHERE d.job_id = j.id)`

func scanJob(row []any) (jobs.Job, error) {
	j := jobs.Job{
		ID: asString(row[0]),
		Spec: jobs.Spec{
			Kind:        asString(row[1]),
			Lane:        jobs.Lane(asString(row[2])),
			Priority:    jobs.Priority(asInt(row[3])),
			Owner:       asString(row[5]),
			Title:       asString(row[6]),
			MaxAttempts: int(asInt(row[8])),
			Idempotent:  asInt(row[9]) != 0,
		},
		State:    jobs.State(asString(row[4])),
		Attempt:  int(asInt(row[7])),
		Created:  fromMS(row[14]),
		Started:  fromMS(row[15]),
		Finished: fromMS(row[16]),
	}
	if p := asBytes(row[10]); len(p) > 0 && string(p) != "null" {
		j.Params = json.RawMessage(p)
	}
	if p := asBytes(row[11]); len(p) > 0 {
		if err := json.Unmarshal(p, &j.Progress); err != nil {
			return j, fmt.Errorf("job %s: progress: %w", j.ID, err)
		}
	}
	if p := asBytes(row[12]); len(p) > 0 {
		j.Result = json.RawMessage(p)
	}
	if p := asBytes(row[13]); len(p) > 0 {
		j.Err = new(jobs.Error)
		if err := json.Unmarshal(p, j.Err); err != nil {
			return j, fmt.Errorf("job %s: error: %w", j.ID, err)
		}
	}
	var after []string
	if err := json.Unmarshal(asBytes(row[17]), &after); err != nil {
		return j, fmt.Errorf("job %s: deps: %w", j.ID, err)
	}
	if len(after) > 0 {
		j.After = after
	}
	return j, nil
}

// LoadJobs returns the jobs matching f, oldest first. Owner "kind:" (ending
// in a colon) is a prefix; Since compares the creation time.
func (r *jobRepo) LoadJobs(ctx context.Context, f jobs.Filter) ([]jobs.Job, error) {
	var where []string
	var args []any
	states := f.States
	if states == nil {
		states = nonTerminal
	}
	if len(states) > 0 {
		marks := strings.TrimSuffix(strings.Repeat("?, ", len(states)), ", ")
		where = append(where, `j.state IN (`+marks+`)`)
		for _, s := range states {
			args = append(args, string(s))
		}
	}
	if f.Lane != "" {
		where, args = append(where, `j.lane = ?`), append(args, string(f.Lane))
	}
	if f.Owner != "" {
		if strings.HasSuffix(f.Owner, ":") {
			where, args = append(where, `substr(j.owner, 1, ?) = ?`), append(args, len(f.Owner), f.Owner)
		} else {
			where, args = append(where, `j.owner = ?`), append(args, f.Owner)
		}
	}
	if f.Kind != "" {
		where, args = append(where, `j.kind = ?`), append(args, f.Kind)
	}
	if !f.Since.IsZero() {
		where, args = append(where, `j.created_at >= ?`), append(args, f.Since.UnixMilli())
	}
	sql := `SELECT ` + jobColumns + ` FROM jobs j`
	if len(where) > 0 {
		sql += ` WHERE ` + strings.Join(where, ` AND `)
	}
	sql += ` ORDER BY j.created_at, j.id`
	sql, args = page(sql, args, f.Limit, 0)
	rows, err := r.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, wrap("jobs", err)
	}
	out := make([]jobs.Job, 0, len(rows.Values))
	for _, row := range rows.Values {
		j, err := scanJob(row)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, nil
}

// LoadJob reads one job by id, whatever its state.
func (r *jobRepo) LoadJob(ctx context.Context, id string) (jobs.Job, bool, error) {
	rows, err := r.db.Query(ctx, `SELECT `+jobColumns+` FROM jobs j WHERE j.id = ?`, id)
	if err != nil {
		return jobs.Job{}, false, wrap("jobs", err)
	}
	if len(rows.Values) == 0 {
		return jobs.Job{}, false, nil
	}
	j, err := scanJob(rows.Values[0])
	if err != nil {
		return jobs.Job{}, false, err
	}
	return j, true, nil
}

// DeleteJobs removes terminal jobs that finished before t, with their
// dependencies and events.
func (r *jobRepo) DeleteJobs(ctx context.Context, finishedBefore time.Time) error {
	_, err := r.db.Execute(ctx, `DELETE FROM jobs WHERE state IN (?, ?, ?) AND finished_at IS NOT NULL AND finished_at < ?`,
		string(jobs.StateSucceeded), string(jobs.StateFailed), string(jobs.StateCancelled), finishedBefore.UnixMilli())
	return wrap("jobs", err)
}

// JobEvent is one logged state change of a job (diagnostics).
type JobEvent struct {
	At     time.Time
	State  jobs.State
	Detail string // the error code of a failure
}

// JobEvents returns a job's state changes, oldest first. Not part of
// jobs.Store: for the diagnostics page and tests.
func (s *Store) JobEvents(ctx context.Context, id string) ([]JobEvent, error) {
	rows, err := s.db.Query(ctx, `SELECT at, state, detail FROM job_events WHERE job_id = ? ORDER BY at, rowid`, id)
	if err != nil {
		return nil, wrap("job events", err)
	}
	out := make([]JobEvent, 0, len(rows.Values))
	for _, row := range rows.Values {
		out = append(out, JobEvent{At: fromMS(row[0]), State: jobs.State(asString(row[1])), Detail: asString(row[2])})
	}
	return out, nil
}
