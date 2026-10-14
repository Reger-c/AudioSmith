// Package enginetest is a scripted protocol-2 audiosmith-engine for tests:
// a test binary runs it instead of its tests when AUDIOSMITH_FAKE_ENGINE is
// set, so the supervisor spawns a real process and talks to it over real
// pipes, without MLX. In the test package:
//
//	func TestMain(m *testing.M) {
//		if enginetest.Enabled() {
//			enginetest.Main()
//		}
//		os.Exit(m.Run())
//	}
//
// and point engine.Config at os.Executable() with Env: enginetest.Env(opts).
//
// The fake serves every method of protocol 2 in the role it is started
// with (--role, or hello's role), as the real engine will: gpu (models,
// tts.*, stt.*, vad.detect, enhance.run), media (media.*), listen
// (capture.*). It reads and writes real WAV files (Audio, ReadWAV,
// WriteWAV), so code downstream of it (joining, analysis, playback) can be
// tested on its output. See fake_m1.go for what each M1 method does.
package enginetest

//
// AUDIOSMITH_FAKE_ENGINE is a comma-separated list of options:
//
//	(empty)        speak protocol 2 (the default; any option below changes it)
//	legacy         behave as a protocol-1 engine: print its ready line
//	mismatch       answer engine.hello with protocol 3
//	refuse         answer engine.hello with protocol_mismatch
//	silent         never answer engine.hello
//	role=R         claim role R in hello
//	crash-start    exit 3 at once
//	hang-cancel    ignore job.cancel (the job keeps running)
//	mem=N          device memory in hello, bytes
//	replay=PATH    replay a fixture transcript (engine/Fixtures/*.jsonl)
//	deny-mic       capture.start fails with permission_denied
//	level-ms=N     capture.level every N ms (default 50)
//
// tts.synthesize reacts to its text, and the other jobs to their input
// path: "crash" exits 3 mid-job, "oom" fails with oom, "slow" runs 100
// steps of 20 ms (cancellable), anything else 3 steps. Models whose repo
// contains "broken" fail to load.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hehehai/AudioSmith/internal/engine/proto"
)

const fakeEnv = "AUDIOSMITH_FAKE_ENGINE"

// Enabled reports whether this process should be the fake engine.
func Enabled() bool {
	_, ok := os.LookupEnv(fakeEnv)
	return ok
}

// Env returns the environment that makes a re-executed test binary the
// fake engine with opts (see the package comment of the file).
func Env(opts string) []string { return []string{fakeEnv + "=" + opts} }

// Main runs the fake engine and exits.
func Main() {
	runFakeEngine()
	os.Exit(0)
}

// Methods returns what an engine of role implements (hello.methods).
func Methods(role proto.Role) []string {
	common := []string{proto.MethodHello, proto.MethodStats, proto.MethodFlush, proto.MethodShutdown, proto.MethodJobCancel}
	switch role {
	case proto.RoleMedia:
		return append(common, proto.MethodMediaProbe, proto.MethodMediaDecode, proto.MethodMediaEncode,
			proto.MethodMediaMux, proto.MethodMediaThumbnail, proto.MethodMediaTranscode)
	case proto.RoleListen:
		return append(common, proto.MethodCaptureDevices, proto.MethodCaptureStart, proto.MethodCaptureStop,
			proto.MethodModelsLoad, proto.MethodModelsUnload, proto.MethodModelsLoaded, proto.MethodSTTTranscribe)
	}
	return append(common, proto.MethodModelsLoad, proto.MethodModelsUnload, proto.MethodModelsLoaded,
		proto.MethodTTSSynthesize, proto.MethodTTSEncodeVoice, proto.MethodSTTTranscribe, proto.MethodSTTAlign,
		proto.MethodVADDetect, proto.MethodEnhanceRun, proto.MethodSeparateRun, proto.MethodDiarizeRun)
}

// Concurrency is the jobs an engine of role runs at once: gpu 1, listen 2
// (a recording and a transcription), media 2.
func Concurrency(role proto.Role) int {
	if role == proto.RoleGPU {
		return 1
	}
	return 2
}

type fakeEngine struct {
	opts map[string]string
	out  *bufio.Writer
	wmu  sync.Mutex

	mu       sync.Mutex
	hello    bool
	role     proto.Role
	running  map[string]string    // job id → method
	cancel   map[string]bool      // jobs asked to stop by job.cancel
	stop     map[string]chan bool // capture.start jobs: closed by capture.stop
	finished map[string]chan struct{}
	models   map[string]time.Time // loaded model → last use
}

