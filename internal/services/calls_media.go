package services

// Speech for phone calls, through the job queue: the caller's utterances
// are "listen:stt.transcribe" jobs on the listen lane (the listen worker's
// own model, never behind renders), the agent's sentences "take.render"
// jobs (no take is made; the render's streamed chunks are the audio). Each
// job is owned by "call:<id>/<n>", so a call's watcher sees its events
// from the start, before Submit returns.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/telephony"
)

// callWatcher hands the queue's events of call jobs to the waits that
// asked for them by owner. Its bus handler never blocks.
type callWatcher struct {
	mu    sync.Mutex
	waits map[string]*callWait
}

type callWait struct {
	mu     sync.Mutex
	q      []jobs.Updated
	signal chan struct{}
}

func (w *callWatcher) publish(u jobs.Updated) {
	if !strings.HasPrefix(u.Job.Owner, CallOwner) {
		return
	}
	w.mu.Lock()
	x := w.waits[u.Job.Owner]
	w.mu.Unlock()
	if x == nil {
		return
	}
	x.mu.Lock()
	x.q = append(x.q, u)
	x.mu.Unlock()
	select {
	case x.signal <- struct{}{}:
	default:
	}
}

func (w *callWatcher) add(owner string) *callWait {
	x := &callWait{signal: make(chan struct{}, 1)}
	w.mu.Lock()
	w.waits[owner] = x
	w.mu.Unlock()
	return x
}

func (w *callWatcher) remove(owner string) {
	w.mu.Lock()
	delete(w.waits, owner)
	w.mu.Unlock()
}

// next waits for the next event.
func (x *callWait) next(ctx context.Context) (jobs.Updated, error) {
	for {
		x.mu.Lock()
		if len(x.q) > 0 {
			u := x.q[0]
			x.q = x.q[1:]
			x.mu.Unlock()
			return u, nil
		}
		x.mu.Unlock()
		select {
		case <-ctx.Done():
			return jobs.Updated{}, ctx.Err()
		case <-x.signal:
		}
	}
}

// follow submits spec under a fresh owner of the call and follows it to
// its end, handing chunks to onChunk. Cancelling ctx cancels the job.
func (s *Calls) follow(ctx context.Context, callID string, spec jobs.Spec, onChunk func(proto.ChunkParams) error) (jobs.Job, error) {
	if s.cfg.Scheduler == nil {
		return jobs.Job{}, fmt.Errorf("calls: no job queue: %w", core.ErrUnsupported)
	}
	s.mu.Lock()
	s.seq++
	owner := fmt.Sprintf("%s%s/%d", CallOwner, callID, s.seq)
	s.mu.Unlock()
	spec.Owner = owner
	x := s.watch.add(owner)
	defer s.watch.remove(owner)
	id, err := s.cfg.Scheduler.Submit(ctx, spec)
	if err != nil {
		return jobs.Job{}, err
	}
	cancel := func() {
		c, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		s.cfg.Scheduler.Cancel(c, id)
	}
	for {
		u, err := x.next(ctx)
		if err != nil {
			cancel()
			return jobs.Job{}, err
		}
		if u.Job.ID != id {
			continue
		}
		if u.Chunk != nil && onChunk != nil {
			var c proto.ChunkParams
			if json.Unmarshal(u.Chunk, &c) == nil && len(c.Data) > 0 {
				if err := onChunk(c); err != nil {
					cancel()
					return u.Job, err
				}
			}
			continue
		}
		if u.Job.State.Terminal() || u.Job.State == jobs.StateInterrupted {
			return u.Job, nil
		}
	}
}

// jobErr is a finished job's failure, or nil when it succeeded.
func jobErr(j jobs.Job) error {
	switch {
	case j.State == jobs.StateSucceeded:
		return nil
	case j.Err != nil:
		return j.Err
	case j.State == jobs.StateCancelled:
		return context.Canceled
	}
	return fmt.Errorf("job %s ended %s", j.ID, j.State)
}

// transcribe turns an utterance (16 kHz) into text on the listen worker.
func (s *Calls) transcribe(ctx context.Context, callID, model, language string, pcm []float32) (string, error) {
	if model == "" {
		return "", errors.New("no speech-to-text model")
	}
	dir := filepath.Join(s.cfg.CacheDir, "calls")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, callID+"-*.wav")
	if err != nil {
		return "", err
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)
	if err := wav.WriteFile(path, dsp.Buffer{Rate: telephony.ListenRate, Channels: 1, Samples: pcm}, wav.PCM16, wav.Options{}); err != nil {
		return "", err
	}
	params, _ := json.Marshal(proto.TranscribeParams{Model: model, Audio: path, Language: language})
	j, err := s.follow(ctx, callID, jobs.Spec{Kind: proto.KindFor(proto.RoleListen, proto.MethodSTTTranscribe), Lane: jobs.LaneListen,
		Priority: jobs.PriorityInteractive, Params: params, Title: i18n.T("Call: listen")}, nil)
	if err != nil {
		return "", err
	}
	if err := jobErr(j); err != nil {
		return "", err
	}
	var res proto.TranscribeResult
	if err := json.Unmarshal(j.Result, &res); err != nil {
		return "", err
	}
	return strings.TrimSpace(res.Text), nil
}

