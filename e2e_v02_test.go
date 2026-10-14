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

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp/loudness"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/engine/enginetest"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/settings"
	"github.com/hehehai/AudioSmith/internal/workflow"
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

// --- the harness -------------------------------------------------------------------

func (v *v02) row(s string) {
	v.mu.Lock()
	v.rows = append(v.rows, s)
	v.mu.Unlock()
}

// wait applies events until done, failing the (sub)test after timeout.
func (v *v02) wait(t *testing.T, what string, timeout time.Duration, done func() bool) {
	t.Helper()
	deadline := time.After(timeout)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for !done() {
		if v.a.engState == engineFailed {
			t.Fatalf("%s: engine failed: %s\n%s", what, v.a.engError, v.a.engTail)
		}
		select {
		case fn := <-v.events:
			fn()
		case <-tick.C:
		case <-deadline:
			t.Fatalf("timed out waiting for %s (studio job %+v, error %q)", what, v.a.job, v.a.jobError)
		}
	}
}

// drain applies the events already queued.
func (v *v02) drain() {
	for {
		select {
		case fn := <-v.events:
			fn()
		default:
			return
		}
	}
}

// do runs fn on a goroutine (it may need the app, e.g. an HTTP call the
// API answers) and applies events until it returns.
func (v *v02) do(t *testing.T, what string, timeout time.Duration, fn func() error) error {
	t.Helper()
	var err error
	done := make(chan struct{})
	go func() { err = fn(); close(done) }()
	v.wait(t, what, timeout, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	})
	return err
}

// job waits for a scheduler job to end and returns it.
func (v *v02) job(t *testing.T, id string, timeout time.Duration) jobs.Job {
	t.Helper()
	var j jobs.Job
	v.wait(t, "job "+id, timeout, func() bool {
		var err error
		j, err = v.a.lib.sched.Get(context.Background(), id)
		return err == nil && j.State.Terminal()
	})
	return j
}

func (v *v02) mlxPeak() float64 {
	var p float64
	for _, w := range v.a.workers {
		if w.Role == proto.RoleGPU && w.Stats != nil {
			p = float64(w.Stats.Memory.Peak) / (1 << 30)
		}
	}
	return p
}

func (v *v02) model(t *testing.T, id string) catalog.Entry {
	t.Helper()
	e, ok := catalog.Builtin().ByID(id)
	if !ok {
		t.Fatalf("no catalogue entry %s", id)
	}
	if ready, _ := v.a.modelOnDisk(e); !ready {
		t.Fatalf("%s is not downloaded", id)
	}
	return e
}

// generate makes a Studio take and returns it.
func (v *v02) generate(t *testing.T, what string, timeout time.Duration) *core.Take {
	t.Helper()
	a := v.a
	before := a.lastClipID
	a.generate()
	if a.job == nil {
		t.Fatalf("%s did not start: %q (download %v)", what, a.jobError, a.needDownload)
	}
	v.wait(t, what, timeout, func() bool { return a.job == nil && a.awaitTake == "" })
	if a.jobError != "" {
		t.Fatalf("%s: %s", what, a.jobError)
	}
	take := a.lastClip()
	if take == nil || a.lastClipID == before {
		t.Fatalf("%s: no take", what)
	}
	return take
}

// sttCheck transcribes a WAV with the default STT model through the
// voice pages' job (as cloning does) and returns the text.
func (v *v02) sttCheck(t *testing.T, path, lang string) string {
	t.Helper()
	var text string
	var err error
	done := false
	v.a.transcribeClip(path, lang, func(s string, e error) { text, err, done = s, e, true })
	v.wait(t, "stt "+filepath.Base(path), 10*time.Minute, func() bool { return done })
	if err != nil {
		t.Errorf("stt %s: %v", path, err)
	}
	return text
}

// say makes a WAV of macOS speech (rate Hz, mono 16-bit).
func say(t *testing.T, dir, name, voice, text string, rate int) string {
	t.Helper()
	aiff := filepath.Join(dir, name+".aiff")
	out := filepath.Join(dir, name+".wav")
	for _, c := range [][]string{{"say", "-v", voice, "-o", aiff, text}, {"afconvert", "-f", "WAVE", "-d", "LEI16@" + strconv.Itoa(rate), "-c", "1", aiff, out}} {
		if b, err := exec.Command(c[0], c[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v %s", c, err, b)
		}
	}
	os.Remove(aiff)
	return out
}

// wavBits reads a WAV's bits per sample from its fmt chunk.
func wavBits(path string) int {
	b, err := os.ReadFile(path)
	if err != nil || len(b) < 44 {
		return 0
	}
	for i := 12; i+8 < len(b); {
		id, n := string(b[i:i+4]), int(uint32(b[i+4])|uint32(b[i+5])<<8|uint32(b[i+6])<<16|uint32(b[i+7])<<24)
		if id == "fmt " && i+24 <= len(b) {
			return int(uint16(b[i+22]) | uint16(b[i+23])<<8)
		}
		i += 8 + n + n%2
	}
	return 0
}

func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '\'')
	})
}

// overlap is the share of want's words found in got.
func overlap(want, got string) float64 {
	w, g := words(want), words(got)
	if len(w) == 0 {
		return 0
	}
	n := 0
	for _, x := range w {
		if slices.Contains(g, x) {
			n++
		}
	}
	return float64(n) / float64(len(w))
}

var hanRE = regexp.MustCompile(`\p{Han}`)

