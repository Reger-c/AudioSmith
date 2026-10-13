package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// AnthropicVersion is the anthropic-version header sent.
const AnthropicVersion = "2023-06-01"

// DefaultMaxTokens is used where the API requires max_tokens (Anthropic)
// and the request leaves it 0.
const DefaultMaxTokens = 4096

// Anthropic is the Anthropic Messages API transport
// (POST {base}/messages, GET {base}/models).
type Anthropic struct{ httpTransport }

// NewAnthropic returns a backend for p (a resolved preset, base ending in /v1).
func NewAnthropic(p Preset, client *http.Client, secrets Secrets) *Anthropic {
	if p.Headers == nil {
		p.Headers = map[string]string{}
	} else {
		h := make(map[string]string, len(p.Headers)+1)
		for k, v := range p.Headers {
			h[k] = v
		}
		p.Headers = h
	}
	if p.Headers["anthropic-version"] == "" {
		p.Headers["anthropic-version"] = AnthropicVersion
	}
	if p.Auth == "" {
		p.Auth = AuthXAPIKey
	}
	return &Anthropic{httpTransport{preset: p, client: client, secrets: secrets}}
}

func (a *Anthropic) Info() BackendInfo {
	return BackendInfo{ID: a.preset.ID, Transport: "anthropic", Local: a.preset.Local, JSONMode: JSONPromptMode, ThirdParty: !a.preset.Local}
}

func (a *Anthropic) body(req Request, model string) map[string]any {
	msgs := req.Messages
	if req.Format == FormatJSON {
		msgs = withJSONInstruction(msgs, req.Schema, true)
	}
	var system []string
	type msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	var out []msg
	for _, m := range msgs {
		if m.Role == System {
			system = append(system, m.Content)
			continue
		}
		role := string(m.Role)
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Content += "\n\n" + m.Content // the API wants alternating turns
			continue
		}
		out = append(out, msg{Role: role, Content: m.Content})
	}
	if len(out) == 0 || out[0].Role != string(User) {
		out = append([]msg{{Role: string(User), Content: "(continue)"}}, out...)
	}
	maxTok := req.MaxTokens
	if maxTok <= 0 {
		maxTok = DefaultMaxTokens
	}
	b := map[string]any{"model": model, "messages": out, "max_tokens": maxTok}
	if len(system) > 0 {
		b["system"] = strings.Join(system, "\n\n")
	}
	if req.Temperature != nil {
		b["temperature"] = min(*req.Temperature, 1)
	}
	if req.TopP != nil {
		b["top_p"] = *req.TopP
	}
	if len(req.Stop) > 0 {
		b["stop_sequences"] = req.Stop
	}
	if req.OnDelta != nil {
		b["stream"] = true
	}
	return b
}

type anUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func (a *Anthropic) Chat(ctx context.Context, req Request) (Response, error) {
	model := req.Model
	if model == "" {
		model = a.preset.DefaultModel
	}
	if model == "" {
		return Response{}, newErr(CodeConfig, "provider", a.preset.ID, "field", "model")
	}
	start := time.Now()
	resp, err := a.send(ctx, http.MethodPost, a.preset.BaseURL+"/messages", a.body(req, model), model, req.Timeout)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	var out Response
	if req.OnDelta != nil {
		out, err = a.readStream(ctx, resp.Body, req.OnDelta, req.Timeout)
	} else {
		out, err = a.readOne(ctx, resp.Body, req.Timeout)
	}
	if err != nil {
		return Response{}, err
	}
	if out.Model == "" {
		out.Model = model
	}
	out.Provider = a.preset.ID
	out.Latency = time.Since(start)
	if out.FinishReason == FinishFilter && out.Text == "" {
		return out, newErr(CodeFiltered, "provider", a.preset.ID)
	}
	return out, nil
}

