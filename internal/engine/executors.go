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

// ExecutorOptions configure the engine executors.
type ExecutorOptions struct {
	// ModelPath returns the local snapshot directory of a model (repo id),
	// or an error wrapping core.ErrNotDownloaded. When set, every job with
	// a model gets the path filled in (proto.LoadParams.Path, the others'
	// modelPath) as it
	// starts, so engines running offline load what the app downloaded. A
	// model that is not on disk fails the job with model_not_downloaded
	// without asking the engine. nil leaves params as submitted.
	ModelPath func(ctx context.Context, model string) (string, error)
	// Licences, when set, is asked again before a job loads or runs a
	// model (the services ask first; jobs submitted some other way, or
	// re-run after the acceptance was withdrawn, are caught here): a
	// refusal fails the job with licence_required.
	Licences core.LicenceService
}

// ExecutorKinds are the job kinds RegisterExecutors registers: every job
// method of the protocol except capture.start, which is interactive and
// runs outside the queue (StartCapture). Submit them on the lane of their
// role: jobs.Lane(proto.RoleOf(kind)). Dictation runs stt.transcribe and
// models.load on the listen worker too, as the kinds
// proto.KindFor(proto.RoleListen, method) on jobs.LaneListen.
var ExecutorKinds = []string{
	proto.MethodModelsLoad, proto.MethodTTSSynthesize, proto.MethodTTSEncodeVoice,
	proto.MethodSTTTranscribe, proto.MethodSTTAlign, proto.MethodVADDetect, proto.MethodEnhanceRun,
	proto.MethodMediaDecode, proto.MethodMediaEncode,
	proto.MethodSeparateRun, proto.MethodDiarizeRun,
	proto.MethodMediaMux, proto.MethodMediaThumbnail, proto.MethodMediaTranscode,
}

// RegisterExecutors registers the engine-backed job kinds (ExecutorKinds)
// on sched, each run on the worker of its role (proto.RoleOf). A job's
// Params are the method's params (proto.LoadParams, proto.SynthesizeParams,
// proto.TranscribeParams…) and its Result the method's result.
func RegisterExecutors(sched jobs.Scheduler, sup Supervisor, opts ...ExecutorOptions) {
	var o ExecutorOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	for _, m := range ExecutorKinds {
		sched.Register(m, executor(sup, proto.RoleOf(m), m, o))
	}
	// The listen worker's own speech-to-text (dictation): "listen:stt.transcribe"…
	for _, m := range []string{proto.MethodSTTTranscribe, proto.MethodModelsLoad} {
		sched.Register(proto.KindFor(proto.RoleListen, m), executor(sup, proto.RoleListen, m, o))
	}
}

// KindExecutor runs jobs of kind (a method, or "role:method" from
// proto.KindFor) on the worker its kind names, as RegisterExecutors's
// executors do without the options.
func KindExecutor(sup Supervisor, kind string) jobs.Executor {
	role, method := proto.SplitKind(kind)
	return executor(sup, role, method, ExecutorOptions{})
}

// Executor runs jobs of kind method as requests to the worker of role,
// with the job's id as request id. job.progress becomes the job's
// progress, tts.chunk its chunks (the chunk params JSON); a log line naming
// the job is left to the bus. Errors become *jobs.Error with the protocol's
// code: oom and busy are retryable, and so is the worker dying mid-job
// (jobs.CodeInterrupted) or not being ready (CodeEngineUnavailable) — the
// job's MaxAttempts decides whether it runs again.
func Executor(sup Supervisor, role proto.Role, method string) jobs.Executor {
	return executor(sup, role, method, ExecutorOptions{})
}

func executor(sup Supervisor, role proto.Role, method string, o ExecutorOptions) jobs.Executor {
	return jobs.ExecutorFunc(func(ctx context.Context, job jobs.Job, report func(jobs.Progress), chunk func(json.RawMessage)) (json.RawMessage, error) {
		params := job.Params
		if len(params) == 0 {
			params = json.RawMessage(`{}`)
		}
		if o.Licences != nil {
			if err := checkLicence(ctx, params, o.Licences); err != nil {
				return nil, err
			}
		}
		if o.ModelPath != nil {
			var err error
			if params, err = withModelPath(ctx, method, params, o.ModelPath); err != nil {
				return nil, err
			}
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

// checkLicence fails a job whose model's licence was not accepted.
func checkLicence(ctx context.Context, params json.RawMessage, gate core.LicenceService) error {
	var head struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(params, &head) != nil || head.Model == "" {
		return nil // the engine answers bad_request
	}
	err := gate.Check(ctx, head.Model)
	if err == nil {
		return nil
	}
	if errors.Is(err, core.ErrLicenceRequired) {
		data, _ := json.Marshal(map[string]string{"model": head.Model})
		return &jobs.Error{Code: proto.CodeLicenceRequired, Message: err.Error(), Data: data}
	}
	return &jobs.Error{Code: proto.CodeInternal, Message: err.Error()}
}

// withModelPath fills the local snapshot directory of the job's model
// into its params. A model that is not downloaded fails the job with
// model_not_downloaded (not retryable: the user has to download it).
func withModelPath(ctx context.Context, method string, params json.RawMessage, resolve func(context.Context, string) (string, error)) (json.RawMessage, error) {
	var head struct {
		Model     string `json:"model"`
		Path      string `json:"path"`
		ModelPath string `json:"modelPath"`
	}
	if err := json.Unmarshal(params, &head); err != nil {
		return nil, &jobs.Error{Code: proto.CodeBadRequest, Message: "params: " + err.Error()}
	}
	if head.Model == "" || head.Path != "" || head.ModelPath != "" {
		return params, nil // the engine answers bad_request, or the caller chose a path
	}
	dir, err := resolve(ctx, head.Model)
	if err != nil {
		if errors.Is(err, core.ErrNotDownloaded) {
			data, _ := json.Marshal(map[string]string{"model": head.Model})
			return nil, &jobs.Error{Code: proto.CodeModelNotDownloaded, Message: err.Error(), Data: data}
		}
		return nil, &jobs.Error{Code: proto.CodeInternal, Message: err.Error()}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(params, &fields); err != nil {
		return nil, &jobs.Error{Code: proto.CodeBadRequest, Message: "params: " + err.Error()}
	}
	key := "modelPath"
	if method == proto.MethodModelsLoad {
		key = "path"
	}
	fields[key], _ = json.Marshal(dir)
	return json.Marshal(fields)
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
