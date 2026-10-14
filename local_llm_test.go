package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/llm"
	"github.com/hehehai/AudioSmith/internal/translate"
)

// The engine's language model joins writing help once it is downloaded:
// first in the automatic routing (before Ollama), its translations on the
// gpu lane, and Settings → Writing help offers it.
func TestLocalLLMProvider(t *testing.T) {
	m := newM2Test(t)
	a, tt := m.a, m.tt
	e, _ := catalog.Builtin().ByID(catalog.DefaultLLM)
	var got llm.GenerateParams
	a.localLLMHook = func(_ context.Context, method string, params any, notify func(string, json.RawMessage)) (json.RawMessage, error) {
		got = params.(llm.GenerateParams)
		return json.RawMessage(`{"text":"{\"lines\":[{\"id\":\"1\",\"text\":\"Hola.\"}]}","finishReason":"stop","promptTokens":20,"completionTokens":9}`), nil
	}
	a.llmOllama = []string{"qwen3:4b"}
	a.setLocal(e, hub.Local{State: hub.Partial}, nil)
	if rs := a.llmRouter.Routes(llm.SkillTranslate); len(rs) != 1 || rs[0].Provider != "ollama" || translateLane(a.llmRouter) != jobs.LaneNet {
		t.Fatalf("not downloaded: %+v", rs)
	}
	a.navigate("/settings/writing")
	tt.Frame()
	if !tt.HasText("On this Mac (AudioSmith)") || !tt.HasText("Download") {
		t.Fatalf("settings: %q", tt.Texts())
	}

	// Downloaded: first, then Ollama.
	a.setLocal(e, hub.Local{State: hub.Downloaded, Snapshot: t.TempDir()}, nil)
	rs := a.llmRouter.Routes(llm.SkillTranslate)
	if len(rs) != 2 || rs[0].Provider != localProvider || rs[1].Provider != "ollama" || translateLane(a.llmRouter) != jobs.LaneGPU {
		t.Fatalf("routes %+v", rs)
	}
	tt.Frame()
	if ok, who := a.llmAvailable(); !ok || who != "On this Mac (AudioSmith) · "+e.Label || !tt.HasText("Ready. Automatic writing help uses it first.") {
		t.Fatalf("available %v %q %q", ok, who, tt.Texts())
	}
	res, err := (&translate.LLM{Router: a.llmRouter}).Translate(context.Background(), translate.Request{Source: "en", Target: "es",
		Segments: []translate.Segment{{ID: "1", Text: "Hello."}}})
	if err != nil || res.Segments[0].Text != "Hola." || got.Model != e.Repo || !got.JSON || len(got.Schema) == 0 {
		t.Fatalf("translate %+v %v %+v", res, err, got)
	}
	// Keep text on this Mac: the engine's model still counts.
	a.llmForm.local = true
	a.prefs.Privacy.LocalOnlyLLM = true
	a.applyLLM()
	if rs := a.llmRouter.Routes(llm.SkillDictationCleanup); len(rs) == 0 || rs[0].Provider != localProvider {
		t.Errorf("local only: %+v", rs)
	}
	// Choosing it by hand.
	a.chooseLLMRoute(localProvider)
	if rs := a.llmRouter.Routes(llm.SkillTranslate); len(rs) != 1 || rs[0].Provider != localProvider {
		t.Errorf("chosen: %+v", rs)
	}
}
