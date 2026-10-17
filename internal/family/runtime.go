package family

// The family's runtime file and memory budget (spec §11.1):
// runtime/<bundle id>.json says which models AudioSmith's engine holds
// (written when they change, removed at quit), and AudioSmith's budget
// shrinks by what the other running family apps hold. Runtime is the
// engine supervisor's engine.Family (internal/engine supervise_family.go).

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/hehehai/AudioSmith/internal/engine/proto"
	actnow "github.com/hehehai/actnowkit/family"
	famruntime "github.com/hehehai/actnowkit/runtime"
)

// RuntimeOptions configure Family.Runtime.
type RuntimeOptions struct {
	// Revision is the commit of a model's files in use (repo id → commit;
	// "" when unknown).
	Revision func(repo string) string
	// Wait bounds RequestMemory's wait for the other apps (0 = 2 s);
	// Poll is how often it rereads their files meanwhile (0 = 100 ms).
	Wait, Poll time.Duration
	// Fresh is how long the other apps' loaded bytes are trusted before
	// Budget reads their files again (0 = 2 s; a notification also
	// refreshes them).
	Fresh time.Duration
	// IdleAfter is UnloadIdleAfter (0 = a minute, the spec's).
	IdleAfter time.Duration
	// Loaded, when set, is told the repos the engine holds whenever they
	// change (on the supervisor's goroutine: it must not block), so that
	// AudioSmith claims a model it loads without claiming it (Reuse).
	Loaded func(repos []string)
}

// Runtime is AudioSmith's runtime file and its share of the family's
// memory. Safe from any goroutine; nothing is written while the family
// directory is not usable (before Start, or read only).
type Runtime struct {
	f    *Family
	o    RuntimeOptions
	req  chan struct{}
	mu   sync.Mutex
	file famruntime.File // what was last published (BundleID set when written)
	// written: the file exists; closed: Close removed it, nothing more is
	// written.
	written, closed, dirty bool
	others                 int64
	othersAt               time.Time
	asked                  time.Time // AudioSmith's own last request
}

// Runtime makes AudioSmith's runtime handle; one per Family.
func (f *Family) Runtime(o RuntimeOptions) *Runtime {
	if o.Wait <= 0 {
		o.Wait = 2 * time.Second
	}
	if o.Poll <= 0 {
		o.Poll = 100 * time.Millisecond
	}
	if o.Fresh <= 0 {
		o.Fresh = 2 * time.Second
	}
	if o.IdleAfter <= 0 {
		o.IdleAfter = famruntime.UnloadIdleAfter
	}
	return &Runtime{f: f, o: o, req: make(chan struct{}, 1)}
}

// usable: the family directory was set up and may be written.
func (r *Runtime) usable() bool {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	return r.f.state == ready || r.f.state == failed
}

// othersLoaded is what the other running family apps hold, reread when
// older than Fresh.
func (r *Runtime) othersLoaded() int64 {
	r.mu.Lock()
	if time.Since(r.othersAt) < r.o.Fresh {
		n := r.others
		r.mu.Unlock()
		return n
	}
	r.mu.Unlock()
	return r.refresh()
}

func (r *Runtime) refresh() int64 {
	n := famruntime.OthersLoadedBytes(r.f.Dir, r.f.BundleID)
	r.mu.Lock()
	r.others, r.othersAt = n, time.Now()
	r.mu.Unlock()
	return n
}

// Budget is famruntime.Budget(own, FamilyCap(physical), others): at most
// the setting, and 60 % of physical memory less what the other running
// family apps hold. physical 0 asks the system.
func (r *Runtime) Budget(own, physical int64) int64 {
	if physical <= 0 {
		physical = int64(famruntime.PhysicalMemory())
	}
	if physical <= 0 {
		return max(own, 0) // nothing known: the setting, or no limit
	}
	return famruntime.Budget(own, famruntime.FamilyCap(uint64(physical)), r.othersLoaded())
}

// Publish writes the runtime file when the models (or whether they are
// busy) changed. Nothing is written while no model was ever loaded.
func (r *Runtime) Publish(loaded map[proto.Role][]proto.LoadedModel, budget int64) {
	now := time.Now()
	var list []famruntime.Loaded
	for _, role := range []proto.Role{proto.RoleGPU, proto.RoleListen, proto.RoleMedia} {
		for _, m := range loaded[role] {
			l := famruntime.Loaded{Repo: m.Model, Worker: string(role), Bytes: m.Bytes}
			if r.o.Revision != nil {
				l.Revision = r.o.Revision(m.Model)
			}
			if m.IdleSeconds > 0 {
				at := now.Add(-time.Duration(m.IdleSeconds * float64(time.Second))).UTC().Truncate(time.Second)
				l.IdleSince = &at
			}
			list = append(list, l)
		}
	}
	r.mu.Lock()
	same := r.file.BudgetBytes == budget && slices.EqualFunc(r.file.Loaded, list, sameLoaded)
	if r.closed || (same && !r.dirty) || (!r.written && len(list) == 0 && !r.file.Pasting) {
		r.mu.Unlock()
		return
	}
	changed := !slices.EqualFunc(r.file.Loaded, list, func(a, b famruntime.Loaded) bool { return a.Repo == b.Repo })
	r.file.Loaded, r.file.BudgetBytes, r.dirty = list, budget, true
	r.mu.Unlock()
	r.flush()
	if changed && r.o.Loaded != nil && len(list) > 0 {
		repos := make([]string, len(list))
		for i, l := range list {
			repos[i] = l.Repo
		}
		r.o.Loaded(repos)
	}
}

