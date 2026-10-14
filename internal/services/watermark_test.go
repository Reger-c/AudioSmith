package services

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/jobs"
)

// A finished take is watermarked in place, in its bit depth; a failure
// leaves it unmarked with a notice, and the render still succeeds.
func TestRenderWatermarks(t *testing.T) {
	e := newRenderEnv(t, kokoroInfo)
	var gotPath string
	var gotBits int
	e.wm = func(_ context.Context, path string, bits int) (bool, error) {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("the take is not written yet: %v", err)
		}
		gotPath, gotBits = path, bits
		return true, nil
	}
	j, tk := e.generate(t, core.Take{Text: "Hello there.", Model: "m/kokoro", Voice: "af_heart", Render: &core.TakeRender{Bits: 24}})
	if j.State != jobs.StateSucceeded || !tk.Render.Watermarked || gotPath == "" || gotBits != 24 {
		t.Fatalf("job %v, render %+v, watermark %q %d", j.State, tk.Render, gotPath, gotBits)
	}
	e.wm = func(context.Context, string, int) (bool, error) { return false, errors.New("engine gone") }
	j, tk = e.generate(t, core.Take{Text: "Hello again.", Model: "m/kokoro", Voice: "af_heart"})
	if j.State != jobs.StateSucceeded || tk.Render.Watermarked {
		t.Fatalf("job %v, render %+v", j.State, tk.Render)
	}
	found := false
	for _, w := range tk.Render.Warnings {
		found = found || w.Code == NoticeWatermarkFailed
	}
	if !found {
		t.Errorf("warnings %+v", tk.Render.Warnings)
	}
}

// A dub marks its final tracks (mix and speech stem) after fitting, not
// its lines.
func TestDubWatermarksTracks(t *testing.T) {
	e := newDubEnv(t, 3)
	p := e.create(t, func(p *dub.Project) { p.Targets = []string{"es"} })
	p = e.run(t, p.ID, RunRequest{Through: dub.StageMix})
	tr := p.Track("es")
	e.mu.Lock()
	marked := slices.Clone(e.marked)
	e.mu.Unlock()
	want := []string{e.dubs.Path(p.ID, tr.File), e.dubs.Path(p.ID, tr.Speech)}
	if tr.File == "" || !slices.Equal(marked, want) {
		t.Fatalf("marked %q, want %q", marked, want)
	}
}
