package engine

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
)

// Job error codes the executors add to the protocol's (proto.Code*).
const (
	// CodeEngineUnavailable: the worker was not ready (starting after a
	// crash, failed, stopped). Retryable.
	CodeEngineUnavailable = "engine_unavailable"
)

// RegisterExecutors registers the engine-backed job kinds of M0 on sched:
// "models.load" and "tts.synthesize", run on the gpu worker. A job's
// Params are the method's params (proto.LoadParams, proto.SynthesizeParams)
// and its Result the method's result.
func RegisterExecutors(sched jobs.Scheduler, sup Supervisor) {
	for _, m := range []string{proto.MethodModelsLoad, proto.MethodTTSSynthesize} {
		sched.Register(m, Executor(sup, proto.RoleGPU, m))
	}
}

// Executor runs jobs of kind method as requests to the worker of role,
// with the job's id as request id. job.progress becomes the job's
// progress, tts.chunk its chunks (the chunk params JSON); a log line naming
// the job is left to the bus. Errors become *jobs.Error with the protocol's
// code: oom and busy are retryable, and so is the worker dying mid-job
// (jobs.CodeInterrupted) or not being ready (CodeEngineUnavailable) — the
// job's MaxAttempts decides whether it runs again.
func Executor(sup Supervisor, role proto.Role, method string) jobs.Executor {
	return jobs.ExecutorFunc(func(ctx context.Context, job jobs.Job, report func(jobs.Progress), chunk func(json.RawMessage)) (json.RawMessage, error) {
		params := job.Params
		if len(params) == 0 {
			params = json.RawMessage(`{}`)
		}
		notify := func(m proto.Message) {
			switch m.Method {
			case proto.MethodJobProgress:
				var p proto.ProgressParams
				if m.DecodeParams(&p) != nil {
					return
				}
				prog := jobs.Progress{Stage: p.Stage, Fraction: -1, AudioSeconds: p.AudioSeconds, Message: p.Message}
				if p.Fraction != nil {
					prog.Fraction = *p.Fraction
				}
				report(prog)
			case proto.MethodTTSChunk:
				if chunk != nil {
					chunk(m.Params)
				}
			}
		}
		res, err := sup.Call(ctx, role, job.ID, method, params, notify)
		if err == nil {
			return res, nil
		}
		return nil, jobError(ctx, err)
	})
}

// jobError maps a Call error to a *jobs.Error (or ctx's error when the
// job was cancelled).
func jobError(ctx context.Context, err error) error {
	var perr *proto.Error
	switch {
	case errors.As(err, &perr):
		if perr.Code == proto.CodeCancelled && ctx.Err() != nil {
			return ctx.Err()
		}
		return &jobs.Error{Code: perr.Code, Message: perr.Message, Data: perr.Data, Retryable: proto.Retryable(perr.Code)}
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, ErrWorkerExited):
		return &jobs.Error{Code: jobs.CodeInterrupted, Message: err.Error(), Retryable: true}
	case errors.Is(err, core.ErrEngine):
		return &jobs.Error{Code: CodeEngineUnavailable, Message: err.Error(), Retryable: true}
	}
	return &jobs.Error{Code: proto.CodeInternal, Message: err.Error()}
}