func runFakeEngine() {
	opts := map[string]string{}
	for _, o := range strings.Split(os.Getenv(fakeEnv), ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(o), "=")
		if k != "" {
			opts[k] = v
		}
	}
	f := &fakeEngine{opts: opts, out: bufio.NewWriter(os.Stdout), role: proto.RoleGPU,
		running: map[string]string{}, cancel: map[string]bool{}, stop: map[string]chan bool{},
		finished: map[string]chan struct{}{}, models: map[string]time.Time{}}
	if i := slices.Index(os.Args, "--role"); i >= 0 && i+1 < len(os.Args) {
		f.role = proto.Role(os.Args[i+1])
	}
	fmt.Fprintln(os.Stderr, "fake engine starting", os.Args[1:])
	if _, ok := opts["crash-start"]; ok {
		fmt.Fprintln(os.Stderr, "fatal: could not start")
		os.Exit(3)
	}
	if _, ok := opts["legacy"]; ok {
		f.writeRaw(`{"event":"ready","version":"0.1.0","protocolVersion":1,"pid":` + strconv.Itoa(os.Getpid()) + `}`)
	}
	if path, ok := opts["replay"]; ok {
		f.replay(path)
		os.Exit(0)
	}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64*1024), proto.MaxLine)
	for sc.Scan() {
		m, err := proto.Decode(sc.Bytes())
		if err != nil {
			fmt.Fprintln(os.Stderr, "bad line:", err)
			continue
		}
		f.handle(m)
	}
	// stdin closed: cancel the jobs, exit.
	f.mu.Lock()
	for id := range f.running {
		f.cancel[id] = true
	}
	f.mu.Unlock()
	os.Exit(0)
}

func (f *fakeEngine) writeRaw(line string) {
	f.wmu.Lock()
	defer f.wmu.Unlock()
	f.out.WriteString(line + "\n")
	f.out.Flush()
}

func (f *fakeEngine) send(m proto.Message) {
	line, _ := proto.Encode(m)
	f.wmu.Lock()
	defer f.wmu.Unlock()
	f.out.Write(line)
	f.out.Flush()
}

func (f *fakeEngine) result(id string, v any) {
	m, _ := proto.NewResult(id, v)
	f.send(m)
}

func (f *fakeEngine) fail(id, code, format string, args ...any) {
	f.send(proto.NewError(id, proto.Errorf(code, format, args...)))
}

// failData answers id with an error carrying data.
func (f *fakeEngine) failData(id, code string, data any, format string, args ...any) {
	e := proto.Errorf(code, format, args...)
	e.Data, _ = json.Marshal(data)
	f.send(proto.NewError(id, e))
}

func (f *fakeEngine) notify(method string, params any) {
	m, _ := proto.NewNotification(method, params)
	f.send(m)
}

func (f *fakeEngine) handle(m proto.Message) {
	if m.Method == proto.MethodHello {
		f.handleHello(m)
		return
	}
	f.mu.Lock()
	ready, role := f.hello, f.role
	f.mu.Unlock()
	if !ready {
		f.fail(m.ID, proto.CodeNotInitialized, "engine.hello must come first")
		return
	}
	if !slices.Contains(Methods(role), m.Method) {
		f.fail(m.ID, proto.CodeMethodNotFound, "Unknown method %s", m.Method)
		return
	}
	switch m.Method {
	case proto.MethodStats:
		f.mu.Lock()
		st := proto.StatsResult{Memory: proto.Memory{Active: 1000, Cache: 10, Peak: 2000}, Models: f.loadedLocked(), Jobs: []string{}}
		for id := range f.running {
			st.Jobs = append(st.Jobs, id)
		}
		slices.Sort(st.Jobs)
		f.mu.Unlock()
		f.result(m.ID, st)
	case proto.MethodFlush:
		f.result(m.ID, nil)
	case proto.MethodShutdown:
		f.result(m.ID, nil)
		os.Exit(0)
	case proto.MethodModelsLoaded:
		f.mu.Lock()
		res := proto.LoadedResult{Models: f.loadedLocked()}
		f.mu.Unlock()
		f.result(m.ID, res)
	case proto.MethodModelsUnload:
		var p proto.UnloadParams
		m.DecodeParams(&p)
		f.mu.Lock()
		res := proto.UnloadResult{Unloaded: []string{}}
		for model := range f.models {
			if p.Model == "" || p.Model == model {
				delete(f.models, model)
				res.Unloaded = append(res.Unloaded, model)
			}
		}
		f.mu.Unlock()
		f.result(m.ID, res)
	case proto.MethodJobCancel:
		var p proto.CancelParams
		m.DecodeParams(&p)
		f.mu.Lock()
		_, running := f.running[p.Job]
		if running {
			if _, hang := f.opts["hang-cancel"]; !hang {
				f.cancel[p.Job] = true
			}
		}
		f.mu.Unlock()
		f.result(m.ID, proto.CancelResult{Cancelled: running})
	case proto.MethodMediaProbe:
		f.probe(m)
	case proto.MethodCaptureDevices:
		f.result(m.ID, proto.DevicesResult{Devices: FakeDevices()})
	case proto.MethodCaptureStop:
		f.captureStop(m)
	default:
		if proto.IsJob(m.Method) {
			f.startJob(m)
			return
		}
		f.fail(m.ID, proto.CodeMethodNotFound, "Unknown method %s", m.Method)
	}
}

