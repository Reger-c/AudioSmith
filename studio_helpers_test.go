package main

import (
	"encoding/json"
	"testing"

	"github.com/hehehai/AudioSmith/internal/services"
)

// renderOf decodes the render job the Studio submitted.
func renderOf(t *testing.T, r sentReq) services.RenderParams {
	t.Helper()
	if r.Spec.Kind != services.KindRender {
		t.Fatalf("not a render: %+v", r)
	}
	var p services.RenderParams
	if err := json.Unmarshal(r.Spec.Params, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// finishRender plays the render job of r to its end: the take's WAV
// (seconds long) is written and the job succeeds.
func finishRender(t *testing.T, eng *fakeEngine, r sentReq, seconds float64) services.RenderParams {
	t.Helper()
	p := renderOf(t, r)
	writeTestWAV(t, p.Output, 24000, seconds)
	res := services.RenderResult{Output: p.Output, SampleRate: 24000, Channels: 1, AudioSeconds: seconds, Seconds: seconds / 4}
	if p.Take.Render != nil {
		res.Render = *p.Take.Render
	}
	eng.succeed(r.ID, res)
	return p
}
