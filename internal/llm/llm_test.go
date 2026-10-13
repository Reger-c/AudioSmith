package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
)

// fake is a scripted HTTP server: each request gets the next handler
// (the last one repeats). It records request bodies and headers.
type fake struct {
	t     *testing.T
	srv   *httptest.Server
	mu    sync.Mutex
	steps []http.HandlerFunc
	n     int
	reqs  []recorded
}

type recorded struct {
	Method, Path string
	Header       http.Header
	Body         map[string]any
}

func newFake(t *testing.T, steps ...http.HandlerFunc) *fake {
	f := &fake{t: t, steps: steps}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.reqs = append(f.reqs, recorded{r.Method, r.URL.Path, r.Header.Clone(), body})
		i := min(f.n, len(f.steps)-1)
		f.n++
		h := f.steps[i]
		f.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.reqs...)
}

func jsonReply(status int, v any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
}

func oaText(text string) map[string]any {
	return map[string]any{
		"model":   "m-reported",
		"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": text}, "finish_reason": "stop"}},
		"usage":   map[string]any{"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18},
	}
}

func sse(events ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for _, e := range events {
			fmt.Fprint(w, e)
			fl.Flush()
		}
	}
}

func oaPreset(base string) Preset {
	p, _ := LookupPreset("openai")
	p.BaseURL = base
	return p
}

func noSleep(context.Context, time.Duration) error { return nil }

func keys() StaticSecrets {
	return StaticSecrets{"llm.openai": "sk-test", "llm.anthropic": "ak-test", "llm.groq": "gk"}
}