func (a *Anthropic) readOne(ctx context.Context, body io.Reader, timeout time.Duration) (Response, error) {
	raw, err := io.ReadAll(io.LimitReader(body, maxBody))
	if err != nil {
		return Response{}, ctxErr(ctx, a.preset.ID, err, timeout)
	}
	var r struct {
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string  `json:"stop_reason"`
		Usage      anUsage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		e := newErr(CodeBadResponse, "provider", a.preset.ID, "detail", clip(string(raw), 200))
		e.Err = err
		return Response{}, e
	}
	var text strings.Builder
	for _, c := range r.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}
	u := Usage{PromptTokens: r.Usage.InputTokens, CompletionTokens: r.Usage.OutputTokens}
	u.TotalTokens = u.PromptTokens + u.CompletionTokens
	return Response{Text: text.String(), Model: r.Model, FinishReason: normFinish(r.StopReason), Usage: u}, nil
}

func (a *Anthropic) readStream(ctx context.Context, body io.Reader, onDelta func(string), timeout time.Duration) (Response, error) {
	var out Response
	var text strings.Builder
	var perr error
	stopped := false
	err := readSSE(body, func(ev sseEvent) bool {
		var r struct {
			Type    string `json:"type"`
			Message struct {
				Model string  `json:"model"`
				Usage anUsage `json:"usage"`
			} `json:"message"`
			Delta struct {
				Type       string `json:"type"`
				Text       string `json:"text"`
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
			Usage *anUsage `json:"usage"`
			Error *struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(ev.Data), &r); err != nil {
			e := newErr(CodeBadResponse, "provider", a.preset.ID, "detail", clip(ev.Data, 200))
			e.Err = err
			perr = e
			return false
		}
		switch r.Type {
		case "message_start":
			out.Model = r.Message.Model
			out.Usage.PromptTokens = r.Message.Usage.InputTokens
			out.Usage.CompletionTokens = r.Message.Usage.OutputTokens
		case "content_block_delta":
			if r.Delta.Type == "text_delta" && r.Delta.Text != "" {
				text.WriteString(r.Delta.Text)
				onDelta(r.Delta.Text)
			}
		case "message_delta":
			if r.Delta.StopReason != "" {
				out.FinishReason = normFinish(r.Delta.StopReason)
			}
			if r.Usage != nil {
				out.Usage.CompletionTokens = r.Usage.OutputTokens
				if r.Usage.InputTokens > 0 {
					out.Usage.PromptTokens = r.Usage.InputTokens
				}
			}
		case "message_stop":
			stopped = true
			return false
		case "error":
			status := 500
			detail := ""
			if r.Error != nil {
				detail = r.Error.Type + ": " + r.Error.Message
				switch r.Error.Type {
				case "overloaded_error":
					status = 529
				case "rate_limit_error":
					status = 429
				case "invalid_request_error":
					status = 400
				case "authentication_error":
					status = 401
				}
			}
			perr = httpErr(a.preset.ID, out.Model, status, detail, 0)
			return false
		}
		return true
	})
	if perr != nil {
		return Response{}, perr
	}
	if err != nil || ctx.Err() != nil {
		if err == nil {
			err = ctx.Err()
		}
		return Response{}, ctxErr(ctx, a.preset.ID, err, timeout)
	}
	if !stopped {
		e := newErr(CodeNetwork, "provider", a.preset.ID, "detail", "stream ended early")
		e.Retryable = true
		return Response{}, e
	}
	out.Usage.TotalTokens = out.Usage.PromptTokens + out.Usage.CompletionTokens
	out.Text = text.String()
	return out, nil
}

func (a *Anthropic) Models(ctx context.Context) ([]ModelInfo, error) {
	if a.preset.ModelsPath == "" {
		return staticModels(a.preset), nil
	}
	resp, err := a.send(ctx, http.MethodGet, a.preset.BaseURL+a.preset.ModelsPath+"?limit=1000", nil, "", 0)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&r); err != nil {
		e := newErr(CodeBadResponse, "provider", a.preset.ID, "detail", "models")
		e.Err = err
		return nil, e
	}
	out := make([]ModelInfo, 0, len(r.Data))
	for _, m := range r.Data {
		out = append(out, ModelInfo{ID: m.ID, OwnedBy: "anthropic"})
	}
	return out, nil
}
