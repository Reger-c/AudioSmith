package main

// TestV02 is the v0.2 release check: every module's main flow driven
// through the app's state (as the views call it) against the real engine
// on a Mac, one subtest per module, in one library. It records wall time
// and peak memory (engine processes + this process) per step in a report
// (AUDIOSMITH_V02_REPORT, default /tmp/audiosmith-v02.md).
//
//	AUDIOSMITH_E2E=1 go test -run TestV02 -v -timeout 3h .
//
// AUDIOSMITH_V02 picks steps (comma-separated; default all): studio,
// clone, design, transcribe, stories, audiobook, dub, batch, workflow,
// api, watermark, crash. Other knobs:
//
//	AUDIOSMITH_V02_DIR         keep inputs and outputs here (default a temp dir)
//	AUDIOSMITH_V02_PY          a python with the openai SDK (api step)
//	AUDIOSMITH_V02_APP         the built app's executable (MCP stdio shim)
//	AUDIOSMITH_V02_BUDGET_GB   the engine's memory budget (simulate an 8 GB Mac: 4.8)
//	AUDIOSMITH_V02_DESIGN      design model id (default qwen3-tts-1.7b-voicedesign-4bit)
//
// Models must be downloaded (the HF cache); ffmpeg/ffprobe on PATH make
// the source video and check the MP4 and M4B.
//
// Files: e2e_v02_test.go (this: TestV02, TestV02FirstRun),
// e2e_v02_harness_test.go (the harness, the memory sampler) and one file
// per group of steps: e2e_v02_{studio,transcribe,stories,dub,flows}_test.go.

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// v02 is the harness: the app over the real engine, its event pump, the
// work folder and the report.
type v02 struct {
	a      *app
	h      *engineHost
	events chan func()
	dir    string
	mem    *memSampler

	mu   sync.Mutex
	rows []string
	// restarting tolerates a failed engine while it restarts (crash step).
	restarting bool
}

