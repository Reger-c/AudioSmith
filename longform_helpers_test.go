package main

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/hehehai/AudioSmith/internal/dsp/dsptest"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/text"
)

// lfSynth plays tts.synthesize for longform renders in view tests: a tone
// as long as the text should take, as a float WAV.
type lfSynth struct {
	mu    sync.Mutex
	texts []string
}

func (s *lfSynth) Run(ctx context.Context, job jobs.Job, report func(jobs.Progress), _ func(json.RawMessage)) (json.RawMessage, error) {
	var p proto.SynthesizeParams
	if err := json.Unmarshal(job.Params, &p); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.texts = append(s.texts, p.Text)
	s.mu.Unlock()
	w, c := text.Count(p.Text)
	secs := max(text.Estimate(w, c).Seconds(), 0.3)
	buf := dsptest.Concat(dsptest.Silence(24000, 1, 0.05), dsptest.Sine(24000, 1, 220, -14, secs, 0), dsptest.Silence(24000, 1, 0.1))
	if err := wav.WriteFile(p.Output.Path, buf, wav.Float32, wav.Options{}); err != nil {
		return nil, err
	}
	seed := int64(1)
	return json.Marshal(proto.SynthesizeResult{Output: p.Output.Path, Model: p.Model, SampleRate: 24000, Channels: 1,
		Frames: int64(buf.Frames()), AudioSeconds: buf.Seconds(), Seconds: 0.01, Seed: &seed})
}

// useLfSynth makes renders of a test app use an lfSynth.
func useLfSynth(a *app) *lfSynth {
	s := &lfSynth{}
	a.lib.useEngine(func() jobs.Executor { return s })
	return s
}

// lfJobs are the longform jobs submitted and not finished yet, in order.
func lfJobs(eng *fakeEngine) []sentReq {
	var out []sentReq
	for _, r := range eng.sent {
		if r.Spec.Kind != services.KindChapter && r.Spec.Kind != services.KindAssemble {
			continue
		}
		if j, ok := eng.jobs[r.ID]; ok && !j.State.Terminal() {
			out = append(out, r)
		}
	}
	return out
}

// playLongform runs the longform jobs the app submitted, chapters first,
// as the scheduler would.
func playLongform(t *testing.T, a *app, eng *fakeEngine) {
	t.Helper()
	for _, r := range lfJobs(eng) {
		ex := a.lib.svc.Longform.ChapterExecutor()
		if r.Spec.Kind == services.KindAssemble {
			ex = a.lib.svc.Longform.AssembleExecutor()
		}
		eng.run(r.ID, ex)
		if j := eng.jobs[r.ID]; j.State != jobs.StateSucceeded {
			t.Fatalf("%s %s: %s %+v", r.Spec.Kind, r.ID, j.State, j.Err)
		}
	}
}

// chapterParams decodes a chapter job's params.
func chapterParams(t *testing.T, r sentReq) services.ChapterParams {
	t.Helper()
	var p services.ChapterParams
	if err := json.Unmarshal(r.Spec.Params, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// hasAnyText reports whether one of the texts shows.
func hasAnyText(texts []string, s string) bool {
	for _, x := range texts {
		if strings.Contains(x, s) {
			return true
		}
	}
	return false
}

func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }
