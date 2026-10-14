package engine

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/engine/proto"
)

// TestRealEngineConformance speaks raw protocol 2 with the built Swift
// engine and checks what the spec promises: handshake errors, error codes,
// busy, cancel by id, streamed chunks without gaps, notifications before
// the response, shutdown. It needs macOS, a built engine and Kokoro in the
// HF cache (scripts/smoke-engine.sh downloads it), so it runs only with
// AUDIOSMITH_E2E=1.
func TestRealEngineConformance(t *testing.T) {
	if os.Getenv("AUDIOSMITH_E2E") == "" {
		t.Skip("set AUDIOSMITH_E2E=1 to run the real engine")
	}
	bin := Find(filepath.Join("..", "..", "resources"))
	if bin == "" {
		t.Fatal("no engine: run scripts/build-engine.sh")
	}
	const kokoro = "mlx-community/Kokoro-82M-bf16"
	dir := t.TempDir()
	// A model folder of an unknown type: refused without the engine
	// resolving the made-up repo id in the HF cache (which left a
	// mlx-audio/someone_not-a-model-type folder in the user's cache).
	badModel := filepath.Join(dir, "bad-model")
	os.MkdirAll(badModel, 0o755)
	os.WriteFile(filepath.Join(badModel, "config.json"), []byte(`{"model_type":"not-a-model-type"}`), 0o644)

	e := startRaw(t, bin)
	// Before hello.
	if m := e.ask(t, "c1", proto.MethodStats, nil); m.Error == nil || m.Error.Code != proto.CodeNotInitialized {
		t.Fatalf("stats before hello: %+v", m)
	}
	m := e.ask(t, "c2", proto.MethodHello, proto.HelloParams{Protocol: 3, App: "test", Role: proto.RoleGPU})
	if m.Error == nil || m.Error.Code != proto.CodeProtocolMismatch || string(m.Error.Data) != `{"supported":[2]}` {
		t.Fatalf("hello 3: %+v", m)
	}
	// A model folder linked to the real one, and Kokoro by its snapshot
	// path as the app sends it: loading by repo id would make
	// mlx-audio-swift copy the model into <cache>/mlx-audio.
	cache := linkedCache(t, kokoro)
	snaps, _ := filepath.Glob(filepath.Join(cache, "models--mlx-community--Kokoro-82M-bf16", "snapshots", "*", "config.json"))
	if len(snaps) == 0 {
		t.Skip("Kokoro is not downloaded")
	}
	kokoroPath := filepath.Dir(snaps[0])
	m = e.ask(t, "c3", proto.MethodHello, proto.HelloParams{Protocol: 2, App: "test", Role: proto.RoleGPU, CacheDir: cache})
	var hello proto.HelloResult
	if err := m.DecodeResult(&hello); err != nil || hello.Protocol != 2 || hello.Role != proto.RoleGPU || hello.Concurrency != 1 {
		t.Fatalf("hello: %+v %v", m, err)
	}
	for _, want := range []string{proto.MethodModelsLoad, proto.MethodTTSSynthesize, proto.MethodJobCancel, proto.MethodStats} {
		if !slices.Contains(hello.Methods, want) {
			t.Errorf("hello lacks %s: %v", want, hello.Methods)
		}
	}
	if !slices.Contains(hello.Families, "kokoro") || hello.Device.Memory == 0 {
		t.Errorf("hello %+v", hello)
	}

	codes := map[string]struct {
		method string
		params any
		code   string
	}{
		"unknown":   {"lid.detect", map[string]string{"audio": "/tmp/a.wav"}, proto.CodeMethodNotFound},
		"no model":  {proto.MethodTTSSynthesize, proto.SynthesizeParams{Text: "Hi", Output: proto.Output{Path: dir + "/x.wav"}}, proto.CodeBadRequest},
		"relative":  {proto.MethodTTSSynthesize, proto.SynthesizeParams{Model: kokoro, ModelPath: kokoroPath, Text: "Hi", Output: proto.Output{Path: "x.wav"}}, proto.CodeBadRequest},
		"clone":     {proto.MethodTTSSynthesize, proto.SynthesizeParams{Model: kokoro, ModelPath: kokoroPath, Text: "Hi", RefAudio: "/tmp/ref.wav", Output: proto.Output{Path: dir + "/c.wav"}}, proto.CodeUnsupported},
		"bad model": {proto.MethodModelsLoad, proto.LoadParams{Model: "someone/not-a-model-type", Path: badModel}, proto.CodeModelLoadFailed},
		"unwritable": {proto.MethodTTSSynthesize, proto.SynthesizeParams{Model: kokoro, ModelPath: kokoroPath, Text: "Hi", Voice: "af_heart",
			Output: proto.Output{Path: "/nonexistent-root-dir/x.wav"}}, proto.CodeInternal},
	}
	for name, c := range codes {
		m := e.ask(t, "e-"+name, c.method, c.params)
		if m.Error == nil || m.Error.Code != c.code {
			t.Errorf("%s: %+v, want %s", name, m, c.code)
		}
	}

	// A streamed take: progress and gapless chunks, all before the result.
	seed := int64(42)
	speed := 1.1
	e.send(t, "j1", proto.MethodTTSSynthesize, proto.SynthesizeParams{Model: kokoro, ModelPath: kokoroPath, Text: "Streaming test.", Voice: "af_heart",
		Speed: &speed, Seed: &seed, Stream: true, Output: proto.Output{Path: dir + "/j1.wav", Bits: 24}})
	var chunks []proto.ChunkParams
	var stages []string
	var frames int64
	for {
		m := e.next(t)
		if m.Kind() == proto.KindResponse {
			if m.ID != "j1" || m.Error != nil {
				t.Fatalf("j1: %+v", m)
			}
			var res proto.SynthesizeResult
			m.DecodeResult(&res)
			if res.Seed == nil || *res.Seed != 42 || res.SampleRate != 24000 || res.Frames == 0 || res.Channels != 1 {
				t.Errorf("result %+v", res)
			}
			if frames != res.Frames {
				t.Errorf("chunks carry %d frames, the file %d", frames, res.Frames)
			}
			break
		}
		switch m.Method {
		case proto.MethodTTSChunk:
			var c proto.ChunkParams
			m.DecodeParams(&c)
			if c.Job != "j1" || c.Seq != len(chunks) || c.Encoding != "pcm16le" || len(c.Data)%2 != 0 || len(c.Data) > 2*c.SampleRate {
				t.Errorf("chunk %d: job %s seq %d enc %s %d bytes", len(chunks), c.Job, c.Seq, c.Encoding, len(c.Data))
			}
			frames += int64(len(c.Data) / 2)
			chunks = append(chunks, c)
		case proto.MethodJobProgress:
			var p proto.ProgressParams
			m.DecodeParams(&p)
			if p.Fraction != nil && (*p.Fraction < 0 || *p.Fraction > 1) {
				t.Errorf("fraction %v", *p.Fraction)
			}
			if len(stages) == 0 || stages[len(stages)-1] != p.Stage {
				stages = append(stages, p.Stage)
			}
		}
	}
	if len(chunks) < 2 || !chunks[len(chunks)-1].Final {
		t.Errorf("%d chunks, last %+v", len(chunks), chunks[len(chunks)-1])
	}
	if !slices.Equal(stages, []string{"generating", "writing"}) && !slices.Equal(stages, []string{"loading", "generating", "writing"}) {
		t.Errorf("stages %v", stages)
	}
	if st, err := os.Stat(dir + "/j1.wav"); err != nil || st.Size() != 44+3*frames {
		t.Errorf("24-bit file: %v %v", st, err)
	}

	// One job at a time; cancel by id.
	// Kokoro takes at most ~510 phonemes at once.
	long := strings.Repeat("This sentence makes the take long enough to cancel it. ", 7)
	j2At := time.Now()
	e.send(t, "j2", proto.MethodTTSSynthesize, proto.SynthesizeParams{Model: kokoro, ModelPath: kokoroPath, Text: long, Voice: "af_heart", Output: proto.Output{Path: dir + "/j2.wav"}})
	e.send(t, "j3", proto.MethodTTSSynthesize, proto.SynthesizeParams{Model: kokoro, ModelPath: kokoroPath, Text: "Second", Output: proto.Output{Path: dir + "/j3.wav"}})
	if m := e.until(t, "j3"); m.Error == nil || m.Error.Code != proto.CodeBusy {
		t.Fatalf("j3: %+v", m)
	}
	// Let it reach the model.
	for {
		m := e.next(t)
		var p proto.ProgressParams
		if m.Method == proto.MethodJobProgress && m.DecodeParams(&p) == nil && p.Job == "j2" && p.Stage == "generating" {
			break
		}
	}
	time.Sleep(1500 * time.Millisecond)
	// Control requests are answered while a job runs.
	var stats proto.StatsResult
	if m := e.until(t, e.send(t, "c4", proto.MethodStats, nil)); m.DecodeResult(&stats) != nil || !slices.Equal(stats.Jobs, []string{"j2"}) || stats.Memory.Active == 0 {
		t.Fatalf("stats during j2: %+v", m)
	}
	cancelAt := time.Now()
	t.Logf("job.cancel sent %.2f s after j2", cancelAt.Sub(j2At).Seconds())
	var cancelled proto.CancelResult
	if m := e.until(t, e.send(t, "c5", proto.MethodJobCancel, proto.CancelParams{Job: "j2"})); m.DecodeResult(&cancelled) != nil || !cancelled.Cancelled {
		t.Fatalf("cancel: %+v", m)
	}
	if m := e.until(t, "j2"); m.Error == nil || m.Error.Code != proto.CodeCancelled {
		t.Fatalf("j2: %+v", m)
	}
	t.Logf("j2 ended %.2f s after job.cancel (Kokoro evaluates its whole take at once)", time.Since(cancelAt).Seconds())
	if _, err := os.Stat(dir + "/j2.wav"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a cancelled job left its output: %v", err)
	}
	if m := e.until(t, e.send(t, "c6", proto.MethodJobCancel, proto.CancelParams{Job: "j2"})); m.DecodeResult(&cancelled) != nil || cancelled.Cancelled {
		t.Errorf("second cancel: %+v", m)
	}

	// Models and memory.
	var loaded proto.LoadedResult
	if m := e.until(t, e.send(t, "c7", proto.MethodModelsLoaded, nil)); m.DecodeResult(&loaded) != nil || len(loaded.Models) != 1 || loaded.Models[0].Model != kokoro {
		t.Errorf("loaded: %+v", m)
	}
	if m := e.until(t, e.send(t, "c8", proto.MethodFlush, nil)); m.Error != nil {
		t.Errorf("flush: %+v", m)
	}
	var unloaded proto.UnloadResult
	if m := e.until(t, e.send(t, "c9", proto.MethodModelsUnload, proto.UnloadParams{})); m.DecodeResult(&unloaded) != nil || !slices.Equal(unloaded.Unloaded, []string{kokoro}) {
		t.Errorf("unload: %+v", m)
	}
	if m := e.until(t, e.send(t, "c10", proto.MethodShutdown, nil)); m.Error != nil {
		t.Errorf("shutdown: %+v", m)
	}
	if err := e.wait(); err != nil {
		t.Errorf("exit after shutdown: %v", err)
	}

	// Offline: a model missing from the cache is not downloaded.
	e = startRaw(t, bin)
	e.ask(t, "c1", proto.MethodHello, proto.HelloParams{Protocol: 2, App: "test", Role: proto.RoleGPU, CacheDir: dir, Offline: true})
	m = e.ask(t, "j1", proto.MethodModelsLoad, proto.LoadParams{Model: kokoro})
	if m.Error == nil || m.Error.Code != proto.CodeModelNotDownloaded || !strings.Contains(string(m.Error.Data), kokoro) {
		t.Errorf("offline load: %+v", m)
	}
	// Closing stdin ends it.
	e.stdin.Close()
	if err := e.wait(); err != nil {
		t.Errorf("exit after stdin closed: %v", err)
	}

	// Other roles have no models.
	e = startRaw(t, bin, "--role", "media")
	m = e.ask(t, "c1", proto.MethodHello, proto.HelloParams{Protocol: 2, App: "test", Role: proto.RoleMedia})
	if m.DecodeResult(&hello) != nil || hello.Role != proto.RoleMedia || slices.Contains(hello.Methods, proto.MethodTTSSynthesize) {
		t.Errorf("media hello: %+v", m)
	}
	if m := e.ask(t, "j1", proto.MethodModelsLoad, proto.LoadParams{Model: kokoro}); m.Error == nil || m.Error.Code != proto.CodeMethodNotFound {
		t.Errorf("media load: %+v", m)
	}
	e.stdin.Close()
	e.wait()
}

