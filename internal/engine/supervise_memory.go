package engine

// Memory: polling engine.stats, the memory budget, the cache directory,
// idle unloading and evicting resident models over the budget.

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"time"

	"github.com/hehehai/AudioSmith/internal/engine/proto"
)

// pollStats polls engine.stats while jobs run (and once after each),
// publishing changes and keeping resident models within the budget.
func (s *supervisor) pollStats(w *worker, p *proc) {
	tick := time.NewTicker(s.cfg.StatsEvery)
	defer tick.Stop()
	var lastIdlePoll time.Time
	for {
		select {
		case <-p.exited:
			return
		case <-tick.C:
			if p.jobCount() == 0 {
				// Idle: poll now and then only to unload idle models.
				if !s.idlePollDue(w, lastIdlePoll) {
					continue
				}
				lastIdlePoll = time.Now()
			}
		case <-p.pollNow:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		raw, err := p.call(ctx, "", proto.MethodStats, nil, nil, false)
		cancel()
		if err != nil {
			continue
		}
		var stats proto.StatsResult
		if json.Unmarshal(raw, &stats) != nil {
			continue
		}
		s.mu.Lock()
		if w.proc != p {
			s.mu.Unlock()
			return
		}
		changed := w.status.Stats == nil || !sameModels(w.status.Stats.Models, stats.Models)
		w.status.Stats = &stats
		st := s.statusLocked(w)
		s.mu.Unlock()
		if changed {
			s.publish(st)
		}
		s.evict()
	}
}

// idlePollDue reports whether an idle worker's stats should be polled to
// find models idle for longer than the idle-unload limit.
func (s *supervisor) idlePollDue(w *worker, last time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	idle := s.cfg.IdleUnload
	if idle <= 0 || w.status.Stats == nil || len(w.status.Stats.Models) == 0 {
		return false
	}
	every := min(max(idle/4, time.Second), 30*time.Second)
	return time.Since(last) >= every
}

// SetMemoryBudget changes Config.MemoryBudget and applies it at once.
func (s *supervisor) SetMemoryBudget(bytes int64) {
	s.mu.Lock()
	s.cfg.MemoryBudget = max(bytes, 0)
	s.mu.Unlock()
	go s.evict()
}

// SetCacheDir changes Config.CacheDir for the workers started next.
func (s *supervisor) SetCacheDir(dir string) {
	s.mu.Lock()
	s.cfg.CacheDir = dir
	s.mu.Unlock()
}

// SetIdleUnload changes Config.IdleUnload; it applies at the next poll.
func (s *supervisor) SetIdleUnload(d time.Duration) {
	s.mu.Lock()
	s.cfg.IdleUnload = max(d, 0)
	s.mu.Unlock()
	go s.evict()
}

func sameModels(a, b []proto.LoadedModel) bool {
	return slices.EqualFunc(a, b, func(x, y proto.LoadedModel) bool { return x.Model == y.Model })
}

// budget is the bytes resident models may take together.
func (s *supervisor) budgetLocked() int64 {
	if s.cfg.MemoryBudget > 0 {
		return s.cfg.MemoryBudget
	}
	for _, w := range s.workers {
		if w.status.Hello != nil && w.status.Hello.Device.Memory > 0 {
			return w.status.Hello.Device.Memory * 6 / 10
		}
	}
	return 0
}

// evict unloads the least recently used idle models while the resident
// models of all workers are over the budget, and every model idle for
// longer than IdleUnload.
func (s *supervisor) evict() {
	type candidate struct {
		role proto.Role
		m    proto.LoadedModel
	}
	s.mu.Lock()
	budget := s.budgetLocked()
	idle := s.cfg.IdleUnload.Seconds()
	var total int64
	var cands []candidate
	for _, w := range s.workers {
		if w.status.State != WorkerReady || w.status.Stats == nil {
			continue
		}
		for _, m := range w.status.Stats.Models {
			total += m.Bytes
			if m.IdleSeconds > 0 {
				cands = append(cands, candidate{w.role, m})
			}
		}
	}
	s.mu.Unlock()
	overBudget := budget > 0 && total > budget
	if !overBudget && idle <= 0 {
		return
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].m.IdleSeconds > cands[j].m.IdleSeconds })
	for _, c := range cands {
		tooIdle := idle > 0 && c.m.IdleSeconds >= idle
		if !tooIdle && (budget <= 0 || total <= budget) {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := s.Call(ctx, c.role, "", proto.MethodModelsUnload, proto.UnloadParams{Model: c.m.Model}, nil)
		cancel()
		if err != nil {
			s.cfg.Logf("engine %s: unloading %s: %v", c.role, c.m.Model, err)
			continue
		}
		why := "to stay within the memory budget"
		if tooIdle {
			why = "after the idle limit"
		}
		s.cfg.Logf("engine %s: unloaded %s (idle %.0f s) %s", c.role, c.m.Model, c.m.IdleSeconds, why)
		total -= c.m.Bytes
		s.mu.Lock()
		if w := s.workers[c.role]; w.status.Stats != nil {
			st := *w.status.Stats
			st.Models = slices.DeleteFunc(slices.Clone(st.Models), func(m proto.LoadedModel) bool { return m.Model == c.m.Model })
			w.status.Stats = &st
		}
		st := s.statusLocked(s.workers[c.role])
		s.mu.Unlock()
		s.publish(st)
	}
}
