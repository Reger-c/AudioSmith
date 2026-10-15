package services

// The longform service: renders of Stories and Audiobook projects.
//
// A render is a DAG of jobs owned by "longform:<render id>":
//
//	chapter 1 ─┐
//	chapter 2 ─┼─▶ assemble (cpu lane) ─▶ book.wav, cues.txt, stems, report.json, (M4B)
//	chapter n ─┘
//
// The chapter jobs (gpu lane, normal priority, idempotent) run one after
// the other in chapter order — the gpu lane has one slot and the job ids
// sort in the order they were made — so a Studio take may run between two
// chapters. The assemble job waits on a lane of its own, so it never holds
// the gpu lane while chapters run (the deadlock thread G found for a join
// job inside the gpu lane). A failed chapter fails the assembly; the other
// chapters still render. Retry (Resume) submits the render's jobs again:
// chapters whose files were made from the same program are kept, the
// others come mostly from the chunk cache. A render the app quit in the
// middle of goes on at the next start (its jobs are idempotent).
//
// Only one full render runs at a time; auditions (one chapter or line, for
// listening) may run beside it.
//
// The render's record (core.Render, migration 003) keeps the manifest and
// the chapters' states; RenderChanged is published on every change.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/store"
	"github.com/hehehai/AudioSmith/internal/text"
)

// Chapter states.
const (
	ChapterPending   = "pending"
	ChapterRendering = "rendering"
	ChapterDone      = "done"
	ChapterCached    = "cached" // no synthesis was needed
	ChapterFailed    = "failed"
	ChapterStopped   = "stopped"
)

// RenderBody is a render's JSON (core.Render.Body).
type RenderBody struct {
	// Spec is the manifest. Bodies published with RenderChanged carry its
	// summary only (Summary), not its chapters and voices.
	Spec     LongformSpec    `json:"spec"`
	Summary  RenderSummary   `json:"summary"`
	Dir      string          `json:"dir"` // the render's files
	Chapters []RenderChapter `json:"chapters"`
	// The assembly: its job, whether it runs and how far.
	AssembleJob string          `json:"assembleJob,omitempty"`
	Assembling  bool            `json:"assembling,omitempty"`
	Assembled   float64         `json:"assembled,omitempty"`
	Output      *AssembleResult `json:"output,omitempty"`
	Error       *jobs.Error     `json:"error,omitempty"`
	Stopping    bool            `json:"stopping,omitempty"`
	Runs        int             `json:"runs"` // 1 + how often it was resumed
	Started     time.Time       `json:"started"`
	Finished    time.Time       `json:"finished,omitzero"`
}

// RenderChapter is a chapter of a render.
type RenderChapter struct {
	Title        string      `json:"title,omitempty"`
	Chars        int         `json:"chars"` // runes to say, for the ETA
	Lines        int         `json:"lines"`
	Job          string      `json:"job,omitempty"`
	State        string      `json:"state"`
	Fraction     float64     `json:"fraction,omitempty"`
	Error        *jobs.Error `json:"error,omitempty"`
	File         string      `json:"file,omitempty"`
	AudioSeconds float64     `json:"audioSeconds,omitempty"`
	Seconds      float64     `json:"seconds,omitempty"` // wall time
	Chunks       int         `json:"chunks,omitempty"`
	Cached       int         `json:"cached,omitempty"` // chunks from the cache
	Started      time.Time   `json:"started,omitzero"`
	Finished     time.Time   `json:"finished,omitzero"`
}

// RenderSummary is how a render was made, for the library's details.
type RenderSummary struct {
	Model    string     `json:"model"`
	Lang     string     `json:"lang,omitempty"`
	Format   string     `json:"format,omitempty"`
	Loudness string     `json:"loudness,omitempty"`
	Voices   []string   `json:"voices,omitempty"`   // VoiceIDs, or speakers ("speaker:<id>")
	MinSpeed float64    `json:"minSpeed,omitempty"` // of the lines
	MaxSpeed float64    `json:"maxSpeed,omitempty"`
	Chapters int        `json:"chapters"`
	Lines    int        `json:"lines"`
	Words    int        `json:"words"`
	Chars    int        `json:"chars"` // unspaced-script characters
	Prod     Production `json:"production"`
	Stems    bool       `json:"stems,omitempty"`
}

