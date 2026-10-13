package services

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/store"
	"github.com/hehehai/AudioSmith/internal/text/lexicon"
)

// pronunciationKey is the document the dictionary is kept in.
const pronunciationKey = "pronunciation"

// PronunciationChanged is published after the dictionary was saved.
// Topic "pronunciation.changed".
type PronunciationChanged struct{ Entries []lexicon.Entry }

func (PronunciationChanged) Topic() string { return "pronunciation.changed" }

// Pronunciation is the pronunciation dictionary (Settings → Pronunciation):
// entries kept as one JSON document in the store (lexicon.File), cached in
// memory after the first read. Goroutine-safe.
type Pronunciation struct {
	docs store.Documents
	bus  *core.Bus

	mu      sync.Mutex
	loaded  bool
	entries []lexicon.Entry
}

func newPronunciation(docs store.Documents, bus *core.Bus) *Pronunciation {
	return &Pronunciation{docs: docs, bus: bus}
}

// Entries returns the dictionary, in its order.
func (p *Pronunciation) Entries(ctx context.Context) ([]lexicon.Entry, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.loadLocked(ctx); err != nil {
		return nil, err
	}
	return slices.Clone(p.entries), nil
}

func (p *Pronunciation) loadLocked(ctx context.Context) error {
	if p.loaded {
		return nil
	}
	data, ok, err := p.docs.Get(ctx, pronunciationKey)
	if err != nil {
		return err
	}
	if ok {
		var f lexicon.File
		if err := json.Unmarshal(data, &f); err != nil {
			return fmt.Errorf("pronunciation dictionary: %w", err)
		}
		p.entries = f.Entries
	}
	p.loaded = true
	return nil
}

// Save replaces the dictionary and publishes PronunciationChanged.
// Entries are stored as given, problems included (lexicon.Validate lists
// them; disabled or broken entries do not apply).
func (p *Pronunciation) Save(ctx context.Context, entries []lexicon.Entry) error {
	data, err := lexicon.Export(entries)
	if err != nil {
		return err
	}
	p.mu.Lock()
	err = p.docs.Put(ctx, pronunciationKey, data)
	if err == nil {
		p.entries, p.loaded = slices.Clone(entries), true
	}
	p.mu.Unlock()
	if err != nil {
		return err
	}
	p.bus.Publish(PronunciationChanged{Entries: slices.Clone(entries)})
	return nil
}

// Import reads a dictionary file (lexicon.Import's shapes) and merges it
// into the dictionary or replaces it, then saves.
func (p *Pronunciation) Import(ctx context.Context, data []byte, mode lexicon.Mode) (lexicon.ImportReport, error) {
	cur, err := p.Entries(ctx)
	if err != nil {
		return lexicon.ImportReport{}, err
	}
	next, rep, err := lexicon.Import(data, cur, mode)
	if err != nil {
		return rep, fmt.Errorf("%w: %w", core.ErrInvalid, err)
	}
	return rep, p.Save(ctx, next)
}

// Export encodes the dictionary as a JSON file.
func (p *Pronunciation) Export(ctx context.Context) ([]byte, error) {
	cur, err := p.Entries(ctx)
	if err != nil {
		return nil, err
	}
	return lexicon.Export(cur)
}

// Dictionary compiles the entries that apply to language lang.
func (p *Pronunciation) Dictionary(ctx context.Context, lang string) (*lexicon.Dictionary, error) {
	cur, err := p.Entries(ctx)
	if err != nil {
		return nil, err
	}
	return lexicon.Compile(cur, lang), nil
}