// ffprobeJSON runs ffprobe with args and decodes its JSON.
func ffprobeJSON(t *testing.T, args ...string) map[string]any {
	t.Helper()
	fp := lookTool("ffprobe")
	if fp == "" {
		t.Log("no ffprobe: not checked")
		return nil
	}
	out, err := exec.Command(fp, append([]string{"-v", "error", "-of", "json"}, args...)...).Output()
	if err != nil {
		t.Errorf("ffprobe %v: %v", args, err)
		return nil
	}
	var m map[string]any
	json.Unmarshal(out, &m)
	return m
}

// --- memory -------------------------------------------------------------------------

// memSampler polls the resident memory of the engine workers and of this
// process every half second; peak is the highest sum since reset, in GB.
type memSampler struct {
	mu   sync.Mutex
	max  int64
	quit chan struct{}
}

func newMemSampler() *memSampler {
	m := &memSampler{quit: make(chan struct{})}
	go func() {
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-m.quit:
				return
			case <-tick.C:
				m.sample()
			}
		}
	}()
	return m
}

func (m *memSampler) sample() {
	out, err := exec.Command("ps", "-axo", "pid=,rss=,comm=").Output()
	if err != nil {
		return
	}
	self := os.Getpid()
	var sum int64
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Fields(l)
		if len(f) < 3 {
			continue
		}
		pid, _ := strconv.Atoi(f[0])
		kb, _ := strconv.ParseInt(f[1], 10, 64)
		if pid == self || strings.HasSuffix(f[len(f)-1], "audiosmith-engine") {
			sum += kb << 10
		}
	}
	m.mu.Lock()
	m.max = max(m.max, sum)
	m.mu.Unlock()
}

func (m *memSampler) reset() { m.mu.Lock(); m.max = 0; m.mu.Unlock() }
func (m *memSampler) peak() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return float64(m.max) / (1 << 30)
}
func (m *memSampler) stop() { close(m.quit) }

// --- the steps ----------------------------------------------------------------------

// studio: long English and Chinese scripts on Kokoro with "Even out
// volume", 24-bit, and Save as… 24-bit WAV.
func (v *v02) studio(t *testing.T) (string, string) {
	a := v.a
	a.selectModel(v.model(t, "kokoro-82m-bf16"))
	a.evenOut, a.bitsIndex = true, slices.Index(bitDepths, 24)
	var notes []string
	var first string
	for _, c := range []struct{ name, voice, text string }{
		{"en", "af_heart", longText(enSentences, 3000)},
		{"zh", "zf_xiaobei", longText(zhSentences, 1500)},
		{"mixed", "zf_xiaobei", "今天我们来聊一聊 AudioSmith 这个应用。It runs every model on your Mac, 不需要联网。"},
	} {
		a.text, a.voice, a.language = c.text, c.voice, ""
		started := time.Now()
		take := v.generate(t, "studio "+c.name, 30*time.Minute)
		if first == "" {
			first = take.ID
		}
		path := a.takeStore.AudioPath(*take)
		buf, _, err := wav.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		lufs := loudness.Integrated(buf)
		chunks := 0
		if take.Render != nil {
			chunks = len(take.Render.Chunks)
		}
		notes = append(notes, fmt.Sprintf("%s %d chars → %.0f s audio in %.0f s, %d chunks, %.1f LUFS, %d-bit, wm=%v",
			c.name, len([]rune(c.text)), take.AudioSeconds, time.Since(started).Seconds(), chunks, lufs, wavBits(path), take.Render != nil && take.Render.Watermarked))
		if lufs < -20 || lufs > -16 {
			t.Errorf("%s: %.1f LUFS", c.name, lufs)
		}
		if b := wavBits(path); b != 24 {
			t.Errorf("%s: %d-bit take, want 24", c.name, b)
		}
		if take.Render != nil {
			for _, n := range take.Render.Warnings {
				t.Logf("%s warning %+v", c.name, n)
			}
		}
	}
	// Save as… a 24-bit WAV.
	out := filepath.Join(v.dir, "studio-en-24.wav")
	if err := a.lib.svc.Render.ExportAs(context.Background(), first, out, 24); err != nil {
		t.Errorf("export: %v", err)
	} else if b := wavBits(out); b != 24 {
		t.Errorf("exported %d-bit", b)
	}
	return strings.Join(notes, "; "), first
}

// clone: a voice from a WAV file (auto transcript, noise cleaning), saved,
// then a take in it on Qwen3-TTS 0.6B Base, read back by Whisper.
func (v *v02) clone(t *testing.T) (string, string) {
	a := v.a
	refText := "I've been recording audiobooks for nearly ten years, and every morning I start with a cup of tea and a quiet room."
	ref := say(t, v.dir, "clone-ref", "Samantha", refText, 44100)
	a.startClone("")
	v.drain()
	a.cloneLoad(ref, filepath.Base(ref), false)
	v.wait(t, "clone transcript", 10*time.Minute, func() bool {
		s := &a.clone
		return s.busy == "" && s.hasClip() && s.asr != "" && s.asr != "running"
	})
	s := &a.clone
	if s.asr != "done" || overlap(refText, s.transcript) < 0.8 {
		t.Errorf("auto transcript %s %q (%s)", s.asr, s.transcript, s.asrErr)
	}
	transcript := s.transcript
	a.cloneClean(false)
	v.wait(t, "clone enhance", 5*time.Minute, func() bool { return s.busy == "" })
	if !s.cleaned {
		t.Errorf("enhance: %s", s.cleanErr)
	}
	s.name = "V02 Clone"
	n := len(a.voices)
	a.cloneSave()
	v.wait(t, "clone save", 2*time.Minute, func() bool { return len(a.voices) > n && !s.saving })
	var vo *core.Voice
	for i := range a.voices {
		if a.voices[i].Name == "V02 Clone" {
			vo = &a.voices[i]
		}
	}
	if vo == nil {
		t.Fatalf("no saved voice: %+v", a.voices)
	}
	if vo.RefAudio == "" || vo.RefText == "" {
		t.Errorf("voice %+v", vo)
	}
	id := vo.ID
	// Render with it.
	a.selectModel(v.model(t, "qwen3-tts-0.6b-base-8bit"))
	a.studioVoice, a.studioClip, a.voice, a.language = id, nil, "", ""
	a.evenOut = true
	text := "Thank you for listening. This sentence is spoken in a cloned voice made from a short recording."
	a.text = text
	started := time.Now()
	take := v.generate(t, "clone take", 10*time.Minute)
	if take.VoiceID != id || take.RefAudio == "" && len(take.VoicePrompt) == 0 {
		t.Errorf("take not in the cloned voice: voice %q ref %q prompt %q", take.VoiceID, take.RefAudio, take.VoicePrompt)
	}
	heard := v.sttCheck(t, a.takeStore.AudioPath(*take), "en")
	if overlap(text, heard) < 0.7 {
		t.Errorf("clone take says %q", heard)
	}
	return fmt.Sprintf("transcript %.0f%% words, cleaned=%v; take %.1f s in %.0f s, read back %.0f%%",
		100*overlap(refText, transcript), s.cleaned, take.AudioSeconds, time.Since(started).Seconds(), 100*overlap(text, heard)), id
}