// startJob runs a job request on its own goroutine, within the role's
// concurrency.
func (f *fakeEngine) startJob(m proto.Message) {
	f.mu.Lock()
	if len(f.running) >= Concurrency(f.role) {
		f.mu.Unlock()
		f.fail(m.ID, proto.CodeBusy, "The engine runs %d job(s) at a time", Concurrency(f.role))
		return
	}
	f.running[m.ID] = m.Method
	done := make(chan struct{})
	f.finished[m.ID] = done
	f.mu.Unlock()
	go func() {
		defer func() {
			f.mu.Lock()
			delete(f.running, m.ID)
			delete(f.cancel, m.ID)
			delete(f.stop, m.ID)
			delete(f.finished, m.ID)
			f.mu.Unlock()
			close(done)
		}()
		switch m.Method {
		case proto.MethodModelsLoad:
			f.load(m)
		case proto.MethodTTSSynthesize:
			f.synthesize(m)
		case proto.MethodTTSEncodeVoice:
			f.encodeVoice(m)
		case proto.MethodSTTTranscribe:
			f.transcribe(m)
		case proto.MethodSTTAlign:
			f.align(m)
		case proto.MethodVADDetect:
			f.vad(m)
		case proto.MethodEnhanceRun:
			f.enhance(m)
		case proto.MethodMediaDecode:
			f.decode(m)
		case proto.MethodMediaEncode:
			f.encode(m)
		case proto.MethodSeparateRun:
			f.separate(m)
		case proto.MethodDiarizeRun:
			f.diarize(m)
		case proto.MethodMediaMux:
			f.mux(m)
		case proto.MethodMediaThumbnail:
			f.thumbnail(m)
		case proto.MethodMediaTranscode:
			f.transcode(m)
		case proto.MethodCaptureStart:
			f.capture(m)
		default:
			f.fail(m.ID, proto.CodeMethodNotFound, "Unknown method %s", m.Method)
		}
	}()
}

func (f *fakeEngine) handleHello(m proto.Message) {
	var p proto.HelloParams
	m.DecodeParams(&p)
	if _, ok := f.opts["silent"]; ok {
		return
	}
	if _, ok := f.opts["refuse"]; ok || p.Protocol != proto.Version {
		f.send(proto.NewError(m.ID, &proto.Error{Code: proto.CodeProtocolMismatch, Message: "This engine speaks protocol 2", Data: json.RawMessage(`{"supported":[2]}`)}))
		return
	}
	role := p.Role
	if role == "" {
		role = proto.RoleGPU
	}
	families := []string{}
	if role == proto.RoleListen {
		families = []string{"whisper", "qwen3_asr", "parakeet"}
	}
	if role == proto.RoleGPU {
		families = []string{"kokoro", "qwen3_tts", "omnivoice", "chatterbox", "whisper", "qwen3_asr", "qwen3_forced_aligner", "silero_vad", "deepfilternet", "htdemucs", "mel_roformer", "sortformer"}
	}
	res := proto.HelloResult{
		Protocol: proto.Version, Version: "fake", Role: role, PID: os.Getpid(), Concurrency: Concurrency(role),
		Methods: Methods(role), Families: families,
		Device: proto.Device{Name: "Fake", Memory: 16 << 30, OS: "test"},
	}
	if r, ok := f.opts["role"]; ok {
		res.Role = proto.Role(r)
	}
	if _, ok := f.opts["mismatch"]; ok {
		res.Protocol = 3
	}
	if mem, ok := f.opts["mem"]; ok {
		res.Device.Memory, _ = strconv.ParseInt(mem, 10, 64)
	}
	f.mu.Lock()
	f.hello, f.role = true, role
	f.mu.Unlock()
	f.notify(proto.MethodLog, proto.LogParams{Level: "info", Message: "fake engine ready"})
	f.result(m.ID, res)
}

