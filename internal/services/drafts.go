package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/store"
)

// DraftPrefix is the store.Documents key prefix of drafts.
const DraftPrefix = "draft:"

// Drafts implements core.DraftService over store.Documents ("draft:<key>").
//
// Save encodes the value at once (so the caller may keep mutating it) and
// writes it after the delay has passed without another Save of the same
// key, on a timer goroutine. Writes are serialized and always write the
// newest value; one equal to what is stored is skipped. Each write
// publishes core.DraftSaved.
type Drafts struct {
	docs  store.Documents
	bus   *core.Bus
	delay time.Duration
	logf  func(string, ...any)

	write   sync.Mutex // held while writing, so writes happen in Save order
	mu      sync.Mutex // guards the maps and closed; never held during I/O
	pending map[string]*draft
	saved   map[string][]byte // last written, to skip no-op writes
	closed  bool
}

type draft struct {
	data  []byte
	timer *time.Timer
}

var _ core.DraftService = (*Drafts)(nil)

func newDrafts(cfg Config) *Drafts {
	return &Drafts{docs: cfg.Store.Documents(), bus: cfg.Bus, delay: cfg.DraftDelay, logf: cfg.Logf,
		pending: map[string]*draft{}, saved: map[string][]byte{}}
}

func (s *Drafts) Save(key string, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		s.logf("draft %s: %v", key, err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		s.logf("draft %s: saved after Close; dropped", key)
		return
	}
	d := s.pending[key]
	if d == nil {
		if bytes.Equal(s.saved[key], data) {
			return
		}
		d = &draft{}
		d.timer = time.AfterFunc(s.delay, func() {
			if err := s.flushKey(context.Background(), key); err != nil {
				s.logf("draft %s: %v", key, err)
			}
		})
		s.pending[key] = d
	} else {
		d.timer.Reset(s.delay)
	}
	d.data = data
}

// flushKey writes key's pending draft, if any.
func (s *Drafts) flushKey(ctx context.Context, key string) error {
	s.write.Lock()
	s.mu.Lock()
	d := s.pending[key]
	delete(s.pending, key)
	s.mu.Unlock()
	if d == nil {
		s.write.Unlock()
		return nil
	}
	d.timer.Stop()
	err := s.docs.Put(ctx, DraftPrefix+key, d.data)
	if err == nil {
		s.mu.Lock()
		s.saved[key] = d.data
		s.mu.Unlock()
	}
	s.write.Unlock()
	if err != nil {
		return err
	}
	s.bus.Publish(core.DraftSaved{Key: key})
	return nil
}

// Load decodes the newest draft of key, pending or stored, into v.
func (s *Drafts) Load(ctx context.Context, key string, v any) (bool, error) {
	s.mu.Lock()
	var data []byte
	if d := s.pending[key]; d != nil {
		data = d.data
	}
	s.mu.Unlock()
	if data == nil {
		doc, ok, err := s.docs.Get(ctx, DraftPrefix+key)
		if err != nil || !ok {
			return false, err
		}
		data = doc
		s.mu.Lock()
		if s.pending[key] == nil {
			s.saved[key] = doc
		}
		s.mu.Unlock()
	}
	if err := json.Unmarshal(data, v); err != nil {
		return false, fmt.Errorf("draft %s: %w", key, err)
	}
	return true, nil
}

// Delete drops a pending draft and the stored one.
func (s *Drafts) Delete(ctx context.Context, key string) error {
	s.write.Lock()
	defer s.write.Unlock()
	s.mu.Lock()
	if d := s.pending[key]; d != nil {
		d.timer.Stop()
		delete(s.pending, key)
	}
	delete(s.saved, key)
	s.mu.Unlock()
	return s.docs.Delete(ctx, DraftPrefix+key)
}

// Flush writes every pending draft now.
func (s *Drafts) Flush(ctx context.Context) error {
	s.mu.Lock()
	keys := make([]string, 0, len(s.pending))
	for k := range s.pending {
		keys = append(keys, k)
	}
	s.mu.Unlock()
	var errs []error
	for _, k := range keys {
		if err := s.flushKey(ctx, k); err != nil {
			errs = append(errs, fmt.Errorf("draft %s: %w", k, err))
		}
	}
	return errors.Join(errs...)
}

// Close refuses later Saves and writes the pending ones.
func (s *Drafts) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return s.Flush(ctx)
}
