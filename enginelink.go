package main

// The link between the UI and the engines: the protocol-2 supervisor (one
// audiosmith-engine process per role; M0 runs the gpu one) and the job
// scheduler of the library, which runs engine work as jobs. The UI
// follows its jobs through jobs.Updated and the workers through
// engine.StatusChanged, both on the library's bus.
//
// The Studio's takes are made by TakeService.Generate: their jobs are
// owned by "take:<id>", and the take arrives with TakeCreated once the
// service saved it. Preloads are the Studio's own models.load jobs, owned
// by "studio:load:…". Engines run offline: the engine executors fill in
// the local snapshot of the model (downloaded by the hub, Models page).

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/licence"
)

type engineState int

const (
	engineStarting engineState = iota
	engineReady
	engineFailed
)

// studioOwner prefixes the owners of the jobs the Studio submits itself
// (preloads); takeOwner those of the jobs TakeService submits.
const (
	studioOwner = "studio:"
	takeOwner   = "take:"
)

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
	takeID       string // the take a "synthesize" job makes
	cancelling   bool
}

// engineHost is the engine side of the app: the supervisor, and the job
// scheduler (the library's) it runs jobs for. connectEngine makes the
// supervisor, unless a test set a fake.
type engineHost struct {
	bin    string // audiosmith-engine; "" when not found
	logDir string
	sup    engine.Supervisor
	sched  jobs.Scheduler
	stop   sync.Once
}

func (h *engineHost) Path() string { return h.bin }

