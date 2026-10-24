package main

// The per-model Mac smoke suite: the gate a catalogue model passes before
// it is listed (unhidden), and after every move of the engine's pins.
//
//	AUDIOSMITH_E2E=1 AUDIOSMITH_SMOKE=all go test -run TestModelSmoke -v -timeout 4h .
//	AUDIOSMITH_E2E=1 AUDIOSMITH_SMOKE=kokoro-82m-bf16,omnivoice-bf16 go test -run TestModelSmoke -v -timeout 1h .
//	scripts/smoke-models.sh [ids]      (the same, building the engine first)
//	AUDIOSMITH_SMOKE_SANDBOX=1 …       (the engine under sandbox-exec, network denied)
//
// For each entry it downloads the model with the app's downloader (the
// system proxy, pinned revisions) into the model folder
// (AUDIOSMITH_SMOKE_CACHE, default the HF cache), starts a fresh gpu worker
// offline, runs the cases its task and capabilities call for, and checks
// the output: TTS takes must be long enough, not silent, and — read back
// by Whisper — say their text; transcripts must match; word times must be
// ordered; VAD must find the speech; denoising must lower the noise. It
// records real-time factor and MLX peak memory per model, writes a report
// (AUDIOSMITH_SMOKE_REPORT, default /tmp/audiosmith-smoke/report.md and
// .json), and deletes what it downloaded unless AUDIOSMITH_SMOKE_KEEP=1.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/engine/enginetest"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/hub"
)

// smokeRow is one case of the report.
type smokeRow struct {
	Model        string  `json:"model"`
	Case         string  `json:"case"`
	Pass         bool    `json:"pass"`
	AudioSeconds float64 `json:"audioSeconds,omitempty"`
	Seconds      float64 `json:"seconds,omitempty"`
	RTF          float64 `json:"rtf,omitempty"`
	LoadSeconds  float64 `json:"loadSeconds,omitempty"`
	PeakGB       float64 `json:"peakGB,omitempty"` // MLX peak of the worker after the model's cases
	RSSGB        float64 `json:"rssGB,omitempty"`  // largest resident size seen
	Heard        string  `json:"heard,omitempty"`  // what Whisper or the model heard
	Score        float64 `json:"score,omitempty"`  // text similarity, 0–1
	Note         string  `json:"note,omitempty"`
	Output       string  `json:"output,omitempty"`
}

const (
	smokeRefText = "The quick brown fox jumps over the lazy dog, and then it rests beside the quiet river for a while."
	smokeEN      = "AudioSmith turns text into natural speech, right here on your Mac."
	smokeZH      = "今天天气很好，我们一起去公园散步吧。"
	smokeDesign  = "A warm, calm middle-aged female narrator with a clear voice."
)