// design: a voice from a description (Qwen3-TTS VoiceDesign, or the
// model in AUDIOSMITH_V02_DESIGN), previewed, saved, then used in the Studio.
func (v *v02) design(t *testing.T) string {
	a := v.a
	id := firstOf(os.Getenv("AUDIOSMITH_V02_DESIGN"), "qwen3-tts-1.7b-voicedesign-4bit")
	e := v.model(t, id)
	a.openDesignFor(nil)
	v.drain()
	d := &a.design
	d.model = e.Label
	d.description = "A calm, warm middle-aged male narrator with a deep voice, speaking slowly."
	d.reparse()
	d.script = "Welcome back. Tonight's story begins on a quiet road, far from the city."
	a.designGenerate()
	v.wait(t, "design preview", 15*time.Minute, func() bool { j := a.voiceJob("design", ""); return j != nil && !j.running() })
	j := a.voiceJob("design", "")
	if j.err != "" || j.take == nil {
		t.Fatalf("design preview: %s", j.err)
	}
	preview := j.take
	d.saveName = "V02 Narrator"
	n := len(a.voices)
	a.designSave()
	v.wait(t, "design save", 2*time.Minute, func() bool { return len(a.voices) > n })
	vo := a.findVoice(d.linked)
	if vo == nil || vo.Kind != core.VoiceDesigned {
		t.Fatalf("designed voice %+v", vo)
	}
	if vo.Sample == "" {
		t.Errorf("no sample kept with the designed voice")
	}
	// Use it in the Studio.
	a.selectModel(e)
	a.studioVoice, a.studioClip, a.voice = vo.ID, nil, ""
	a.text = "The road was empty, and the only sound was the wind in the trees."
	take := v.generate(t, "design take", 15*time.Minute)
	au, _ := enginetest.ReadWAV(a.takeStore.AudioPath(*take))
	f0 := medianF0(au.Mono(), au.Rate)
	heard := v.sttCheck(t, a.takeStore.AudioPath(*take), "en")
	if overlap(a.text, heard) < 0.7 {
		t.Errorf("design take says %q", heard)
	}
	if f0 > 180 {
		t.Errorf("a deep male voice at %.0f Hz", f0)
	}
	return fmt.Sprintf("%s: preview %.1f s; take %.1f s, F0 %.0f Hz, read back %.0f%%", e.ID, preview.AudioSeconds, take.AudioSeconds, f0, 100*overlap(a.text, heard))
}

// transcribe: a minute of two voices, Accurate (Whisper turbo + the
// forced aligner), word timings, SRT and VTT exports.
func (v *v02) transcribe(t *testing.T) string {
	a := v.a
	lines := []string{
		"Good morning everyone, and welcome to the weekly product meeting.",
		"Today we will look at the release plan, the open bugs, and the feedback from our first users.",
		"The most common request is a faster way to export subtitles for long videos.",
		"We also heard that the first launch should explain which models to download.",
	}
	var parts []string
	for i, l := range lines {
		parts = append(parts, say(t, v.dir, fmt.Sprintf("tr%d", i), []string{"Daniel", "Samantha"}[i%2], l, 16000))
	}
	in := filepath.Join(v.dir, "meeting.wav")
	var all []float64
	for _, p := range parts {
		au, err := enginetest.ReadWAV(p)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, au.Samples...)
		all = append(all, make([]float64, 8000)...)
	}
	enginetest.WriteWAV(in, enginetest.Audio{Rate: 16000, Channels: 1, Samples: all}, 16)
	a.trMode = 1
	started := time.Now()
	a.transcribeFile(in)
	if a.trJob == nil {
		t.Fatalf("transcription did not start: %q", a.trErr)
	}
	v.wait(t, "transcription", 15*time.Minute, func() bool { return a.trJob == nil })
	if a.trErr != "" {
		t.Fatalf("transcription: %s", a.trErr)
	}
	ts, err := a.trSvc.List(context.Background(), core.TranscriptQuery{})
	if err != nil || len(ts) == 0 {
		t.Fatalf("transcripts %v %v", ts, err)
	}
	tr, _ := a.trSvc.Get(context.Background(), ts[0].ID)
	var text string
	nw := 0
	ordered := true
	last := -1.0
	for _, s := range tr.Segments {
		text += " " + s.Text
		for _, w := range s.Words {
			nw++
			if w.Start < last-0.05 {
				ordered = false
			}
			last = w.Start
		}
	}
	want := strings.Join(lines, " ")
	if overlap(want, text) < 0.9 || tr.Mode != settings.TranscribeAccurate || nw < len(words(want))*8/10 || !ordered {
		t.Errorf("transcript (%s, aligner %q): %d words timed, ordered %v: %q", tr.Mode, tr.Aligner, nw, ordered, text)
	}
	srt, err := services.Export(tr, services.ExportSRT)
	os.WriteFile(filepath.Join(v.dir, "meeting.srt"), srt, 0o644)
	if err != nil || !bytes.Contains(srt, []byte("-->")) || !bytes.Contains(srt, []byte("00:00:0")) {
		t.Errorf("srt %v %q", err, srt)
	}
	vtt, err := services.Export(tr, services.ExportVTT)
	if err != nil || !bytes.HasPrefix(vtt, []byte("WEBVTT")) {
		t.Errorf("vtt %v %q", err, vtt)
	}
	return fmt.Sprintf("%.0f s audio in %.0f s, %d segments, %d timed words, %.0f%% words right, aligner %s",
		tr.Duration, time.Since(started).Seconds(), len(tr.Segments), nw, 100*overlap(want, text), filepath.Base(tr.Aligner))
}

