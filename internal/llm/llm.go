// Package llm is AudioSmith's provider-agnostic chat-model layer.
//
// Layers, bottom up:
//
//   - Backend: one model endpoint, one attempt per call. Transports:
//     OpenAI-compatible (OpenAI, OpenRouter, Groq, DeepSeek, Mistral,
//     Together, SiliconFlow, DashScope, Moonshot, Gemini, Ollama, LM Studio,
//     custom…), the Anthropic Messages API, and EngineBackend for the local
//     MLX engine's llm.generate (engine-protocol adapter, see local.go).
//   - Client: a Backend plus timeouts, retries with backoff on 429/5xx,
//     usage accounting and JSON / structured output (schema validation and
//     a repair retry).
//   - Router: skills (translate, dictation_cleanup, …) → ordered routes
//     (provider + model) with fallback.
//
// Provider presets are data (presets.json). API keys come from a Secrets
// provider (the app backs it with the Keychain); HTTP goes through an
// injected *http.Client (the app passes one honouring the macOS system
// proxy). Everything is goroutine-safe and blocks: call it off the UI
// thread. Errors are *Error values with a stable Code and Params, never
// user-visible text.
package llm

import (
	"context"
	"encoding/json"
	"time"
)

// Role is a chat message role.
type Role string

const (
	System    Role = "system"
	User      Role = "user"
	Assistant Role = "assistant"
)

// Message is one chat turn. Text only: AudioSmith's skills don't send images.
type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`
}

// Format selects the output format.
type Format uint8

const (
	FormatText Format = iota // free text
	FormatJSON               // a JSON value; Schema (if set) constrains it
)

// Request is one chat completion.
type Request struct {
	// Model is the provider's model id. Empty: the backend's default.
	Model    string
	Messages []Message // a leading System message is moved where the API wants it

	Temperature *float64 // nil: provider default
	TopP        *float64
	MaxTokens   int // 0: provider default (Anthropic: DefaultMaxTokens)
	Stop        []string
	Seed        *int64

	// Format and Schema ask for JSON. Backends use the provider's native
	// mode when the preset has one (json_schema / json_object) and the
	// prompt otherwise; Client.JSON validates and repairs either way.
	Format Format
	Schema *Schema

	// OnDelta, when set, streams: it is called with each text delta, in
	// order, on the calling goroutine, before Chat returns. It must not
	// block for long. The Response still carries the whole text.
	OnDelta func(delta string)

	// Timeout bounds one attempt (0: the Client's default).
	Timeout time.Duration

	// Skill tags the call for usage accounting (set by Router).
	Skill Skill
}

// F64 returns a pointer to v, for Request.Temperature and TopP.
func F64(v float64) *float64 { return &v }

// Usage is the token count of a call. Estimated is true when the
// provider reported none and the counts come from EstimateTokens.
type Usage struct {
	PromptTokens     int  `json:"promptTokens"`
	CompletionTokens int  `json:"completionTokens"`
	TotalTokens      int  `json:"totalTokens"`
	Estimated        bool `json:"estimated,omitempty"`
}

// Add returns u + v.
func (u Usage) Add(v Usage) Usage {
	return Usage{
		PromptTokens:     u.PromptTokens + v.PromptTokens,
		CompletionTokens: u.CompletionTokens + v.CompletionTokens,
		TotalTokens:      u.TotalTokens + v.TotalTokens,
		Estimated:        u.Estimated || v.Estimated,
	}
}

// Finish reasons, normalised across providers.
const (
	FinishStop   = "stop"
	FinishLength = "length" // hit MaxTokens
	FinishFilter = "content_filter"
	FinishOther  = "other"
)

// Response is a finished completion.
type Response struct {
	Text         string
	FinishReason string
	Model        string // as reported by the provider (may differ from the request)
	Provider     string // backend id
	Usage        Usage
	Latency      time.Duration // of the successful attempt
	Attempts     int           // 1 + retries (Client)
	Repairs      int           // JSON repair turns (Client.JSON)
	// JSON is the validated value for FormatJSON requests made through
	// Client.JSON (the extracted JSON text otherwise empty).
	JSON json.RawMessage
}

// ModelInfo is one entry of a provider's model list.
type ModelInfo struct {
	ID            string `json:"id"`
	OwnedBy       string `json:"ownedBy,omitempty"`
	ContextLength int    `json:"contextLength,omitempty"`
}

// BackendInfo describes a backend.
type BackendInfo struct {
	ID         string // provider id ("openai", "ollama", "local")
	Transport  string // "openai", "anthropic", "engine"
	Local      bool   // runs on this Mac (Ollama, LM Studio, the engine): no data leaves it
	JSONMode   JSONMode
	ThirdParty bool // a third-party network service
}

// Backend is one model endpoint. Chat makes a single attempt (Client adds
// retries); it honours ctx cancellation and deadlines and returns *Error.
// Implementations are goroutine-safe.
type Backend interface {
	Info() BackendInfo
	Chat(ctx context.Context, req Request) (Response, error)
	Models(ctx context.Context) ([]ModelInfo, error)
}