func TestOpenAIChatNonStreaming(t *testing.T) {
	f := newFake(t, jsonReply(200, oaText("hola")))
	c := &Client{Backend: NewOpenAI(oaPreset(f.srv.URL), f.srv.Client(), keys())}
	resp, err := c.Chat(context.Background(), Request{
		Model:       "gpt-x",
		Messages:    []Message{{Role: System, Content: "sys"}, {Role: User, Content: "hello"}},
		Temperature: F64(0.2), MaxTokens: 50, Stop: []string{"\n\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "hola" || resp.Model != "m-reported" || resp.Provider != "openai" || resp.FinishReason != FinishStop {
		t.Fatalf("resp %+v", resp)
	}
	if resp.Usage != (Usage{11, 7, 18, false}) || resp.Attempts != 1 {
		t.Fatalf("usage %+v attempts %d", resp.Usage, resp.Attempts)
	}
	r := f.requests()[0]
	if r.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer sk-test" || r.Header.Get("User-Agent") != UserAgent {
		t.Fatalf("req %+v", r)
	}
	if r.Body["model"] != "gpt-x" || r.Body["temperature"] != 0.2 || r.Body["max_completion_tokens"] != 50.0 || r.Body["stream"] != nil {
		t.Fatalf("body %v", r.Body)
	}
	msgs := r.Body["messages"].([]any)
	if len(msgs) != 2 || msgs[0].(map[string]any)["role"] != "system" {
		t.Fatalf("messages %v", msgs)
	}
}

func TestOpenAIStreaming(t *testing.T) {
	f := newFake(t, sse(
		": keep-alive\n\n",
		`data: {"model":"m1","choices":[{"delta":{"role":"assistant"}}]}`+"\n\n",
		`data: {"choices":[{"delta":{"content":"Hel"}}]}`+"\n\n",
		`data: {"choices":[{"delta":{"content":"lo"},"finish_reason":"stop"}]}`+"\n\n",
		`data: {"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`+"\n\n",
		"data: [DONE]\n\n",
	))
	var deltas []string
	c := &Client{Backend: NewOpenAI(oaPreset(f.srv.URL), f.srv.Client(), keys())}
	resp, err := c.Chat(context.Background(), Request{Messages: []Message{{Role: User, Content: "hi"}}, OnDelta: func(s string) { deltas = append(deltas, s) }})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "Hello" || strings.Join(deltas, "|") != "Hel|lo" || resp.Usage.TotalTokens != 5 || resp.Model != "m1" {
		t.Fatalf("resp %+v deltas %q", resp, deltas)
	}
	b := f.requests()[0].Body
	if b["stream"] != true || b["stream_options"] == nil || b["model"] != "gpt-4.1-mini" {
		t.Fatalf("body %v", b)
	}
}

func TestOpenAIStreamOptionsAndTemperatureRelaxed(t *testing.T) {
	f := newFake(t,
		jsonReply(400, map[string]any{"error": map[string]any{"message": "Unrecognized request argument: stream_options"}}),
		jsonReply(400, map[string]any{"error": map[string]any{"message": "Unsupported value: 'temperature' does not support 0.3"}}),
		sse(`data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n", "data: [DONE]\n\n"),
	)
	c := &Client{Backend: NewOpenAI(oaPreset(f.srv.URL), f.srv.Client(), keys())}
	resp, err := c.Chat(context.Background(), Request{Messages: []Message{{Role: User, Content: "hi"}}, Temperature: F64(0.3), OnDelta: func(string) {}})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Usage.Estimated || resp.Text != "ok" {
		t.Fatalf("resp %+v", resp)
	}
	rs := f.requests()
	if len(rs) != 3 || rs[1].Body["stream_options"] != nil || rs[2].Body["temperature"] != nil || rs[1].Body["temperature"] == nil {
		t.Fatalf("requests %+v", rs)
	}
}

func TestOpenAIStreamTruncated(t *testing.T) {
	f := newFake(t, sse(`data: {"choices":[{"delta":{"content":"par"}}]}`+"\n\n"))
	c := &Client{Backend: NewOpenAI(oaPreset(f.srv.URL), f.srv.Client(), keys()), Sleep: noSleep}
	_, err := c.Chat(context.Background(), Request{Messages: []Message{{Role: User, Content: "hi"}}, OnDelta: func(string) {}})
	if Code(err) != CodeNetwork {
		t.Fatalf("err %v", err)
	}
	// Text was streamed: no retry (it would repeat deltas).
	if n := len(f.requests()); n != 1 {
		t.Fatalf("%d requests", n)
	}
}

func TestRetries(t *testing.T) {
	var slept []time.Duration
	sleep := func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	f := newFake(t,
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "2")
			jsonReply(429, map[string]any{"error": map[string]any{"message": "rate limit per minute"}})(w, r)
		},
		jsonReply(503, map[string]any{"error": "busy"}),
		jsonReply(200, oaText("done")),
	)
	m := &Meter{}
	c := &Client{Backend: NewOpenAI(oaPreset(f.srv.URL), f.srv.Client(), keys()), Sleep: sleep, Meter: m}
	resp, err := c.Chat(context.Background(), Request{Messages: []Message{{Role: User, Content: "x"}}, Skill: SkillTranslate})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Attempts != 3 || len(slept) != 2 || slept[0] != 2*time.Second {
		t.Fatalf("attempts %d slept %v", resp.Attempts, slept)
	}
	if slept[1] < 500*time.Millisecond || slept[1] > time.Second {
		t.Fatalf("backoff %v", slept[1])
	}
	snap := m.Snapshot()
	if len(snap) != 1 || snap[0].Calls != 1 || snap[0].Attempts != 3 || snap[0].Skill != SkillTranslate || snap[0].Usage.TotalTokens != 18 {
		t.Fatalf("meter %+v", snap)
	}
}

func TestRetryGivesUp(t *testing.T) {
	f := newFake(t, jsonReply(500, map[string]any{"error": "boom"}))
	c := &Client{Backend: NewOpenAI(oaPreset(f.srv.URL), f.srv.Client(), keys()), Sleep: noSleep, Retry: RetryPolicy{MaxAttempts: 4}}
	_, err := c.Chat(context.Background(), Request{Messages: []Message{{Role: User, Content: "x"}}})
	var e *Error
	if !errors.As(err, &e) || e.Code != CodeServer || e.Status != 500 || len(f.requests()) != 4 {
		t.Fatalf("err %v reqs %d", err, len(f.requests()))
	}
	// A long Retry-After fails at once.
	f2 := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(429)
	})
	c.Backend = NewOpenAI(oaPreset(f2.srv.URL), f2.srv.Client(), keys())
	_, err = c.Chat(context.Background(), Request{Messages: []Message{{Role: User, Content: "x"}}})
	if Code(err) != CodeRateLimited || len(f2.requests()) != 1 || !errors.Is(err, core.ErrBusy) {
		t.Fatalf("err %v", err)
	}
}

func TestHTTPErrorMapping(t *testing.T) {
	cases := []struct {
		status int
		body   any
		code   string
		retry  bool
	}{
		{401, map[string]any{"error": map[string]any{"message": "bad key"}}, CodeAuth, false},
		{403, nil, CodeAuth, false},
		{404, map[string]any{"error": map[string]any{"message": "The model `x` does not exist"}}, CodeModelNotFound, false},
		{400, map[string]any{"error": map[string]any{"code": "context_length_exceeded", "message": "maximum context length is 8192"}}, CodeContextLength, false},
		{400, map[string]any{"error": map[string]any{"message": "messages: field required"}}, CodeBadRequest, false},
		{429, map[string]any{"error": map[string]any{"type": "insufficient_quota", "message": "You exceeded your current quota"}}, CodeRateLimited, false},
		{529, map[string]any{"type": "error", "error": map[string]any{"type": "overloaded_error", "message": "Overloaded"}}, CodeOverloaded, true},
		{502, "<html>bad gateway</html>", CodeServer, true},
	}
	for _, tc := range cases {
		f := newFake(t, jsonReply(tc.status, tc.body))
		c := &Client{Backend: NewOpenAI(oaPreset(f.srv.URL), f.srv.Client(), keys()), Retry: RetryPolicy{MaxAttempts: 1}}
		_, err := c.Chat(context.Background(), Request{Model: "x", Messages: []Message{{Role: User, Content: "x"}}})
		var e *Error
		if !errors.As(err, &e) || e.Code != tc.code || e.Retryable != tc.retry {
			t.Errorf("%d: got %v (retryable %v), want %s", tc.status, err, e != nil && e.Retryable, tc.code)
		}
		if !errors.Is(err, &Error{Code: tc.code}) {
			t.Errorf("errors.Is by code failed for %s", tc.code)
		}
	}
}

func TestNetworkErrors(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	p, _ := ProviderConfig{ID: "ollama", BaseURL: "http://" + addr + "/v1"}.Resolve()
	if !p.Local {
		t.Fatal("loopback should be local")
	}
	c := &Client{Backend: NewOpenAI(p, nil, nil), Sleep: noSleep}
	_, err := c.Chat(context.Background(), Request{Messages: []Message{{Role: User, Content: "x"}}})
	var e *Error
	if !errors.As(err, &e) || e.Code != CodeNetwork || e.Retryable || e.Params["provider"] != "ollama" {
		t.Fatalf("err %v", err)
	}
}

func TestTimeoutAndCancel(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	var calls atomic.Int32
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case <-block:
		case <-r.Context().Done():
		}
	})
	c := &Client{Backend: NewOpenAI(oaPreset(f.srv.URL), f.srv.Client(), keys()), Timeout: 50 * time.Millisecond, Sleep: noSleep, Retry: RetryPolicy{MaxAttempts: 2}}
	_, err := c.Chat(context.Background(), Request{Messages: []Message{{Role: User, Content: "x"}}})
	if Code(err) != CodeTimeout || calls.Load() != 2 {
		t.Fatalf("err %v calls %d", err, calls.Load())
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	c.Timeout = 10 * time.Second
	_, err = c.Chat(ctx, Request{Messages: []Message{{Role: User, Content: "x"}}})
	if Code(err) != CodeCancelled || !errors.Is(err, core.ErrCancelled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
}

func TestCancelMidStream(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"a"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{Backend: NewOpenAI(oaPreset(f.srv.URL), f.srv.Client(), keys())}
	_, err := c.Chat(ctx, Request{Messages: []Message{{Role: User, Content: "x"}}, OnDelta: func(string) { cancel() }})
	if Code(err) != CodeCancelled {
		t.Fatalf("err %v", err)
	}
}

func TestNoKey(t *testing.T) {
	c := &Client{Backend: NewOpenAI(oaPreset("http://127.0.0.1:1"), nil, StaticSecrets{})}
	_, err := c.Chat(context.Background(), Request{Messages: []Message{{Role: User, Content: "x"}}})
	if Code(err) != CodeNoKey {
		t.Fatalf("err %v", err)
	}
	// Keyless local server: no Authorization header; a stored key is sent.
	f := newFake(t, jsonReply(200, oaText("x")))
	p, _ := ProviderConfig{ID: "lmstudio", BaseURL: f.srv.URL, Model: "qwen"}.Resolve()
	c = &Client{Backend: NewOpenAI(p, f.srv.Client(), StaticSecrets{})}
	if _, err := c.Chat(context.Background(), Request{Messages: []Message{{Role: User, Content: "x"}}}); err != nil {
		t.Fatal(err)
	}
	c = &Client{Backend: NewOpenAI(p, f.srv.Client(), StaticSecrets{"llm.lmstudio": "lm-key"})}
	if _, err := c.Chat(context.Background(), Request{Messages: []Message{{Role: User, Content: "x"}}}); err != nil {
		t.Fatal(err)
	}
	rs := f.requests()
	if rs[0].Header.Get("Authorization") != "" || rs[1].Header.Get("Authorization") != "Bearer lm-key" {
		t.Fatalf("auth %q %q", rs[0].Header.Get("Authorization"), rs[1].Header.Get("Authorization"))
	}
}

func TestProxyInjection(t *testing.T) {
	var proxied atomic.Value
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied.Store(r.URL.String())
		jsonReply(200, oaText("via proxy"))(w, r)
	}))
	defer proxy.Close()
	pu, _ := url.Parse(proxy.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	p, _ := ProviderConfig{ID: "custom", BaseURL: "http://llm.example.test/v1", Model: "m"}.Resolve()
	c := &Client{Backend: NewOpenAI(p, client, nil)}
	resp, err := c.Chat(context.Background(), Request{Messages: []Message{{Role: User, Content: "x"}}})
	if err != nil || resp.Text != "via proxy" {
		t.Fatalf("%v %+v", err, resp)
	}
	if got, _ := proxied.Load().(string); got != "http://llm.example.test/v1/chat/completions" {
		t.Fatalf("proxy saw %q", got)
	}
}

func TestOpenAIJSONModes(t *testing.T) {
	schema := Object(map[string]*Schema{"a": String()}, "a")
	schema.Name = "thing"
	for _, mode := range []JSONMode{JSONSchemaMode, JSONObjectMode, JSONPromptMode} {
		f := newFake(t, jsonReply(200, oaText(`{"a":"x"}`)))
		p := oaPreset(f.srv.URL)
		p.JSONMode = mode
		c := &Client{Backend: NewOpenAI(p, f.srv.Client(), keys())}
		var out struct{ A string }
		resp, err := c.JSON(context.Background(), Request{Messages: []Message{{Role: User, Content: "x"}}, Schema: schema}, &out, 0)
		if err != nil || out.A != "x" || string(resp.JSON) != `{"a":"x"}` {
			t.Fatalf("%s: %v %+v", mode, err, out)
		}
		b := f.requests()[0].Body
		rf, _ := b["response_format"].(map[string]any)
		sys := b["messages"].([]any)[0].(map[string]any)
		switch mode {
		case JSONSchemaMode:
			js := rf["json_schema"].(map[string]any)
			if rf["type"] != "json_schema" || js["name"] != "thing" || js["schema"].(map[string]any)["additionalProperties"] != false {
				t.Fatalf("schema mode body %v", b)
			}
			if strings.Contains(sys["content"].(string), "JSON Schema") {
				t.Fatal("schema mode should not inline the schema")
			}
		case JSONObjectMode:
			if rf["type"] != "json_object" || !strings.Contains(sys["content"].(string), `"required":["a"]`) {
				t.Fatalf("object mode body %v", b)
			}
		case JSONPromptMode:
			if rf != nil || sys["role"] != "system" || !strings.Contains(sys["content"].(string), "JSON Schema") {
				t.Fatalf("prompt mode body %v", b)
			}
		}
	}
}

func TestJSONRepair(t *testing.T) {
	schema := Object(map[string]*Schema{
		"lines": Array(Object(map[string]*Schema{"id": String(), "text": String()}, "id", "text")).Count(2, 2),
	}, "lines")
	f := newFake(t,
		jsonReply(200, oaText("Sure! Here you go: no json")),
		jsonReply(200, oaText("```json\n{\"lines\":[{\"id\":\"1\",\"text\":\"a\"}]}\n```")),
		jsonReply(200, oaText(`{"lines":[{"id":"1","text":"a"},{"id":"2","text":"b"}]}`)),
	)
	c := &Client{Backend: NewOpenAI(oaPreset(f.srv.URL), f.srv.Client(), keys())}
	var out struct {
		Lines []struct{ ID, Text string }
	}
	resp, err := c.JSON(context.Background(), Request{Messages: []Message{{Role: System, Content: "s"}, {Role: User, Content: "u"}}, Schema: schema}, &out, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Lines) != 2 || resp.Repairs != 2 || resp.Usage.TotalTokens != 54 || resp.Attempts != 3 {
		t.Fatalf("out %+v resp %+v", out, resp)
	}
	rs := f.requests()
	last := rs[2].Body["messages"].([]any)
	if len(last) != 4 {
		t.Fatalf("repair turn has %d messages", len(last))
	}
	fix := last[3].(map[string]any)["content"].(string)
	if !strings.Contains(fix, "/lines: must have at least 2 items") {
		t.Fatalf("repair prompt %q", fix)
	}
	// Out of repairs → schema error with the path.
	f2 := newFake(t, jsonReply(200, oaText(`{"lines":[]}`)))
	c.Backend = NewOpenAI(oaPreset(f2.srv.URL), f2.srv.Client(), keys())
	_, err = c.JSON(context.Background(), Request{Messages: []Message{{Role: User, Content: "u"}}, Schema: schema}, nil, -1)
	var e *Error
	if !errors.As(err, &e) || e.Code != CodeSchema || e.Params["path"] != "/lines" || !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("err %v", err)
	}
}

