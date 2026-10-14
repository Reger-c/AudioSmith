package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
)

// llm.generate as a job kind: streamed tokens arrive as chunks, in order,
// before the result; a prompt over the context fails with context_too_long.
func TestLLMExecutor(t *testing.T) {
	sup, w := newTestSup(t, "", allRoles)
	w.until(t, "ready", ready)
	ctx := context.Background()
	run := func(id string, p proto.LLMGenerateParams) (proto.LLMGenerateResult, string, *jobs.Error) {
		raw, _ := json.Marshal(p)
		var streamed strings.Builder
		res, err := KindExecutor(sup, proto.MethodLLMGenerate).Run(ctx, jobs.Job{ID: id, Spec: jobs.Spec{Params: raw}}, func(jobs.Progress) {},
			func(c json.RawMessage) {
				var tk proto.LLMTokenParams
				if json.Unmarshal(c, &tk) == nil {
					streamed.WriteString(tk.Text)
				}
			})
		var je *jobs.Error
		if errors.As(err, &je) {
			return proto.LLMGenerateResult{}, "", je
		}
		if err != nil {
			t.Fatal(err)
		}
		var r proto.LLMGenerateResult
		json.Unmarshal(res, &r)
		return r, streamed.String(), nil
	}
	msgs := []proto.LLMMessage{{Role: "system", Content: "Translate."}, {Role: "user", Content: "Hello there, friend."}}
	r, streamed, e := run("L1", proto.LLMGenerateParams{Model: "mlx-community/Qwen3-4B-Instruct-2507-4bit", Messages: msgs, Stream: true})
	if e != nil || r.Text != "echo: Hello there, friend." || streamed != r.Text || r.FinishReason != "stop" || r.CompletionTokens == 0 {
		t.Fatalf("stream: %+v %q %v", r, streamed, e)
	}
	r, _, e = run("L2", proto.LLMGenerateParams{Model: "qwen3", Messages: []proto.LLMMessage{{Role: "user", Content: `{"a":1}`}}, JSON: true, Schema: json.RawMessage(`{"type":"object"}`)})
	if e != nil || r.Text != `{"a":1}` || !r.Constrained {
		t.Fatalf("json: %+v %v", r, e)
	}
	_, _, e = run("L3", proto.LLMGenerateParams{Model: "qwen3", Messages: []proto.LLMMessage{{Role: "user", Content: strings.Repeat("word ", 20000)}}})
	if e == nil || e.Code != proto.CodeContextTooLong || !strings.Contains(string(e.Data), `"limit":16384`) {
		t.Fatalf("too long: %+v", e)
	}
}
