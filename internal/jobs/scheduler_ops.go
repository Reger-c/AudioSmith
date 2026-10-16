package jobs

// Operations on jobs: Cancel, Retry, Get, List and Idle.

import (
	"context"
	"fmt"
	"slices"
	"time"
)

// Cancel cancels a job and the jobs waiting on it. A running job's context
// is cancelled; it becomes Cancelled when its executor returns.
func (s *Sched) Cancel(ctx context.Context, id string) error {
	s.mu.Lock()
	e, ok := s.jobs[id]
	if !ok {
		_, isDone := s.done[id]
		s.mu.Unlock()
		if isDone {
			return nil
		}
		if _, err := s.load(ctx, id); err != nil {
			return err
		}
		return nil // finished in an earlier run
	}
	defer s.mu.Unlock()
	s.cancelLocked(e)
	return nil
}

func (s *Sched) cancelLocked(e *entry) {
	switch e.job.State {
	case StateRunning:
		if e.cancel != nil {
			e.cancel(errUserCancel)
		}
		// Its dependents are cancelled now, not when it returns.
		for _, d := range s.dependents[e.job.ID] {
			if de, ok := s.jobs[d]; ok && !de.job.State.Terminal() {
				s.cancelLocked(de)
			}
		}
	case StateWaiting, StateQueued, StateInterrupted:
		s.finishLocked(e, StateCancelled, &Error{Code: CodeCancelled, Message: "Cancelled"})
		for _, d := range s.dependents[e.job.ID] {
			if de, ok := s.jobs[d]; ok && !de.job.State.Terminal() {
				s.cancelLocked(de)
			}
		}
	}
}

// Retry re-queues a failed, cancelled or interrupted job for a new attempt.
func (s *Sched) Retry(ctx context.Context, id string) error {
	s.mu.Lock()
	e, ok := s.jobs[id]
	s.mu.Unlock()
	if !ok {
		j, err := s.load(ctx, id)
		if err != nil {
			return err
		}
		s.mu.Lock()
		if e, ok = s.jobs[id]; !ok {
			e = &entry{job: j}
			s.jobs[id] = e
			for _, d := range j.After {
				s.dependents[d] = append(s.dependents[d], id)
			}
		}
		s.mu.Unlock()
	}
	s.mu.Lock()
	unknown := s.unknownDeps([]*entry{e})
	s.mu.Unlock()
	if len(unknown) > 0 {
		if err := s.lookupDone(ctx); err != nil {
			return err
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	switch e.job.State {
	case StateFailed, StateCancelled, StateInterrupted:
	default:
		return fmt.Errorf("%w: job %s is %s", ErrInvalid, id, e.job.State)
	}
	delete(s.done, id)
	if i := slices.Index(s.finished, id); i >= 0 {
		s.finished = slices.Delete(s.finished, i, i+1)
	}
	e.base = e.job.Attempt
	e.notBefore = time.Time{}
	e.job.Err, e.job.Result, e.job.Finished = nil, nil, time.Time{}
	e.job.Progress = Progress{Fraction: -1}
	e.job.State = StateQueued
	if len(e.job.After) > 0 {
		e.job.State = StateWaiting
	}
	s.out(e, nil, true)
	s.evaluate(e, true)
	s.poke()
	return nil
}

// load reads one job from the store (one not in memory).
func (s *Sched) load(ctx context.Context, id string) (Job, error) {
	all, err := s.opts.Store.LoadJobs(ctx, Filter{States: []State{
		StateWaiting, StateQueued, StateRunning, StateSucceeded, StateFailed, StateCancelled, StateInterrupted}})
	if err != nil {
		return Job{}, err
	}
	for _, j := range all {
		if j.ID == id {
			return j, nil
		}
	}
	return Job{}, fmt.Errorf("%w: %s", ErrNotFound, id)
}

func (s *Sched) Get(ctx context.Context, id string) (Job, error) {
	s.mu.Lock()
	if e, ok := s.jobs[id]; ok {
		j := cloneJob(e.job)
		s.mu.Unlock()
		return j, nil
	}
	s.mu.Unlock()
	return s.load(ctx, id)
}

// List returns the jobs f selects, oldest first: the store's, overlaid
// with the (newer) states in memory.
func (s *Sched) List(ctx context.Context, f Filter) ([]Job, error) {
	stored, err := s.opts.Store.LoadJobs(ctx, Filter{States: f.States, Lane: f.Lane, Owner: f.Owner, Kind: f.Kind, Since: f.Since})
	if err != nil {
		return nil, err
	}
	byID := make(map[string]int, len(stored))
	for i, j := range stored {
		byID[j.ID] = i
	}
	var out []Job
	s.mu.Lock()
	for _, j := range stored {
		if e, ok := s.jobs[j.ID]; ok {
			j = cloneJob(e.job)
		}
		if f.Match(j) {
			out = append(out, j)
		}
	}
	for id, e := range s.jobs {
		if _, ok := byID[id]; !ok && f.Match(e.job) {
			out = append(out, cloneJob(e.job))
		}
	}
	s.mu.Unlock()
	sortJobs(out)
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

func (s *Sched) Idle() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.holds) > 0 {
		return false
	}
	for _, e := range s.jobs {
		switch e.job.State {
		case StateWaiting, StateQueued, StateRunning:
			return false
		}
	}
	return true
}