// Done reports whether every chapter is made.
func (b *RenderBody) Done() int {
	n := 0
	for _, c := range b.Chapters {
		if c.State == ChapterDone || c.State == ChapterCached {
			n++
		}
	}
	return n
}

// Failed counts the failed chapters.
func (b *RenderBody) Failed() int {
	n := 0
	for _, c := range b.Chapters {
		if c.State == ChapterFailed {
			n++
		}
	}
	return n
}

// DecodeRender reads a render's body.
func DecodeRender(r core.Render) (RenderBody, error) {
	var b RenderBody
	if len(r.Body) == 0 {
		return b, nil
	}
	err := json.Unmarshal(r.Body, &b)
	return b, err
}

// Longform runs and keeps longform renders.
type Longform struct {
	renders  store.Renders
	sched    jobs.Scheduler
	bus      *core.Bus
	now      func() time.Time
	logf     func(string, ...any)
	async    func(func())
	dataDir  string
	cacheDir string
	encode   func() jobs.Executor
	render   *renderer

	mu   sync.Mutex
	live map[string]*liveRender // renders followed in memory, by id

	qmu      sync.Mutex
	queue    []jobs.Job
	draining bool
	closed   bool
	drained  sync.WaitGroup
	unsub    func()
}

type liveRender struct {
	rec  core.Render
	body RenderBody
}

func newLongform(cfg Config, t *Takes) *Longform {
	s := &Longform{renders: cfg.Store.Renders(), sched: cfg.Scheduler, bus: cfg.Bus, now: cfg.Now, logf: cfg.Logf,
		async: cfg.Async, dataDir: cfg.DataDir, cacheDir: cfg.CacheDir, encode: cfg.Encode, render: t.render,
		live: map[string]*liveRender{}}
	if s.dataDir == "" {
		s.dataDir = filepath.Dir(cfg.Store.Takes().AudioDir())
	}
	if cfg.Scheduler != nil {
		cfg.Scheduler.Register(KindChapter, chapterRenderer{t.render})
		cfg.Scheduler.Register(KindAssemble, assembler{s})
	}
	s.unsub = core.On(cfg.Bus, s.onJob)
	return s
}

// ChapterExecutor and AssembleExecutor are the executors New registers,
// for tests that run them.
func (s *Longform) ChapterExecutor() jobs.Executor  { return chapterRenderer{s.render} }
func (s *Longform) AssembleExecutor() jobs.Executor { return assembler{s} }

// EncodeSupported reports whether books can be encoded as M4B (the engine
// has media.encode).
func (s *Longform) EncodeSupported() bool { return s.encode != nil && s.encode() != nil }

// Close stops following jobs and waits for the updates being applied.
func (s *Longform) Close() {
	s.unsub()
	s.qmu.Lock()
	s.closed = true
	s.queue = nil
	s.qmu.Unlock()
	s.drained.Wait()
}

// Start begins a render of spec. A full render is refused with
// core.ErrBusy while another runs.
func (s *Longform) Start(ctx context.Context, spec LongformSpec) (core.Render, error) {
	if s.sched == nil {
		return core.Render{}, fmt.Errorf("longform: no job scheduler: %w", core.ErrUnsupported)
	}
	if err := checkSpec(&spec); err != nil {
		return core.Render{}, err
	}
	now := s.now()
	id := core.NewID(now)
	dir := filepath.Join(s.dataDir, "renders", id)
	if spec.Audition {
		dir = filepath.Join(s.cacheDir, "auditions", id)
		s.pruneAuditions(ctx)
	}
	rec := core.Render{ID: id, ProjectID: spec.Project, Kind: spec.Kind, Title: spec.Title, State: core.RenderRunning,
		Audition: spec.Audition, CreatedAt: now, UpdatedAt: now}
	body := RenderBody{Spec: spec, Summary: summarize(&spec), Dir: dir, Runs: 1, Started: now}
	for _, ch := range spec.Chapters {
		body.Chapters = append(body.Chapters, RenderChapter{Title: ch.Title, Chars: chapterChars(ch), Lines: len(ch.Lines), State: ChapterPending})
	}
	if !spec.Audition {
		if title, busy := s.busy(ctx, ""); busy {
			return core.Render{}, fmt.Errorf("longform: %q is rendering: %w", title, core.ErrBusy)
		}
	}
	s.mu.Lock()
	if !spec.Audition {
		for _, l := range s.live {
			if !l.rec.Audition && l.rec.State == core.RenderRunning {
				s.mu.Unlock()
				return core.Render{}, fmt.Errorf("longform: %q is rendering: %w", l.rec.Title, core.ErrBusy)
			}
		}
	}
	lr := &liveRender{rec: rec, body: body}
	s.live[id] = lr
	s.mu.Unlock()
	if err := os.MkdirAll(filepath.Join(dir, "chapters"), 0o755); err != nil {
		s.forget(id)
		return core.Render{}, err
	}
	if err := s.save(ctx, id); err != nil {
		s.forget(id)
		return core.Render{}, err
	}
	if err := s.submit(ctx, id); err != nil {
		s.mu.Lock()
		lr.rec.State, lr.body.Error = core.RenderFailed, &jobs.Error{Code: CodeRenderFailed, Message: err.Error()}
		s.mu.Unlock()
		s.save(context.WithoutCancel(ctx), id)
		return core.Render{}, err
	}
	out, _ := s.snapshot(id)
	return out, nil
}