func TestAnthropic(t *testing.T) {
	f := newFake(t, jsonReply(200, map[string]any{
		"model":       "claude-x",
		"content":     []any{map[string]any{"type": "text", "text": "bonjour"}},
		"stop_reason": "end_turn",
		"usage":       map[string]any{"input_tokens": 9, "output_tokens": 3},
	}))
	p, _ := ProviderConfig{ID: "anthropic", BaseURL: f.srv.URL}.Resolve()
	c := &Client{Backend: NewAnthropic(p, f.srv.Client(), keys())}
	resp, err := c.Chat(context.Background(), Request{Messages: []Message{
		{Role: System, Content: "be brief"}, {Role: User, Content: "a"}, {Role: User, Content: "b"},
	}, Temperature: F64(1.5)})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "bonjour" || resp.Usage.TotalTokens != 12 || resp.FinishReason != FinishStop {
		t.Fatalf("resp %+v", resp)
	}
	r := f.requests()[0]
	if r.Path != "/messages" || r.Header.Get("x-api-key") != "ak-test" || r.Header.Get("anthropic-version") != AnthropicVersion || r.Header.Get("Authorization") != "" {
		t.Fatalf("headers %v", r.Header)
	}
	msgs := r.Body["messages"].([]any)
	if r.Body["system"] != "be brief" || len(msgs) != 1 || msgs[0].(map[string]any)["content"] != "a\n\nb" || r.Body["max_tokens"] != float64(DefaultMaxTokens) || r.Body["temperature"] != 1.0 {
		t.Fatalf("body %v", r.Body)
	}
}

