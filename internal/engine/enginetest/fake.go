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
//
// tts.synthesize reacts to its text: "crash" exits 3 mid-job, "oom" fails
// with oom, "slow" generates 100 steps of 20 ms (cancellable), anything
// else 3 steps. Models whose repo contains "broken" fail to load.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
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

type fakeEngine struct {
	opts map[string]string
	out  *bufio.Writer
	wmu  sync.Mutex

	mu      sync.Mutex
	hello   bool
	job     string // running job id
	cancel  map[string]bool
	models  map[string]time.Time // loaded model → last use
	jobDone chan struct{}
}

func runFakeEngine() {
	opts := map[string]string{}
	for _, o := range strings.Split(os.Getenv(fakeEnv), ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(o), "=")
		if k != "" {
			opts[k] = v
		}
	}
	f := &fakeEngine{opts: opts, out: bufio.NewWriter(os.Stdout), cancel: map[string]bool{}, models: map[string]time.Time{}}
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
	// stdin closed: cancel the job, exit.
	f.mu.Lock()
	if f.job != "" {
		f.cancel[f.job] = true
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
	ready := f.hello
	f.mu.Unlock()
	if !ready {
		f.fail(m.ID, proto.CodeNotInitialized, "engine.hello must come first")
		return
	}
	switch m.Method {
	case proto.MethodStats:
		f.mu.Lock()
		st := proto.StatsResult{Memory: proto.Memory{Active: 1000, Cache: 10, Peak: 2000}, Models: f.loadedLocked(), Jobs: []string{}}
		if f.job != "" {
			st.Jobs = []string{f.job}
		}
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
		running := f.job == p.Job && p.Job != ""
		if running {
			if _, hang := f.opts["hang-cancel"]; !hang {
				f.cancel[p.Job] = true
			}
		}
		f.mu.Unlock()
		f.result(m.ID, proto.CancelResult{Cancelled: running})
	case proto.MethodModelsLoad, proto.MethodTTSSynthesize:
		f.mu.Lock()
		if f.job != "" {
			f.mu.Unlock()
			f.fail(m.ID, proto.CodeBusy, "The engine runs 1 job at a time")
			return
		}
		f.job = m.ID
		f.mu.Unlock()
		go func() {
			if m.Method == proto.MethodModelsLoad {
				f.load(m)
			} else {
				f.synthesize(m)
			}
			f.mu.Lock()
			f.job = ""
			delete(f.cancel, m.ID)
			f.mu.Unlock()
		}()
	default:
		f.fail(m.ID, proto.CodeMethodNotFound, "Unknown method %s", m.Method)
	}
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
	res := proto.HelloResult{
		Protocol: proto.Version, Version: "fake", Role: p.Role, PID: os.Getpid(), Concurrency: 1,
		Methods:  []string{proto.MethodHello, proto.MethodStats, proto.MethodFlush, proto.MethodShutdown, proto.MethodModelsLoad, proto.MethodModelsUnload, proto.MethodModelsLoaded, proto.MethodTTSSynthesize, proto.MethodJobCancel},
		Families: []string{"kokoro"},
		Device:   proto.Device{Name: "Fake", Memory: 16 << 30, OS: "test"},
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
	f.hello = true
	f.mu.Unlock()
	f.notify(proto.MethodLog, proto.LogParams{Level: "info", Message: "fake engine ready"})
	f.result(m.ID, res)
}

func (f *fakeEngine) loadedLocked() []proto.LoadedModel {
	out := []proto.LoadedModel{}
	for model, used := range f.models {
		idle := time.Since(used).Seconds()
		if f.job != "" {
			idle = 0 // simplification: everything counts as in use while a job runs
		}
		out = append(out, proto.LoadedModel{Model: model, Task: "tts", SampleRate: 24000, Bytes: 100, IdleSeconds: idle})
	}
	return out
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

func (f *fakeEngine) load(m proto.Message) {
	var p proto.LoadParams
	if err := m.DecodeParams(&p); err != nil || p.Model == "" {
		f.fail(m.ID, proto.CodeBadRequest, "Missing model")
		return
	}
	if !f.ensure(m.ID, p.Model) {
		return
	}
	f.result(m.ID, proto.LoadResult{Model: p.Model, Task: "tts", SampleRate: 24000, Seconds: 0.01, Voices: []string{"af_heart"}})
}

func (f *fakeEngine) synthesize(m proto.Message) {
	var p proto.SynthesizeParams
	if err := m.DecodeParams(&p); err != nil || p.Model == "" {
		f.fail(m.ID, proto.CodeBadRequest, "Missing model")
		return
	}
	if !f.ensure(m.ID, p.Model) {
		return
	}
	steps, step := 3, time.Millisecond
	switch {
	case strings.Contains(p.Text, "slow"):
		steps, step = 100, 20*time.Millisecond
	case strings.Contains(p.Text, "oom"):
		f.send(proto.NewError(m.ID, &proto.Error{Code: proto.CodeOOM, Message: "Out of memory while generating", Data: json.RawMessage(`{"active":1}`)}))
		return
	}
	for i := range steps {
		if f.cancelled(m.ID) {
			f.fail(m.ID, proto.CodeCancelled, "Cancelled")
			return
		}
		if strings.Contains(p.Text, "crash") && i == 1 {
			fmt.Fprintln(os.Stderr, "fatal: boom")
			os.Exit(3)
		}
		time.Sleep(step)
		frac := float64(i+1) / float64(steps)
		f.notify(proto.MethodJobProgress, proto.ProgressParams{Job: m.ID, Stage: "generating", Fraction: &frac, AudioSeconds: 0.1 * float64(i+1)})
		if p.Stream {
			f.notify(proto.MethodTTSChunk, proto.ChunkParams{Job: m.ID, Seq: i, SampleRate: 24000, Channels: 1, Encoding: "pcm16le",
				Data: []byte{byte(i), 0, byte(i), 0}, Final: i == steps-1})
		}
	}
	if p.Output.Path != "" {
		if err := os.WriteFile(p.Output.Path, []byte("RIFF fake"), 0o644); err != nil {
			f.fail(m.ID, proto.CodeInternal, "Could not write %s: %v", p.Output.Path, err)
			return
		}
	}
	f.mu.Lock()
	f.models[p.Model] = time.Now()
	f.mu.Unlock()
	f.result(m.ID, proto.SynthesizeResult{Output: p.Output.Path, Model: p.Model, SampleRate: 24000, Channels: 1,
		Frames: int64(2400 * steps), AudioSeconds: 0.1 * float64(steps), Seconds: 0.05, RTF: 2 * float64(steps)})
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
			f.result(m.ID, proto.HelloResult{Protocol: proto.Version, Version: "replay", Role: proto.RoleGPU, PID: os.Getpid(), Concurrency: 1})
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