// stories: a two-character script, auto-cast on Kokoro, rendered.
func (v *v02) stories(t *testing.T) string {
	a := v.a
	p := a.lfStories
	a.newLf(p)
	v.drain()
	p.doc.Model = v.model(t, "kokoro-82m-bf16").Repo
	p.doc.ImportText = "# The Storm\n\nThe rain fell all night on the little town.\n\nANNA: Did you hear that noise outside?\n\nBEN: It was only the wind. Go back to sleep.\n\nANNA: I am not so sure. Someone is at the door.\n\nBEN: Fine. I will go and look."
	a.autoCastStory(p)
	p.doc.Title = "V02 Story"
	if len(p.doc.Cast) != 2 {
		t.Fatalf("cast %+v", p.doc.Cast)
	}
	if msg := a.lfBlocker(p); msg != "" {
		t.Fatalf("blocked: %s", msg)
	}
	started := time.Now()
	a.startLf(p, "")
	v.wait(t, "story render start", time.Minute, func() bool { return p.render != "" })
	r := v.waitRender(t, p.render, 20*time.Minute)
	out := r.body.Output
	buf, _, err := wav.ReadFile(out.Book)
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	if buf.Seconds() < 10 {
		t.Errorf("story %.1f s", buf.Seconds())
	}
	return fmt.Sprintf("%d lines, 3 voices → %.1f s in %.0f s, %.1f LUFS", len(p.doc.Lines), buf.Seconds(), time.Since(started).Seconds(), loudness.Integrated(buf))
}

// waitRender waits for a longform render to end and returns it.
func (v *v02) waitRender(t *testing.T, id string, timeout time.Duration) *lfRender {
	t.Helper()
	a := v.a
	v.wait(t, "render "+id, timeout, func() bool {
		r := a.lfRenders[id]
		return r != nil && (r.rec.State == core.RenderDone || r.rec.State == core.RenderFailed || r.rec.State == core.RenderStopped) && !r.body.Assembling
	})
	// The live snapshot carries the summary; read the whole body.
	rec, body, err := a.lib.svc.Longform.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != core.RenderDone || body.Output == nil {
		t.Fatalf("render %s: %s %+v", id, rec.State, body.Error)
	}
	return &lfRender{rec: rec, body: body}
}

// audiobook: a TXT of two chapters, ACX loudness, M4B with a cover.
func (v *v02) audiobook(t *testing.T) string {
	a := v.a
	p := a.lfBook
	a.newLf(p)
	v.drain()
	book := filepath.Join(v.dir, "book.txt")
	os.WriteFile(book, []byte("Chapter One\n\n"+longText(enSentences[:5], 700)+"\n\nChapter Two\n\n"+longText(enSentences[5:], 700)+"\n"), 0o644)
	a.importBookFrom(p, book)
	v.wait(t, "import", time.Minute, func() bool { return !p.importing && strings.Contains(p.doc.Script, "Chapter Two") })
	cover := filepath.Join(v.dir, "cover.png")
	img := image.NewRGBA(image.Rect(0, 0, 600, 600))
	for y := range 600 {
		for x := range 600 {
			img.Set(x, y, color.RGBA{uint8(x / 3), 80, uint8(y / 3), 255})
		}
	}
	f, _ := os.Create(cover)
	png.Encode(f, img)
	f.Close()
	p.doc.Model = v.model(t, "kokoro-82m-bf16").Repo
	p.doc.Voice = services.SpeakerRef + "bm_george"
	p.doc.Title, p.doc.Book.Author, p.doc.Book.Cover = "V02 Book", "AudioSmith", cover
	p.doc.Book.Loudness = services.LoudnessACX
	p.doc.Format = services.FormatM4B
	if msg := a.lfBlocker(p); msg != "" {
		t.Fatalf("blocked: %s", msg)
	}
	started := time.Now()
	a.startLf(p, "")
	v.wait(t, "book render start", time.Minute, func() bool { return p.render != "" })
	r := v.waitRender(t, p.render, 30*time.Minute)
	out := r.body.Output
	if len(out.Chapters) != 2 {
		t.Errorf("chapters %+v", out.Chapters)
	}
	if out.Encoded == "" {
		t.Fatalf("no M4B: %s", out.EncodeError)
	}
	note := fmt.Sprintf("2 chapters → %.0f s in %.0f s, book %.1f LUFS, M4B %s", out.Seconds, time.Since(started).Seconds(), out.Loudness.Integrated, filepath.Base(out.Encoded))
	for _, c := range out.Chapters {
		if c.Report.Integrated < -24 || c.Report.Integrated > -18 || c.Report.TruePeak > -3 {
			t.Errorf("ACX chapter %q: %.1f LUFS, %.1f dBTP", c.Title, c.Report.Integrated, c.Report.TruePeak)
		}
	}
	cp := filepath.Join(v.dir, "V02 Book.m4b")
	copyWAV(out.Encoded, cp)
	if m := ffprobeJSON(t, "-show_chapters", "-show_streams", "-show_format", out.Encoded); m != nil {
		chs, _ := m["chapters"].([]any)
		streams, _ := m["streams"].([]any)
		var kinds []string
		pic := false
		for _, s := range streams {
			s := s.(map[string]any)
			kinds = append(kinds, fmt.Sprint(s["codec_type"], ":", s["codec_name"]))
			if d, ok := s["disposition"].(map[string]any); ok && d["attached_pic"] == float64(1) {
				pic = true
			}
		}
		tags := map[string]any{}
		if f, ok := m["format"].(map[string]any); ok {
			tags, _ = f["tags"].(map[string]any)
		}
		note += fmt.Sprintf(", ffprobe: %d chapters, streams %v, cover %v, title %v, artist %v", len(chs), kinds, pic, tags["title"], tags["artist"])
		if len(chs) != 2 || !pic || !slices.Contains(kinds, "audio:aac") {
			t.Errorf("M4B: %s", note)
		}
	}
	return note
}