func (f *fakeEngine) loadedLocked() []proto.LoadedModel {
	out := []proto.LoadedModel{}
	for model, used := range f.models {
		idle := time.Since(used).Seconds()
		if len(f.running) > 0 {
			idle = 0 // simplification: everything counts as in use while a job runs
		}
		out = append(out, proto.LoadedModel{Model: model, Task: taskOf(model), SampleRate: 24000, Bytes: 100, IdleSeconds: idle})
	}
	slices.SortFunc(out, func(a, b proto.LoadedModel) int { return strings.Compare(a.Model, b.Model) })
	return out
}

// taskOf guesses a model's catalogue task from its repo id.
func taskOf(model string) string {
	l := strings.ToLower(model)
	switch {
	case strings.Contains(l, "aligner"):
		return "align"
	case strings.Contains(l, "whisper"), strings.Contains(l, "asr"), strings.Contains(l, "parakeet"):
		return "stt"
	case strings.Contains(l, "vad"):
		return "vad"
	case strings.Contains(l, "deepfilter"):
		return "enhance"
	}
	return "tts"
}

func (f *fakeEngine) cancelled(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cancel[id]
}

// ensure loads model (job id reports progress); false when it failed (answered).
func (f *fakeEngine) ensure(id, model string) bool {
	f.mu.Lock()
	_, ok := f.models[model]
	f.mu.Unlock()
	if ok {
		return true
	}
	if strings.Contains(model, "broken") {
		f.fail(id, proto.CodeModelLoadFailed, "Unsupported model type: nil")
		return false
	}
	f.notify(proto.MethodJobProgress, proto.ProgressParams{Job: id, Stage: "loading", Message: "Loading " + model})
	f.mu.Lock()
	f.models[model] = time.Now()
	f.mu.Unlock()
	return true
}

func (f *fakeEngine) touch(model string) {
	f.mu.Lock()
	f.models[model] = time.Now()
	f.mu.Unlock()
}

func (f *fakeEngine) load(m proto.Message) {
	var p proto.LoadParams
	if err := m.DecodeParams(&p); err != nil || p.Model == "" {
		f.fail(m.ID, proto.CodeBadRequest, "Missing model")
		return
	}
	if !f.ensure(m.ID, p.Model) {
		return
	}
	task := p.Task
	if task == "" {
		task = "tts"
	}
	res := proto.LoadResult{Model: p.Model, Task: task, SampleRate: 24000, Seconds: 0.01}
	if task == "tts" {
		res.Voices = []string{"af_heart"}
	} else {
		res.SampleRate = 16000
	}
	f.result(m.ID, res)
}

// work runs the steps of a job whose behaviour key (text or input path)
// is key, reporting stage; false when the job ended (answered).
func (f *fakeEngine) work(id, key, stage string, each func(i, steps int)) bool {
	steps, step := 3, time.Millisecond
	switch {
	case strings.Contains(key, "slow"):
		steps, step = 100, 20*time.Millisecond
	case strings.Contains(key, "oom"):
		f.send(proto.NewError(id, &proto.Error{Code: proto.CodeOOM, Message: "Out of memory while generating", Data: json.RawMessage(`{"active":1}`)}))
		return false
	}
	for i := range steps {
		if f.cancelled(id) {
			f.fail(id, proto.CodeCancelled, "Cancelled")
			return false
		}
		if strings.Contains(key, "crash") && i == 1 {
			fmt.Fprintln(os.Stderr, "fatal: boom")
			os.Exit(3)
		}
		time.Sleep(step)
		if each != nil {
			each(i, steps)
		} else {
			frac := float64(i+1) / float64(steps)
			f.notify(proto.MethodJobProgress, proto.ProgressParams{Job: id, Stage: stage, Fraction: &frac})
		}
	}
	return true
}

// FakeKokoroLimit is the longest text, in characters, the fake lets a
// Kokoro model take in one request (the real limit is 510 phonemes).
const FakeKokoroLimit = 500