// busy finds a full render other than except recorded as running: one
// the scheduler resumes after a restart is in the store before its first
// update brings it into memory.
func (s *Longform) busy(ctx context.Context, except string) (string, bool) {
	running, err := s.renders.List(ctx, store.RenderQuery{States: []core.RenderState{core.RenderRunning}})
	if err != nil {
		return "", false
	}
	for _, r := range running {
		if r.ID == except {
			continue
		}
		s.mu.Lock()
		lr := s.live[r.ID]
		state := r.State
		if lr != nil {
			state = lr.rec.State
		}
		s.mu.Unlock()
		if state == core.RenderRunning {
			return r.Title, true
		}
	}
	return "", false
}

// checkSpec validates a manifest.
func checkSpec(spec *LongformSpec) error {
	if spec.Model == "" {
		return fmt.Errorf("longform: no model: %w", core.ErrInvalid)
	}
	if spec.Kind != core.ProjectStory && spec.Kind != core.ProjectAudiobook {
		return fmt.Errorf("longform: kind %q: %w", spec.Kind, core.ErrInvalid)
	}
	if len(spec.Chapters) == 0 {
		return fmt.Errorf("longform: nothing to say: %w", core.ErrInvalid)
	}
	for ci, ch := range spec.Chapters {
		if len(ch.Lines) == 0 {
			return fmt.Errorf("longform: chapter %d has no lines: %w", ci+1, core.ErrInvalid)
		}
		for li, l := range ch.Lines {
			for _, sp := range l.Spans {
				if _, ok := spec.Voices[sp.Voice]; !ok {
					return fmt.Errorf("longform: chapter %d, line %d: voice %q: %w", ci+1, li+1, sp.Voice, core.ErrInvalid)
				}
			}
		}
	}
	if spec.Book.Loudness != LoudnessOff && loudnessTarget(spec.Book.Loudness) == nil {
		return fmt.Errorf("longform: loudness %q: %w", spec.Book.Loudness, core.ErrInvalid)
	}
	if spec.Title == "" {
		spec.Title = "Untitled"
	}
	return nil
}

func chapterChars(ch ChapterSpec) int {
	n := 0
	for _, l := range ch.Lines {
		for _, sp := range l.Spans {
			w, c := text.Count(sp.Text)
			n += w*6 + c // a spaced-script word is about six characters
		}
	}
	return max(n, 1)
}

// summarize is how a spec renders, for the library.
func summarize(spec *LongformSpec) RenderSummary {
	sum := RenderSummary{Model: spec.Model, Lang: spec.Lang, Format: spec.Format, Loudness: spec.Book.Loudness,
		Chapters: len(spec.Chapters), Prod: spec.Production, Stems: spec.Stems}
	used := map[string]bool{}
	for _, ch := range spec.Chapters {
		for _, l := range ch.Lines {
			sum.Lines++
			sp := l.Speed
			if sp <= 0 {
				sp = 1
			}
			if sum.MinSpeed == 0 || sp < sum.MinSpeed {
				sum.MinSpeed = sp
			}
			sum.MaxSpeed = max(sum.MaxSpeed, sp)
			for _, s := range l.Spans {
				w, c := text.Count(s.Text)
				sum.Words += w
				sum.Chars += c
				if !used[s.Voice] {
					used[s.Voice] = true
					v := spec.Voices[s.Voice]
					switch {
					case v.VoiceID != "":
						sum.Voices = append(sum.Voices, v.VoiceID)
					case v.Voice != "":
						sum.Voices = append(sum.Voices, SpeakerRef+v.Voice)
					}
				}
			}
		}
	}
	sum.Voices = slices.Compact(sum.Voices)
	return sum
}

