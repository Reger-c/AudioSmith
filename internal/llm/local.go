package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
)

// The local MLX LLM speaks the engine's llm.generate (protocol 2,
// docs/protocol-v2.md §5.4): a job on the gpu worker that streams its
// text as llm.token notifications. The types are the engine protocol's.
const (
	MethodGenerate = proto.MethodLLMGenerate // params GenerateParams → result GenerateResult
	NotifyToken    = proto.MethodLLMToken    // params TokenNotification, in order, before the result
)

type (
	GenerateParams    = proto.LLMGenerateParams
	GenerateResult    = proto.LLMGenerateResult
	TokenNotification = proto.LLMTokenParams
)

// EngineCaller sends one engine request and waits for its result. The app
// wires it to engine.Supervisor.Call on the gpu role (through a jobs
// executor if LLM work should queue behind TTS); notify receives the
// request's notifications in order, before the call returns.
type EngineCaller func(ctx context.Context, method string, params any, notify func(method string, params json.RawMessage)) (json.RawMessage, error)

// EngineBackend is the local MLX model behind the engine protocol.
type EngineBackend struct {
	ID           string // registry id; default "local"
	Call         EngineCaller
	DefaultModel string
	// ListModels returns the downloaded LLM catalogue entries; nil → DefaultModel only.
	ListModels func(ctx context.Context) ([]ModelInfo, error)
	// MapError converts the caller's errors (e.g. *proto.Error codes) to
	// *Error; nil uses the core sentinels only.
	MapError func(error) error
}

func (e *EngineBackend) id() string {
	if e.ID == "" {
		return "local"
	}
	return e.ID
}

func (e *EngineBackend) Info() BackendInfo {
	return BackendInfo{ID: e.id(), Transport: "engine", Local: true, JSONMode: JSONPromptMode}
}

func (e *EngineBackend) Chat(ctx context.Context, req Request) (Response, error) {
	if e.Call == nil {
		return Response{}, newErr(CodeUnsupported, "provider", e.id(), "feature", "engine")
	}
	model := req.Model
	if model == "" {
		model = e.DefaultModel
	}
	if model == "" {
		return Response{}, newErr(CodeConfig, "provider", e.id(), "field", "model")
	}
	msgs := req.Messages
	if req.Format == FormatJSON {
		msgs = withJSONInstruction(msgs, req.Schema, true)
	}
	p := GenerateParams{
		Model: model, MaxTokens: req.MaxTokens, Temperature: req.Temperature,
		TopP: req.TopP, Seed: req.Seed, Stop: req.Stop, Stream: req.OnDelta != nil,
		JSON: req.Format == FormatJSON,
	}
	for _, m := range msgs {
		p.Messages = append(p.Messages, proto.LLMMessage{Role: string(m.Role), Content: m.Content})
	}
	if req.Format == FormatJSON && req.Schema != nil {
		if b, err := json.Marshal(req.Schema); err == nil {
			p.Schema = b
		}
	}
	start := time.Now()
	var notify func(string, json.RawMessage)
	if req.OnDelta != nil {
		notify = func(method string, params json.RawMessage) {
			if method != NotifyToken {
				return
			}
			var t TokenNotification
			if json.Unmarshal(params, &t) == nil && t.Text != "" {
				req.OnDelta(t.Text)
			}
		}
	}
	raw, err := e.Call(ctx, MethodGenerate, p, notify)
	if err != nil {
		return Response{}, e.mapErr(ctx, err, model, req.Timeout)
	}
	var r GenerateResult
	if err := json.Unmarshal(raw, &r); err != nil {
		x := newErr(CodeBadResponse, "provider", e.id(), "detail", clip(string(raw), 200))
		x.Err = err
		return Response{}, x
	}
	if r.Model == "" {
		r.Model = model
	}
	return Response{
		Text: r.Text, FinishReason: normFinish(r.FinishReason), Model: r.Model, Provider: e.id(),
		Usage:   Usage{PromptTokens: r.PromptTokens, CompletionTokens: r.CompletionTokens, TotalTokens: r.PromptTokens + r.CompletionTokens},
		Latency: time.Since(start),
	}, nil
}

func (e *EngineBackend) mapErr(ctx context.Context, err error, model string, timeout time.Duration) error {
	if e.MapError != nil {
		if m := e.MapError(err); m != nil {
			var x *Error
			if errors.As(m, &x) {
				return x
			}
			err = m
		}
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, core.ErrCancelled) {
		if ctx.Err() == nil && errors.Is(err, core.ErrCancelled) {
			x := newErr(CodeCancelled)
			x.Err = err
			return x
		}
		return ctxErr(ctx, e.id(), err, timeout)
	}
	var perr *proto.Error
	if errors.As(err, &perr) {
		switch perr.Code {
		case proto.CodeContextTooLong, proto.CodeInputTooLong:
			x := newErr(CodeContextLength, "provider", e.id(), "model", model)
			x.Err = err
			return x
		case proto.CodeModelNotDownloaded:
			err = fmt.Errorf("%w: %w", core.ErrNotDownloaded, err)
		case proto.CodeLicenceRequired, proto.CodeUnsupported:
			err = fmt.Errorf("%w: %w", core.ErrUnsupported, err)
		case proto.CodeBusy, proto.CodeOOM:
			err = fmt.Errorf("%w: %w", core.ErrBusy, err)
		case proto.CodeBadRequest:
			x := newErr(CodeBadRequest, "provider", e.id(), "status", "engine", "detail", clip(perr.Message, 200))
			x.Err = err
			return x
		}
	}
	switch {
	case errors.Is(err, core.ErrNotDownloaded), errors.Is(err, core.ErrNotFound):
		x := newErr(CodeModelNotFound, "provider", e.id(), "model", model)
		x.Err = err
		return x
	case errors.Is(err, core.ErrLicenceRequired), errors.Is(err, core.ErrUnsupported):
		x := newErr(CodeUnsupported, "provider", e.id(), "feature", "model")
		x.Err = err
		return x
	case errors.Is(err, core.ErrEngine), errors.Is(err, core.ErrBusy):
		x := newErr(CodeServer, "provider", e.id(), "status", "engine")
		x.Retryable, x.Err = true, err
		return x
	}
	x := newErr(CodeServer, "provider", e.id(), "status", "engine")
	x.Err = err
	return x
}

func (e *EngineBackend) Models(ctx context.Context) ([]ModelInfo, error) {
	if e.ListModels != nil {
		return e.ListModels(ctx)
	}
	if e.DefaultModel == "" {
		return nil, nil
	}
	return []ModelInfo{{ID: e.DefaultModel}}, nil
}