type rawEngine struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	lines chan []byte
	held  []proto.Message // read while waiting for another id
}

func startRaw(t *testing.T, bin string, args ...string) *rawEngine {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = filepath.Dir(bin)
	cmd.Stderr = testWriter{t}
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	e := &rawEngine{cmd: cmd, stdin: stdin, lines: make(chan []byte, 1024)}
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64<<10), proto.MaxLine)
		for sc.Scan() {
			e.lines <- append([]byte(nil), sc.Bytes()...)
		}
		close(e.lines)
	}()
	t.Cleanup(func() { cmd.Process.Kill() })
	return e
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Logf("stderr: %s", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func (e *rawEngine) send(t *testing.T, id, method string, params any) string {
	t.Helper()
	m, err := proto.NewRequest(id, method, params)
	if err != nil {
		t.Fatal(err)
	}
	line, _ := proto.Encode(m)
	if _, err := e.stdin.Write(line); err != nil {
		t.Fatal(err)
	}
	return id
}

// next returns the next message, held ones first.
func (e *rawEngine) next(t *testing.T) proto.Message {
	t.Helper()
	if len(e.held) > 0 {
		m := e.held[0]
		e.held = e.held[1:]
		return m
	}
	select {
	case line, ok := <-e.lines:
		if !ok {
			t.Fatal("the engine closed stdout")
		}
		m, err := proto.Decode(line)
		if err != nil {
			t.Fatalf("bad line %s: %v", line, err)
		}
		return m
	case <-time.After(5 * time.Minute):
		t.Fatal("no message from the engine")
	}
	return proto.Message{}
}

// until returns the response to id, dropping the notifications of job
// id; other messages are held for next.
func (e *rawEngine) until(t *testing.T, id string) proto.Message {
	t.Helper()
	var other []proto.Message
	for {
		m := e.next(t)
		if m.Kind() == proto.KindResponse && m.ID == id {
			e.held = append(other, e.held...)
			return m
		}
		var ref struct {
			Job string `json:"job"`
		}
		if m.DecodeParams(&ref); ref.Job == id {
			continue
		}
		other = append(other, m)
	}
}

func (e *rawEngine) ask(t *testing.T, id, method string, params any) proto.Message {
	t.Helper()
	return e.until(t, e.send(t, id, method, params))
}

func (e *rawEngine) wait() error {
	done := make(chan error, 1)
	go func() { done <- e.cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		e.cmd.Process.Kill()
		return errors.New("did not exit within 10 s")
	}
}