func TestAnthropicStream(t *testing.T) {
	f := newFake(t, sse(
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-x\",\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\n",
		"event: ping\ndata: {\"type\":\"ping\"}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hi\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\" there\"}}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"max_tokens\"},\"usage\":{\"output_tokens\":4}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	))
	p, _ := ProviderConfig{ID: "anthropic", BaseURL: f.srv.URL}.Resolve()
	var got strings.Builder
	c := &Client{Backend: NewAnthropic(p, f.srv.Client(), keys())}
	resp, err := c.Chat(context.Background(), Request{Messages: []Message{{Role: User, Content: "a"}}, OnDelta: func(s string) { got.WriteString(s) }})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "Hi there" || got.String() != "Hi there" || resp.Usage != (Usage{5, 4, 9, false}) || resp.FinishReason != FinishLength {
		t.Fatalf("resp %+v", resp)
	}
	// An error event mid-stream.
	f2 := newFake(t, sse("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"))
	p.BaseURL = f2.srv.URL
	c = &Client{Backend: NewAnthropic(p, f2.srv.Client(), keys()), Retry: RetryPolicy{MaxAttempts: 1}}
	_, err = c.Chat(context.Background(), Request{Messages: []Message{{Role: User, Content: "a"}}, OnDelta: func(string) {}})
	if Code(err) != CodeOverloaded {
		t.Fatalf("err %v", err)
	}
}

func TestModels(t *testing.T) {
	f := newFake(t, jsonReply(200, map[string]any{"object": "list", "data": []any{
		map[string]any{"id": "models/gemini-2.5-flash", "owned_by": "google"},
		map[string]any{"id": "llama3", "context_length": 8192},
	}}))
	reg := NewRegistry(f.srv.Client(), StaticSecrets{"llm.gemini": "g"})
	if err := reg.Configure([]ProviderConfig{{ID: "gemini", BaseURL: f.srv.URL}, {ID: "zhipu"}, {ID: "custom"}}); Code(err) != CodeConfig {
		t.Fatalf("custom without baseURL: %v", err)
	}
	ms, err := reg.Models(context.Background(), "gemini")
	if err != nil || len(ms) != 2 || ms[0].ID != "gemini-2.5-flash" || ms[1].ContextLength != 8192 {
		t.Fatalf("%v %+v", err, ms)
	}
	if f.requests()[0].Path != "/models" {
		t.Fatal(f.requests()[0].Path)
	}
	ms, err = reg.Models(context.Background(), "zhipu")
	if err != nil || len(ms) == 0 {
		t.Fatalf("static: %v %v", err, ms)
	}
	if _, err := reg.Models(context.Background(), "nope"); Code(err) != CodeUnknown {
		t.Fatal(err)
	}
	// Anthropic list.
	f2 := newFake(t, jsonReply(200, map[string]any{"data": []any{map[string]any{"id": "claude-a"}}}))
	p, _ := ProviderConfig{ID: "anthropic", BaseURL: f2.srv.URL}.Resolve()
	ms, err = NewAnthropic(p, f2.srv.Client(), keys()).Models(context.Background())
	if err != nil || len(ms) != 1 || ms[0].ID != "claude-a" || f2.requests()[0].Header.Get("x-api-key") != "ak-test" {
		t.Fatalf("%v %v", err, ms)
	}
}

func TestPresets(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Presets() {
		if seen[p.ID] {
			t.Errorf("duplicate %s", p.ID)
		}
		seen[p.ID] = true
		if p.Transport != "openai" && p.Transport != "anthropic" {
			t.Errorf("%s: transport %q", p.ID, p.Transport)
		}
		switch p.JSONMode {
		case JSONSchemaMode, JSONObjectMode, JSONPromptMode:
		default:
			t.Errorf("%s: jsonMode %q", p.ID, p.JSONMode)
		}
		if p.ID != "custom" {
			u, err := url.Parse(p.BaseURL)
			if err != nil || u.Host == "" {
				t.Errorf("%s: baseURL %q", p.ID, p.BaseURL)
			}
			if p.Local != (u.Hostname() == "127.0.0.1") {
				t.Errorf("%s: local flag", p.ID)
			}
		}
		if p.ModelsPath == "" && len(p.Models) == 0 {
			t.Errorf("%s: no model list", p.ID)
		}
	}
	for _, id := range []string{"openai", "openrouter", "groq", "deepseek", "mistral", "together", "siliconflow", "dashscope", "moonshot", "gemini", "ollama", "lmstudio", "custom", "anthropic"} {
		if !seen[id] {
			t.Errorf("missing preset %s", id)
		}
	}
	p, err := ProviderConfig{ID: "custom-2", Preset: "custom", BaseURL: "https://x.test/v1/", Headers: map[string]string{"X-A": "1"}}.Resolve()
	if err != nil || p.ID != "custom-2" || p.SecretName() != "llm.custom-2" || p.BaseURL != "https://x.test/v1" || p.Local {
		t.Fatalf("%v %+v", err, p)
	}
	p, _ = ProviderConfig{ID: "moonshot-intl"}.Resolve()
	if p.SecretName() != "llm.moonshot" {
		t.Fatal(p.SecretName())
	}
	if _, err := (ProviderConfig{ID: "x", Preset: "openai", BaseURL: "ftp://x"}).Resolve(); Code(err) != CodeConfig {
		t.Fatal(err)
	}
}

// scripted is a Backend returning canned results.
type scripted struct {
	id    string
	local bool
	mu    sync.Mutex
	errs  []error
	text  string
	calls []Request
}

func (s *scripted) Info() BackendInfo { return BackendInfo{ID: s.id, Local: s.local, Transport: "test"} }
func (s *scripted) Chat(ctx context.Context, req Request) (Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, req)
	if len(s.errs) > 0 {
		err := s.errs[0]
		if len(s.errs) > 1 {
			s.errs = s.errs[1:]
		}
		if err != nil {
			return Response{}, err
		}
	}
	if req.OnDelta != nil {
		req.OnDelta(s.text)
	}
	return Response{Text: s.text, Provider: s.id, Model: req.Model, Usage: Usage{1, 1, 2, false}}, nil
}
func (s *scripted) Models(context.Context) ([]ModelInfo, error) { return nil, nil }

func TestRouterFallback(t *testing.T) {
	down := &scripted{id: "cloud", errs: []error{&Error{Code: CodeServer, Retryable: true}}}
	up := &scripted{id: "ollama", local: true, text: "ok"}
	reg := NewRegistry(nil, nil)
	reg.Register("cloud", down)
	reg.Register("ollama", up)
	r := &Router{Registry: reg, Sleep: noSleep}
	r.SetRouting(Routing{
		Default: []Route{{Provider: "ollama", Model: "qwen3:4b"}},
		Skills: map[Skill][]Route{
			SkillTranslate: {{Provider: "cloud", Model: "big", Temperature: F64(0.9)}, {Provider: "missing"}},
		},
	})
	got := r.Routing().Resolve(SkillFitRewrite)
	if len(got) != 3 || got[0].Provider != "cloud" || got[2].Provider != "ollama" {
		t.Fatalf("resolve %+v", got)
	}
	if ps := r.ProvidersFor(SkillTranslateCinematic); strings.Join(ps, ",") != "cloud,ollama" {
		t.Fatal(ps)
	}
	resp, err := r.Chat(context.Background(), SkillTranslateCinematic, Request{Messages: []Message{{Role: User, Content: "x"}}})
	if err != nil || resp.Provider != "ollama" || resp.Model != "qwen3:4b" {
		t.Fatalf("%v %+v", err, resp)
	}
	if len(down.calls) != DefaultMaxAttempts || *down.calls[0].Temperature != 0.9 || down.calls[0].Skill != SkillTranslateCinematic {
		t.Fatalf("down calls %d", len(down.calls))
	}
	if *up.calls[0].Temperature != DefaultTemperature(SkillTranslateCinematic) {
		t.Fatal(*up.calls[0].Temperature)
	}
	// Local only skips the cloud route.
	r.SetLocalOnly(true)
	if ps := r.ProvidersFor(SkillTranslate); strings.Join(ps, ",") != "ollama" {
		t.Fatal(ps)
	}
	r.SetLocalOnly(false)
	// Cancellation stops the fallback.
	down.errs = []error{&Error{Code: CodeCancelled}}
	up.calls = nil
	_, err = r.Chat(context.Background(), SkillTranslate, Request{})
	if Code(err) != CodeCancelled || len(up.calls) != 0 {
		t.Fatalf("%v %d", err, len(up.calls))
	}
	// No route.
	r2 := &Router{Registry: reg}
	if _, err := r2.Chat(context.Background(), SkillDictationCleanup, Request{}); Code(err) != CodeNoRoute || r2.Available(SkillDictationCleanup) {
		t.Fatal(err)
	}
	var nilRouter *Router
	if nilRouter.Available(SkillTranslate) {
		t.Fatal("nil router available")
	}
}

func TestEngineBackend(t *testing.T) {
	var gotParams GenerateParams
	call := func(ctx context.Context, method string, params any, notify func(string, json.RawMessage)) (json.RawMessage, error) {
		if method != MethodGenerate {
			t.Fatalf("method %s", method)
		}
		b, _ := json.Marshal(params)
		_ = json.Unmarshal(b, &gotParams)
		if notify != nil {
			notify("job.progress", json.RawMessage(`{"fraction":0.5}`))
			notify(NotifyToken, json.RawMessage(`{"text":"你"}`))
			notify(NotifyToken, json.RawMessage(`{"text":"好"}`))
		}
		return json.RawMessage(`{"text":"你好","finishReason":"stop","promptTokens":4,"completionTokens":2}`), nil
	}
	eb := &EngineBackend{Call: call, DefaultModel: "mlx-community/Qwen3-4B-4bit"}
	reg := NewRegistry(nil, nil)
	reg.Register("local", eb)
	r := &Router{Registry: reg}
	r.SetLocalOnly(true)
	r.SetRouting(Routing{Default: []Route{{Provider: "local"}}})
	var deltas string
	resp, err := r.Chat(context.Background(), SkillDictationCleanup, Request{Messages: []Message{{Role: User, Content: "x"}}, OnDelta: func(s string) { deltas += s }})
	if err != nil || resp.Text != "你好" || deltas != "你好" || resp.Usage.TotalTokens != 6 || resp.Provider != "local" {
		t.Fatalf("%v %+v %q", err, resp, deltas)
	}
	if gotParams.Model != "mlx-community/Qwen3-4B-4bit" || !gotParams.Stream || *gotParams.Temperature != 0.1 {
		t.Fatalf("params %+v", gotParams)
	}
	// Engine errors map to codes.
	eb.Call = func(context.Context, string, any, func(string, json.RawMessage)) (json.RawMessage, error) {
		return nil, fmt.Errorf("weights: %w", core.ErrNotDownloaded)
	}
	if _, err := r.Chat(context.Background(), SkillDictationCleanup, Request{}); Code(err) != CodeModelNotFound {
		t.Fatal(err)
	}
	n := 0
	eb.Call = func(context.Context, string, any, func(string, json.RawMessage)) (json.RawMessage, error) {
		n++
		if n == 1 {
			return nil, fmt.Errorf("worker restarting: %w", core.ErrEngine)
		}
		return json.RawMessage(`{"text":"ok"}`), nil
	}
	r.Sleep = noSleep
	if resp, err := r.Chat(context.Background(), SkillDictationCleanup, Request{}); err != nil || resp.Attempts != 2 {
		t.Fatal(err, resp.Attempts)
	}
}

func TestThinkingStripped(t *testing.T) {
	if got := StripThinking("<think>\nhmm\n</think>\n\nAnswer"); got != "Answer" {
		t.Fatal(got)
	}
	if got := StripThinking("Answer <think>"); got != "Answer <think>" {
		t.Fatal(got)
	}
	var out strings.Builder
	f := &thinkFilter{emit: func(s string) { out.WriteString(s) }}
	for _, d := range []string{"<th", "ink>reason", "ing</thi", "nk>", "\n  ", "Hi", " you"} {
		f.write(d)
	}
	f.flush()
	if out.String() != "Hi you" {
		t.Fatalf("%q", out.String())
	}
	out.Reset()
	f.reset()
	for _, d := range []string{"<", "b>bold"} {
		f.write(d)
	}
	f.flush()
	if out.String() != "<b>bold" {
		t.Fatalf("%q", out.String())
	}
}

func TestExtractJSONAndSchema(t *testing.T) {
	cases := map[string]string{
		`{"a":1}`:                           `{"a":1}`,
		"```json\n[1,2]\n```":               `[1,2]`,
		`Here: {"a":"}"} trailing`:          `{"a":"}"}`,
		"<think>{x}</think>\n{\"b\":true}": `{"b":true}`,
		`text [not json] {"ok":1}`:          `{"ok":1}`,
		"nothing":                           ``,
	}
	for in, want := range cases {
		if got := string(ExtractJSON(in)); got != want {
			t.Errorf("ExtractJSON(%q) = %q, want %q", in, got, want)
		}
	}
	s := Object(map[string]*Schema{
		"e":    Enum("a", "b"),
		"n":    Number().Range(0, 1),
		"i":    Integer(),
		"opt":  {Type: "string", Nullable: true},
		"list": Array(Boolean()).Count(1, -1),
	}, "e", "n", "list")
	errs := s.Validate(json.RawMessage(`{"e":"c","n":2,"i":1.5,"opt":null,"list":[],"x":1}`))
	var paths []string
	for _, e := range errs {
		paths = append(paths, e.Path)
	}
	if strings.Join(paths, ",") != "/e,/i,/list,/n,/" {
		t.Fatalf("%v", errs)
	}
	if errs := s.Validate(json.RawMessage(`{"e":"a","n":0.5,"list":[true],"opt":"x"}`)); len(errs) != 0 {
		t.Fatal(errs)
	}
	b, _ := json.Marshal(s)
	if !strings.Contains(string(b), `"additionalProperties":false`) || !strings.Contains(string(b), `"type":["string","null"]`) {
		t.Fatal(string(b))
	}
}

func TestEstimateTokensAndEnvSecrets(t *testing.T) {
	if n := EstimateTokens("你好世界"); n != 4 {
		t.Fatal(n)
	}
	if n := EstimateTokens("abcdefgh"); n != 2 {
		t.Fatal(n)
	}
	t.Setenv("AUDIOSMITH_LLM_OPENAI", "env-key")
	v, err := EnvSecrets{}.Secret(context.Background(), "llm.openai")
	if err != nil || v != "env-key" {
		t.Fatal(v, err)
	}
	if _, err := (EnvSecrets{Fallback: StaticSecrets{}}).Secret(context.Background(), "llm.nope"); !errors.Is(err, ErrNoSecret) {
		t.Fatal(err)
	}
}