// sameLoaded: the same model, as busy as before (idleSince moves on
// every poll; it is written when a model turns busy or idle).
func sameLoaded(a, b famruntime.Loaded) bool {
	return a.Repo == b.Repo && a.Revision == b.Revision && a.Worker == b.Worker && a.Bytes == b.Bytes &&
		(a.IdleSince == nil) == (b.IdleSince == nil)
}

// Flush writes what was published while the directory was not usable
// yet (Start had not finished); the app calls it after Start.
func (r *Runtime) Flush() { r.flush() }

func (r *Runtime) flush() {
	if !r.usable() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || !r.dirty {
		return
	}
	f := r.file
	f.BundleID = r.f.BundleID
	f.Loaded = slices.Clone(f.Loaded)
	if err := famruntime.Write(r.f.Dir, f); err != nil {
		r.f.logf("family: runtime file: %v", err)
		return
	}
	r.written, r.dirty = true, false
}

// RequestMemory asks the other apps to unload their idle models
// (TopicMemory) and waits until what they hold shrank, at most Wait.
func (r *Runtime) RequestMemory(ctx context.Context) {
	if !r.usable() {
		return
	}
	before := r.refresh()
	if before == 0 {
		return // nobody else holds anything
	}
	r.mu.Lock()
	r.asked = time.Now()
	r.mu.Unlock()
	if err := famruntime.RequestMemory(r.f.Dir); err != nil {
		r.f.logf("family: memory request: %v", err)
		return
	}
	deadline := time.Now().Add(r.o.Wait)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(r.o.Poll):
		}
		if r.refresh() < before {
			return
		}
	}
}

// Requested receives the other apps' memory requests (Handle).
func (r *Runtime) Requested() <-chan struct{} { return r.req }

// UnloadIdleAfter: a request unloads the models idle this long.
func (r *Runtime) UnloadIdleAfter() time.Duration { return r.o.IdleAfter }

// Handle takes the family notifications the app gets (Family.OnChange):
// another app's runtime file changed (its loaded bytes are reread), or
// it asks for memory (Requested; AudioSmith's own request, which comes
// back to it, is not).
func (r *Runtime) Handle(topics []string) {
	if slices.Contains(topics, actnow.TopicRuntime) {
		r.mu.Lock()
		r.othersAt = time.Time{}
		r.mu.Unlock()
	}
	if slices.Contains(topics, actnow.TopicMemory) {
		r.mu.Lock()
		own := !r.asked.IsZero() && time.Since(r.asked) < r.o.Wait+time.Second
		r.mu.Unlock()
		if own {
			return
		}
		select {
		case r.req <- struct{}{}:
		default:
		}
	}
}

// SetPasting says whether dictation is pasting (writing the clipboard,
// ⌘V, restoring it), so that the other voice-input apps wait (spec §11.1
// pasting). Before setting it, WaitPasting waits for theirs.
func (r *Runtime) SetPasting(on bool) {
	r.mu.Lock()
	if r.closed || r.file.Pasting == on {
		r.mu.Unlock()
		return
	}
	r.file.Pasting, r.dirty = on, true
	r.mu.Unlock()
	r.flush()
}

// WaitPasting waits while another app pastes, at most half a second; it
// reports whether the way is clear.
func (r *Runtime) WaitPasting(ctx context.Context) bool {
	if !r.usable() {
		return true
	}
	return famruntime.WaitPasting(ctx, r.f.Dir, r.f.BundleID)
}

// LoadedIn names the other running family apps the UI may name
// (installed release builds, spec §7.10) that hold repo loaded, for "Also
// loaded in Dicta" (any revision when revision is "").
func (r *Runtime) LoadedIn(repo, revision string) []string {
	files := famruntime.LoadedBy(r.f.Dir, r.f.BundleID, repo, revision)
	visible := visibleApps(r.f.Found())
	var out []string
	for _, file := range files {
		if name, ok := visible[file.BundleID]; ok && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// Close removes the runtime file (at quit, before the engines stop);
// nothing is written afterwards.
func (r *Runtime) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	written := r.written
	r.mu.Unlock()
	if !written {
		return
	}
	if err := famruntime.Remove(r.f.Dir, r.f.BundleID); err != nil {
		r.f.logf("family: runtime file: %v", err)
	}
}
