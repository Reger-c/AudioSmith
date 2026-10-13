package main

// The link between the UI and the engines: the protocol-2 supervisor (one
// audiosmith-engine process per role; M0 runs the gpu one) and the job
// scheduler that runs engine work as jobs. The Studio's takes and preloads
// are jobs owned by "studio:…"; the UI follows them through jobs.Updated
// and the workers through engine.StatusChanged, both on the library's bus.
//
// Integration step I1 moves the Studio's Generate to TakeService.Generate
// (jobs owned by "take:…"); until then startSynthesis submits the job
// itself and takeFinished saves the take.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/jobs"
)

type engineState int

const (
	engineStarting engineState = iota
	engineReady
	engineFailed
)

// studioOwner prefixes the owners of the jobs the Studio submits itself.
const studioOwner = "studio:"

// job is the UI's view of the Studio's job: one at a time.
type job struct {
	id           string // "" until Submit returns (or its first update arrives)
	owner        string // studioOwner + …: matches updates before the id is known
	kind         string // "synthesize" or "load"
	model        string
	started      time.Time
	status       string
	fraction     float64 // 0–1, or -1 when unknown
	audioSeconds float64
	take         *core.Take // what the result becomes, for "synthesize"
	cancelling   bool
}

// engineHost is the engine side of the app: the supervisor and the job
// scheduler. connectEngine makes them, unless a test set fakes.
type engineHost struct {
	bin    string // audiosmith-engine; "" when not found
	logDir string
	sup    engine.Supervisor
	sched  jobs.Scheduler
}

func (h *engineHost) Path() string { return h.bin }

// Stop interrupts the running jobs (they re-run or wait for Retry at the
// next start) and shuts the engines down.
func (h *engineHost) Stop() {
	done := make(chan struct{})
	go func() {
		if h.sched != nil {
			h.sched.Close()
		}
		close(done)
	}()
	if h.sup != nil {
		h.sup.Stop()
	}
	<-done
}

// engineLink is the engine's view state.
type engineLink struct {
	eng     *engineHost
	logPath string

	workers     []engine.WorkerStatus // by role, as last published
	engState    engineState           // the gpu worker's
	engVersion  string
	engPID      int
	engError    string // translated
	engTail     string
	loaded      []string // resident models of the gpu worker, most recent first
	loadedModel string   // the model the Studio used last (or loaded), "" after unloading

	job      *job
	jobError string

	// enginePost brings engine events to the main thread: a.post, or a
	// queue in tests that drive real engines.
	enginePost func(func())
}

// engineSummary is what the shell shows about the engines: the sidebar
// status today, the status bar and the ⌘E switcher (thread C) next. Thread
// B keeps it filled from the supervisor; the shell reads nothing else of
// the engine link.
type engineSummary struct {
	State   engineState
	Version string
	PID     int
	Err     string
	Loaded  []string // repos of resident models, most recent first
	Busy    bool     // a job runs
}

func (a *app) engineSummary() engineSummary {
	s := engineSummary{State: a.engState, Version: a.engVersion, PID: a.engPID, Err: a.engError, Busy: a.job != nil}
	s.Loaded = append(s.Loaded, a.loaded...)
	if len(s.Loaded) == 0 && a.loadedModel != "" {
		s.Loaded = []string{a.loadedModel}
	}
	return s
}

// newEngineProcess finds the engine among the app's resources.
func newEngineProcess(resources, logDir string) *engineHost {
	return &engineHost{bin: engine.Find(resources), logDir: logDir}
}

// connectEngine makes the supervisor and the scheduler (over the
// library's jobs table and bus) unless they are set, and routes their
// events to the main thread. The engine starts with restartEngine.
func (a *app) connectEngine(h *engineHost) {
	a.eng = h
	if a.enginePost == nil {
		a.enginePost = a.post
	}
	if h.logDir != "" {
		a.logPath = filepath.Join(h.logDir, "engine-gpu.log")
	}
	if h.sup == nil {
		cacheDir := hub.CacheDir()
		ring := a.logs
		h.sup = engine.NewSupervisor(engine.Config{
			Bin: h.bin, Roles: []proto.Role{proto.RoleGPU}, AppVersion: mygo.App.Version(),
			CacheDir: cacheDir, LogDir: h.logDir, Bus: a.bus,
			Log:          func(e core.LogEntry) { ring.Append(e) },
			MemoryBudget: int64(a.prefs.Engine.MemoryBudgetGB * (1 << 30)),
			// A failed load leaves an empty mlx-audio/<repo> dir behind,
			// and the next load would then wipe the repo's cache.
			Prepare: func(proto.Role) {
				removed, err := hub.CleanEngineCopies(cacheDir)
				if err != nil {
					ring.Append(core.LogEntry{Level: core.SeverityWarning, Source: "engine:gpu", Message: "cleaning the engine's model copies: " + err.Error()})
				} else if len(removed) > 0 {
					ring.Append(core.LogEntry{Source: "engine:gpu", Message: "removed incomplete model copies: " + strings.Join(removed, ", ")})
				}
			},
		})
	}
	if h.sched == nil {
		s := jobs.New(jobs.Options{
			Store:   a.lib.store.Jobs(),
			Publish: func(u jobs.Updated) { a.bus.Publish(u) },
			NewID:   func() string { return core.NewID(time.Now()) },
		})
		engine.RegisterExecutors(s, h.sup)
		h.sched = s
	}
	post := a.enginePost
	core.On(a.bus, func(ev engine.StatusChanged) { post(func() { a.workerChanged(ev.Status) }) })
	core.On(a.bus, func(u jobs.Updated) {
		if strings.HasPrefix(u.Job.Owner, studioOwner) && u.Chunk == nil {
			post(func() { a.jobUpdated(u.Job) })
		}
	})
	if err := h.sched.Start(context.Background()); err != nil {
		a.engState, a.engError = engineFailed, err.Error()
	}
}