// Stop interrupts the running jobs (they re-run or wait for Retry at the
// next start) and shuts the engines down.
func (h *engineHost) Stop() {
	h.stop.Do(func() {
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
	})
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
	// needDownload is the model the last Generate or preload found missing:
	// the Studio offers to download it next to the error.
	needDownload *catalog.Entry

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

// connectEngine makes the supervisor (unless a test set one), registers
// the engine's job executors on the library's scheduler and routes engine
// and job events to the main thread. startEngine starts both.
func (a *app) connectEngine(h *engineHost) {
	a.eng = h
	if a.enginePost == nil {
		a.enginePost = a.post
	}
	if h.logDir != "" {
		a.logPath = filepath.Join(h.logDir, "engine-gpu.log")
	}
	h.sched = a.lib.sched
	if h.sup == nil {
		lib := a.lib
		ring := a.logs
		h.sup = engine.NewSupervisor(engine.Config{
			Bin: h.bin, Roles: []proto.Role{proto.RoleGPU}, AppVersion: mygo.App.Version(),
			// The microphone and media workers start when first used.
			OnDemand: []proto.Role{proto.RoleListen, proto.RoleMedia},
			CacheDir: modelCache(a.prefs), Offline: true, LogDir: h.logDir, Bus: a.bus,
			Log:          func(e core.LogEntry) { ring.Append(e) },
			MemoryBudget: gb(a.prefs.Engine.MemoryBudgetGB),
			IdleUnload:   time.Duration(a.prefs.Models.AutoUnload) * time.Minute,
			// A failed load leaves an empty mlx-audio/<repo> dir behind,
			// and the next load would then wipe the repo's cache.
			// Only the gpu worker loads models: cleaning while another
			// worker starts could remove a copy the gpu worker is writing.
			Prepare: func(role proto.Role) {
				if role != proto.RoleGPU {
					return
				}
				cacheDir := modelCache(lib.settings.Get())
				removed, err := hub.CleanEngineCopies(cacheDir)
				if err != nil {
					ring.Append(core.LogEntry{Level: core.SeverityWarning, Source: "engine:gpu", Message: "cleaning the engine's model copies: " + err.Error()})
				} else if len(removed) > 0 {
					ring.Append(core.LogEntry{Source: "engine:gpu", Message: "removed incomplete model copies: " + strings.Join(removed, ", ")})
				}
			},
		})
		// Before OnQuit, where the library closes the database the
		// scheduler records the interrupted jobs in.
		mygo.App.OnWillQuit(func(*mygo.QuitEvent) { h.Stop() })
	}
	h.sup = withMediaTools(h.sup, a.mediaTool) // M7a: ffmpeg behind the media methods (ffmpeg_engine.go)
	engine.RegisterExecutors(h.sched, h.sup, engine.ExecutorOptions{
		ModelPath: a.lib.modelSnapshot,
		Licences:  a.lib.licences,
	})
	post := a.enginePost
	core.On(a.bus, func(ev engine.StatusChanged) { post(func() { a.workerChanged(ev.Status) }) })
	core.On(a.bus, func(u jobs.Updated) {
		if u.Chunk != nil {
			return
		}
		post(func() {
			a.jobsChanged(u.Job) // shell_status.go: every job, for the status bar
			if u.Job.Kind == hub.KindDownload {
				a.downloadUpdated(u.Job) // shell_models.go
			}
			if strings.HasPrefix(u.Job.Owner, studioOwner) || strings.HasPrefix(u.Job.Owner, takeOwner) {
				a.jobUpdated(u.Job)
			}
		})
	})
}

// gb converts the settings' gigabytes to bytes.
func gb(n float64) int64 { return int64(n * (1 << 30)) }

// startEngine starts the gpu worker, then the scheduler: jobs left from
// the last run (downloads, takes) continue once the engine is up. It runs
// once, when the window opens.
func (a *app) startEngine() {
	a.engState, a.engError, a.engTail = engineStarting, "", ""
	sup, sched, post := a.eng.sup, a.eng.sched, a.enginePost
	a.async(func() {
		ctx := context.Background()
		err := sup.Start(ctx)
		if err == nil {
			err = sched.Start(ctx)
		}
		active, _ := sched.List(ctx, jobs.Filter{})
		workers := sup.Status()
		post(func() {
			// Workers whose events have not arrived yet (events that did
			// are newer than this snapshot may be).
			for _, st := range workers {
				known := slices.ContainsFunc(a.workers, func(w engine.WorkerStatus) bool { return w.Role == st.Role })
				if !known && st.State != engine.WorkerStopped {
					a.workerChanged(st)
				}
			}
			if err != nil {
				a.engState, a.engError = engineFailed, err.Error()
			}
			for _, j := range active {
				a.jobsChanged(j)
				if j.Kind == hub.KindDownload {
					a.downloadUpdated(j)
				}
			}
		})
	})
}

// startTake asks TakeService for the take whose recipe is t (its id
// chosen) and follows its job, owned by "take:<id>". The take arrives with
// TakeCreated (takeCreated), after the service saved it.
func (a *app) startTake(t core.Take) {
	status := i18n.T("Starting…")
	if a.loadedModel != t.Model {
		status = i18n.T("Loading model…")
	}
	owner := takeOwner + t.ID
	a.jobError, a.needDownload = "", nil
	a.awaitTake = t.ID
	a.job = &job{owner: owner, kind: "synthesize", model: t.Model, started: time.Now(), status: status, fraction: -1, takeID: t.ID}
	takes, post := a.lib.svc.Takes, a.enginePost
	a.async(func() {
		id, err := takes.Generate(context.Background(), core.TakeRequest{Recipe: t})
		post(func() {
			j := a.job
			if j == nil || j.owner != owner {
				return
			}
			if err != nil {
				a.job = nil
				if a.awaitTake == t.ID {
					a.awaitTake = ""
				}
				a.generateRefused(t, err)
				return
			}
			j.id = id
			if j.cancelling {
				a.cancelJob(id)
			}
		})
	})
}

// generateRefused explains why Generate did not queue the take, and
// offers the fix: the licence dialog, or a download.
func (a *app) generateRefused(t core.Take, err error) {
	e := entryFor(t.Model)
	var lic *core.LicenceRequiredError
	switch {
	case errors.As(err, &lic):
		a.askLicence(e, func() { a.generate() })
	case errors.Is(err, core.ErrNotDownloaded):
		a.jobError = i18n.T("%s is not downloaded yet.", e.Label)
		a.needDownload = &e
	case errors.As(err, new(*core.NeedsReferenceError)):
		a.jobError = engineErrorText(err)
	default:
		a.jobError = err.Error()
	}
}

// preload loads the chosen model without generating, so the first take is quick.
func (a *app) preload() {
	e := a.preset()
	repo := e.Repo
	if a.engState != engineReady || a.job != nil || repo == "" || repo == a.loadedModel {
		return
	}
	if licence.Required(e) != nil && !licence.Covers(a.accepted, e) {
		a.askLicence(e, a.preload)
		return
	}
	params, _ := json.Marshal(proto.LoadParams{Model: repo})
	a.jobError, a.needDownload = "", nil
	a.job = &job{owner: studioOwner + "load:" + core.NewID(time.Now()), kind: "load", model: repo, started: time.Now(),
		status: i18n.T("Loading model…"), fraction: -1}
	a.submit(jobs.Spec{Kind: proto.MethodModelsLoad, Lane: jobs.LaneGPU, Priority: jobs.PriorityInteractive,
		Params: params, Title: i18n.T("Load model"), Owner: a.job.owner})
}

// submit queues the Studio's own job (a preload) in the background; its
// id arrives with the result or with its first update, whichever is first.
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

// flushEngine drops MLX's buffer cache in the gpu worker (⌘E).
func (a *app) flushEngine() {
	sup, post := a.eng.sup, a.enginePost
	a.async(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := sup.Call(ctx, proto.RoleGPU, "", proto.MethodFlush, proto.Empty{}, nil)
		post(func() {
			if err != nil {
				a.toast(i18n.T("Engine: %s", err.Error()))
				return
			}
			a.toast(i18n.T("Cache cleared"))
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
	case "transcribing":
		return i18n.T("Transcribing…")
	case "aligning":
		return i18n.T("Aligning words…")
	case "detecting":
		return i18n.T("Finding speech…")
	case "enhancing":
		return i18n.T("Removing noise…")
	case "decoding":
		return i18n.T("Reading audio…")
	case "encoding":
		return i18n.T("Preparing the voice…")
	case "recording":
		return i18n.T("Recording…")
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
	case proto.CodeLicenceRequired:
		return i18n.T("The model's licence has not been accepted. Review it on the Models page.")
	case proto.CodeOOM:
		return i18n.T("Out of memory. Unload other models, or choose a smaller one.")
	case proto.CodeInputTooLong:
		return i18n.T("The text is too long for this model in one piece. Split it into shorter parts.")
	case proto.CodeFileNotFound:
		var d struct{ Path string }
		if json.Unmarshal(e.Data, &d) == nil && d.Path != "" {
			return i18n.T("File not found: %s", d.Path)
		}
		return i18n.T("A file the job needs was not found.")
	case proto.CodeUnsupportedMedia:
		return i18n.T("This file's format cannot be read. Convert it to WAV, M4A or MP3 and try again.")
	case proto.CodePermissionDenied:
		return i18n.T("AudioSmith may not use the microphone. Allow it in System Settings → Privacy & Security → Microphone.")
	case proto.CodeDeviceUnavailable:
		return i18n.T("The microphone is not available. Check that it is connected, or choose another input.")
	case core.CodeNeedsReference:
		var d struct{ Model string }
		json.Unmarshal(e.Data, &d)
		if d.Model == "" {
			return needsReferenceText(modelInMessage(e.Message))
		}
		return needsReferenceText(entryFor(d.Model).Label)
	case proto.CodeBadRequest:
		// The engine's own refusal of a clone-only model without a
		// reference (MOSS-TTS-Nano), in older or newer words.
		if m := strings.ToLower(e.Message); strings.Contains(m, "only clones voices") || strings.Contains(m, "requires refaudio") {
			return needsReferenceText(modelInMessage(e.Message))
		}
	}
	return readableEngineMessage(e.Message)
}

// needsReferenceText explains that model only clones voices, wherever a
// take was refused for want of a reference clip.
func needsReferenceText(model string) string {
	if model == "" {
		model = "MOSS-TTS-Nano"
	}
	return i18n.T("%s only clones voices: it needs a reference clip or a saved cloned voice.", model)
}

// modelInMessage is the model an engine message starts with
// ("MOSS-TTS-Nano-100M only clones…"), as the UI names it, or "".
func modelInMessage(msg string) string {
	for _, e := range catalog.Builtin().Entries {
		if strings.HasPrefix(msg, catalog.ShortRepo(e.Repo)+" ") || strings.HasPrefix(msg, e.Label+" ") {
			return e.Label
		}
	}
	return ""
}

// readableEngineMessage turns Cocoa's generic sentence for a Swift error
// without a description ("The operation couldn’t be completed.
// (MLXAudioTTS.MossTTSNanoError error 0.)", what engines before 0.3.1
// sent) into one that at least names the error; other messages pass.
func readableEngineMessage(msg string) string {
	// Its English and Chinese forms (the engine runs in the Mac's language).
	if !strings.HasPrefix(msg, "The operation couldn") && !strings.HasPrefix(msg, "未能完成操作") {
		return msg
	}
	msg = strings.NewReplacer("（", "(", "）", ")", "错误", " error ", "。", ".").Replace(msg)
	i, j := strings.LastIndex(msg, "("), strings.LastIndex(msg, ")")
	if i < 0 || j < i {
		return i18n.T("The engine failed without saying why.")
	}
	name := strings.Join(strings.Fields(strings.TrimSuffix(strings.TrimSpace(msg[i+1:j]), ".")), " ")
	return i18n.T("The engine failed: %s.", name)
}

// engineErrorText explains an error of an engine call or job (a
// *jobs.Error from the executors and engine.Probe/Devices/StartCapture),
// translated where the code is known.
func engineErrorText(err error) string {
	var je *jobs.Error
	if errors.As(err, &je) {
		return jobErrorText(je)
	}
	var nr *core.NeedsReferenceError
	if errors.As(err, &nr) {
		return needsReferenceText(nr.Model)
	}
	if errors.Is(err, context.Canceled) {
		return i18n.T("Cancelled")
	}
	return err.Error()
}

// Engine calls outside the job queue, for the areas (thread V's reference
// recorder, imports). Each runs on a goroutine and calls then on the main
// thread; errors explain themselves with engineErrorText.

// probeMedia describes a media file (media.probe on the media worker).
func (a *app) probeMedia(path string, then func(proto.ProbeResult, error)) {
	sup, post := a.eng.sup, a.enginePost
	a.async(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		info, err := engine.Probe(ctx, sup, path)
		post(func() { then(info, err) })
	})
}

// captureDevices lists the audio inputs, the system default first.
func (a *app) captureDevices(then func([]proto.CaptureDevice, error)) {
	sup, post := a.eng.sup, a.enginePost
	a.async(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		devs, err := engine.Devices(ctx, sup)
		post(func() { then(devs, err) })
	})
}

// startRecording records p from the microphone. level gets the meter
// (about 20 Hz) and then the started recording (or why it could not
// start), both on the main thread; stopRecording or Recording.Cancel end
// it. The recording also ends by itself (p.MaxSeconds, the device going
// away): watch Recording.Done, or call stopRecording, which is safe then.
func (a *app) startRecording(p proto.CaptureParams, level func(proto.LevelParams), then func(*engine.Recording, error)) {
	sup, post := a.eng.sup, a.enginePost
	a.async(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute) // the permission prompt waits for the user
		defer cancel()
		rec, err := engine.StartCapture(ctx, sup, "", p, func(l proto.LevelParams) {
			if level != nil {
				post(func() { level(l) })
			}
		})
		post(func() { then(rec, err) })
	})
}

// stopRecording ends rec and calls then with the file.
func (a *app) stopRecording(rec *engine.Recording, then func(proto.CaptureResult, error)) {
	post := a.enginePost
	a.async(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		res, err := rec.Stop(ctx)
		post(func() { then(res, err) })
	})
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
		}
		// A take arrives with TakeCreated, once the service saved it.
	case jobs.StateCancelled:
		a.job = nil
		a.dropAwait(j)
		a.toast(i18n.T("Cancelled"))
	case jobs.StateFailed, jobs.StateInterrupted:
		a.job = nil
		a.dropAwait(j)
		a.jobError = jobErrorText(u.Err)
		if u.Err != nil && u.Err.Code == proto.CodeModelNotDownloaded {
			e := entryFor(j.model)
			a.needDownload = &e
		}
	}
}

// dropAwait stops waiting for the take of a job that will not make it.
func (a *app) dropAwait(j *job) {
	if j.takeID != "" && a.awaitTake == j.takeID {
		a.awaitTake = ""
	}
}
