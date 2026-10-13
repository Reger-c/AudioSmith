package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxBody caps a non-streamed response body.
const maxBody = 32 << 20

// httpTransport is what the HTTP backends share.
type httpTransport struct {
	preset  Preset
	client  *http.Client
	secrets Secrets
}

func (t *httpTransport) httpClient() *http.Client {
	if t.client != nil {
		return t.client
	}
	return http.DefaultClient
}

// authHeaders fetches the key and returns the request headers.
func (t *httpTransport) headers(ctx context.Context) (http.Header, error) {
	h := http.Header{}
	h.Set("User-Agent", UserAgent)
	for k, v := range t.preset.Headers {
		h.Set(k, v)
	}
	key, err := lookupKey(ctx, t.secrets, t.preset)
	if err != nil {
		return nil, err
	}
	if key == "" {
		return h, nil
	}
	switch t.preset.Auth {
	case AuthXAPIKey:
		h.Set("x-api-key", key)
	case AuthAPIKey:
		h.Set("api-key", key)
	default: // bearer, and "none" when the user stored a key anyway (LM Studio with auth on)
		h.Set("Authorization", "Bearer "+key)
	}
	return h, nil
}

// UserAgent is sent with every request.
var UserAgent = "AudioSmith"

// send POSTs (or GETs when body is nil) and returns the response when the
// status is 2xx, else an *Error from the body.
func (t *httpTransport) send(ctx context.Context, method, url string, body any, model string, timeout time.Duration) (*http.Response, error) {
	h, err := t.headers(ctx)
	if err != nil {
		return nil, err
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e := newErr(CodeBadRequest, "provider", t.preset.ID, "detail", "marshal")
			e.Err = err
			return nil, e
		}
		rd = bytes.NewReader(b)
		h.Set("Content-Type", "application/json")
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		e := newErr(CodeConfig, "provider", t.preset.ID, "field", "baseURL")
		e.Err = err
		return nil, e
	}
	req.Header = h
	resp, err := t.httpClient().Do(req)
	if err != nil {
		return nil, ctxErr(ctx, t.preset.ID, err, timeout)
	}
	if resp.StatusCode/100 == 2 {
		return resp, nil
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	return nil, httpErr(t.preset.ID, model, resp.StatusCode, errorDetail(raw), parseRetryAfter(resp.Header))
}

// errorDetail pulls the message out of the usual error bodies:
// {"error":{"message":…,"code":…}}, {"error":"…"}, {"message":…}, {"detail":…}.
func errorDetail(raw []byte) string {
	var v struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
		Detail  any             `json:"detail"`
		Type    string          `json:"type"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return strings.TrimSpace(string(raw))
	}
	if len(v.Error) > 0 {
		var s string
		if json.Unmarshal(v.Error, &s) == nil {
			return s
		}
		var o struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
		}
		if json.Unmarshal(v.Error, &o) == nil {
			parts := []string{}
			for _, p := range []any{o.Type, o.Code, o.Message} {
				if s, ok := p.(string); ok && s != "" {
					parts = append(parts, s)
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, ": ")
			}
		}
	}
	if v.Message != "" {
		return v.Message
	}
	if v.Detail != nil {
		b, _ := json.Marshal(v.Detail)
		return string(b)
	}
	return strings.TrimSpace(string(raw))
}

// parseRetryAfter reads Retry-After (seconds or HTTP date) and the
// OpenAI-style retry-after-ms.
func parseRetryAfter(h http.Header) time.Duration {
	if ms := h.Get("retry-after-ms"); ms != "" {
		if f, err := strconv.ParseFloat(ms, 64); err == nil && f > 0 {
			return time.Duration(f * float64(time.Millisecond))
		}
	}
	v := h.Get("Retry-After")
	if v == "" {
		return 0
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
		return time.Duration(f * float64(time.Second))
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// jsonInstruction is appended to the system prompt for FormatJSON requests.
func jsonInstruction(s *Schema, withSchema bool) string {
	var b strings.Builder
	b.WriteString("\n\nReply with a single JSON value only: no prose, no Markdown fences.")
	if withSchema && s != nil {
		js, _ := json.Marshal(s)
		b.WriteString(" It must match this JSON Schema:\n")
		b.Write(js)
	}
	return b.String()
}

// withJSONInstruction returns msgs with the JSON instruction appended to
// the system message (one is added when there is none).
func withJSONInstruction(msgs []Message, s *Schema, withSchema bool) []Message {
	instr := jsonInstruction(s, withSchema)
	out := make([]Message, 0, len(msgs)+1)
	added := false
	for _, m := range msgs {
		if m.Role == System && !added {
			m.Content += instr
			added = true
		}
		out = append(out, m)
	}
	if !added {
		out = append([]Message{{Role: System, Content: strings.TrimSpace(instr)}}, out...)
	}
	return out
}

func normFinish(r string) string {
	switch r {
	case "stop", "end_turn", "stop_sequence", "eos":
		return FinishStop
	case "length", "max_tokens":
		return FinishLength
	case "content_filter", "refusal", "safety":
		return FinishFilter
	case "":
		return ""
	}
	return FinishOther
}
