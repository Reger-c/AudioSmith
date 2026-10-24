package jobs

// Submitting jobs: one, or a graph of jobs that wait on each other.

import (
	"context"
	"fmt"
	"slices"
)

func (s *Sched) Submit(ctx context.Context, spec Spec) (string, error) {
	ids, err := s.SubmitGraph(ctx, []Node{{Key: "job", Spec: spec}})
	if err != nil {
		return "", err
	}
	return ids["job"], nil
}

// SubmitGraph validates the graph (unique keys, known references, no
// cycle), saves every job, then makes them all visible at once.
func (s *Sched) SubmitGraph(ctx context.Context, nodes []Node) (map[string]string, error) {
	if len(nodes) == 0 {
		return nil, fmt.Errorf("%w: empty graph", ErrInvalid)
	}
	keys := map[string]int{}
	for i, n := range nodes {
		if n.Key == "" {
			return nil, fmt.Errorf("%w: node %d has no key", ErrInvalid, i)
		}
		if _, dup := keys[n.Key]; dup {
			return nil, fmt.Errorf("%w: duplicate key %q", ErrInvalid, n.Key)
		}
		if n.Spec.Kind == "" || n.Spec.Lane == "" {
			return nil, fmt.Errorf("%w: node %q needs a kind and a lane", ErrInvalid, n.Key)
		}
		keys[n.Key] = i
	}
	order, err := topoOrder(nodes, keys)
	if err != nil {
		return nil, err
	}

	// External references: jobs in memory, or finished ones in the store.
	var external []string
	for _, n := range nodes {
		for _, a := range n.Spec.After {
			if _, ok := keys[a]; !ok {
				external = append(external, a)
			}
		}
	}
	s.mu.Lock()
	closed := s.closed
	var unknown []string
	for _, id := range external {
		if _, ok := s.jobs[id]; !ok {
			if _, ok := s.done[id]; !ok {
				unknown = append(unknown, id)
			}
		}
	}
	s.mu.Unlock()
	if closed {
		return nil, ErrClosed
	}
	if len(unknown) > 0 {
		if err := s.lookupDone(ctx, unknown); err != nil {
			return nil, err
		}
		s.mu.Lock()
		for _, id := range unknown {
			_, inMem := s.jobs[id]
			_, isDone := s.done[id]
			if !inMem && !isDone {
				s.mu.Unlock()
				return nil, fmt.Errorf("%w: dependency %s", ErrNotFound, id)
			}
		}
		s.mu.Unlock()
	}

	now := s.opts.Now()
	ids := make(map[string]string, len(nodes))
	for _, n := range nodes {
		ids[n.Key] = s.opts.NewID()
	}
	built := make([]Job, 0, len(nodes))
	for _, i := range order {
		n := nodes[i]
		j := Job{ID: ids[n.Key], Spec: n.Spec, State: StateQueued, Created: now, Progress: Progress{Fraction: -1}}
		j.After = nil
		for _, a := range n.Spec.After {
			if id, ok := ids[a]; ok {
				a = id
			}
			if !slices.Contains(j.After, a) {
				j.After = append(j.After, a)
			}
		}
		if len(j.After) > 0 {
			j.State = StateWaiting
		}
		j.Params = slices.Clone(j.Params)
		built = append(built, j)
	}
	for k, j := range built {
		if err := s.opts.Store.SaveJob(ctx, j); err != nil {
			// Undo what was saved: those jobs never become visible.
			for _, prev := range built[:k] {
				prev.State, prev.Finished = StateCancelled, now
				prev.Err = &Error{Code: CodeCancelled, Message: "submission failed"}
				if err := s.opts.Store.SaveJob(context.WithoutCancel(ctx), prev); err != nil {
					s.opts.Logf("jobs: %s: %v", prev.ID, err)
				}
			}
			return nil, fmt.Errorf("jobs: saving: %w", err)
		}
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	var es []*entry
	for _, j := range built {
		e := &entry{job: j}
		s.jobs[j.ID] = e
		for _, d := range j.After {
			s.dependents[d] = append(s.dependents[d], j.ID)
		}
		s.out(e, nil, false) // saved above
		es = append(es, e)
	}
	// Dependencies may have finished since the states were chosen.
	for _, e := range es {
		if e.job.State == StateWaiting {
			s.evaluate(e, true)
		}
	}
	s.mu.Unlock()
	s.poke()
	return ids, nil
}

// topoOrder returns node indexes with in-graph dependencies first.
func topoOrder(nodes []Node, keys map[string]int) ([]int, error) {
	state := make([]int, len(nodes)) // 0 new, 1 visiting, 2 done
	var order []int
	var visit func(i int) error
	visit = func(i int) error {
		switch state[i] {
		case 1:
			return fmt.Errorf("%w: cycle through %q", ErrInvalid, nodes[i].Key)
		case 2:
			return nil
		}
		state[i] = 1
		for _, a := range nodes[i].Spec.After {
			if j, ok := keys[a]; ok {
				if err := visit(j); err != nil {
					return err
				}
			}
		}
		state[i] = 2
		order = append(order, i)
		return nil
	}
	for i := range nodes {
		if err := visit(i); err != nil {
			return nil, err
		}
	}
	return order, nil
}