func TestV02(t *testing.T) {
	if os.Getenv("AUDIOSMITH_E2E") == "" {
		t.Skip("set AUDIOSMITH_E2E=1 to run the real engine")
	}
	bin := engine.Find("resources")
	if bin == "" {
		t.Fatal("no engine: run scripts/build-engine.sh")
	}
	want := map[string]bool{}
	for _, s := range strings.Split(os.Getenv("AUDIOSMITH_V02"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			want[s] = true
		}
	}
	on := func(step string) bool { return len(want) == 0 || want[step] }

	dir := os.Getenv("AUDIOSMITH_V02_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	os.MkdirAll(dir, 0o755)
	data := filepath.Join(dir, "data")
	lib := openTestLibraryWith(t, data, libraryOptions{})
	h := &engineHost{bin: bin, logDir: filepath.Join(dir, "logs")}
	os.MkdirAll(h.logDir, 0o755)
	if gbs := os.Getenv("AUDIOSMITH_V02_BUDGET_GB"); gbs != "" {
		f, _ := strconv.ParseFloat(gbs, 64)
		lib.settings.Update(t.Context(), func(s *settings.Settings) { s.Engine.MemoryBudgetGB = f })
	}
	a := newApp(h, lib)
	a.autoplay = false
	v := &v02{a: a, h: h, events: make(chan func(), 1<<16), dir: dir, mem: newMemSampler()}
	defer v.mem.stop()
	a.enginePost = func(fn func()) { v.events <- fn }
	a.postQueue = a.enginePost
	h.sup = engine.NewSupervisor(engine.Config{Bin: bin, Bus: a.bus, LogDir: h.logDir, Offline: true, CacheDir: modelCache(a.prefs),
		OnDemand: []proto.Role{proto.RoleListen, proto.RoleMedia}, MemoryBudget: gb(a.prefs.Engine.MemoryBudgetGB),
		IdleUnload: time.Duration(a.prefs.Models.AutoUnload) * time.Minute,
		Log: func(e core.LogEntry) {
			if e.Level != core.SeverityInfo && e.Level != core.SeverityDebug {
				t.Logf("%s %s: %s", e.Level, e.Source, e.Message)
			}
		}, Logf: t.Logf})
	a.connectEngine(h)
	defer h.Stop()
	a.startEngine()
	v.wait(t, "engine ready", time.Minute, func() bool { return a.engState == engineReady })
	// What the Models page knows (downloaded or not): the Studio's
	// download prompts, the transcriber's aligner and the local LLM read it.
	a.refreshModels()
	v.drain()
	a.applyLLM()
	acceptLicences(t, a)
	t.Logf("engine %s, data %s, budget %.1f GB, local LLM ready %v, routes %+v", a.engVersion, data,
		a.prefs.Engine.MemoryBudgetGB, a.localLLMReady(), a.llmRouter.Routes("translate"))

	step := func(name string, fn func(t *testing.T) string) {
		if !on(name) {
			return
		}
		v.mem.reset()
		started := time.Now()
		var note string
		ok := t.Run(name, func(t *testing.T) { note = fn(t) })
		rss, mlx := v.mem.peak(), v.mlxPeak()
		res := "PASS"
		if !ok {
			res = "FAIL"
		}
		v.row(fmt.Sprintf("| %s | %s | %.0f s | %.2f GB | %.2f GB | %s |", name, res, time.Since(started).Seconds(), rss, mlx, strings.ReplaceAll(note, "|", "/")))
	}

	var clonedVoice, firstTake string
	step("studio", func(t *testing.T) string { n, id := v.studio(t); firstTake = id; return n })
	step("clone", func(t *testing.T) string { n, id := v.clone(t); clonedVoice = id; return n })
	step("design", func(t *testing.T) string { return v.design(t) })
	step("transcribe", func(t *testing.T) string { return v.transcribe(t) })
	step("stories", func(t *testing.T) string { return v.stories(t) })
	step("audiobook", func(t *testing.T) string { return v.audiobook(t) })
	step("dub", func(t *testing.T) string { return v.dubFlow(t) })
	step("batch", func(t *testing.T) string { return v.batchFlow(t) })
	step("workflow", func(t *testing.T) string { return v.workflowFlow(t) })
	step("api", func(t *testing.T) string { return v.apiFlow(t, data) })
	step("watermark", func(t *testing.T) string { return v.watermarkFlow(t, firstTake) })
	step("crash", func(t *testing.T) string { return v.crashFlow(t) })
	_ = clonedVoice

	report := os.Getenv("AUDIOSMITH_V02_REPORT")
	if report == "" {
		report = "/tmp/audiosmith-v02.md"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# AudioSmith v0.2 E2E (%s)\n\nengine %s · budget %.1f GB · steps %s\n\n", time.Now().Format(time.RFC3339), a.engVersion,
		a.prefs.Engine.MemoryBudgetGB, os.Getenv("AUDIOSMITH_V02"))
	b.WriteString("| step | result | wall | peak RSS (engines + app) | MLX peak (gpu worker) | notes |\n|---|---|---|---|---|---|\n")
	for _, r := range v.rows {
		b.WriteString(r + "\n")
	}
	os.WriteFile(report, []byte(b.String()), 0o644)
	t.Logf("report: %s\n%s", report, b.String())
}

var _ = io.Discard

// TestV02FirstRun is a first launch on a fresh Mac without the network:
// an empty model folder, downloads through a dead proxy, the engine
// sandboxed. The app must start, every page must draw, and each main
// action must say which model to download instead of failing obscurely.
//
//	AUDIOSMITH_E2E=1 go test -run TestV02FirstRun -v .
func TestV02FirstRun(t *testing.T) {
	if os.Getenv("AUDIOSMITH_E2E") == "" {
		t.Skip("set AUDIOSMITH_E2E=1 to run the real engine")
	}
	bin := engine.Find("resources")
	if bin == "" {
		t.Fatal("no engine: run scripts/build-engine.sh")
	}
	bin = offlineEngine(t, bin)
	dir := t.TempDir()
	data, models := filepath.Join(dir, "data"), filepath.Join(dir, "models")
	os.MkdirAll(models, 0o755)
	lib := openTestLibraryWith(t, data, libraryOptions{})
	lib.settings.Update(t.Context(), func(s *settings.Settings) {
		s.Models.CacheDir, s.Models.Proxy = models, "http://127.0.0.1:9"
	})
	h := &engineHost{bin: bin, logDir: filepath.Join(dir, "logs")}
	a := newApp(h, lib)
	a.autoplay = false
	v := &v02{a: a, h: h, events: make(chan func(), 1<<16), dir: dir}
	a.enginePost = func(fn func()) { v.events <- fn }
	a.postQueue = a.enginePost
	h.sup = engine.NewSupervisor(engine.Config{Bin: bin, Bus: a.bus, LogDir: h.logDir, Offline: true, CacheDir: modelCache(a.prefs),
		OnDemand: []proto.Role{proto.RoleListen, proto.RoleMedia}, Logf: t.Logf})
	a.connectEngine(h)
	defer h.Stop()
	a.startEngine()
	v.wait(t, "engine ready", time.Minute, func() bool { return a.engState == engineReady })
	a.refreshModels()
	v.drain()
	tt := ui.NewTester(a.view, 1200, 800)

	// The wizard: packs with sizes still to download.
	a.openOnboarding()
	v.drain()
	tt.Frame()
	total, missing, _ := a.packSizes("fast")
	if total == 0 || missing != total {
		t.Errorf("fast pack: %d of %d missing", missing, total)
	}
	a.obOpen = false

	// Every page draws.
	for _, p := range pages {
		a.navigate(p.path)
		v.drain()
		tt.Frame()
	}

	// Studio: Generate says Kokoro is to be downloaded.
	a.navigate("/studio")
	a.selectModel(catalog.Builtin().Entries[0])
	a.text = "Hello"
	a.generate()
	v.wait(t, "studio refusal", time.Minute, func() bool { return a.job == nil })
	tt.Frame()
	if a.needDownload == nil || !tt.HasText("Download") {
		t.Errorf("studio: error %q, download %v: %q", a.jobError, a.needDownload, tt.Texts())
	}
	t.Logf("studio: %q", a.jobError)

	// Transcribe a file: the speech-to-text model is to be downloaded.
	in := say(t, dir, "hello", "Samantha", "Hello there.", 16000)
	a.navigate("/transcripts")
	a.trMode = 1
	a.transcribeFile(in)
	v.wait(t, "transcribe refusal", time.Minute, func() bool { return a.trJob == nil })
	if a.trNeed == nil {
		t.Errorf("transcribe: %q", a.trErr)
	}
	t.Logf("transcribe: %q", a.trErr)

	// Clone from a file: no transcript model, the clip still loads.
	a.startClone("")
	a.cloneLoad(in, "hello.wav", false)
	v.wait(t, "clone", 2*time.Minute, func() bool { return a.clone.busy == "" && a.clone.asr != "" && a.clone.asr != "running" })
	tt.Frame()
	if !a.clone.hasClip() || a.clone.asr != "noasr" {
		t.Errorf("clone: clip %v asr %q %q", a.clone.hasClip(), a.clone.asr, a.clone.asrErr)
	}

	// A download through the dead proxy fails as a job, and says why.
	e := catalog.Builtin().Entries[0]
	a.download(e)
	v.wait(t, "download fails", 5*time.Minute, func() bool { m := a.model(e); return m.jobID != "" && !m.downloading })
	m := a.model(e)
	t.Logf("download: %q", m.err)
	if m.err == "" {
		t.Errorf("download through a dead proxy: no error shown")
	}
	a.navigate("/models")
	tt.Frame()

	// The Local API answers, and says the model is missing.
	port := 3919
	a.updateSettings(func(s *settings.Settings) { s.API.Enabled, s.API.Port = true, port })
	v.drain()
	a.startAPI(data)
	v.wait(t, "api up", 30*time.Second, func() bool { return a.apiPort == port || a.apiErr != "" })
	var out []byte
	v.do(t, "api speech", time.Minute, func() error {
		var err error
		out, err = exec.Command("curl", "-sS", "--noproxy", "*", "-w", "\n%{http_code}", "-H", "Content-Type: application/json",
			"-d", `{"model":"tts-1","voice":"alloy","input":"hi"}`, fmt.Sprintf("http://127.0.0.1:%d/v1/audio/speech", port)).CombinedOutput()
		return err
	})
	t.Logf("api speech: %s", out)
	if !bytes.HasSuffix(bytes.TrimSpace(out), []byte("409")) && !bytes.HasSuffix(bytes.TrimSpace(out), []byte("503")) {
		t.Errorf("api speech without models: %s", out)
	}
	if a.engState != engineReady {
		t.Errorf("engine %v %s", a.engState, a.engError)
	}
}