func TestModelSmoke(t *testing.T) {
	ids := os.Getenv("AUDIOSMITH_SMOKE")
	if os.Getenv("AUDIOSMITH_E2E") == "" || ids == "" {
		t.Skip("set AUDIOSMITH_E2E=1 and AUDIOSMITH_SMOKE=all or a list of catalogue ids")
	}
	bin := engine.Find("resources")
	if bin == "" {
		t.Fatal("no engine: run scripts/build-engine.sh")
	}
	if os.Getenv("AUDIOSMITH_SMOKE_SANDBOX") != "" {
		bin = offlineEngine(t, bin)
	}
	cat := catalog.Builtin()
	var entries []catalog.Entry
	if ids == "all" {
		entries = cat.Entries
	} else {
		for _, id := range strings.Split(ids, ",") {
			e, ok := cat.ByID(strings.TrimSpace(id))
			if !ok {
				t.Fatalf("no catalogue entry %q", id)
			}
			entries = append(entries, e)
		}
	}
	cacheDir := os.Getenv("AUDIOSMITH_SMOKE_CACHE")
	if cacheDir == "" {
		cacheDir = hub.CacheDir()
	}
	outDir := "/tmp/audiosmith-smoke"
	report := os.Getenv("AUDIOSMITH_SMOKE_REPORT")
	if report == "" {
		report = filepath.Join(outDir, "report.md")
	}
	outDir = filepath.Dir(report)
	os.MkdirAll(outDir, 0o755)
	keep := os.Getenv("AUDIOSMITH_SMOKE_KEEP") != ""

	ctx := context.Background()
	client := hub.New(hub.Options{CacheDir: cacheDir})
	t.Logf("models in %s, downloads through %q", cacheDir, hub.DescribeProxy(""))
	var downloaded []catalog.Entry
	defer func() {
		if keep {
			return
		}
		for _, e := range downloaded {
			if err := client.Delete(ctx, e); err != nil {
				t.Logf("delete %s: %v", e.ID, err)
			} else {
				t.Logf("deleted %s", e.ID)
			}
		}
	}()
	ensure := func(t *testing.T, e catalog.Entry) string {
		t.Helper()
		l, err := client.Status(ctx, e)
		if err != nil {
			t.Fatalf("%s: status: %v", e.ID, err)
		}
		if l.State != hub.Downloaded {
			start := time.Now()
			last := time.Now()
			err := client.Download(ctx, e, func(p hub.Progress) {
				if time.Since(last) > 15*time.Second {
					last = time.Now()
					t.Logf("%s: %d / %d MB", e.ID, p.Bytes>>20, p.TotalBytes>>20)
				}
			})
			if err != nil {
				t.Fatalf("%s: download: %v", e.ID, err)
			}
			downloaded = append(downloaded, e)
			if l, err = client.Status(ctx, e); err != nil || l.State != hub.Downloaded {
				t.Fatalf("%s: after download: %+v %v", e.ID, l, err)
			}
			t.Logf("%s: downloaded %d MB in %.0f s", e.ID, l.Bytes>>20, time.Since(start).Seconds())
		}
		return l.Snapshot
	}

	bus := core.NewBus()
	sup := engine.NewSupervisor(engine.Config{Bin: bin, Bus: bus, LogDir: outDir, Offline: true, CacheDir: cacheDir,
		OnDemand: []proto.Role{proto.RoleMedia}, StatsEvery: -1, Logf: t.Logf,
		Log: func(e core.LogEntry) {
			if e.Level == core.SeverityWarning || e.Level == core.SeverityError {
				t.Logf("engine %s: %s", e.Level, e.Message)
			}
		}})
	sup.Start(ctx)
	defer sup.Stop()
	waitReady := func() {
		t.Helper()
		deadline := time.Now().Add(2 * time.Minute)
		for time.Now().Before(deadline) {
			for _, st := range sup.Status() {
				if st.Role == proto.RoleGPU && st.State == engine.WorkerReady {
					return
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("engine not ready: %+v", sup.Status())
	}
	waitReady()
	call := func(method string, params, result any) error {
		chunks, firstChunk := 0, time.Duration(0)
		start := time.Now()
		raw, err := sup.Call(ctx, proto.RoleOf(method), core.NewID(time.Now()), method, params, func(m proto.Message) {
			if m.Method == proto.MethodTTSChunk {
				if chunks == 0 {
					firstChunk = time.Since(start)
				}
				chunks++
			}
		})
		lastChunks, lastFirstChunk = chunks, firstChunk
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, result)
	}

	// The reference speaker: Kokoro reads smokeRefText.
	kokoro, _ := cat.ByID("kokoro-82m-bf16")
	kokoroPath := ensure(t, kokoro)
	ref := filepath.Join(outDir, "reference.wav")
	var refRes proto.SynthesizeResult
	if err := call(proto.MethodTTSSynthesize, proto.SynthesizeParams{Model: kokoro.Repo, ModelPath: kokoroPath, Text: smokeRefText,
		Voice: "af_heart", Output: proto.Output{Path: ref}}, &refRes); err != nil {
		t.Fatalf("reference: %v", err)
	}
	// Speech framed by a second of silence, and a noisy copy.
	refAudio, err := enginetest.ReadWAV(ref)
	if err != nil {
		t.Fatal(err)
	}
	padded := filepath.Join(outDir, "reference-padded.wav")
	pad := make([]float64, refAudio.Rate)
	enginetest.WriteWAV(padded, enginetest.Audio{Rate: refAudio.Rate, Channels: 1, Samples: slices.Concat(pad, refAudio.Samples, pad)}, 16)
	noisy := filepath.Join(outDir, "reference-noisy.wav")
	rng := rand.New(rand.NewPCG(1, 2))
	ns := slices.Concat(pad, refAudio.Samples, pad)
	for i := range ns {
		ns[i] += 0.03 * rng.NormFloat64()
	}
	enginetest.WriteWAV(noisy, enginetest.Audio{Rate: refAudio.Rate, Channels: 1, Samples: ns}, 16)

	// Whisper hears the takes, when it is in the run.
	whisper, _ := cat.ByID(catalog.DefaultSTT)
	hearing := ids == "all" || strings.Contains(ids, whisper.ID) || os.Getenv("AUDIOSMITH_SMOKE_HEAR") != ""
	var whisperPath string
	if hearing {
		whisperPath = ensure(t, whisper)
	}
	hear := func(path, lang string) (string, error) {
		if whisperPath == "" {
			return "", nil
		}
		var tr proto.TranscribeResult
		err := call(proto.MethodSTTTranscribe, proto.TranscribeParams{Model: whisper.Repo, ModelPath: whisperPath, Audio: path, Language: lang}, &tr)
		return tr.Text, err
	}

	// Two speakers over music, for separation and diarization.
	clip := sync.OnceValues(func() (smokeClip, error) { return makeSmokeClip(outDir, ref, call, kokoro, kokoroPath) })

	var rows []smokeRow
	defer func() { writeSmokeReport(t, report, rows) }()

	for _, e := range entries {
		t.Run(e.ID, func(t *testing.T) {
			snap := ensure(t, e)
			if err := sup.Restart(proto.RoleGPU); err != nil {
				t.Fatal(err)
			}
			waitReady()
			var pid int
			for _, st := range sup.Status() {
				if st.Role == proto.RoleGPU {
					pid = st.PID
				}
			}
			stopRSS := watchRSS(pid)
			var modelRows []smokeRow
			add := func(r smokeRow) {
				r.Model = e.ID
				if !r.Pass {
					t.Errorf("%s %s: %s", e.ID, r.Case, r.Note)
				} else {
					t.Logf("%s %s: ok %.2f s audio in %.2f s (RTF %.2f) %s %s", e.ID, r.Case, r.AudioSeconds, r.Seconds, r.RTF, r.Heard, r.Note)
				}
				modelRows = append(modelRows, r)
			}
			// Load first, to time it apart.
			var lr proto.LoadResult
			start := time.Now()
			task := "tts"
			if len(e.Tasks) > 0 {
				task = string(e.Tasks[0])
			}
			if task == "tts" {
				if err := call(proto.MethodModelsLoad, proto.LoadParams{Model: e.Repo, Path: snap, Task: task}, &lr); err != nil {
					add(smokeRow{Case: "load", Note: err.Error()})
					stopRSS()
					rows = append(rows, modelRows...)
					return
				}
				add(smokeRow{Case: "load", Pass: true, LoadSeconds: time.Since(start).Seconds(), Note: "capabilities " + strings.Join(lr.Capabilities, ",")})
			}

			switch task {
			case "tts":
				smokeTTS(t, e, snap, ref, refRes.AudioSeconds, outDir, call, hear, add, func() float64 {
					var st proto.StatsResult
					if call(proto.MethodStats, nil, &st) != nil {
						return 0
					}
					return float64(st.Memory.Peak) / (1 << 30)
				})
			case "stt":
				smokeSTT(e, snap, ref, outDir, call, add, kokoro, kokoroPath)
			case "align":
				var ar proto.AlignResult
				err := call(proto.MethodSTTAlign, proto.AlignParams{Model: e.Repo, ModelPath: snap, Audio: padded, Text: smokeRefText, Language: "en"}, &ar)
				r := smokeRow{Case: "align-en", Seconds: ar.Seconds, AudioSeconds: ar.AudioSeconds}
				switch {
				case err != nil:
					r.Note = err.Error()
				case len(ar.Words) != len(strings.Fields(smokeRefText)):
					r.Note = fmt.Sprintf("%d words, want %d", len(ar.Words), len(strings.Fields(smokeRefText)))
				case !ordered(ar.Words) || ar.Words[0].Start < 0.7 || ar.Words[len(ar.Words)-1].End > ar.AudioSeconds-0.5:
					r.Note = fmt.Sprintf("times %v … %v of %.2f s", ar.Words[0], ar.Words[len(ar.Words)-1], ar.AudioSeconds)
				default:
					r.Pass = true
					r.RTF = ar.AudioSeconds / max(ar.Seconds, 1e-3)
					r.Note = fmt.Sprintf("first %s@%.2f, last %s@%.2f", ar.Words[0].Text, ar.Words[0].Start, ar.Words[len(ar.Words)-1].Text, ar.Words[len(ar.Words)-1].End)
				}
				add(r)
				// Chinese: per character.
				zh := filepath.Join(outDir, e.ID+"-zh-src.wav")
				var zr proto.SynthesizeResult
				if err := call(proto.MethodTTSSynthesize, proto.SynthesizeParams{Model: kokoro.Repo, ModelPath: kokoroPath, Text: smokeZH, Voice: "zf_xiaobei", Output: proto.Output{Path: zh}}, &zr); err == nil {
					err = call(proto.MethodSTTAlign, proto.AlignParams{Model: e.Repo, ModelPath: snap, Audio: zh, Text: smokeZH, Language: "zh"}, &ar)
					r := smokeRow{Case: "align-zh"}
					hanzi := len([]rune(strings.Map(func(r rune) rune {
						if unicode.Is(unicode.Han, r) {
							return r
						}
						return -1
					}, smokeZH)))
					switch {
					case err != nil:
						r.Note = err.Error()
					case len(ar.Words) != hanzi || !ordered(ar.Words):
						r.Note = fmt.Sprintf("%d units for %d characters", len(ar.Words), hanzi)
					default:
						r.Pass = true
						r.Note = fmt.Sprintf("%d characters timed", hanzi)
					}
					add(r)
				}
			case "vad":
				var vr proto.VADResult
				err := call(proto.MethodVADDetect, proto.VADParams{Model: e.Repo, ModelPath: snap, Audio: padded}, &vr)
				r := smokeRow{Case: "vad", Seconds: vr.Seconds, AudioSeconds: vr.AudioSeconds}
				switch {
				case err != nil:
					r.Note = err.Error()
				case len(vr.Regions) == 0 || vr.Regions[0].Start < 0.7 || vr.Regions[0].Start > 1.4 || vr.Regions[len(vr.Regions)-1].End > vr.AudioSeconds-0.6:
					r.Note = fmt.Sprintf("regions %v of %.2f s", vr.Regions, vr.AudioSeconds)
				default:
					r.Pass = true
					r.RTF = vr.AudioSeconds / max(vr.Seconds, 1e-3)
					r.Note = fmt.Sprintf("%d regions, %.2f–%.2f s", len(vr.Regions), vr.Regions[0].Start, vr.Regions[len(vr.Regions)-1].End)
				}
				add(r)
			case "llm":
				smokeLLM(e, snap, call, add)
			case "watermark":
				smokeWatermark(e, snap, ref, outDir, call, add)
			case "separate":
				smokeSeparate(e, snap, outDir, clip, call, add)
			case "diarize":
				smokeDiarize(e, snap, clip, call, add)
			case "enhance":
				out := filepath.Join(outDir, e.ID+"-clean.wav")
				var er proto.EnhanceResult
				err := call(proto.MethodEnhanceRun, proto.EnhanceParams{Model: e.Repo, ModelPath: snap, Audio: noisy, Output: proto.Output{Path: out}}, &er)
				r := smokeRow{Case: "enhance", Seconds: er.Seconds, AudioSeconds: er.AudioSeconds, Output: out}
				if err != nil {
					r.Note = err.Error()
				} else if a, err := enginetest.ReadWAV(out); err != nil {
					r.Note = err.Error()
				} else {
					before := dbfs(ns[:refAudio.Rate*8/10])
					after := dbfs(a.Mono()[:a.Rate*8/10])
					r.RTF = er.AudioSeconds / max(er.Seconds, 1e-3)
					r.Note = fmt.Sprintf("noise %.1f → %.1f dBFS, %d Hz", before, after, a.Rate)
					r.Pass = after < before-10 && math.Abs(a.Seconds()-float64(len(ns))/float64(refAudio.Rate)) < 0.05
				}
				add(r)
			}

			var st proto.StatsResult
			if call(proto.MethodStats, nil, &st) == nil {
				for i := range modelRows {
					if modelRows[i].PeakGB == 0 {
						modelRows[i].PeakGB = float64(st.Memory.Peak) / (1 << 30)
					}
				}
			}
			rss := stopRSS()
			for i := range modelRows {
				modelRows[i].RSSGB = float64(rss) / (1 << 30)
			}
			rows = append(rows, modelRows...)
			var unloaded proto.UnloadResult
			call(proto.MethodModelsUnload, proto.UnloadParams{}, &unloaded)
			if !keep && slices.ContainsFunc(downloaded, func(d catalog.Entry) bool { return d.ID == e.ID }) && e.ID != whisper.ID && e.ID != kokoro.ID {
				if err := client.Delete(ctx, e); err == nil {
					downloaded = slices.DeleteFunc(downloaded, func(d catalog.Entry) bool { return d.ID == e.ID })
					t.Logf("deleted %s", e.ID)
				}
			}
		})
	}
}
