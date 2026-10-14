package main

// The smoke suite's case for language models (catalogue task llm).

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
)

// smokeLLM: a sentence translated into Chinese and Spanish, and JSON
// constrained to a schema; tokens per second as the RTF column.
func smokeLLM(e catalog.Entry, snap string, call func(string, any, any) error, add func(smokeRow)) {
	zero := 0.0
	han := func(s string) bool {
		return strings.IndexFunc(s, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0
	}
	for _, tc := range []struct {
		to    string
		check func(string) bool
	}{
		{"Chinese (Simplified)", han},
		{"Spanish", func(s string) bool { return strings.Contains(strings.ToLower(s), "parque") }},
	} {
		var r proto.LLMGenerateResult
		err := call(proto.MethodLLMGenerate, proto.LLMGenerateParams{Model: e.Repo, ModelPath: snap, Temperature: &zero, MaxTokens: 120, Messages: []proto.LLMMessage{
			{Role: "system", Content: "You translate from English to " + tc.to + ". Reply with the translation only."},
			{Role: "user", Content: "The weather is lovely today, so let's walk to the park."}}}, &r)
		row := smokeRow{Case: "translate-" + strings.Fields(tc.to)[0], Seconds: r.Seconds, RTF: r.TokensPerSecond, Heard: r.Text}
		switch {
		case err != nil:
			row.Note = err.Error()
		case !tc.check(r.Text) || r.FinishReason != "stop":
			row.Note = fmt.Sprintf("finish %s", r.FinishReason)
		default:
			row.Pass = true
			row.Note = fmt.Sprintf("%d+%d tokens, RTF = tokens/s", r.PromptTokens, r.CompletionTokens)
		}
		add(row)
	}
	var r proto.LLMGenerateResult
	err := call(proto.MethodLLMGenerate, proto.LLMGenerateParams{Model: e.Repo, ModelPath: snap, JSON: true, MaxTokens: 200,
		Schema:   json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"},"country":{"type":"string"}},"required":["city","country"],"additionalProperties":false}`),
		Messages: []proto.LLMMessage{{Role: "user", Content: "Which city is the capital of France? Answer as JSON with city and country."}}}, &r)
	var v struct{ City, Country string }
	row := smokeRow{Case: "json", Seconds: r.Seconds, RTF: r.TokensPerSecond, Heard: r.Text}
	switch {
	case err != nil:
		row.Note = err.Error()
	case json.Unmarshal([]byte(r.Text), &v) != nil || !strings.Contains(v.City, "Paris"):
		row.Note = "not the JSON asked for"
	default:
		row.Pass = true
		row.Note = fmt.Sprintf("constrained %v", r.Constrained)
	}
	add(row)
}
