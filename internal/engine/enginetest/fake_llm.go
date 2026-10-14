package enginetest

// The fake's llm.generate: the reply is the last user message after
// "echo: " (with json or a schema: that message when it is JSON, else
// {}), cut at the first stop string and at maxTokens words. A prompt of
// over FakeLLMContext tokens (4 characters each) with maxTokens fails with
// context_too_long. With stream, each word is an llm.token. Models named
// "slow", "oom" or "crash" behave as for the other methods.

import (
	"encoding/json"
	"strings"

	"github.com/hehehai/AudioSmith/internal/engine/proto"
)

// FakeLLMContext is the context the fake allows, in tokens.
const FakeLLMContext = 16384

func (f *fakeEngine) generate(m proto.Message) {
	var p proto.LLMGenerateParams
	if !f.modelJob(m, &p, func() string { return p.Model }) {
		return
	}
	if len(p.Messages) == 0 {
		f.fail(m.ID, proto.CodeBadRequest, "No messages")
		return
	}
	chars, last := 0, ""
	for _, msg := range p.Messages {
		chars += len(msg.Content)
		if msg.Role == "user" {
			last = msg.Content
		}
	}
	prompt := chars/4 + 4*len(p.Messages)
	maxTokens := p.MaxTokens
	if maxTokens == 0 {
		maxTokens = 2048
	}
	if prompt+maxTokens > FakeLLMContext {
		f.failData(m.ID, proto.CodeContextTooLong, map[string]any{"limit": FakeLLMContext, "actual": prompt + maxTokens, "unit": "tokens"},
			"The prompt (%d tokens) and the reply (%d) are over the %d tokens this model may use", prompt, maxTokens, FakeLLMContext)
		return
	}
	if !f.work(m.ID, p.Model, "generating", func(int, int) {}) {
		return
	}
	text := "echo: " + last
	constrained := false
	if p.JSON || len(p.Schema) > 0 {
		constrained = true
		text = "{}"
		if json.Valid([]byte(strings.TrimSpace(last))) {
			text = strings.TrimSpace(last)
		}
	}
	finish := "stop"
	for _, s := range p.Stop {
		if i := strings.Index(text, s); s != "" && i >= 0 {
			text = text[:i]
		}
	}
	pieces := strings.SplitAfter(text, " ")
	if len(pieces) > maxTokens {
		pieces, finish = pieces[:maxTokens], "length"
		text = strings.Join(pieces, "")
	}
	if p.Stream {
		for _, w := range pieces {
			if w != "" {
				f.notify(proto.MethodLLMToken, proto.LLMTokenParams{Job: m.ID, Text: w})
			}
		}
	}
	f.result(m.ID, proto.LLMGenerateResult{Text: text, FinishReason: finish, Model: p.Model, PromptTokens: prompt,
		CompletionTokens: len(pieces), Seconds: 0.01, TokensPerSecond: float64(len(pieces)) / 0.01, Constrained: constrained})
}
