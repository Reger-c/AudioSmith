package family

// The family's shared general settings (spec §9, §15.2 steps 4–5): the
// interface language, the proxy, the Hugging Face download source and the
// model folder, in $FAMILY/settings.json, and AudioSmith's own copy of
// them in its settings (General.Language, Models.Proxy,
// Models.Endpoints, Models.CacheDir).
//
// AudioSmith keeps reading its own settings everywhere: Shared writes the
// family's values into them for the keys that follow the family (Adopt),
// so the existing appliers run as for any change (the language, the
// downloader, the model folder's Store and the manifest). A change the
// user makes in AudioSmith reaches the family file while the key follows
// (Changed), and stays AudioSmith's own otherwise. The first start with
// the spec merges both (Migrate, settings.Migrate of actnowkit).
//
// Shared works one call at a time (its own lock); the app gives it its
// settings changes and the family's notifications in order (Go).

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	ass "github.com/hehehai/AudioSmith/internal/settings"
	"github.com/hehehai/actnowkit/models"
	famset "github.com/hehehai/actnowkit/settings"
)

// OwnSettings is AudioSmith's settings service, as Shared uses it.
type OwnSettings interface {
	Get() ass.Settings
	Update(ctx context.Context, fn func(*ass.Settings)) error
}

// SharedOptions configure NewShared.
type SharedOptions struct {
	Own OwnSettings
	// DefaultHub is the model folder of the default rules (hub.CacheDir:
	// $HF_HUB_CACHE, $HF_HOME/hub, ~/.cache/huggingface/hub).
	DefaultHub func() string
	// HasModels reports whether AudioSmith has models in hub: then the
	// default folder is "in use" at the migration (spec §9.4), and a new
	// family folder is not followed silently (the Models page offers to
	// move them instead).
	HasModels func(hub string) bool
}

// Shared keeps AudioSmith's shared settings in step with the family's.
type Shared struct {
	f *Family
	o SharedOptions

	run sync.Mutex // one call at a time

	mu       sync.Mutex
	fam      famset.Settings   // settings.json as last read
	by       map[string]string // key → bundle id of the app that set its value, as far as seen
	read     bool
	readOnly bool
	// applied are the values Adopt wrote into AudioSmith's settings, until
	// Changed saw them (they are the family's: not written back).
	applied map[string]string

	qmu   sync.Mutex
	queue []func()
	busy  bool
	idle  *sync.Cond
}

// NewShared makes the synchroniser of f; it reads nothing yet.
func NewShared(f *Family, o SharedOptions) *Shared {
	s := &Shared{f: f, o: o, applied: map[string]string{}, by: map[string]string{}}
	s.idle = sync.NewCond(&s.qmu)
	if s.o.HasModels == nil {
		s.o.HasModels = func(string) bool { return false }
	}
	return s
}

// Defaults of the shared keys in the family file's form.
func defaultValue(key string) string {
	if key == famset.KeyHFEndpoints {
		return famset.EndpointsAuto
	}
	return ""
}

// OwnValue is AudioSmith's value of a shared key, in the family file's
// form.
func OwnValue(s ass.Settings, key string) string {
	switch key {
	case famset.KeyLanguage:
		return s.General.Language
	case famset.KeyProxy:
		return s.Models.Proxy
	case famset.KeyHFEndpoints:
		return EndpointsValue(s.Models.Endpoints)
	case famset.KeyModelsRoot:
		if s.Models.CacheDir == "" {
			return ""
		}
		return filepath.Clean(s.Models.CacheDir)
	}
	return ""
}

// SetOwnValue writes a family value of key into AudioSmith's settings.
func SetOwnValue(s *ass.Settings, key, value string) {
	switch key {
	case famset.KeyLanguage:
		s.General.Language = value
	case famset.KeyProxy:
		s.Models.Proxy = value
	case famset.KeyHFEndpoints:
		s.Models.Endpoints = EndpointsList(value)
	case famset.KeyModelsRoot:
		s.Models.CacheDir = value
	}
}

