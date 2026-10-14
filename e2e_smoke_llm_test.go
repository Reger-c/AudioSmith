package main

// The smoke suite's cases for language models (catalogue task llm) and the
// watermark (task watermark).

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/watermark"
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

// smokeWatermark: the reference take watermarked with AudioSmith's message
// and found again; the clean take not.
func smokeWatermark(e catalog.Entry, snap, ref, outDir string, call func(string, any, any) error, add func(smokeRow)) {
	r := smokeRow{Case: "watermark"}
	dir, err := watermark.Prepare(snap, filepath.Join(outDir, "audioseal"))
	if err != nil {
		r.Note = err.Error()
		add(r)
		return
	}
	out := filepath.Join(outDir, e.ID+"-wm.wav")
	msg := watermark.Message(1)
	var er proto.WatermarkEmbedResult
	if err := call(proto.MethodWatermarkEmbed, proto.WatermarkEmbedParams{Model: e.Repo, ModelPath: dir, Input: ref, Output: proto.Output{Path: out}, Message: msg}, &er); err != nil {
		r.Note = err.Error()
		add(r)
		return
	}
	var on, off proto.WatermarkDetectResult
	err1 := call(proto.MethodWatermarkDetect, proto.WatermarkDetectParams{Model: e.Repo, ModelPath: dir, Input: out}, &on)
	err2 := call(proto.MethodWatermarkDetect, proto.WatermarkDetectParams{Model: e.Repo, ModelPath: dir, Input: ref}, &off)
	r.Seconds, r.AudioSeconds, r.Output = er.Seconds, er.AudioSeconds, out
	r.RTF = er.AudioSeconds / max(er.Seconds, 1e-3)
	r.Note = fmt.Sprintf("SNR %.1f dB; marked p=%.3f %#04x; clean p=%.3f", er.SNR, on.Probability, on.Message, off.Probability)
	r.Pass = err1 == nil && err2 == nil && on.Probability >= 0.9 && on.Message == msg && off.Probability <= 0.1
	add(r)
}
