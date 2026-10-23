// Package jobs is AudioSmith's work queue: every piece of work that takes
// longer than a click (TTS, transcription, separation, downloads, muxing,
// a dub's pipeline) is a Job, scheduled on a Lane by Priority, persisted
// so it survives a crash or quit, and cancellable by id.
//
// This file is the contract (thread B implements the Scheduler in M0):
//
//   - The scheduler owns the truth about jobs and persists every state
//     change through a Store before publishing Updated on the core.Bus.
//   - Executors do the work. Engine-backed kinds ("tts.synthesize",
//     "stt.transcribe"…) are executed by internal/engine through its
//     Supervisor; Go-side kinds ("model.download", "take.export") by the
//     packages that own them. Executors are stateless and must honour ctx.
//   - Lanes bound concurrency by resource: the GPU worker runs one job at a
//     time, the listen worker one, media and network a few.
//   - Within a lane, higher Priority runs first, then older jobs. Running
//     jobs are not preempted; an interactive request waits for the current
//     GPU job to finish (or for the user to cancel it).
//   - A job may depend on others (a DAG). It waits until they all
//     succeed; if one fails or is cancelled, it fails/cancels too.
//   - On start-up, jobs found "running" were interrupted: Idempotent ones
//     are re-queued, others become Interrupted for the user to retry.
//
// Imports: nothing internal (Updated satisfies core.Event structurally);
// internal/store implements Store.
package jobs

import (
	"context"
	"encoding/json"
	"time"
)

// Lane is a pool of execution slots for one kind of resource.
type Lane string

const (
	LaneGPU    Lane = "gpu"    // engine --role gpu: TTS, STT, LLM, separation… 1 slot
	LaneListen Lane = "listen" // engine --role listen: dictation, capture. 1 slot
	LaneMedia  Lane = "media"  // engine --role media: decode/encode/mux. 2 slots
	LaneNet    Lane = "net"    // Go: model downloads, URL import. 3 slots
	LaneCPU    Lane = "cpu"    // Go: text pipeline, DSP, exports. 2 slots
)

// Priority orders jobs within a lane: higher first.
type Priority int

const (
	PriorityBackground  Priority = -10 // batch dubbing, gallery previews, watch folders
	PriorityNormal      Priority = 0   // longform chapters, imports
	PriorityInteractive Priority = 10  // the user waits: Studio Generate, API requests, previews
)

// State is where a job is in its life.
type State string

const (
	StateWaiting     State = "waiting"     // dependencies not done yet
	StateQueued      State = "queued"      // runnable, waiting for a lane slot
	StateRunning     State = "running"     // an executor has it
	StateSucceeded   State = "succeeded"   // terminal
	StateFailed      State = "failed"      // terminal (after MaxAttempts)
	StateCancelled   State = "cancelled"   // terminal
	StateInterrupted State = "interrupted" // was running when the app or worker died; Retry re-queues it
)

// Terminal reports whether the job will not change again (without Retry).
func (s State) Terminal() bool {
	return s == StateSucceeded || s == StateFailed || s == StateCancelled
}

// Spec describes work to do. It is what Submit takes and what is persisted.
type Spec struct {
	// Kind names the executor: an engine method ("tts.synthesize") or a
	// Go-side kind ("model.download"). Registered with Scheduler.Register.
	Kind     string
	Lane     Lane
	Priority Priority
	// Params is the executor's input, JSON so it persists as-is. For engine
	// kinds it is the protocol's params object (internal/engine/proto).
	Params json.RawMessage
	// Title is shown in the jobs list and notifications ("Generate take").
	Title string
	// Owner links the job to what it works for, as "kind:id" ("take:01J…",
	// "project:01J…"), so a page can find its jobs after a restart.
	Owner string
	// After lists job ids that must succeed first.
	After []string
	// MaxAttempts bounds automatic retries of failures that are marked
	// retryable (engine crash, OOM). 0 means 1.
	MaxAttempts int
	// Idempotent jobs are re-run automatically after an interruption
	// (their output is written atomically and keyed by input).
	Idempotent bool
}

// Progress is a running job's latest report.
type Progress struct {
	Stage        string  `json:"stage,omitempty"`        // "loading", "generating", "writing", "downloading"…
	Fraction     float64 `json:"fraction"`               // 0–1, or -1 when unknown
	Message      string  `json:"message,omitempty"`      // human-readable, already localized by the executor or not at all
	AudioSeconds float64 `json:"audioSeconds,omitempty"` // TTS: audio made so far
	Bytes        int64   `json:"bytes,omitempty"`        // downloads: done
	TotalBytes   int64   `json:"totalBytes,omitempty"`   // downloads: expected
	ETA          float64 `json:"eta,omitempty"`          // downloads: seconds left (0: not known)
}