// dubClip writes the 30 s two-speaker clip over music, and an MP4 of it.
func (v *v02) dubClip(t *testing.T, name string, lines []struct{ voice, text string }, video bool) string {
	t.Helper()
	const rate = 44100
	var speech []float64
	speech = append(speech, make([]float64, rate/2)...)
	for i, l := range lines {
		p := say(t, v.dir, fmt.Sprintf("%s-line%d", name, i), l.voice, l.text, rate)
		au, err := enginetest.ReadWAV(p)
		if err != nil {
			t.Fatal(err)
		}
		speech = append(speech, au.Samples...)
		speech = append(speech, make([]float64, rate*7/10)...)
	}
	music := synthMusic(len(speech), rate)
	mix := make([]float64, 2*len(speech))
	for i, s := range speech {
		mix[2*i], mix[2*i+1] = 0.9*s+music[2*i], 0.9*s+music[2*i+1]
	}
	wavPath := filepath.Join(v.dir, name+".wav")
	enginetest.WriteWAV(wavPath, enginetest.Audio{Rate: rate, Channels: 2, Samples: mix}, 16)
	if !video {
		return wavPath
	}
	ff := lookTool("ffmpeg")
	if ff == "" {
		t.Fatal("needs ffmpeg for the source video")
	}
	mp4 := filepath.Join(v.dir, name+".mp4")
	secs := float64(len(speech)) / rate
	if b, err := exec.Command(ff, "-loglevel", "error", "-y", "-f", "lavfi", "-i", fmt.Sprintf("testsrc=size=640x360:rate=25:duration=%.2f", secs), "-i", wavPath,
		"-c:v", "h264_videotoolbox", "-b:v", "1M", "-c:a", "aac", "-shortest", mp4).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, b)
	}
	return mp4
}

var dubLines = []struct{ voice, text string }{
	{"Samantha", "Good morning, and welcome back to the show. Today we are talking about trains."},
	{"Daniel", "Thanks for having me. I have loved trains since I was a little boy."},
	{"Samantha", "What is the longest journey you have ever taken?"},
	{"Daniel", "Probably the night train from London to the far north of Scotland."},
	{"Samantha", "That sounds wonderful. Did you sleep well on the way, or did you stay up watching the hills?"},
	{"Daniel", "I stayed up most of the night. The hills in the moonlight were too beautiful to miss, and the tea was good."},
}

