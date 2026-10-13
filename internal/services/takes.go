package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/store"
)

// Takes implements core.TakeService.
//
// Generate submits a "tts.synthesize" job owned by "take:<id>" and keeps
// the take's recipe until the job ends. It listens to jobs.Updated: when
// the job succeeds, the take is filled from the result, saved and
// announced with TakeCreated (on a goroutine, as bus handlers must not
// block). A job that finishes after a restart (re-queued because it is
// idempotent) has no recipe in memory: the take is then rebuilt from the
// job's params, without the relations (voice, project) params do not carry.
type Takes struct {
	store    store.Takes
	bus      *core.Bus
	sched    jobs.Scheduler
	licences core.LicenceService
	ready    func(context.Context, string) error
	now      func() time.Time
	logf     func(string, ...any)
	async    func(func())

	rmw     sync.Mutex // serializes read-modify-write of takes
	mu      sync.Mutex // guards pending and closed; never held during I/O
	pending map[string]core.Take
	closed  bool
	wg      sync.WaitGroup
	unsub   func()
}

var _ core.TakeService = (*Takes)(nil)

func newTakes(cfg Config) *Takes {
	t := &Takes{
		store: cfg.Store.Takes(), bus: cfg.Bus, sched: cfg.Scheduler, licences: cfg.Licences,
		ready: cfg.Ready, now: cfg.Now, logf: cfg.Logf, async: cfg.Async, pending: map[string]core.Take{},
	}
	t.unsub = core.On(cfg.Bus, t.onJob)
	return t
}

// Close stops listening for jobs and waits for takes being saved.
func (s *Takes) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.unsub()
	s.wg.Wait()
	return nil
}

func (s *Takes) List(ctx context.Context, q core.TakeQuery) ([]core.Take, error) {
	return s.store.List(ctx, q)
}

func (s *Takes) Get(ctx context.Context, id string) (core.Take, error) {
	return s.store.Get(ctx, id)
}

func (s *Takes) AudioDir() string { return s.store.AudioDir() }

func (s *Takes) AudioPath(t core.Take) string { return s.store.AudioPath(t) }

// SynthesizeParams is the engine request for a recipe: zero parameters
// are left out so the model's defaults apply.
func SynthesizeParams(t core.Take, output string, stream bool) proto.SynthesizeParams {
	p := proto.SynthesizeParams{
		Model: t.Model, Text: t.Text, Voice: t.Voice, Language: t.Language,
		MaxTokens: t.MaxTokens, RefAudio: t.RefAudio, RefText: t.RefText,
		Output: proto.Output{Path: output}, Stream: stream,
	}
	if t.Speed != 0 {
		p.Speed = &t.Speed
	}
	if t.Temperature != 0 {
		p.Temperature = &t.Temperature
	}
	if t.TopP != 0 {
		p.TopP = &t.TopP
	}
	if t.Seed != 0 {
		p.Seed = &t.Seed
	}
	if t.Duration != 0 {
		p.Duration = &t.Duration
	}
	p.Steps = t.Steps
	return p
}

func (s *Takes) Generate(ctx context.Context, req core.TakeRequest) (string, error) {
	r := req.Recipe
	r.Text, r.Model = strings.TrimSpace(r.Text), strings.TrimSpace(r.Model)
	if r.Text == "" {
		return "", fmt.Errorf("generate: no text: %w", core.ErrInvalid)
	}
	if r.Model == "" {
		return "", fmt.Errorf("generate: no model: %w", core.ErrInvalid)
	}
	if s.sched == nil {
		return "", fmt.Errorf("generate: no job scheduler: %w", core.ErrUnsupported)
	}
	if s.licences != nil {
		if err := s.licences.Check(ctx, r.Model); err != nil {
			return "", err
		}
	}
	if s.ready != nil {
		if err := s.ready(ctx, r.Model); err != nil {
			return "", err
		}
	}

	now := s.now()
	t := r
	if t.ID == "" {
		t.ID = core.NewID(now)
	} else if filepath.Base(t.ID) != t.ID || strings.ContainsAny(t.ID, `/\.`) {
		return "", fmt.Errorf("generate: take id %q: %w", t.ID, core.ErrInvalid)
	}
	t.CreatedAt = now
	t.File = t.ID + ".wav"
	t.JobID, t.Starred = "", false
	t.SampleRate, t.AudioSeconds, t.GenSeconds = 0, 0, 0
	params, err := json.Marshal(SynthesizeParams(t, s.store.AudioPath(t), req.Stream))
	if err != nil {
		return "", err
	}
	priority := jobs.PriorityInteractive
	if req.Background {
		priority = jobs.PriorityBackground
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return "", fmt.Errorf("generate: %w", core.ErrCancelled)
	}
	// Registered before Submit: the job may end before Submit returns.
	s.pending[t.ID] = t
	s.mu.Unlock()
	id, err := s.sched.Submit(ctx, jobs.Spec{
		Kind:     proto.MethodTTSSynthesize,
		Lane:     jobs.LaneGPU,
		Priority: priority,
		Params:   params,
		Title:    i18n.T("Generate take"),
		Owner:    "take:" + t.ID,
		// The engine writes the file atomically under the take's id, so a
		// crashed or interrupted run can simply run again.
		MaxAttempts: 2,
		Idempotent:  true,
	})
	if err != nil {
		s.mu.Lock()
		delete(s.pending, t.ID)
		s.mu.Unlock()
		return "", err
	}
	return id, nil
}

