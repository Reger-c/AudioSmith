package gallery

import (
	"context"
	"slices"
	"sync"
)

// FavoriteStore persists favorites; the app implements it (e.g. on its
// settings or documents table). Keys are archetype ids, or later
// "community:<repo>:<id>" for community voices.
type FavoriteStore interface {
	LoadFavorites(ctx context.Context) ([]string, error)
	SaveFavorite(ctx context.Context, id string, on bool) error
}

// Favorites is the in-memory favorites set, written through to a store.
// Has is cheap and safe to call from a build pass; Set does I/O and
// belongs on a goroutine.
type Favorites struct {
	mu    sync.RWMutex
	set   map[string]bool
	store FavoriteStore
}

// LoadFavorites reads the set from store (nil store = memory only).
func LoadFavorites(ctx context.Context, store FavoriteStore) (*Favorites, error) {
	f := &Favorites{set: map[string]bool{}, store: store}
	if store == nil {
		return f, nil
	}
	ids, err := store.LoadFavorites(ctx)
	if err != nil {
		return f, err
	}
	for _, id := range ids {
		f.set[id] = true
	}
	return f, nil
}

// Has reports whether id is a favorite.
func (f *Favorites) Has(id string) bool {
	if f == nil {
		return false
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.set[id]
}

// Set marks or unmarks a favorite, persisting first; on a store error the
// set is unchanged.
func (f *Favorites) Set(ctx context.Context, id string, on bool) error {
	if f.store != nil {
		if err := f.store.SaveFavorite(ctx, id, on); err != nil {
			return err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if on {
		f.set[id] = true
	} else {
		delete(f.set, id)
	}
	return nil
}

// Toggle flips a favorite and returns the new state.
func (f *Favorites) Toggle(ctx context.Context, id string) (bool, error) {
	on := !f.Has(id)
	return on, f.Set(ctx, id, on)
}

// List returns the favorites, sorted.
func (f *Favorites) List() []string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]string, 0, len(f.set))
	for id := range f.set {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// MemoryFavorites is a FavoriteStore in memory (tests, previews).
type MemoryFavorites struct {
	mu  sync.Mutex
	ids map[string]bool
	Err error // returned by every call when set
}

func (m *MemoryFavorites) LoadFavorites(context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return nil, m.Err
	}
	var out []string
	for id := range m.ids {
		out = append(out, id)
	}
	slices.Sort(out)
	return out, nil
}

func (m *MemoryFavorites) SaveFavorite(_ context.Context, id string, on bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	if m.ids == nil {
		m.ids = map[string]bool{}
	}
	if on {
		m.ids[id] = true
	} else {
		delete(m.ids, id)
	}
	return nil
}