// dubFlow: the whole pipeline into Chinese with the local LLM, then an
// MP4 with the original and the dubbed track and subtitles.
func (v *v02) dubFlow(t *testing.T) string {
	a := v.a
	if ok, _ := a.dubTranslatorReady(dub.Settings{Translator: "llm"}); !ok {
		t.Fatalf("no translator (local LLM ready %v)", a.localLLMReady())
	}
	src := v.dubClip(t, "dub", dubLines, true)
	n := len(a.dubList)
	a.updateSettings(func(s *settings.Settings) {
		s.Dubbing.Targets, s.Dubbing.NoReview, s.Dubbing.Translator = []string{"zh"}, true, "llm"
		s.Dubbing.Model = v.model(t, "qwen3-tts-0.6b-base-8bit").Repo
	})
	a.newDub(dub.Source{Kind: dub.SourceFile, Path: src, Name: filepath.Base(src)})
	v.wait(t, "dub created", time.Minute, func() bool { return a.dub != nil && len(a.dubList) > n })
	id := a.dub.ID
	started := time.Now()
	ids, err := a.dubSvc.Run(context.Background(), id, services.RunRequest{Through: dub.StageMix})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	stageTimes := map[string]float64{}
	for st, jid := range ids {
		j := v.job(t, jid, 60*time.Minute)
		if j.State != jobs.StateSucceeded {
			t.Errorf("stage %s: %s %+v", st, j.State, j.Err)
		}
		if !j.Started.IsZero() && !j.Finished.IsZero() {
			stageTimes[st] = j.Finished.Sub(j.Started).Seconds()
		}
	}
	p, err := a.dubSvc.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	speakers := map[string]bool{}
	han := 0
	for _, s := range p.Segments {
		speakers[s.Speaker] = true
		if hanRE.MatchString(s.Lines["zh"].Text) {
			han++
		}
	}
	for k, st := range p.Stages {
		if st.Status != dub.StatusDone {
			t.Logf("stage %s: %s %s %s %v", k, st.Status, st.Code, st.Message, st.Warnings)
		}
	}
	if len(p.Segments) < 4 || len(speakers) != 2 || han < len(p.Segments)*8/10 || p.Track("zh").File == "" {
		t.Errorf("dub: %d segments, speakers %v, %d translated, track %+v", len(p.Segments), speakers, han, p.Track("zh"))
	}
	for i, s := range p.Segments {
		if i < 3 {
			t.Logf("  %s %.1f–%.1f %q → %q", s.Speaker, s.Start, s.End, s.Text, s.Lines["zh"].Text)
		}
	}
	// Export the MP4: original + Chinese, soft subtitles.
	out := filepath.Join(v.dir, "V02 Dub.mp4")
	jid, err := a.dubSvc.ExportJob(context.Background(), services.ExportRequest{Dub: id, Format: dub.ExportMP4, Tracks: []string{dub.OrigTrack, "zh"},
		Default: "zh", Subtitles: true, Output: out})
	if err != nil {
		t.Fatal(err)
	}
	if j := v.job(t, jid, 10*time.Minute); j.State != jobs.StateSucceeded {
		t.Fatalf("export: %+v", j.Err)
	}
	note := fmt.Sprintf("%.0f s clip → %d lines, %d speakers, %d/%d in Chinese, in %.0f s (stages %v)", p.Source.Duration, len(p.Segments), len(speakers), han, len(p.Segments),
		time.Since(started).Seconds(), roundMap(stageTimes))
	if m := ffprobeJSON(t, "-show_streams", out); m != nil {
		streams, _ := m["streams"].([]any)
		count := map[string]int{}
		var langs []string
		for _, s := range streams {
			s := s.(map[string]any)
			count[fmt.Sprint(s["codec_type"])]++
			if tags, ok := s["tags"].(map[string]any); ok {
				langs = append(langs, fmt.Sprint(s["codec_type"], ":", tags["language"]))
			}
		}
		note += fmt.Sprintf("; MP4 streams %v %v", count, langs)
		if count["video"] != 1 || count["audio"] != 2 || count["subtitle"] < 1 {
			t.Errorf("MP4: %v", count)
		}
	}
	av := avTracks(t, v.dir, out)
	t.Logf("AVFoundation: %s", av)
	if !strings.Contains(av, "soun zh") {
		t.Errorf("AVFoundation does not see the Chinese track: %s", av)
	}
	return note
}

func roundMap(m map[string]float64) map[string]int {
	out := map[string]int{}
	for k, v := range m {
		out[k] = int(v + 0.5)
	}
	return out
}

// batchFlow: one file through the batch queue into Chinese.
func (v *v02) batchFlow(t *testing.T) string {
	a := v.a
	src := v.dubClip(t, "batch", dubLines[:2], false)
	bs := services.DefaultBatchSettings()
	bs.Targets, bs.Model = []string{"zh"}, v.model(t, "qwen3-tts-0.6b-base-8bit").Repo
	started := time.Now()
	ids, errs := a.dubSvc.Enqueue(context.Background(), []string{src}, bs)
	if len(ids) != 1 || len(errs) > 0 {
		t.Fatalf("enqueue %v %v", ids, errs)
	}
	var p *dub.Project
	v.wait(t, "batch item", 40*time.Minute, func() bool {
		act, _ := a.dubSvc.Active(context.Background(), ids[0])
		if len(act) > 0 {
			return false
		}
		p, _ = a.dubSvc.Get(context.Background(), ids[0])
		return p != nil && (p.Track("zh").File != "" || p.Run == nil && len(p.Stages) > 0)
	})
	if p.Track("zh").File == "" {
		t.Errorf("batch item: %+v", p.Stages)
	}
	q, _ := a.dubSvc.Queue(context.Background())
	phase := ""
	for _, r := range q {
		if r.ID == ids[0] {
			phase = r.Phase
		}
	}
	return fmt.Sprintf("1 item (%.0f s) in %.0f s, phase %q, %d lines", p.Source.Duration, time.Since(started).Seconds(), phase, len(p.Segments))
}

