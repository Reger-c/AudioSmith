package jobs

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"
)

// MemStore is a Store in memory, for tests and for running without a
// database. Goroutine-safe; it keeps copies.
type MemStore struct {
	mu   sync.Mutex
	jobs map[string]Job
	// Saves counts SaveJob calls (tests).
	saves int
}

var (
	_ Store     = (*MemStore)(nil)
	_ JobLoader = (*MemStore)(nil)
)

// NewMemStore returns an empty store.
func NewMemStore() *MemStore { return &MemStore{jobs: map[string]Job{}} }

func (m *MemStore) SaveJob(ctx context.Context, j Job) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobs[j.ID] = cloneJob(j)
	m.saves++
	return nil
}

// LoadJobs returns the jobs f selects, oldest first, as the SQLite store
// does: nil States selects every non-terminal state (interrupted too).
func (m *MemStore) LoadJobs(ctx context.Context, f Filter) ([]Job, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	var out []Job
	for _, j := range m.jobs {
		if f.Match(j) {
			out = append(out, cloneJob(j))
		}
	}
	m.mu.Unlock()
	sortJobs(out)
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

func (m *MemStore) LoadJob(ctx context.Context, id string) (Job, bool, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return Job{}, false, nil
	}
	return cloneJob(j), true, nil
}

func (m *MemStore) DeleteJobs(ctx context.Context, finishedBefore time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, j := range m.jobs {
		if j.State.Terminal() && !j.Finished.IsZero() && j.Finished.Before(finishedBefore) {
			delete(m.jobs, id)
		}
	}
	return nil
}

// Saves returns how many times SaveJob was called.
func (m *MemStore) Saves() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saves
}

// nonTerminal are the states a zero Filter selects.
var nonTerminal = []State{StateWaiting, StateQueued, StateRunning, StateInterrupted}

// Match reports whether f selects j (the rules Store.LoadJobs follows):
// nil States means every non-terminal state, Owner ending in ":" is a
// prefix, Since compares the creation time. Limit is not applied.
func (f Filter) Match(j Job) bool {
	states := f.States
	if states == nil {
		states = nonTerminal
	}
	if !slices.Contains(states, j.State) {
		return false
	}
	if f.Lane != "" && j.Lane != f.Lane {
		return false
	}
	if f.Owner != "" {
		if strings.HasSuffix(f.Owner, ":") {
			if !strings.HasPrefix(j.Owner, f.Owner) {
				return false
			}
		} else if j.Owner != f.Owner {
			return false
		}
	}
	if f.Kind != "" && j.Kind != f.Kind {
		return false
	}
	if !f.Since.IsZero() && j.Created.Before(f.Since) {
		return false
	}
	return true
}

// sortJobs orders jobs oldest first, then by id.
func sortJobs(js []Job) {
	slices.SortFunc(js, func(a, b Job) int {
		if c := a.Created.Compare(b.Created); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
}

// cloneJob copies j's slices so the copy can be handed out.
func cloneJob(j Job) Job {
	j.After = slices.Clone(j.After)
	j.Params = slices.Clone(j.Params)
	j.Result = slices.Clone(j.Result)
	if j.Err != nil {
		e := *j.Err
		e.Data = slices.Clone(e.Data)
		j.Err = &e
	}
	return j
}