// startSynthesis submits a tts.synthesize job whose result becomes take t.
func (a *app) startSynthesis(t *core.Take, req proto.SynthesizeParams) {
	params, err := json.Marshal(req)
	if err != nil {
		a.jobError = err.Error()
		return
	}
	status := i18n.T("Starting…")
	if a.loadedModel != t.Model {
		status = i18n.T("Loading model…")
	}
	a.jobError = ""
	a.job = &job{owner: studioOwner + t.ID, kind: "synthesize", model: t.Model, started: t.CreatedAt, status: status, fraction: -1, take: t}
	a.submit(jobs.Spec{Kind: proto.MethodTTSSynthesize, Lane: jobs.LaneGPU, Priority: jobs.PriorityInteractive,
		Params: params, Title: i18n.T("Generate take"), Owner: a.job.owner})
}

// preload loads the chosen model without generating, so the first take is quick.
func (a *app) preload() {
	repo := a.preset().Repo
	if a.engState != engineReady || a.job != nil || repo == "" || repo == a.loadedModel {
		return
	}
	params, _ := json.Marshal(proto.LoadParams{Model: repo})
	a.jobError = ""
	a.job = &job{owner: studioOwner + "load:" + core.NewID(time.Now()), kind: "load", model: repo, started: time.Now(),
		status: i18n.T("Loading model…"), fraction: -1}
	a.submit(jobs.Spec{Kind: proto.MethodModelsLoad, Lane: jobs.LaneGPU, Priority: jobs.PriorityInteractive,
		Params: params, Title: i18n.T("Load model"), Owner: a.job.owner})
}

// submit queues the Studio's job in the background; its id arrives with
// the result or with its first update, whichever is first.
func (a *app) submit(spec jobs.Spec) {
	sched, owner, post := a.eng.sched, spec.Owner, a.enginePost
	a.async(func() {
		id, err := sched.Submit(context.Background(), spec)
		post(func() {
			j := a.job
			if j == nil || j.owner != owner {
				return
			}
			if err != nil {
				a.job, a.jobError = nil, err.Error()
				return
			}
			j.id = id
			if j.cancelling {
				a.cancelJob(id)
			}
		})
	})
}

func (a *app) cancel() {
	if a.job == nil {
		return
	}
	a.job.status = i18n.T("Cancelling…")
	a.job.cancelling = true
	if a.job.id != "" {
		a.cancelJob(a.job.id)
	}
}

func (a *app) cancelJob(id string) {
	sched := a.eng.sched
	a.async(func() { sched.Cancel(context.Background(), id) })
}

// unload frees every model of the gpu worker.
func (a *app) unload() {
	if a.job != nil || (a.loadedModel == "" && len(a.loaded) == 0) {
		return
	}
	sup, post := a.eng.sup, a.enginePost
	a.async(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := sup.Call(ctx, proto.RoleGPU, "", proto.MethodModelsUnload, proto.UnloadParams{}, nil)
		post(func() {
			if err != nil {
				a.toast(i18n.T("Engine: %s", err.Error()))
				return
			}
			a.loaded, a.loadedModel = nil, ""
			a.toast(i18n.T("Model unloaded"))
		})
	})
}

// restartEngine starts the gpu worker, or replaces it.
func (a *app) restartEngine() {
	a.loaded, a.loadedModel = nil, ""
	a.engState, a.engError, a.engTail = engineStarting, "", ""
	sup, post := a.eng.sup, a.enginePost
	a.async(func() {
		if err := sup.Restart(proto.RoleGPU); err != nil {
			post(func() { a.engState, a.engError = engineFailed, err.Error() })
		}
	})
}