// submit queues the render's jobs: its chapters, then the assembly.
func (s *Longform) submit(ctx context.Context, id string) error {
	s.mu.Lock()
	lr := s.live[id]
	if lr == nil {
		s.mu.Unlock()
		return fmt.Errorf("render %s: %w", id, core.ErrNotFound)
	}
	spec, dir, title := lr.body.Spec, lr.body.Dir, lr.rec.Title
	s.mu.Unlock()
	priority := jobs.PriorityNormal
	loud := spec.Book.Loudness
	if spec.Audition {
		priority, loud = jobs.PriorityInteractive, LoudnessOff
	}
	var nodes []jobs.Node
	var keys []string
	for i, ch := range spec.Chapters {
		p := ChapterParams{Render: id, Index: i, Title: ch.Title, Model: spec.Model, Lang: spec.Lang, Seed: spec.Seed,
			Voices: map[string]core.Take{}, Lines: ch.Lines, Production: spec.Production, Lexicon: spec.Lexicon,
			Loudness: loud, Output: chapterPath(dir, i), Scene: spec.Scene, Workflow: string(spec.Kind)}
		for _, l := range ch.Lines {
			for _, sp := range l.Spans {
				p.Voices[sp.Voice] = spec.Voices[sp.Voice]
			}
		}
		params, err := json.Marshal(p)
		if err != nil {
			return err
		}
		label := ch.Title
		if label == "" {
			label = strconv.Itoa(i + 1)
		}
		key := "c" + strconv.Itoa(i)
		keys = append(keys, key)
		jt := i18n.T("Chapter %s · %s", label, title)
		if spec.Audition {
			jt = i18n.T("Audition · %s", title)
		}
		nodes = append(nodes, jobs.Node{Key: key, Spec: jobs.Spec{Kind: KindChapter, Lane: jobs.LaneGPU, Priority: priority,
			Params: params, Title: jt, Owner: OwnerLongform + id, MaxAttempts: 2, Idempotent: true}})
	}
	if !spec.Audition {
		params, _ := json.Marshal(AssembleParams{Render: id})
		nodes = append(nodes, jobs.Node{Key: "book", Spec: jobs.Spec{Kind: KindAssemble, Lane: jobs.LaneCPU, Priority: priority,
			Params: params, Title: i18n.T("Assemble · %s", title), Owner: OwnerLongform + id, After: keys, Idempotent: true}})
	}
	ids, err := s.sched.SubmitGraph(ctx, nodes)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if lr := s.live[id]; lr != nil {
		for i := range lr.body.Chapters {
			if j := ids["c"+strconv.Itoa(i)]; j != "" && lr.body.Chapters[i].Job == "" {
				lr.body.Chapters[i].Job = j
			}
		}
		if lr.body.AssembleJob == "" {
			lr.body.AssembleJob = ids["book"]
		}
	}
	s.mu.Unlock()
	return s.save(ctx, id)
}