// onJob is the jobs.Updated handler: it must not block.
func (s *Takes) onJob(u jobs.Updated) {
	j := u.Job
	if j.Kind != proto.MethodTTSSynthesize || !j.State.Terminal() {
		return
	}
	id, ok := strings.CutPrefix(j.Owner, "take:")
	if !ok || id == "" {
		return
	}
	s.mu.Lock()
	t, known := s.pending[id]
	delete(s.pending, id)
	if s.closed || j.State != jobs.StateSucceeded {
		// Failures and cancellations reach the UI as the job's own events.
		s.mu.Unlock()
		return
	}
	s.wg.Add(1)
	s.mu.Unlock()
	s.async(func() {
		defer s.wg.Done()
		if err := s.complete(j, id, t, known); err != nil {
			s.logf("take %s from job %s: %v", id, j.ID, err)
		}
	})
}

// complete saves the take a succeeded job made and publishes TakeCreated.
func (s *Takes) complete(j jobs.Job, id string, t core.Take, known bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if !known {
		if _, err := s.store.Get(ctx, id); err == nil {
			return nil // already saved
		}
		var p proto.SynthesizeParams
		if err := json.Unmarshal(j.Params, &p); err != nil {
			return fmt.Errorf("params: %w", err)
		}
		t = core.Take{
			ID: id, CreatedAt: j.Created, File: filepath.Base(p.Output.Path),
			Text: p.Text, Model: p.Model, Voice: p.Voice, Language: p.Language,
			MaxTokens: p.MaxTokens, RefAudio: p.RefAudio, RefText: p.RefText,
		}
		if p.Speed != nil {
			t.Speed = *p.Speed
		}
		if p.Temperature != nil {
			t.Temperature = *p.Temperature
		}
		if p.TopP != nil {
			t.TopP = *p.TopP
		}
		if p.Seed != nil {
			t.Seed = *p.Seed
		}
		if p.Duration != nil {
			t.Duration = *p.Duration
		}
		t.Steps = p.Steps
	}
	var res proto.SynthesizeResult
	if err := json.Unmarshal(j.Result, &res); err != nil {
		return fmt.Errorf("result: %w", err)
	}
	t.JobID = j.ID
	t.SampleRate, t.AudioSeconds, t.GenSeconds = res.SampleRate, res.AudioSeconds, res.Seconds
	if res.Seed != nil {
		t.Seed = *res.Seed
	}
	if res.Output != "" {
		t.File = filepath.Base(res.Output)
	}
	if err := s.store.Put(ctx, t); err != nil {
		return err
	}
	s.bus.Publish(core.TakeCreated{Take: t})
	return nil
}

func (s *Takes) Add(ctx context.Context, t core.Take) (core.Take, error) {
	if t.File == "" || filepath.Base(t.File) != t.File {
		return t, fmt.Errorf("add take: file %q: %w", t.File, core.ErrInvalid)
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = s.now()
	}
	if t.ID == "" {
		t.ID = core.NewID(t.CreatedAt)
	}
	if err := s.store.Put(ctx, t); err != nil {
		return t, err
	}
	s.bus.Publish(core.TakeCreated{Take: t})
	return t, nil
}

func (s *Takes) Delete(ctx context.Context, id string) error {
	if err := s.store.Delete(ctx, id); err != nil {
		return err
	}
	s.bus.Publish(core.TakeDeleted{ID: id})
	return nil
}

func (s *Takes) SetStarred(ctx context.Context, id string, starred bool) error {
	s.rmw.Lock()
	t, err := s.store.Get(ctx, id)
	changed := err == nil && t.Starred != starred
	if changed {
		t.Starred = starred
		err = s.store.Put(ctx, t)
	}
	s.rmw.Unlock()
	if err != nil || !changed {
		return err
	}
	s.bus.Publish(core.TakeUpdated{Take: t})
	return nil
}

// Export streams the take's audio into dst through a temporary file
// beside it, renamed into place when complete.
func (s *Takes) Export(ctx context.Context, id, dst string) error {
	t, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}
	return copyFile(ctx, s.store.AudioPath(t), dst)
}

func copyFile(ctx context.Context, src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".*.part")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			out.Close()
			os.Remove(out.Name())
		}
	}()
	if _, err = io.Copy(out, ctxReader{ctx, in}); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	if err = os.Chmod(out.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(out.Name(), dst)
}

// ctxReader stops a copy when its context ends.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, errors.Join(core.ErrCancelled, err)
	}
	return c.r.Read(p)
}