// workflowFlow: the "Translate and narrate" template on two recordings,
// into Chinese, spoken by Kokoro, exported to a folder.
func (v *v02) workflowFlow(t *testing.T) string {
	a := v.a
	clips := []string{
		say(t, v.dir, "wf1", "Samantha", "The museum opens at nine in the morning and closes at six.", 24000),
		say(t, v.dir, "wf2", "Daniel", "Tickets are free for children under twelve years old.", 24000),
	}
	a.navigate("/workflows")
	v.drain()
	a.wfNew("translate-narrate")
	v.drain()
	w := a.wf
	if w == nil {
		t.Fatal("no workflow")
	}
	outDir := filepath.Join(v.dir, "workflow-out")
	os.MkdirAll(outDir, 0o755)
	kokoro := v.model(t, "kokoro-82m-bf16").Repo
	var src string
	a.wfEdit(func(w *workflow.Workflow) {
		for i := range w.Nodes {
			n := &w.Nodes[i]
			switch n.Kind {
			case workflow.KindSource:
				src = n.ID
			case workflow.KindTranslate:
				n.P.To = "zh"
			case workflow.KindSpeak:
				n.P.Model, n.P.Voice, n.P.Language = kokoro, services.SpeakerRef+"zf_xiaobei", "zh"
			case workflow.KindExport:
				n.P.Folder = outDir
			}
		}
	})
	a.wfClipFiles(src, clips)
	v.drain()
	if rep := a.wfReport(); !rep.OK() {
		t.Fatalf("invalid: %+v", rep)
	}
	started := time.Now()
	a.wfStart(false)
	v.wait(t, "workflow run", 40*time.Minute, func() bool {
		return a.wfRun != nil && a.wfRun.State != workflow.RunRunning && a.wfRun.State != workflow.RunIdle && !a.wfBusy()
	})
	run := a.wfRun
	if run.State != workflow.RunDone {
		t.Errorf("run %s %s %s", run.State, run.Code, run.Err)
	}
	files, _ := filepath.Glob(filepath.Join(outDir, "*"))
	outs := services.Outputs(a.wf, run)
	if len(outs) != 2 || len(files) != 2 {
		t.Errorf("outputs %d, files %v", len(outs), files)
	}
	var said []string
	for _, f := range files {
		if strings.HasSuffix(f, ".wav") || strings.HasSuffix(f, ".m4a") {
			said = append(said, v.sttCheck(t, f, "zh"))
		}
	}
	for _, s := range said {
		if !hanRE.MatchString(s) {
			t.Errorf("narration not Chinese: %q", s)
		}
	}
	return fmt.Sprintf("2 clips → %d files in %.0f s; heard %q", len(files), time.Since(started).Seconds(), said)
}

// apiFlow: the Local API on a test port: health, models, speech (wav)
// and transcription with curl, the openai SDK when there is a python
// with it, and the MCP stdio shim of the built app.
func (v *v02) apiFlow(t *testing.T, data string) string {
	a := v.a
	port := 3917
	a.updateSettings(func(s *settings.Settings) { s.API.Enabled, s.API.Port = true, port })
	v.drain()
	a.startAPI(data)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	v.wait(t, "api up", 30*time.Second, func() bool { return a.apiPort == port || a.apiErr != "" })
	if a.apiErr != "" {
		t.Fatalf("api: %s", a.apiErr)
	}
	var notes []string
	curl := func(what string, args ...string) (string, error) {
		var out []byte
		err := v.do(t, what, 10*time.Minute, func() error {
			var err error
			out, err = exec.Command("curl", append([]string{"-sS", "--noproxy", "*", "-w", "\n%{http_code} %{time_total}"}, args...)...).CombinedOutput()
			return err
		})
		return string(out), err
	}
	out, err := curl("health", base+"/health")
	if err != nil || !strings.Contains(out, "\n200 ") {
		t.Errorf("health: %v %s", err, out)
	}
	out, err = curl("models", base+"/v1/models")
	if err != nil || !strings.Contains(out, "\n200 ") || !strings.Contains(out, "tts-1") {
		t.Errorf("models: %v %.300s", err, out)
	}
	speech := filepath.Join(v.dir, "api-speech.wav")
	out, err = curl("speech", "-o", speech, "-H", "Content-Type: application/json", "-d",
		`{"model":"tts-1","voice":"alloy","input":"Hello from the AudioSmith local API.","response_format":"wav"}`, base+"/v1/audio/speech")
	if err != nil || !strings.Contains(out, "200 ") || wavBits(speech) == 0 {
		t.Errorf("speech: %v %s", err, out)
	}
	notes = append(notes, "curl speech "+strings.TrimSpace(out))
	out, err = curl("transcription", "-F", "file=@"+speech, "-F", "model=whisper-1", base+"/v1/audio/transcriptions")
	if err != nil || !strings.Contains(strings.ToLower(out), "local api") {
		t.Errorf("transcription: %v %s", err, out)
	}
	notes = append(notes, "curl transcription "+strings.ReplaceAll(strings.TrimSpace(out), "\n", " "))
	if py := os.Getenv("AUDIOSMITH_V02_PY"); py != "" {
		script := filepath.Join(v.dir, "openai_check.py")
		os.WriteFile(script, []byte(`import sys, time
from openai import OpenAI
c = OpenAI(base_url=sys.argv[1] + "/v1", api_key="local")
t = time.time()
with c.audio.speech.with_streaming_response.create(model="gpt-4o-mini-tts", voice="nova", input="The official OpenAI SDK works with AudioSmith.", response_format="wav") as r:
    r.stream_to_file(sys.argv[2])
print("speech", round(time.time() - t, 2))
t = time.time()
tr = c.audio.transcriptions.create(model="whisper-1", file=open(sys.argv[2], "rb"))
print("transcription", round(time.time() - t, 2), tr.text)
tr = c.audio.transcriptions.create(model="whisper-1", file=open(sys.argv[2], "rb"), response_format="srt")
print("srt", "-->" in tr)
`), 0o644)
		var pyOut []byte
		err := v.do(t, "openai sdk", 10*time.Minute, func() error {
			cmd := exec.Command(py, "-I", script, base, filepath.Join(v.dir, "sdk-speech.wav"))
			cmd.Env = append(os.Environ(), "NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost")
			var err error
			pyOut, err = cmd.CombinedOutput()
			return err
		})
		s := strings.TrimSpace(string(pyOut))
		if err != nil || !strings.Contains(strings.ToLower(s), "openai sdk") || !strings.Contains(s, "srt True") {
			t.Errorf("openai sdk: %v\n%s", err, s)
		}
		notes = append(notes, "openai-python: "+strings.ReplaceAll(s, "\n", "; "))
	}
	if exe := os.Getenv("AUDIOSMITH_V02_APP"); exe != "" {
		notes = append(notes, v.mcpStdio(t, exe, base))
	}
	return strings.Join(notes, " · ")
}

