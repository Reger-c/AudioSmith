package engine

// The ActNow family's memory coordination (family spec §11.1), from the
// supervisor's side: the app hands it a Family (internal/family Runtime;
// this package does not know actnowkit). The family's budget caps the
// models of all its apps together; before a job loads a model, idle
// models are unloaded to make room for it, then the other apps are asked
// to unload theirs, and the model loads anyway if that did not help.
// Another app's request unloads the models idle for a minute.

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/hehehai/AudioSmith/internal/engine/proto"
)

// Family is the ActNow family's memory coordination (Config.Family).
// Methods may read and write small files; the supervisor calls them
// without its lock held.
type Family interface {
	// Budget is the bytes this app's models may take: at most own (the
	// setting; 0 = none), and the family's share of physical memory (the
	// engine's report; 0 = unknown) less what the other running family
	// apps have loaded.
	Budget(own, physical int64) int64
	// Publish records the models the workers hold now, by role, and the
	// budget (the app's runtime file).
	Publish(loaded map[proto.Role][]proto.LoadedModel, budget int64)
	// RequestMemory asks the other family apps to unload their idle
	// models and waits a moment for them to do so.
	RequestMemory(ctx context.Context)
	// Requested receives the other apps' requests for memory.
	Requested() <-chan struct{}
	// UnloadIdleAfter is how long a model must have been idle to be
	// unloaded on another app's request (a minute).
	UnloadIdleAfter() time.Duration
}

// watchFamily unloads the models idle for UnloadIdleAfter whenever
// another family app asks for memory. It runs for the life of the
// supervisor.
func (s *supervisor) watchFamily(fam Family) {
	for range fam.Requested() {
		s.mu.Lock()
		stopped := s.stopped
		s.mu.Unlock()
		if !stopped {
			s.unloadIdleFor(fam.UnloadIdleAfter())
		}
	}
}

// unloadIdleFor unloads the models idle for at least d, by the workers'
// stats asked for now: a model a job uses is busy, never unloaded.
func (s *supervisor) unloadIdleFor(d time.Duration) {
	s.refreshStats()
	_, cands := s.resident()
	unloaded := false
	for _, c := range cands {
		if c.m.IdleSeconds >= d.Seconds() && s.unload(c, "on another ActNow app's request for memory") {
			unloaded = true
		}
	}
	if unloaded {
		s.publishFamily()
	}
}

// refreshStats asks the ready workers for engine.stats now.
func (s *supervisor) refreshStats() {
	s.mu.Lock()
	procs := map[proto.Role]*proc{}
	for _, r := range s.roles {
		if w := s.workers[r]; w.status.State == WorkerReady && w.proc != nil {
			procs[r] = w.proc
		}
	}
	s.mu.Unlock()
	for r, p := range procs {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		raw, err := p.call(ctx, "", proto.MethodStats, nil, nil, false)
		cancel()
		var stats proto.StatsResult
		if err != nil || json.Unmarshal(raw, &stats) != nil {
			continue
		}
		s.mu.Lock()
		if w := s.workers[r]; w.proc == p {
			w.status.Stats = &stats
		}
		s.mu.Unlock()
	}
}

// publishFamily writes what the workers hold to the family (nothing
// without one).
func (s *supervisor) publishFamily() {
	s.mu.Lock()
	fam := s.cfg.Family
	loaded := map[proto.Role][]proto.LoadedModel{}
	for _, r := range s.roles {
		if w := s.workers[r]; w.status.State == WorkerReady && w.status.Stats != nil && len(w.status.Stats.Models) > 0 {
			loaded[r] = slices.Clone(w.status.Stats.Models)
		}
	}
	s.mu.Unlock()
	if fam == nil {
		return
	}
	fam.Publish(loaded, s.budget())
}

// makeRoom runs before a job that names a model the worker of role has
// not loaded (spec §11.1): while the resident models and the new one's
// weights would exceed the budget, idle models are unloaded, the least
// recently used first; when that is not enough, the other family apps
// are asked for memory once, and then the model loads anyway (the user
// is not blocked).
func (s *supervisor) makeRoom(ctx context.Context, role proto.Role, params any) {
	model, path := modelOf(params)
	if model == "" {
		return
	}
	s.mu.Lock()
	w := s.workers[role]
	loaded := w != nil && w.status.Stats != nil &&
		slices.ContainsFunc(w.status.Stats.Models, func(m proto.LoadedModel) bool { return m.Model == model })
	fam := s.cfg.Family
	s.mu.Unlock()
	if loaded {
		return
	}
	need := weightsBytes(path)
	for asked := false; ; asked = true {
		budget := s.budget()
		if budget <= 0 {
			return
		}
		total, cands := s.resident()
		unloaded := false
		for _, c := range cands {
			if total+need <= budget {
				break
			}
			if s.unload(c, "to make room for "+model) {
				total -= c.m.Bytes
				unloaded = true
			}
		}
		if unloaded {
			s.publishFamily()
		}
		if total+need <= budget {
			return
		}
		if asked || fam == nil || ctx.Err() != nil {
			s.cfg.Logf("engine %s: loading %s although the models would take %d MB of a %d MB budget", role, model, (total+need)>>20, budget>>20)
			return
		}
		s.cfg.Logf("engine %s: asking the other ActNow apps for memory for %s", role, model)
		fam.RequestMemory(ctx)
	}
}

// modelOf reads the model and its local directory from a job's params.
func modelOf(params any) (model, path string) {
	var raw []byte
	switch p := params.(type) {
	case nil:
		return "", ""
	case json.RawMessage:
		raw = p
	case []byte:
		raw = p
	default:
		var err error
		if raw, err = json.Marshal(p); err != nil {
			return "", ""
		}
	}
	var head struct {
		Model     string `json:"model"`
		Path      string `json:"path"`
		ModelPath string `json:"modelPath"`
	}
	if json.Unmarshal(raw, &head) != nil {
		return "", ""
	}
	path = head.ModelPath
	if path == "" {
		path = head.Path
	}
	return head.Model, path
}

// weightsBytes estimates the memory a model takes from the size of its
// files (the weights, mostly): 0 when unknown.
func weightsBytes(dir string) int64 {
	if dir == "" {
		return 0
	}
	var n int64
	seen := 0
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if seen++; seen > 5000 {
			return filepath.SkipAll
		}
		if d.IsDir() {
			return nil
		}
		// Hugging Face snapshots link their files to the blobs.
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			n += fi.Size()
		}
		return nil
	})
	return n
}
