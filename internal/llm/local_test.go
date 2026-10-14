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