// workerChanged applies a worker's status; it runs on the main thread.
func (a *app) workerChanged(st engine.WorkerStatus) {
	replaced := false
	for i := range a.workers {
		if a.workers[i].Role == st.Role {
			a.workers[i], replaced = st, true
		}
	}
	if !replaced {
		a.workers = append(a.workers, st)
	}
	if st.Role != proto.RoleGPU {
		return
	}
	a.engPID, a.engTail, a.engError = st.PID, st.StderrTail, failureText(st)
	a.engVersion = ""
	if st.Hello != nil {
		a.engVersion = st.Hello.Version
	}
	switch st.State {
	case engine.WorkerReady:
		a.engState = engineReady
	case engine.WorkerStarting:
		a.engState = engineStarting
	default:
		a.engState = engineFailed
	}
	if st.State != engine.WorkerReady {
		a.loaded, a.loadedModel = nil, ""
		return
	}
	if st.Stats != nil {
		a.loaded = a.loaded[:0]
		for _, m := range st.Stats.Models {
			a.loaded = append(a.loaded, m.Model)
		}
		a.loadedModel = ""
		if len(a.loaded) > 0 {
			a.loadedModel = a.loaded[0]
		}
	}
}

// failureText says, translated, why a worker failed ("" when it did not).
func failureText(st engine.WorkerStatus) string {
	if st.State != engine.WorkerFailed {
		return ""
	}
	var msg string
	switch st.Reason {
	case engine.FailMissing:
		msg = i18n.T("audiosmith-engine was not found: build it with scripts/build-engine.sh, or set AUDIOSMITH_ENGINE.")
	case engine.FailProtocol:
		msg = i18n.T("Engine version mismatch: rebuild audiosmith-engine.") + " (" + st.Err + ")"
	case engine.FailHandshake:
		msg = i18n.T("The engine did not answer in time.")
	case engine.FailStuck:
		msg = i18n.T("A cancelled job did not stop, so the engine was stopped.")
	case engine.FailStart:
		msg = i18n.T("The engine could not be started: %s", st.Err)
	default:
		msg = i18n.T("The engine stopped") + ": " + st.Err
	}
	if !st.RestartAt.IsZero() {
		msg += " " + i18n.T("Restarting…")
	}
	return msg
}

// stageText is a job stage as the Studio shows it.
func stageText(stage string) string {
	switch stage {
	case "loading":
		return i18n.T("Loading model…")
	case "generating":
		return i18n.T("Generating…")
	case "writing":
		return i18n.T("Saving…")
	}
	return i18n.T("Working…")
}

// jobErrorText explains a failed job, translated where the code is known.
func jobErrorText(e *jobs.Error) string {
	if e == nil {
		return i18n.T("The job failed")
	}
	switch e.Code {
	case jobs.CodeInterrupted, jobs.CodeShutdown:
		return i18n.T("The engine stopped while it worked. Try again.")
	case engine.CodeEngineUnavailable:
		return i18n.T("The engine is not running. Restart it on the Engine page.")
	case proto.CodeModelNotDownloaded:
		return i18n.T("The model is not downloaded. Download it on the Models page.")
	case proto.CodeOOM:
		return i18n.T("Out of memory. Unload other models, or choose a smaller one.")
	}
	return e.Message
}

// jobUpdated applies an update of the Studio's job; it runs on the main thread.
func (a *app) jobUpdated(u jobs.Job) {
	j := a.job
	if j == nil || u.Owner != j.owner {
		return
	}
	j.id = u.ID
	switch u.State {
	case jobs.StateWaiting, jobs.StateQueued:
		if !j.cancelling {
			j.status = i18n.T("Waiting for the engine…")
		}
	case jobs.StateRunning:
		if j.cancelling {
			return
		}
		if u.Progress.Stage != "" {
			j.status = stageText(u.Progress.Stage)
		}
		j.fraction = u.Progress.Fraction
		if u.Progress.AudioSeconds > 0 {
			j.audioSeconds = u.Progress.AudioSeconds
		}
	case jobs.StateSucceeded:
		a.job = nil
		a.loadedModel = j.model
		if j.kind == "load" {
			var res proto.LoadResult
			json.Unmarshal(u.Result, &res)
			a.toast(i18n.T("Loaded %s in %.1f s", catalog.ShortRepo(j.model), res.Seconds))
			return
		}
		var res proto.SynthesizeResult
		if err := json.Unmarshal(u.Result, &res); err != nil {
			a.jobError = err.Error()
			return
		}
		t := j.take
		t.JobID = u.ID
		t.SampleRate, t.AudioSeconds, t.GenSeconds = res.SampleRate, res.AudioSeconds, res.Seconds
		if res.Seed != nil {
			t.Seed = *res.Seed
		}
		if res.Output != "" {
			t.File = filepath.Base(res.Output)
		}
		a.takeFinished(t)
	case jobs.StateCancelled:
		a.job = nil
		a.toast(i18n.T("Cancelled"))
	case jobs.StateFailed, jobs.StateInterrupted:
		a.job = nil
		a.jobError = jobErrorText(u.Err)
	}
}