// Stop cancels a render's jobs; the chapters made are kept, and Resume
// goes on from them.
func (s *Longform) Stop(ctx context.Context, id string) error {
	s.mu.Lock()
	lr := s.live[id]
	if lr == nil || lr.rec.State != core.RenderRunning {
		s.mu.Unlock()
		return nil
	}
	lr.body.Stopping = true
	var ids []string
	for _, c := range lr.body.Chapters {
		if c.Job != "" && (c.State == ChapterPending || c.State == ChapterRendering) {
			ids = append(ids, c.Job)
		}
	}
	if lr.body.AssembleJob != "" {
		ids = append(ids, lr.body.AssembleJob)
	}
	s.mu.Unlock()
	var errs []error
	for _, j := range ids {
		if err := s.sched.Cancel(ctx, j); err != nil && !errors.Is(err, jobs.ErrNotFound) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Resume renders again what a stopped or failed render lacks: its
// chapters made from the same program are kept, the rest (failed or not
// yet made) are rendered, then the book is assembled.
func (s *Longform) Resume(ctx context.Context, id string) (core.Render, error) {
	rec, body, err := s.load(ctx, id)
	if err != nil {
		return core.Render{}, err
	}
	if rec.State == core.RenderRunning {
		return rec, fmt.Errorf("longform: %q is rendering: %w", rec.Title, core.ErrBusy)
	}
	if !rec.Audition {
		if title, busy := s.busy(ctx, id); busy {
			return core.Render{}, fmt.Errorf("longform: %q is rendering: %w", title, core.ErrBusy)
		}
	}
	now := s.now()
	s.mu.Lock()
	if !rec.Audition {
		for _, l := range s.live {
			if l.rec.ID != id && !l.rec.Audition && l.rec.State == core.RenderRunning {
				s.mu.Unlock()
				return core.Render{}, fmt.Errorf("longform: %q is rendering: %w", l.rec.Title, core.ErrBusy)
			}
		}
	}
	lr := s.live[id]
	if lr == nil {
		lr = &liveRender{rec: rec, body: body}
		s.live[id] = lr
	}
	lr.rec.State, lr.rec.UpdatedAt = core.RenderRunning, now
	b := &lr.body
	b.Runs++
	b.Stopping, b.Error, b.Assembling, b.Assembled, b.Finished = false, nil, false, 0, time.Time{}
	b.AssembleJob = ""
	for i := range b.Chapters {
		c := &b.Chapters[i]
		c.Job, c.Fraction, c.Error = "", 0, nil
		if c.State != ChapterDone && c.State != ChapterCached {
			c.State = ChapterPending
		}
	}
	s.mu.Unlock()
	if err := s.submit(ctx, id); err != nil {
		s.mu.Lock()
		lr.rec.State, lr.body.Error = core.RenderFailed, &jobs.Error{Code: CodeRenderFailed, Message: err.Error()}
		s.mu.Unlock()
		s.save(context.WithoutCancel(ctx), id)
		return core.Render{}, err
	}
	out, _ := s.snapshot(id)
	return out, nil
}

// Get returns a render with its whole body (manifest included).
func (s *Longform) Get(ctx context.Context, id string) (core.Render, RenderBody, error) {
	return s.load(ctx, id)
}

// List lists renders (their bodies as stored).
func (s *Longform) List(ctx context.Context, q store.RenderQuery) ([]core.Render, error) {
	out, err := s.renders.List(ctx, q)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	for i, r := range out {
		if lr := s.live[r.ID]; lr != nil {
			out[i].State, out[i].UpdatedAt = lr.rec.State, lr.rec.UpdatedAt
		}
	}
	s.mu.Unlock()
	return out, nil
}

// Active is the full render running now, if any (published body).
func (s *Longform) Active() (core.Render, bool) {
	s.mu.Lock()
	var id string
	for _, l := range s.live {
		if !l.rec.Audition && l.rec.State == core.RenderRunning {
			id = l.rec.ID
		}
	}
	s.mu.Unlock()
	if id == "" {
		return core.Render{}, false
	}
	return s.snapshot(id)
}

// Delete removes a render's record and files; a running render is refused.
func (s *Longform) Delete(ctx context.Context, id string) error {
	rec, body, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	if rec.State == core.RenderRunning {
		return fmt.Errorf("longform: %q is rendering: %w", rec.Title, core.ErrBusy)
	}
	if err := s.renders.Delete(ctx, id); err != nil {
		return err
	}
	s.forget(id)
	s.removeDir(body.Dir)
	s.bus.Publish(core.RenderChanged{Render: rec, Deleted: true})
	return nil
}

// removeDir deletes a render's folder if it is one of ours.
func (s *Longform) removeDir(dir string) {
	for _, root := range []string{filepath.Join(s.dataDir, "renders"), filepath.Join(s.cacheDir, "auditions")} {
		if rel, err := filepath.Rel(root, dir); err == nil && rel != "." && !strings.HasPrefix(rel, "..") && !strings.Contains(rel, string(filepath.Separator)) {
			os.RemoveAll(dir)
		}
	}
}

// pruneAuditions deletes auditions older than an hour.
func (s *Longform) pruneAuditions(ctx context.Context) {
	old, err := s.renders.List(ctx, store.RenderQuery{Auditions: true, States: []core.RenderState{core.RenderDone, core.RenderFailed, core.RenderStopped}})
	if err != nil {
		return
	}
	for _, r := range old {
		if r.Audition && s.now().Sub(r.UpdatedAt) > time.Hour {
			s.Delete(ctx, r.ID)
		}
	}
}

// Reconcile settles renders recorded as running whose jobs are all gone
// or finished (the app stopped between two of their updates): they
// become failed, for Resume. Call it once the scheduler has started.
func (s *Longform) Reconcile(ctx context.Context) error {
	running, err := s.renders.List(ctx, store.RenderQuery{Auditions: true, States: []core.RenderState{core.RenderRunning}})
	if err != nil {
		return err
	}
	for _, r := range running {
		_, body, err := s.load(ctx, r.ID)
		if err != nil {
			continue
		}
		ids := []string{body.AssembleJob}
		for _, c := range body.Chapters {
			ids = append(ids, c.Job)
		}
		alive := false
		for _, j := range ids {
			if j == "" {
				continue
			}
			if job, err := s.sched.Get(ctx, j); err == nil {
				if !job.State.Terminal() {
					alive = true
				}
				s.apply(job) // catch up
			}
		}
		if alive {
			continue
		}
		s.mu.Lock()
		lr := s.live[r.ID]
		changed := lr != nil && lr.rec.State == core.RenderRunning
		if changed {
			lr.rec.State = core.RenderFailed
			if lr.body.Error == nil {
				lr.body.Error = &jobs.Error{Code: jobs.CodeInterrupted, Message: "AudioSmith stopped while the book rendered"}
			}
			for i := range lr.body.Chapters {
				if c := &lr.body.Chapters[i]; c.State == ChapterPending || c.State == ChapterRendering {
					c.State = ChapterStopped
				}
			}
		}
		s.mu.Unlock()
		if changed {
			s.save(ctx, r.ID)
			s.publish(r.ID)
		}
	}
	return nil
}

// load returns a render, from memory when it is followed.
func (s *Longform) load(ctx context.Context, id string) (core.Render, RenderBody, error) {
	s.mu.Lock()
	if lr := s.live[id]; lr != nil {
		rec, body := lr.rec, lr.body
		body.Chapters = slices.Clone(body.Chapters)
		s.mu.Unlock()
		return rec, body, nil
	}
	s.mu.Unlock()
	rec, err := s.renders.Get(ctx, id)
	if err != nil {
		return rec, RenderBody{}, err
	}
	body, err := DecodeRender(rec)
	return rec, body, err
}

func (s *Longform) forget(id string) {
	s.mu.Lock()
	delete(s.live, id)
	s.mu.Unlock()
}

// snapshot is a followed render with its published body.
func (s *Longform) snapshot(id string) (core.Render, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lr := s.live[id]
	if lr == nil {
		return core.Render{}, false
	}
	return lr.published(), true
}

// published is the render with its body without the manifest's chapters
// and voices. Caller holds mu.
func (lr *liveRender) published() core.Render {
	b := lr.body
	b.Spec = LongformSpec{Project: b.Spec.Project, Kind: b.Spec.Kind, Title: b.Spec.Title, Model: b.Spec.Model,
		Lang: b.Spec.Lang, Format: b.Spec.Format, Stems: b.Spec.Stems, Book: b.Spec.Book, Production: b.Spec.Production,
		Audition: b.Spec.Audition}
	b.Chapters = slices.Clone(b.Chapters)
	rec := lr.rec
	rec.Body, _ = json.Marshal(b)
	return rec
}

// save writes a followed render's record.
func (s *Longform) save(ctx context.Context, id string) error {
	s.mu.Lock()
	lr := s.live[id]
	if lr == nil {
		s.mu.Unlock()
		return nil
	}
	rec := lr.rec
	rec.UpdatedAt = s.now()
	lr.rec.UpdatedAt = rec.UpdatedAt
	body, err := json.Marshal(lr.body)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	rec.Body = body
	if err := s.renders.Put(ctx, rec); err != nil {
		s.logf("render %s: %v", id, err)
		return err
	}
	return nil
}

func (s *Longform) publish(id string) {
	if r, ok := s.snapshot(id); ok {
		s.bus.Publish(core.RenderChanged{Render: r})
	}
}

// onJob is the jobs.Updated handler: updates of longform jobs are applied
// in order, off the publisher's goroutine.
func (s *Longform) onJob(u jobs.Updated) {
	if u.Chunk != nil || !strings.HasPrefix(u.Job.Owner, OwnerLongform) {
		return
	}
	s.qmu.Lock()
	if s.closed {
		s.qmu.Unlock()
		return
	}
	s.queue = append(s.queue, u.Job)
	if s.draining {
		s.qmu.Unlock()
		return
	}
	s.draining = true
	s.drained.Add(1)
	s.qmu.Unlock()
	s.async(s.drain)
}

func (s *Longform) drain() {
	defer s.drained.Done()
	for {
		s.qmu.Lock()
		if len(s.queue) == 0 || s.closed {
			s.draining = false
			s.qmu.Unlock()
			return
		}
		j := s.queue[0]
		s.queue = s.queue[1:]
		s.qmu.Unlock()
		s.apply(j)
	}
}

// apply folds one job update into its render.
func (s *Longform) apply(j jobs.Job) {
	id := strings.TrimPrefix(j.Owner, OwnerLongform)
	ctx := context.Background()
	s.mu.Lock()
	lr := s.live[id]
	s.mu.Unlock()
	if lr == nil {
		rec, err := s.renders.Get(ctx, id)
		if err != nil {
			return // a render deleted meanwhile
		}
		body, err := DecodeRender(rec)
		if err != nil {
			s.logf("render %s: %v", id, err)
			return
		}
		s.mu.Lock()
		if lr = s.live[id]; lr == nil {
			lr = &liveRender{rec: rec, body: body}
			s.live[id] = lr
		}
		s.mu.Unlock()
	}

	s.mu.Lock()
	b := &lr.body
	before := lr.rec.State
	durable := false
	revive := func() {
		if lr.rec.State != core.RenderRunning && !j.State.Terminal() && j.State != jobs.StateInterrupted {
			// The scheduler runs a job of a render recorded as ended (re-run
			// after a restart): it renders again.
			lr.rec.State, b.Finished, b.Error = core.RenderRunning, time.Time{}, nil
		}
	}
	switch j.Kind {
	case KindChapter:
		i := s.chapterOf(b, j)
		if i < 0 {
			s.mu.Unlock()
			return
		}
		revive()
		durable = applyChapter(&b.Chapters[i], j, s.now())
	case KindAssemble:
		if b.AssembleJob == "" {
			b.AssembleJob = j.ID
		}
		if b.AssembleJob != j.ID {
			s.mu.Unlock()
			return
		}
		revive()
		switch j.State {
		case jobs.StateRunning:
			durable = !b.Assembling
			b.Assembling = true
			if j.Progress.Fraction >= 0 {
				b.Assembled = j.Progress.Fraction
			}
		case jobs.StateSucceeded:
			var res AssembleResult
			if err := json.Unmarshal(j.Result, &res); err == nil {
				b.Output = &res
				lr.rec.AudioSeconds = res.Seconds
			}
			b.Assembling, b.Assembled, durable = false, 1, true
		case jobs.StateFailed, jobs.StateCancelled:
			b.Assembling, durable = false, true
			if j.Err != nil && j.Err.Code != jobs.CodeDependency && j.Err.Code != jobs.CodeCancelled {
				b.Error = j.Err
			}
		}
	default:
		s.mu.Unlock()
		return
	}
	s.settle(lr)
	if lr.rec.State != before {
		durable = true
	}
	s.mu.Unlock()
	if durable {
		s.save(ctx, id)
	}
	s.publish(id)
}

// chapterOf finds the chapter a job renders: by its id, else by its
// params (an update that came before Submit returned). -1 for a job of
// an earlier run. Caller holds mu.
func (s *Longform) chapterOf(b *RenderBody, j jobs.Job) int {
	for i, c := range b.Chapters {
		if c.Job == j.ID {
			return i
		}
	}
	var p struct {
		Index int `json:"index"`
	}
	if err := json.Unmarshal(j.Params, &p); err != nil || p.Index < 0 || p.Index >= len(b.Chapters) {
		return -1
	}
	if c := &b.Chapters[p.Index]; c.Job == "" {
		c.Job = j.ID
		return p.Index
	}
	return -1
}

// applyChapter folds a chapter job's update; it reports whether the
// chapter's state changed (worth saving).
func applyChapter(c *RenderChapter, j jobs.Job, now time.Time) bool {
	old := c.State
	switch j.State {
	case jobs.StateWaiting, jobs.StateQueued:
		c.State = ChapterPending
	case jobs.StateRunning:
		c.State = ChapterRendering
		if c.Started.IsZero() || old != ChapterRendering {
			c.Started = j.Started
			if c.Started.IsZero() {
				c.Started = now
			}
		}
		if j.Progress.Fraction >= 0 {
			c.Fraction = j.Progress.Fraction
		}
		c.AudioSeconds = j.Progress.AudioSeconds
	case jobs.StateSucceeded:
		var r ChapterResult
		json.Unmarshal(j.Result, &r)
		c.State, c.Fraction, c.Error = ChapterDone, 1, nil
		if r.Reused || (r.Chunks > 0 && r.Cached == r.Chunks) {
			c.State = ChapterCached
		}
		c.File, c.AudioSeconds, c.Seconds, c.Chunks, c.Cached = r.Output, r.AudioSeconds, r.Seconds, r.Chunks, r.Cached
		c.Finished = j.Finished
		if c.Finished.IsZero() {
			c.Finished = now
		}
	case jobs.StateFailed:
		c.State, c.Error = ChapterFailed, j.Err
		c.Finished = now
	case jobs.StateCancelled:
		c.State = ChapterStopped
		c.Finished = now
	case jobs.StateInterrupted:
		c.State = ChapterStopped
		if j.Idempotent && j.Err != nil && j.Err.Code == jobs.CodeShutdown {
			c.State = ChapterPending // the app quits: it runs again at the next start
		}
	}
	return c.State != old
}

// settle decides a render's state from its jobs. Caller holds mu.
func (s *Longform) settle(lr *liveRender) {
	b := &lr.body
	if lr.rec.State != core.RenderRunning {
		return
	}
	for _, c := range b.Chapters {
		if c.State == ChapterPending || c.State == ChapterRendering {
			return
		}
	}
	allMade := b.Done() == len(b.Chapters)
	switch {
	case lr.rec.Audition && allMade:
		lr.rec.State = core.RenderDone
	case lr.rec.Audition:
		lr.rec.State = core.RenderFailed
	case b.Output != nil && allMade:
		lr.rec.State = core.RenderDone
	case b.Assembling || (allMade && b.Error == nil && !b.Stopping && b.AssembleJob != ""):
		return // the book is being assembled (or about to be)
	case b.Stopping:
		lr.rec.State = core.RenderStopped
	default:
		lr.rec.State = core.RenderFailed
	}
	if lr.rec.State != core.RenderRunning {
		b.Finished = s.now()
		b.Stopping = false
		if lr.rec.State == core.RenderDone && lr.rec.Audition {
			for _, c := range b.Chapters {
				lr.rec.AudioSeconds += c.AudioSeconds
			}
		}
	}
}

// ETA estimates the seconds a render still needs from the characters done
// so far and the time they took ("" when too early to say). Pure.
func ETA(b *RenderBody, now time.Time) (time.Duration, bool) {
	var doneChars, leftChars float64
	var spent time.Duration
	for _, c := range b.Chapters {
		switch c.State {
		case ChapterDone:
			doneChars += float64(c.Chars)
			spent += c.Finished.Sub(c.Started)
		case ChapterRendering:
			doneChars += float64(c.Chars) * c.Fraction
			leftChars += float64(c.Chars) * (1 - c.Fraction)
			if !c.Started.IsZero() {
				spent += now.Sub(c.Started)
			}
		case ChapterPending, ChapterFailed, ChapterStopped:
			leftChars += float64(c.Chars)
		}
	}
	if doneChars < 1 || spent <= 0 {
		return 0, false
	}
	per := float64(spent) / doneChars
	return time.Duration(per * leftChars), true
}