// speak renders text in a recipe's voice and hands the audio to out as
// the render streams it (a sentence at a time).
func (s *Calls) speak(ctx context.Context, callID string, recipe core.Take, out func(pcm []float32, rate int) error) error {
	t, err := s.cfg.Takes.check(ctx, recipe)
	if err != nil {
		return err
	}
	dir := filepath.Join(s.cfg.CacheDir, "calls")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	t.ID = core.NewID(s.cfg.Now())
	t.File = t.ID + ".wav"
	// No mastering: phone audio is levelled by the agent, and the job
	// should end as soon as its audio is out.
	t.Render = &core.TakeRender{Bits: 16, SeedPinned: t.Seed != 0}
	info := s.cfg.Takes.model(t.Model)
	output := filepath.Join(dir, t.File)
	defer os.Remove(output)
	params, err := json.Marshal(RenderParams{Take: t, Lang: info.Lang(t.Language, t.Voice), Output: output, Stream: true})
	if err != nil {
		return err
	}
	frames := 0
	final := false
	rate := 0
	j, err := s.follow(ctx, callID, jobs.Spec{Kind: KindRender, Lane: jobs.LaneGPU, Priority: jobs.PriorityInteractive,
		Params: params, Title: i18n.T("Call: speak")}, func(c proto.ChunkParams) error {
		rate = c.SampleRate
		pcm := telephony.PCM16ToFloat(nil, c.Data, c.Channels)
		frames += len(pcm)
		final = final || c.Final
		return out(pcm, c.SampleRate)
	})
	if err != nil {
		return err
	}
	if err := jobErr(j); err != nil {
		return err
	}
	if final {
		return nil
	}
	// The stream did not end (a render that did not stream): the rest
	// is in the file.
	b, _, err := wav.ReadFile(output)
	if err != nil {
		return nil // what streamed is what there is
	}
	b = dsp.Remix(b, 1)
	if rate != 0 && b.Rate != rate {
		return nil
	}
	if frames < len(b.Samples) {
		return out(b.Samples[frames:], b.Rate)
	}
	return nil
}

// preload warms the call's models (fire and forget).
func (s *Calls) preload(sttModel, ttsModel string) {
	if s.cfg.Scheduler == nil {
		return
	}
	ctx := context.Background()
	if sttModel != "" {
		p, _ := json.Marshal(proto.LoadParams{Model: sttModel, Task: "stt"})
		s.cfg.Scheduler.Submit(ctx, jobs.Spec{Kind: proto.KindFor(proto.RoleListen, proto.MethodModelsLoad), Lane: jobs.LaneListen,
			Priority: jobs.PriorityInteractive, Params: p, Title: i18n.T("Load model"), Owner: CallOwner + "load"})
	}
	if ttsModel != "" {
		p, _ := json.Marshal(proto.LoadParams{Model: ttsModel, Task: "tts"})
		s.cfg.Scheduler.Submit(ctx, jobs.Spec{Kind: proto.MethodModelsLoad, Lane: jobs.LaneGPU,
			Priority: jobs.PriorityInteractive, Params: p, Title: i18n.T("Load model"), Owner: CallOwner + "load"})
	}
}

// PhonePreview renders text in a voice and returns it as a phone carries
// it (8 kHz μ-law, back to 16-bit WAV at 8 kHz) for the setup page.
func (s *Calls) PhonePreview(ctx context.Context, voiceID, text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("phone preview: no text: %w", core.ErrInvalid)
	}
	v, err := s.CallVoice(ctx, voiceID)
	if err != nil {
		return "", err
	}
	recipe, err := s.cfg.Recipe(v, text)
	if err != nil {
		return "", err
	}
	var pcm []float32
	rate := 0
	if err := s.speak(ctx, "preview", recipe, func(p []float32, r int) error {
		rate = r
		pcm = append(pcm, p...)
		return nil
	}); err != nil {
		return "", err
	}
	if rate == 0 || len(pcm) == 0 {
		return "", errors.New("the voice made no audio")
	}
	phone := telephony.PhoneQuality(dsp.Buffer{Rate: rate, Channels: 1, Samples: pcm}, 0)
	dir := filepath.Join(s.cfg.CacheDir, "calls")
	os.MkdirAll(dir, 0o755)
	removeOld(dir, time.Hour)
	out := filepath.Join(dir, "phone-"+core.NewID(s.cfg.Now())+".wav")
	if err := wav.WriteFile(out, phone, wav.PCM16, wav.Options{}); err != nil {
		return "", err
	}
	return out, nil
}