// Error is a failed job's reason, typed so the UI can offer a fix.
type Error struct {
	Code      string          `json:"code"` // proto error codes ("model_not_downloaded", "oom"…) or Go-side ones
	Message   string          `json:"message"`
	Data      json.RawMessage `json:"data,omitempty"`
	Retryable bool            `json:"retryable,omitempty"`
}

func (e *Error) Error() string { return e.Message }

// Job is a Spec with its state.
type Job struct {
	ID string // core.NewID
	Spec
	State    State
	Attempt  int // 1-based attempt running or last run
	Progress Progress
	Result   json.RawMessage // executor output on success
	Err      *Error          // on failure
	Created  time.Time
	Started  time.Time // zero until it runs
	Finished time.Time // zero until terminal
}

// Updated is published on the core.Bus after every state or progress
// change (progress at most ~10×/s per job). Topic "job.updated".
type Updated struct {
	Job Job
	// Chunk carries streamed output of this update, e.g. a tts.chunk's
	// params (proto.ChunkParams JSON), when the job streams. Not persisted.
	Chunk json.RawMessage
}

func (Updated) Topic() string { return "job.updated" }

// Node is one job of a graph submission. Key names it within the graph;
// After may name other nodes' keys or existing job ids.
type Node struct {
	Key  string
	Spec Spec
}

// Filter selects jobs to list. The zero value is every unfinished job.
type Filter struct {
	States []State // nil = every non-terminal state
	Lane   Lane
	Owner  string // exact, or a "kind:" prefix
	Kind   string
	Since  time.Time
	Limit  int
}

// Scheduler queues, runs, persists and cancels jobs. Goroutine-safe.
type Scheduler interface {
	// Register sets the executor of a kind. Call before Start.
	Register(kind string, ex Executor)
	// Start loads persisted jobs (re-queueing or interrupting the ones
	// that were running) and starts dispatching.
	Start(ctx context.Context) error
	// Submit queues one job and returns its id.
	Submit(ctx context.Context, spec Spec) (string, error)
	// SubmitGraph queues a DAG atomically and returns each node's job id by key.
	SubmitGraph(ctx context.Context, nodes []Node) (map[string]string, error)
	// Cancel cancels a job and, transitively, the jobs that depend on it.
	// Running jobs get their context cancelled; cancelling a terminal job is a no-op.
	Cancel(ctx context.Context, id string) error
	// Retry re-queues a failed, cancelled or interrupted job (new attempt).
	Retry(ctx context.Context, id string) error
	Get(ctx context.Context, id string) (Job, error)
	List(ctx context.Context, f Filter) ([]Job, error)
	// Idle reports whether no job is queued or running (e.g. to allow an
	// update to relaunch the app).
	Idle() bool
	// Close stops dispatching, cancels running jobs (they become
	// interrupted) and waits for executors to return.
	Close() error
}

// Executor runs jobs of one kind.
type Executor interface {
	// Run does the work. It reports progress with report (cheap, may be
	// called often; the scheduler throttles), streams chunks with chunk
	// (nil when the job did not ask to stream), returns the result JSON,
	// or an error: a *Error for typed failures, ctx.Err() when cancelled.
	Run(ctx context.Context, job Job, report func(Progress), chunk func(json.RawMessage)) (json.RawMessage, error)
}

// ExecutorFunc adapts a function to Executor.
type ExecutorFunc func(ctx context.Context, job Job, report func(Progress), chunk func(json.RawMessage)) (json.RawMessage, error)

func (f ExecutorFunc) Run(ctx context.Context, job Job, report func(Progress), chunk func(json.RawMessage)) (json.RawMessage, error) {
	return f(ctx, job, report, chunk)
}

// Store persists jobs (internal/store implements it on SQLite: tables jobs
// and job_events). Save is an upsert of the whole job.
type Store interface {
	SaveJob(ctx context.Context, j Job) error
	LoadJobs(ctx context.Context, f Filter) ([]Job, error)
	// DeleteJobs removes terminal jobs finished before t (housekeeping).
	DeleteJobs(ctx context.Context, finishedBefore time.Time) error
}

// LaneSlots is the default concurrency of each lane.
var LaneSlots = map[Lane]int{
	LaneGPU:    1,
	LaneListen: 1,
	LaneMedia:  2,
	LaneNet:    3,
	LaneCPU:    2,
}
