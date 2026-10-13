package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
)

// This file is the protocol-2 contract thread B implements (M0): a
// Supervisor that runs one engine process per role, handshakes, routes
// requests and notifications by id, restarts crashed workers and keeps the
// resident models of all workers under one memory budget. See
// docs/protocol-v2.md for the wire format and docs/architecture.md for
// how the job scheduler sits on top.

// WorkerState is where an engine process is in its life.
type WorkerState string

const (
	WorkerStopped  WorkerState = "stopped"  // not started, or stopped on purpose
	WorkerStarting WorkerState = "starting" // spawned, engine.hello not answered yet
	WorkerReady    WorkerState = "ready"    // handshake done, accepting requests
	WorkerFailed   WorkerState = "failed"   // exited or refused; Err and StderrTail say why
)

// WorkerStatus is a snapshot of one worker, for the status bar, the ⌘E
// switcher and the Engine/Diagnostics pages.
type WorkerStatus struct {
	Role       proto.Role
	State      WorkerState
	PID        int
	Hello      *proto.HelloResult // nil until ready
	Stats      *proto.StatsResult // last engine.stats, polled while jobs run; nil before the first
	Running    []string           // ids of requests in flight
	Err        string             // why it failed: crash, protocol mismatch, missing binary…
	StderrTail string             // last stderr lines after a crash
	Restarts   int                // automatic restarts since Start
	Since      time.Time          // when State last changed
}

// StatusChanged is published on the core.Bus whenever a worker's State,
// PID, loaded models or Err change. Topic "engine.status".
type StatusChanged struct{ Status WorkerStatus }

func (StatusChanged) Topic() string { return "engine.status" }

// Config configures a Supervisor.
type Config struct {
	Bin        string       // audiosmith-engine executable (Find)
	Roles      []proto.Role // roles to run; M0 runs only proto.RoleGPU
	AppVersion string       // sent in engine.hello
	CacheDir   string       // HF hub cache (hub.CacheDir), sent in engine.hello
	Offline    bool         // engines never download; the Go hub does (M0 goal)
	LogDir     string       // stderr of role r goes to LogDir/engine-<r>.log (rotated)
	Bus        *core.Bus    // StatusChanged, and log lines as core.LogAppended
	// MemoryBudget is the bytes resident models of all workers may take
	// together; above it the supervisor unloads the least recently used
	// idle models (models.unload). 0 means 60% of physical memory.
	MemoryBudget int64
	// HelloTimeout bounds the handshake (default 30 s: first launch
	// compiles Metal shaders).
	HelloTimeout time.Duration
	// MaxRestarts bounds automatic restarts within RestartWindow (default 3
	// in 10 min); beyond it the worker stays failed until Restart.
	MaxRestarts   int
	RestartWindow time.Duration
}

// Supervisor runs and talks to the engine workers. Goroutine-safe.
type Supervisor interface {
	// Start spawns the configured roles and returns; handshakes complete in
	// the background and are reported as StatusChanged.
	Start(ctx context.Context) error

	// Call sends a request to the worker of role and waits for its
	// response, returning the raw result or an error:
	//   - *proto.Error for an error response (use errors.Is with a code);
	//   - an error wrapping core.ErrEngine when the worker is not ready,
	//     or exits before answering (a job then counts as interrupted).
	// id is the request id; job methods pass the jobs.Job id so logs and
	// notifications correlate, "" lets the supervisor number control
	// requests. notify receives the job's notifications (job.progress,
	// tts.chunk, log lines naming it) on the reader goroutine, in order,
	// before Call returns; it must not block. Cancelling ctx sends
	// job.cancel for job methods and Call returns once the engine answers
	// (or after a 5 s grace, killing the worker if it is stuck).
	Call(ctx context.Context, role proto.Role, id, method string, params any, notify func(proto.Message)) (json.RawMessage, error)

	// Status returns a snapshot of every configured worker.
	Status() []WorkerStatus
	// Restart stops the worker of role (cancelling its calls) and starts a
	// fresh one; it resets the restart budget.
	Restart(role proto.Role) error
	// Stop asks every worker to shut down (engine.shutdown), then kills
	// what is left after a grace period. Calls in flight fail with core.ErrEngine.
	Stop()
}

// ErrWorkerExited is wrapped by Call errors when the worker died mid-call.
var ErrWorkerExited = fmt.Errorf("engine worker exited: %w", core.ErrEngine)
