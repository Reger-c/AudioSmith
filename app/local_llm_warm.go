package main

// Warming the engine's language model (thread #47, from #44 §4.3–4.4).
// Dictation clean-up (4 s) and the Tools direction coach run within a
// short budget; a model that still has to load misses it every time on
// a cold start. So the model is loaded (a models.load job on the gpu
// lane, task "llm") when the feature is about to be used: dictation
// starts recording with clean-up on, clean-up is switched on, or a note
// is typed in the direction coach. Nothing loads at app start. A call
// that finds the model cold gets a longer budget (and the UI says it is
// loading); if it still runs out, the deterministic result is used as
// before (dictation types the polished text, the coach reads keywords)
// and the warm-up continues for the next one.

import (
	"context"
	"encoding/json"
	"time"

	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/llm"
	"github.com/hehehai/AudioSmith/internal/translate"
)

const (
	// llmWarmRetry: a warm-up whose model is still not resident after
	// this is tried again (it failed, or the model was unloaded since).
	llmWarmRetry = 90 * time.Second
	// coldCleanupBudget is dictation clean-up's budget when the model is
	// not loaded yet (translate.DefaultCleanupBudget when it is).
	coldCleanupBudget = 10 * time.Second
	// coachBudget and coldCoachBudget bound the direction coach.
	coachBudget     = translate.DefaultDirectionBudget
	coldCoachBudget = 15 * time.Second
	// llmWarmOwner owns the warm-up jobs.
	llmWarmOwner = "llm:warm"
)

// llmWarmState follows warm-ups (main thread).
type llmWarmState struct {
	warmRepo string    // the model a warm-up was last asked for
	warmAt   time.Time // when
}

// localLLMFirst reports whether skill goes to the engine's language model
// first.
func (a *app) localLLMFirst(s llm.Skill) bool {
	if a.llmRouter == nil || a.llmReg == nil {
		return false
	}
	rs := a.llmRouter.Routes(s)
	return len(rs) > 0 && rs[0].Provider == localProvider
}

// localLLMCold reports whether skill would wait for the engine's language
// model to load: it goes there first and the gpu worker does not hold it.
func (a *app) localLLMCold(s llm.Skill) bool {
	if !a.localLLMFirst(s) {
		return false
	}
	repo := a.localLLMEntry().Repo
	return repo != "" && !containsString(a.loaded, repo)
}

// warmLocalLLM loads the engine's language model ahead of skill when
// skill would find it cold; a no-op when it is resident, routed
// elsewhere, or a warm-up for it started less than llmWarmRetry ago.
// Main thread; cheap enough to call while building a view.
func (a *app) warmLocalLLM(s llm.Skill) {
	if a.engState != engineReady || a.lib == nil || a.lib.sched == nil || !a.localLLMCold(s) {
		return
	}
	repo := a.localLLMEntry().Repo
	if a.warmRepo == repo && time.Since(a.warmAt) < llmWarmRetry {
		return
	}
	a.warmRepo, a.warmAt = repo, time.Now()
	params, _ := json.Marshal(proto.LoadParams{Model: repo, Task: "llm"})
	sched := a.lib.sched
	a.async(func() {
		sched.Submit(context.Background(), jobs.Spec{Kind: proto.MethodModelsLoad, Lane: jobs.LaneGPU, Priority: jobs.PriorityInteractive,
			Params: params, Title: i18n.T("Load model"), Owner: llmWarmOwner})
	})
}