// mcpStdio talks MCP to the app's stdio shim: initialize, list_voices,
// generate_speech.
func (v *v02) mcpStdio(t *testing.T, exe, base string) string {
	cmd := exec.Command(exe, "--mcp-stdio", "--client", "v02-test")
	cmd.Env = append(os.Environ(), "AUDIOSMITH_API_URL="+base, "NO_PROXY=127.0.0.1")
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Errorf("mcp: %v", err)
		return "mcp: " + err.Error()
	}
	defer func() { stdin.Close(); cmd.Wait() }()
	rd := bufio.NewReaderSize(stdout, 1<<20)
	call := func(id int, method string, params any) (map[string]any, error) {
		msg, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		var res map[string]any
		err := v.do(t, "mcp "+method, 10*time.Minute, func() error {
			if _, err := stdin.Write(append(msg, '\n')); err != nil {
				return err
			}
			for {
				line, err := rd.ReadBytes('\n')
				if err != nil {
					return fmt.Errorf("%v (stderr %s)", err, stderr.String())
				}
				var m map[string]any
				if json.Unmarshal(line, &m) == nil && m["id"] == float64(id) {
					res = m
					return nil
				}
			}
		})
		return res, err
	}
	started := time.Now()
	init, err := call(1, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "v02-test", "version": "1"}})
	if err != nil || init["result"] == nil {
		t.Errorf("mcp initialize: %v %v", err, init)
		return "mcp initialize failed"
	}
	stdin.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"))
	tools, _ := call(2, "tools/list", map[string]any{})
	lv, err := call(3, "tools/call", map[string]any{"name": "list_voices", "arguments": map[string]any{}})
	lvs, _ := json.Marshal(lv)
	if err != nil || !strings.Contains(string(lvs), "V02") && !strings.Contains(string(lvs), "af_heart") {
		t.Errorf("list_voices: %v %.400s", err, lvs)
	}
	gs, err := call(4, "tools/call", map[string]any{"name": "generate_speech", "arguments": map[string]any{"text": "Hello from MCP over stdio."}})
	gss, _ := json.Marshal(gs)
	if err != nil || !strings.Contains(string(gss), "audio/wav") && !strings.Contains(string(gss), ".wav") {
		t.Errorf("generate_speech: %v %.400s", err, gss)
	}
	nt := 0
	if r, ok := tools["result"].(map[string]any); ok {
		ts, _ := r["tools"].([]any)
		nt = len(ts)
	}
	return fmt.Sprintf("MCP stdio: %d tools, list_voices %d B, generate_speech %d B in %.1f s", nt, len(lvs), len(gss), time.Since(started).Seconds())
}

// watermarkFlow: AudioSeal finds the mark in a Studio take and in the
// API's take, not in macOS speech.
func (v *v02) watermarkFlow(t *testing.T, takeID string) string {
	a := v.a
	if takeID == "" && a.lastClip() != nil {
		takeID = a.lastClip().ID
	}
	take, err := a.lib.svc.Takes.Get(context.Background(), takeID)
	if err != nil {
		t.Fatalf("no take to check: %v", err)
	}
	clean := say(t, v.dir, "clean", "Samantha", "This recording was never watermarked by AudioSmith.", 24000)
	var notes []string
	for _, c := range []struct {
		name, path string
		want       bool
	}{{"take", a.takeStore.AudioPath(take), true}, {"say", clean, false}} {
		a.checkWatermark(c.path)
		v.wait(t, "detect "+c.name, 5*time.Minute, func() bool { return !a.wmCheck.busy })
		if a.wmCheck.err != "" || a.wmCheck.res == nil {
			t.Fatalf("%s: %s", c.name, a.wmCheck.err)
		}
		r := a.wmCheck.res
		notes = append(notes, fmt.Sprintf("%s: %+v", c.name, *r))
		if r.Detected != c.want {
			t.Errorf("%s: detected %v, want %v (%+v)", c.name, r.Detected, c.want, *r)
		}
	}
	return strings.Join(notes, "; ")
}

// crashFlow: the gpu worker killed in the middle of a render; the
// supervisor restarts it, the job is retried or fails cleanly, and the
// next take works.
func (v *v02) crashFlow(t *testing.T) string {
	a := v.a
	a.selectModel(v.model(t, "kokoro-82m-bf16"))
	a.studioVoice, a.voice, a.text = "", "af_heart", longText(enSentences, 2500)
	a.generate()
	v.wait(t, "rendering", 2*time.Minute, func() bool { return a.job != nil && a.job.fraction > 0.05 })
	pid := 0
	for _, w := range a.workers {
		if w.Role == proto.RoleGPU {
			pid = w.PID
		}
	}
	if pid == 0 {
		t.Fatal("no gpu worker pid")
	}
	syscall.Kill(pid, syscall.SIGKILL)
	killed := time.Now()
	v.wait(t, "after the crash", 15*time.Minute, func() bool { return a.job == nil && a.awaitTake == "" })
	outcome := "retried: take made"
	if a.jobError != "" {
		outcome = "failed: " + a.jobError
	}
	restarts := 0
	for _, w := range a.workers {
		if w.Role == proto.RoleGPU {
			restarts = w.Restarts
		}
	}
	v.wait(t, "engine back", 2*time.Minute, func() bool { return a.engState == engineReady })
	a.text = "After the crash, AudioSmith keeps working."
	take := v.generate(t, "after crash", 5*time.Minute)
	return fmt.Sprintf("killed pid %d; %s after %.0f s; restarts %d; next take %.1f s", pid, outcome, time.Since(killed).Seconds(), restarts, take.AudioSeconds)
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
