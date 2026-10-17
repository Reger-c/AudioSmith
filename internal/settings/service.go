package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
)

// Key is the document the settings are stored under (store.Documents).
const Key = "settings:app"

// Documents is where a Store keeps its document. store.Documents satisfies
// it (thread A, SQLite); until that lands DirDocuments keeps it in a JSON
// file.
type Documents interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Put(ctx context.Context, key string, doc []byte) error
}

// Scales are the UI scales Appearance offers (90, 100, 110 and 125 %).
var Scales = []float64{0.9, 1, 1.1, 1.25}

// Themes are the values of Appearance.Theme.
var Themes = []string{"system", "light", "dark"}

// Store implements Service over Documents.
type Store struct {
	docs    Documents
	publish func(Changed)

	update sync.Mutex // serializes Update: read, save, swap
	mu     sync.RWMutex
	cur    Settings
}

var _ Service = (*Store)(nil)

// Open loads the settings document from docs, migrating it, and returns a
// Store that saves there. publish (may be nil) receives Changed after each
// saved update; main wires it to the core.Bus. A document that does not
// decode is replaced by the defaults at the next update: Open returns the
// Store with defaults and the error, for the log.
func Open(ctx context.Context, docs Documents, publish func(Changed)) (*Store, error) {
	s := &Store{docs: docs, publish: publish, cur: Defaults()}
	data, ok, err := docs.Get(ctx, Key)
	if err != nil {
		return s, fmt.Errorf("settings: %w", err)
	}
	if !ok {
		return s, nil
	}
	cur, err := Migrate(data)
	s.cur = normalize(cur)
	if err != nil {
		return s, fmt.Errorf("settings: %w", err)
	}
	return s, nil
}

// NewMemory returns a Store with the defaults that keeps them in memory:
// for tests, and the UI before the app wires the real one.
func NewMemory() *Store {
	s, _ := Open(context.Background(), &memDocs{}, nil)
	return s
}

// Get returns a copy of the current settings, from memory.
func (s *Store) Get() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clone(s.cur)
}

// Update applies fn to a copy, normalizes it (unknown themes become
// "system", scales snap to Scales), saves it, swaps it in and publishes
// Changed. Nothing is saved or published when fn changes nothing.
func (s *Store) Update(ctx context.Context, fn func(*Settings)) error {
	s.update.Lock()
	old := s.Get()
	next := clone(old)
	fn(&next)
	next = normalize(next)
	stampDictation(old, &next) // family.go
	if reflect.DeepEqual(old, next) {
		s.update.Unlock()
		return nil
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err == nil {
		err = s.docs.Put(ctx, Key, data)
	}
	if err != nil {
		s.update.Unlock()
		return fmt.Errorf("settings: %w", err)
	}
	s.mu.Lock()
	s.cur = next
	s.mu.Unlock()
	s.update.Unlock()
	if s.publish != nil {
		s.publish(Changed{Old: old, New: clone(next)})
	}
	return nil
}

func clone(s Settings) Settings {
	s.Models.Endpoints = slices.Clone(s.Models.Endpoints)
	cloneM2(&s)
	cloneM7(&s)
	cloneExport(&s)
	cloneFamily(&s)
	return s
}

// normalize keeps values in their ranges.
func normalize(s Settings) Settings {
	s.Version = Version
	if !slices.Contains(Themes, s.Appearance.Theme) {
		s.Appearance.Theme = "system"
	}
	// Snap the scale to the nearest offered one.
	best := 1.0
	for _, v := range Scales {
		if abs(v-s.Appearance.Scale) < abs(best-s.Appearance.Scale) {
			best = v
		}
	}
	s.Appearance.Scale = best
	s.Models.CacheDir = strings.TrimSpace(s.Models.CacheDir)
	s.Models.Proxy = strings.TrimSpace(s.Models.Proxy)
	var eps []string
	for _, e := range s.Models.Endpoints {
		e = strings.TrimRight(strings.TrimSpace(e), "/")
		if e != "" && !slices.Contains(eps, e) {
			eps = append(eps, e)
		}
	}
	s.Models.Endpoints = eps
	s.Models.AutoUnload = max(s.Models.AutoUnload, 0)
	s.Engine.MemoryBudgetGB = max(s.Engine.MemoryBudgetGB, 0)
	normalizeM2(&s)
	normalizeM7(&s)
	normalizeExport(&s)
	return s
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

type memDocs struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (d *memDocs) Get(_ context.Context, key string) ([]byte, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	v, ok := d.m[key]
	return slices.Clone(v), ok, nil
}

func (d *memDocs) Put(_ context.Context, key string, doc []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.m == nil {
		d.m = map[string][]byte{}
	}
	d.m[key] = slices.Clone(doc)
	return nil
}

// DirDocuments keeps documents as JSON files in a directory, one per key
// ("settings:app" → settings-app.json), each written atomically. It stands
// in for store.Documents until the SQLite store lands; its files are then
// imported once.
type DirDocuments struct{ Dir string }

func (d DirDocuments) path(key string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '-'
	}, key)
	return filepath.Join(d.Dir, name+".json")
}

func (d DirDocuments) Get(_ context.Context, key string) ([]byte, bool, error) {
	data, err := os.ReadFile(d.path(key))
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func (d DirDocuments) Put(_ context.Context, key string, doc []byte) error {
	if err := os.MkdirAll(d.Dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(d.Dir, ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := f.Write(doc); err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return err
	}
	return os.Rename(f.Name(), d.path(key))
}
