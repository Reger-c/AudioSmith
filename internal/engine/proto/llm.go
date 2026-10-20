package proto

import "encoding/json"

// The local language model (docs/protocol-v2.md §5.4): llm.generate runs
// a chat completion on an MLX LLM in the gpu worker, streaming its text
// as llm.token notifications. internal/llm's EngineBackend speaks it
// (its GenerateParams/GenerateResult/TokenNotification alias these).

// Methods added for the local LLM.
const (
	MethodLLMGenerate = "llm.generate" // request, job (gpu)
	MethodLLMToken    = "llm.token"    // notification, engine → app: generated text, in order
)

// LLMMessage is a chat message.
type LLMMessage struct {
	Role    string `json:"role"` // "system" | "user" | "assistant"
	Content string `json:"content"`
}

// LLMGenerateParams are llm.generate's params.
type LLMGenerateParams struct {
	Model     string       `json:"model"` // repo id
	ModelPath string       `json:"modelPath,omitempty"`
	Messages  []LLMMessage `json:"messages"`
	// MaxTokens caps the reply (0 = the engine's default, 2048, within the
	// context the engine allows).
	MaxTokens   int      `json:"maxTokens,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"` // nil = the model's default (0.7); 0 = greedy
	TopP        *float64 `json:"topP,omitempty"`        // nil = 0.8
	Seed        *int64   `json:"seed,omitempty"`
	Stop        []string `json:"stop,omitempty"` // strings that end the reply (not included)
	// Stream asks for llm.token notifications.
	Stream bool `json:"stream,omitempty"`
	// JSON asks for one JSON value; Schema (JSON Schema) constrains it.
	// The engine constrains decoding to the grammar when it can (greedy:
	// Temperature, TopP and Seed are ignored unless SampleConstrained),
	// and the app validates either way.
	JSON   bool            `json:"json,omitempty"`
	Schema json.RawMessage `json:"schema,omitempty"`
	// SampleConstrained samples a constrained reply with Temperature, TopP
	// and Seed as a free one.
	SampleConstrained bool `json:"sampleConstrained,omitempty"`
}

// LLMGenerateResult is llm.generate's result.
type LLMGenerateResult struct {
	Text             string  `json:"text"`
	FinishReason     string  `json:"finishReason"` // "stop" | "length"
	Model            string  `json:"model,omitempty"`
	PromptTokens     int     `json:"promptTokens"`
	CompletionTokens int     `json:"completionTokens"`
	Seconds          float64 `json:"seconds,omitempty"`         // generation time, excluding model load
	TokensPerSecond  float64 `json:"tokensPerSecond,omitempty"` // completion tokens / decode time
	Constrained      bool    `json:"constrained,omitempty"`     // JSON grammar applied
}

// LLMTokenParams is llm.token's params: the next piece of the reply.
type LLMTokenParams struct {
	Job  string `json:"job"`
	Text string `json:"text"`
}
