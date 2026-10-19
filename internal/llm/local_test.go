package llm

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hehehai/AudioSmith/internal/engine/proto"
)

// The engine's typed errors map to llm codes, and JSON requests carry
// their schema to the engine.
func TestEngineBackendProtocol(t *testing.T) {
	var got GenerateParams
	var fail *proto.Error
	eb := &EngineBackend{DefaultModel: "m", Call: func(_ context.Context, _ string, params any, _ func(string, json.RawMessage)) (json.RawMessage, error) {
		got = params.(GenerateParams)
		if fail != nil {
			return nil, fail
		}
		return json.RawMessage(`{"text":"{\"a\":1}","finishReason":"stop","constrained":true}`), nil
	}}
	resp, err := eb.Chat(context.Background(), Request{Messages: []Message{{Role: User, Content: "x"}}, Format: FormatJSON, Schema: Object(map[string]*Schema{"a": Integer()}, "a")})
	if err != nil || resp.Text != `{"a":1}` || !got.JSON || len(got.Schema) == 0 || got.Messages[0].Role != "system" || got.Messages[1].Content != "x" {
		t.Fatalf("%v %+v %+v", err, resp, got)
	}
	for code, want := range map[string]string{
		proto.CodeContextTooLong:     CodeContextLength,
		proto.CodeModelNotDownloaded: CodeModelNotFound,
		proto.CodeBadRequest:         CodeBadRequest,
		proto.CodeUnsupported:        CodeUnsupported,
	} {
		fail = &proto.Error{Code: code, Message: "no"}
		if _, err := eb.Chat(context.Background(), Request{}); Code(err) != want {
			t.Errorf("%s → %v, want %s", code, err, want)
		}
	}
	fail = &proto.Error{Code: proto.CodeBusy, Message: "busy"}
	if _, err := eb.Chat(context.Background(), Request{}); Code(err) != CodeServer {
		t.Errorf("busy → %v", err)
	}
}

// A JSON request is greedy on the engine; its repair turns are sampled
// (warm enough to leave the greedy answer), the same sample on a rerun.
func TestEngineBackendRepairSampled(t *testing.T) {
	var got []GenerateParams
	replies := []string{`{"a":"x"}`, `{"a":"y"}`, `{"a":1}`}
	eb := &EngineBackend{DefaultModel: "m", Call: func(_ context.Context, _ string, params any, _ func(string, json.RawMessage)) (json.RawMessage, error) {
		got = append(got, params.(GenerateParams))
		b, _ := json.Marshal(map[string]any{"text": replies[len(got)-1], "finishReason": "stop", "constrained": true})
		return b, nil
	}}
	c := &Client{Backend: eb}
	var out struct{ A int }
	resp, err := c.JSON(context.Background(), Request{Messages: []Message{{Role: User, Content: "x"}}, Temperature: F64(0.1),
		Schema: Object(map[string]*Schema{"a": Integer()}, "a")}, &out, 2)
	if err != nil || out.A != 1 || resp.Repairs != 2 || len(got) != 3 {
		t.Fatalf("%v %+v %+v", err, resp, got)
	}
	if got[0].SampleConstrained || *got[0].Temperature != 0.1 || got[0].Seed != nil {
		t.Errorf("first turn %+v", got[0])
	}
	for _, p := range got[1:] {
		if !p.SampleConstrained || *p.Temperature != RepairTemperature || *p.TopP != 0.95 || *p.Seed != 1 {
			t.Errorf("repair turn %+v", p)
		}
	}
	// A free reply is never sampled as constrained.
	got = nil
	replies = []string{"hi"}
	eb.Chat(context.Background(), Request{Messages: []Message{{Role: User, Content: "x"}}, SampleConstrained: true})
	if got[0].SampleConstrained || got[0].Temperature != nil {
		t.Errorf("free %+v", got[0])
	}
}
