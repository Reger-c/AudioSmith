package translate

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/llm"
)

// model is a fake LLM backend: handle gets the system and user prompts
// and returns the reply text (or an error).
type model struct {
	mu     sync.Mutex
	handle func(system, user string) (string, error)
	calls  []call
	local  bool
}

type call struct{ System, User string }

func (m *model) Info() llm.BackendInfo {
	return llm.BackendInfo{ID: "fake", Transport: "test", Local: m.local}
}

func (m *model) Chat(ctx context.Context, req llm.Request) (llm.Response, error) {
	var sys, user string
	for _, msg := range req.Messages {
		switch msg.Role {
		case llm.System:
			sys = msg.Content
		case llm.User:
			user = msg.Content // the last user turn (repair turns replace it)
		}
	}
	m.mu.Lock()
	m.calls = append(m.calls, call{sys, user})
	h := m.handle
	m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	text, err := h(sys, user)
	if err != nil {
		return llm.Response{}, err
	}
	return llm.Response{Text: text, Provider: "fake", Usage: llm.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}}, nil
}

func (m *model) Models(context.Context) ([]llm.ModelInfo, error) { return nil, nil }

func (m *model) history() []call {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]call(nil), m.calls...)
}

func router(m *model) *llm.Router {
	reg := llm.NewRegistry(nil, nil)
	reg.Register("fake", m)
	r := &llm.Router{Registry: reg, Sleep: func(context.Context, time.Duration) error { return nil }, Retry: llm.RetryPolicy{MaxAttempts: 1}}
	r.SetRouting(llm.Routing{Default: []llm.Route{{Provider: "fake", Model: "m"}}})
	return r
}

// jsonAfter decodes the first JSON value after header in s into v.
func jsonAfter(t *testing.T, s, header string, v any) bool {
	t.Helper()
	i := strings.Index(s, header)
	if i < 0 {
		return false
	}
	rest := s[i+len(header):]
	raw := llm.ExtractJSON(rest)
	if raw == nil {
		t.Fatalf("no JSON after %q in %q", header, s)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("decode after %q: %v", header, err)
	}
	return true
}

type lineJSON struct {
	ID         string  `json:"id"`
	Text       string  `json:"text"`
	Source     string  `json:"source"`
	Draft      string  `json:"draft"`
	Notes      string  `json:"notes"`
	MaxSeconds float64 `json:"maxSeconds"`
	MaxChars   int     `json:"maxChars"`
}

func linesReply(lines []lineJSON, f func(l lineJSON) string) string {
	type out struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	var o struct {
		Lines []out `json:"lines"`
	}
	for _, l := range lines {
		o.Lines = append(o.Lines, out{l.ID, f(l)})
	}
	b, _ := json.Marshal(o)
	return string(b)
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