// EndpointsValue is AudioSmith's download source (settings.Models.
// Endpoints) as network.hfEndpoints: none = "auto" (the race of the
// official hub and its mirror), the official hub alone "official", the
// mirror alone "mirror", other lists their URLs joined by spaces.
func EndpointsValue(eps []string) string {
	var out []string
	for _, e := range eps {
		if e = strings.TrimRight(strings.TrimSpace(e), "/"); e != "" && !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	switch {
	case len(out) == 0:
		return famset.EndpointsAuto
	case len(out) == 1 && out[0] == famset.OfficialEndpoint:
		return famset.EndpointsOfficial
	case len(out) == 1 && out[0] == famset.MirrorEndpoint:
		return famset.EndpointsMirror
	}
	return strings.Join(out, " ")
}

// EndpointsList is the reverse: "auto" (or "") is AudioSmith's default
// (nil: the downloader races both), the others the endpoints they name
// (settings.Endpoints).
func EndpointsList(value string) []string {
	switch strings.TrimSpace(value) {
	case "", famset.EndpointsAuto:
		return nil
	}
	return famset.Endpoints(value)
}

// sameValue compares two values of key in the family file's form.
func sameValue(key, a, b string) bool {
	switch key {
	case famset.KeyHFEndpoints:
		return slices.Equal(famset.Endpoints(a), famset.Endpoints(b))
	case famset.KeyModelsRoot:
		return a == b || a != "" && b != "" && filepath.Clean(a) == filepath.Clean(b)
	}
	return a == b
}

// usable reports whether the family directory may be written.
func (s *Shared) usable() bool {
	s.f.mu.Lock()
	st := s.f.state
	s.f.mu.Unlock()
	return st != off
}

// reread reads settings.json, keeping it for View.
func (s *Shared) reread() (famset.Settings, bool, error) {
	fam, ro, err := famset.Read(s.f.Dir)
	if err != nil {
		return fam, ro, err
	}
	s.mu.Lock()
	s.keep(fam)
	s.readOnly = ro
	s.mu.Unlock()
	return fam, ro, nil
}

// keep takes fam as the last read, noting who set each value that
// changed since (settings.json names only its last writer: a value seen
// first is credited to it); the caller holds mu.
func (s *Shared) keep(fam famset.Settings) {
	for _, k := range famset.Keys {
		nv, nset := fam.Get(k)
		pv, pset := s.fam.Get(k)
		if !nset {
			delete(s.by, k)
		} else if !s.read || !pset || nv != pv {
			s.by[k] = fam.UpdatedBy
		}
	}
	s.fam, s.read = fam, true
}

// Start merges AudioSmith's settings with the family's on the first start
// with the spec (Migrate), then adopts the family's values. Family.Start
// runs first; a read-only directory (a newer spec's) is left alone.
func (s *Shared) Start(ctx context.Context) error {
	s.run.Lock()
	defer s.run.Unlock()
	if !s.usable() {
		return ErrReadOnly
	}
	if _, ro, err := s.reread(); err != nil {
		return err
	} else if ro {
		return ErrReadOnly
	}
	if err := s.migrate(ctx); err != nil {
		return err
	}
	return s.adopt(ctx)
}

// migrate is spec §9.4 (once: settings.Family.SharedMigrated).
func (s *Shared) migrate(ctx context.Context) error {
	own := s.o.Own.Get()
	if own.Family.SharedMigrated {
		return nil
	}
	local := map[string]famset.Local{}
	for _, k := range famset.Keys {
		v := OwnValue(own, k)
		l := famset.Local{Value: v, IsDefault: sameValue(k, v, defaultValue(k))}
		if k == famset.KeyModelsRoot && l.IsDefault && s.o.DefaultHub != nil {
			l.InUse = s.o.HasModels(s.o.DefaultHub())
		}
		local[k] = l
	}
	out, err := famset.Migrate(ctx, s.f.Dir, s.f.BundleID, local)
	if err != nil {
		return err
	}
	for _, o := range out {
		if o.Wrote || o.Conflict {
			s.f.logf("family: settings %s: wrote %v, conflict %v (family %q)", o.Key, o.Wrote, o.Conflict, o.Family)
		}
	}
	return s.o.Own.Update(ctx, func(st *ass.Settings) {
		for _, o := range out {
			st.Family.SetFollows(o.Key, o.Follow)
			if o.Conflict {
				if st.Family.OtherValue == nil {
					st.Family.OtherValue = map[string]bool{}
				}
				st.Family.OtherValue[o.Key] = true
			}
		}
		st.Family.SharedMigrated = true
	})
}

// Adopt rereads settings.json (after its notification, or when the app
// becomes active) and writes the family's values of the keys AudioSmith
// follows into its settings.
func (s *Shared) Adopt(ctx context.Context) error {
	s.run.Lock()
	defer s.run.Unlock()
	if !s.usable() {
		return nil
	}
	if _, ro, err := s.reread(); err != nil || ro {
		return err
	}
	return s.adopt(ctx)
}

// rootDir is the folder a models.root value means.
func (s *Shared) rootDir(v string) string {
	if v == "" && s.o.DefaultHub != nil {
		return s.o.DefaultHub()
	}
	return v
}

// adopt applies s.fam; the caller holds run.
func (s *Shared) adopt(ctx context.Context) error {
	own := s.o.Own.Get()
	if !own.Family.SharedMigrated {
		return nil
	}
	s.mu.Lock()
	fam := s.fam
	s.mu.Unlock()
	changes := map[string]string{}
	keepRoot := false
	var errs []error
	for _, k := range famset.Keys {
		if !own.Family.Follows(k) {
			continue
		}
		ov := OwnValue(own, k)
		fv, set := fam.Get(k)
		if !set {
			// Nobody set it (the family file is new, or was lost): a value
			// the user chose becomes the family's, as at the migration.
			if !sameValue(k, ov, defaultValue(k)) {
				if fam, err := famset.Set(ctx, s.f.Dir, s.f.BundleID, k, ov); err != nil {
					errs = append(errs, err)
				} else {
					s.mu.Lock()
					s.keep(fam)
					s.mu.Unlock()
				}
			}
			continue
		}
		if sameValue(k, fv, ov) {
			continue
		}
		if k == famset.KeyModelsRoot {
			cur, next := s.rootDir(ov), s.rootDir(fv)
			if cur == next || models.SameHub(cur, next) {
				continue
			}
			if s.o.HasModels(cur) {
				// AudioSmith's models are where it is: it stays there and
				// offers to move them (spec §15.2 step 5).
				keepRoot = true
				continue
			}
		}
		changes[k] = fv
	}
	if len(changes) == 0 && !keepRoot {
		return errors.Join(errs...)
	}
	s.mu.Lock()
	for k, v := range changes {
		s.applied[k] = v
	}
	s.mu.Unlock()
	if keepRoot {
		s.f.logf("family: the family's model folder changed; AudioSmith keeps its models where they are and offers to move them")
	}
	err := s.o.Own.Update(ctx, func(st *ass.Settings) {
		for k, v := range changes {
			SetOwnValue(st, k, v)
		}
		if keepRoot {
			st.Family.SetFollows(famset.KeyModelsRoot, false)
			if st.Family.OtherValue == nil {
				st.Family.OtherValue = map[string]bool{}
			}
			st.Family.OtherValue[famset.KeyModelsRoot] = true
		}
	})
	return errors.Join(append(errs, err)...)
}

// Changed takes a change of AudioSmith's settings (settings.Changed): a
// shared value the user changed is written to the family file while it
// follows the family; a key that follows again ("Use theirs", "Follow
// again") takes the family's value.
func (s *Shared) Changed(ctx context.Context, old, new ass.Settings) error {
	s.run.Lock()
	defer s.run.Unlock()
	if !new.Family.SharedMigrated || !s.usable() {
		return nil
	}
	s.mu.Lock()
	ro, read := s.readOnly, s.read
	s.mu.Unlock()
	if ro || !read {
		return nil
	}
	again := false
	var errs []error
	for _, k := range famset.Keys {
		was, is := old.Family.Follows(k), new.Family.Follows(k)
		ov, nv := OwnValue(old, k), OwnValue(new, k)
		switch {
		case !is:
		case !was:
			again = true
		case !sameValue(k, ov, nv):
			s.mu.Lock()
			a, ok := s.applied[k]
			delete(s.applied, k)
			s.mu.Unlock()
			if ok && sameValue(k, a, nv) {
				continue // the family's own value, adopted
			}
			fam, err := famset.Set(ctx, s.f.Dir, s.f.BundleID, k, nv)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			s.mu.Lock()
			s.keep(fam)
			s.mu.Unlock()
		}
	}
	if again {
		if _, _, err := s.reread(); err != nil {
			errs = append(errs, err)
		} else if err := s.adopt(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// SharedView is what the settings rows show of the family's settings
// (cached: no file is read).
type SharedView struct {
	// Values are the family's values that are set, by key.
	Values map[string]string
	// By names the app that set a key's value (as far as AudioSmith saw
	// the file change; at first the file's last writer, updatedBy) when
	// the UI may name it: a release build of another family app. A key
	// without one says "Other ActNow apps" (spec §7.10's one exception).
	By       map[string]string
	ReadOnly bool
}

// View is the family's settings as last read.
func (s *Shared) View() SharedView {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := SharedView{Values: map[string]string{}, By: map[string]string{}, ReadOnly: s.readOnly}
	for _, k := range famset.Keys {
		if x, ok := s.fam.Get(k); ok {
			v.Values[k] = x
		}
		if bid := s.by[k]; bid != "" && bid != s.f.BundleID {
			if names := VisibleNames(models.User{BundleID: bid}); len(names) > 0 {
				v.By[k] = names[0]
			}
		}
	}
	return v
}

// Conflict reports whether AudioSmith's value of key differs from the
// family's while it keeps its own (does not follow): what "Other ActNow
// apps use a different …" is about.
func (v SharedView) Conflict(own ass.Settings, key string) (family string, ok bool) {
	fv, set := v.Values[key]
	if !set || own.Family.Follows(key) {
		return "", false
	}
	return fv, !sameValue(key, fv, OwnValue(own, key))
}

// Effective is the value AudioSmith uses for key now, in the family
// file's form.
func (s *Shared) Effective(key string) string { return OwnValue(s.o.Own.Get(), key) }

// Go runs fn after the work given before it, on a goroutine of its own:
// the app's settings changes and the family's notifications reach Shared
// in order.
func (s *Shared) Go(fn func()) {
	s.qmu.Lock()
	s.queue = append(s.queue, fn)
	if s.busy {
		s.qmu.Unlock()
		return
	}
	s.busy = true
	s.qmu.Unlock()
	go func() {
		for {
			s.qmu.Lock()
			if len(s.queue) == 0 {
				s.busy = false
				s.idle.Broadcast()
				s.qmu.Unlock()
				return
			}
			next := s.queue[0]
			s.queue = s.queue[1:]
			s.qmu.Unlock()
			next()
		}
	}()
}

// Wait returns when the work given to Go is done (tests, quit).
func (s *Shared) Wait() {
	s.qmu.Lock()
	for s.busy {
		s.idle.Wait()
	}
	s.qmu.Unlock()
}