func (f *fakeEngine) synthesize(m proto.Message) {
	var p proto.SynthesizeParams
	if err := m.DecodeParams(&p); err != nil || p.Model == "" {
		f.fail(m.ID, proto.CodeBadRequest, "Missing model")
		return
	}
	if !f.ensure(m.ID, p.Model) {
		return
	}
	lower := strings.ToLower(p.Model)
	kokoro := strings.Contains(lower, "kokoro")
	if n := len([]rune(p.Text)); kokoro && n > FakeKokoroLimit {
		f.failData(m.ID, proto.CodeInputTooLong, map[string]any{"limit": FakeKokoroLimit, "actual": n, "unit": "chars"},
			"The text is %d characters; %s takes at most %d per request", n, p.Model, FakeKokoroLimit)
		return
	}
	if !f.useVoicePrompt(m.ID, p) {
		return
	}
	if p.RefAudio != "" && (kokoro || strings.Contains(lower, "customvoice") || strings.Contains(lower, "voicedesign")) {
		f.failData(m.ID, proto.CodeUnsupported, map[string]string{"capability": "clone"}, "%s cannot clone voices", p.Model)
		return
	}
	if p.RefAudio != "" && !f.inputOK(m.ID, p.RefAudio) {
		return
	}
	var steps int
	ok := f.work(m.ID, p.Text, "generating", func(i, n int) {
		steps = n
		frac := float64(i+1) / float64(n)
		f.notify(proto.MethodJobProgress, proto.ProgressParams{Job: m.ID, Stage: "generating", Fraction: &frac, AudioSeconds: 0.1 * float64(i+1)})
		if p.Stream {
			pcm := make([]byte, 0, 4)
			pcm = append(pcm, byte(i), 0, byte(i), 0)
			f.notify(proto.MethodTTSChunk, proto.ChunkParams{Job: m.ID, Seq: i, SampleRate: 24000, Channels: 1, Encoding: "pcm16le",
				Data: pcm, Final: i == n-1})
		}
	})
	if !ok {
		return
	}
	audio := Tone(24000, 1, 0.1*float64(steps), 220, 0.25)
	if p.Output.Path != "" {
		if err := WriteWAV(p.Output.Path, audio, p.Output.Bits); err != nil {
			f.fail(m.ID, proto.CodeInternal, "Could not write %s: %v", p.Output.Path, err)
			return
		}
	}
	f.touch(p.Model)
	res := proto.SynthesizeResult{Output: p.Output.Path, Model: p.Model, SampleRate: 24000, Channels: 1,
		Frames: int64(audio.Frames()), AudioSeconds: audio.Seconds(), Seconds: 0.05, RTF: audio.Seconds() / 0.05}
	if p.Seed != nil {
		res.Seed = p.Seed
	}
	f.result(m.ID, res)
}

// replay answers requests from a fixture transcript: each request takes
// the next unused fixture request of the same method, and the app-bound
// lines that follow it are written with the fixture's ids replaced by the
// live ones. Lines before the first request are written after hello.
func (f *fakeEngine) replay(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	type line struct {
		To  string          `json:"to"`
		Msg json.RawMessage `json:"msg"`
	}
	var lines []line
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var ln line
		if err := json.Unmarshal([]byte(l), &ln); err != nil || ln.Msg == nil {
			continue
		}
		lines = append(lines, ln)
	}
	used := make([]bool, len(lines))
	ids := map[string]string{} // fixture id → live id
	rewrite := func(raw json.RawMessage) string {
		var m proto.Message
		json.Unmarshal(raw, &m)
		if to, ok := ids[m.ID]; ok {
			m.ID = to
		}
		if len(m.Params) > 0 {
			var params map[string]any
			json.Unmarshal(m.Params, &params)
			if job, ok := params["job"].(string); ok {
				if to, ok := ids[job]; ok {
					params["job"] = to
				}
			}
			m.Params, _ = json.Marshal(params)
		}
		out, _ := json.Marshal(m)
		return string(out)
	}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64*1024), proto.MaxLine)
	for sc.Scan() {
		m, err := proto.Decode(sc.Bytes())
		if err != nil {
			continue
		}
		if m.Method == proto.MethodHello {
			var p proto.HelloParams
			m.DecodeParams(&p)
			if p.Role == "" {
				p.Role = proto.RoleGPU
			}
			f.result(m.ID, proto.HelloResult{Protocol: proto.Version, Version: "replay", Role: p.Role, PID: os.Getpid(), Concurrency: Concurrency(p.Role)})
			continue
		}
		if m.Method == proto.MethodStats && !strings.Contains(string(data), `"engine.stats"`) {
			f.result(m.ID, proto.StatsResult{Models: []proto.LoadedModel{}, Jobs: []string{}})
			continue
		}
		found := -1
		for i, ln := range lines {
			if used[i] || ln.To != "engine" {
				continue
			}
			var fm proto.Message
			json.Unmarshal(ln.Msg, &fm)
			if fm.Method == m.Method {
				found = i
				ids[fm.ID] = m.ID
				break
			}
		}
		if found < 0 {
			f.fail(m.ID, proto.CodeMethodNotFound, "not in the fixture: %s", m.Method)
			continue
		}
		used[found] = true
		for i := found + 1; i < len(lines) && lines[i].To == "app"; i++ {
			used[i] = true
			f.writeRaw(rewrite(lines[i].Msg))
		}
	}
}
