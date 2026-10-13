package services

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/store"
)

// Voices implements core.VoiceService: CRUD over the store in M0; voice
// packages (.asvoice) arrive in M1.
type Voices struct {
	store store.Voices
	bus   *core.Bus
	now   func() time.Time
	mu    sync.Mutex // serializes Save/Delete (read-modify-write)
}

var _ core.VoiceService = (*Voices)(nil)

func (s *Voices) List(ctx context.Context, q core.VoiceQuery) ([]core.Voice, error) {
	return s.store.List(ctx, q)
}

func (s *Voices) Get(ctx context.Context, id string) (core.Voice, error) {
	return s.store.Get(ctx, id)
}

// Save creates a profile (empty ID) or updates one, keeping its creation
// time; updating an unknown id fails with ErrNotFound.
func (s *Voices) Save(ctx context.Context, v core.Voice) (core.Voice, error) {
	v.Name = strings.TrimSpace(v.Name)
	if v.Name == "" {
		return v, fmt.Errorf("voice: no name: %w", core.ErrInvalid)
	}
	if !slices.Contains([]core.VoiceKind{core.VoiceBuiltin, core.VoiceCloned, core.VoiceDesigned}, v.Kind) {
		return v, fmt.Errorf("voice: kind %q: %w", v.Kind, core.ErrInvalid)
	}
	s.mu.Lock()
	now := s.now()
	if v.ID == "" {
		v.ID, v.CreatedAt = core.NewID(now), now
	} else {
		old, err := s.store.Get(ctx, v.ID)
		if err != nil {
			s.mu.Unlock()
			return v, err
		}
		v.CreatedAt = old.CreatedAt
	}
	v.UpdatedAt = now
	err := s.store.Put(ctx, v)
	s.mu.Unlock()
	if err != nil {
		return v, err
	}
	s.bus.Publish(core.VoiceChanged{Voice: v})
	return v, nil
}

func (s *Voices) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	v, err := s.store.Get(ctx, id)
	if err == nil {
		err = s.store.Delete(ctx, id)
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	s.bus.Publish(core.VoiceChanged{Voice: v, Deleted: true})
	return nil
}

func (s *Voices) Import(ctx context.Context, path string) (core.Voice, error) {
	return core.Voice{}, fmt.Errorf("voice packages: %w", core.ErrUnsupported)
}

func (s *Voices) Export(ctx context.Context, id, dst string) error {
	return fmt.Errorf("voice packages: %w", core.ErrUnsupported)
}

// Projects implements core.ProjectService: CRUD over the store.
type Projects struct {
	store store.Projects
	bus   *core.Bus
	now   func() time.Time
	mu    sync.Mutex
}

var _ core.ProjectService = (*Projects)(nil)

var projectKinds = []core.ProjectKind{core.ProjectStory, core.ProjectAudiobook, core.ProjectDub, core.ProjectTranscript, core.ProjectWorkflow}

func (s *Projects) List(ctx context.Context, kind core.ProjectKind, search string) ([]core.Project, error) {
	return s.store.List(ctx, kind, search)
}

func (s *Projects) Get(ctx context.Context, id string) (core.Project, error) {
	return s.store.Get(ctx, id)
}

// Save creates (empty ID) or updates a project, keeping its creation time.
func (s *Projects) Save(ctx context.Context, p core.Project) (core.Project, error) {
	p.Title = strings.TrimSpace(p.Title)
	if p.Title == "" {
		return p, fmt.Errorf("project: no title: %w", core.ErrInvalid)
	}
	if !slices.Contains(projectKinds, p.Kind) {
		return p, fmt.Errorf("project: kind %q: %w", p.Kind, core.ErrInvalid)
	}
	s.mu.Lock()
	now := s.now()
	if p.ID == "" {
		p.ID, p.CreatedAt = core.NewID(now), now
	} else {
		old, err := s.store.Get(ctx, p.ID)
		if err != nil {
			s.mu.Unlock()
			return p, err
		}
		p.CreatedAt = old.CreatedAt
	}
	p.UpdatedAt = now
	if len(p.Body) == 0 {
		p.Body = []byte("{}")
	}
	err := s.store.Put(ctx, p)
	s.mu.Unlock()
	if err != nil {
		return p, err
	}
	s.bus.Publish(core.ProjectChanged{Project: p})
	return p, nil
}

func (s *Projects) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	p, err := s.store.Get(ctx, id)
	if err == nil {
		err = s.store.Delete(ctx, id)
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	s.bus.Publish(core.ProjectChanged{Project: p, Deleted: true})
	return nil
}
