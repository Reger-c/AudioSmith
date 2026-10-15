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
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
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

// smokeTTS runs the cases of a TTS model.
// lastChunks and lastFirstChunk describe the tts.chunk notifications of
// the last call: how many, and when the first came.
var (
	lastChunks     int
	lastFirstChunk time.Duration
)

func smokeTTS(t *testing.T, e catalog.Entry, snap, ref string, refSeconds float64, outDir string,
	call func(string, any, any) error, hear func(string, string) (string, error), add func(smokeRow), peakGB func() float64) {
	langOf := func(code string) (string, bool) {
		for _, l := range e.Languages {
			if l.Code == code || strings.HasPrefix(l.Code, code+"-") {
				if l.Value != "" {
					return l.Value, true
				}
				return l.Code, true
			}
		}
		return "", len(e.Languages) == 0 && code == "en"
	}
	type tcase struct {
		name, text, lang string
		mod              func(*proto.SynthesizeParams)
		minSeconds       float64
		// pitch checks the median F0 of the take: > 0 at least, < 0 at most.
		pitch float64
		// minScore is the Whisper similarity a pass needs (default 0.6).
		minScore float64
	}
	var cases []tcase
	voiceFor := func(lang string) string {
		for _, v := range e.Voices {
			if strings.HasPrefix(v.Language, lang) || v.Language == "" {
				return v.ID
			}
		}
		if len(e.Voices) > 0 {
			return e.Voices[0].ID
		}
		return ""
	}
	base := func(lang string) func(*proto.SynthesizeParams) {
		return func(p *proto.SynthesizeParams) {
			p.Voice = voiceFor(lang)
			if e.Has(catalog.CapDesign) && !e.Has(catalog.CapClone) || e.Family == "qwen3_tts" && strings.Contains(strings.ToLower(e.Repo), "voicedesign") {
				p.Voice = smokeDesign
			}
			if e.Has(catalog.CapLanguage) {
				p.Language, _ = langOf(lang)
			}
			if e.Has(catalog.CapClone) && len(e.Voices) == 0 && !e.Has(catalog.CapDesign) {
				// Clone-only models need a reference.
				p.RefAudio, p.RefText = ref, smokeRefText
			}
		}
	}
	if _, ok := langOf("en"); ok {
		cases = append(cases, tcase{name: "en", text: smokeEN, lang: "en", mod: func(p *proto.SynthesizeParams) {
			base("en")(p)
			// AUDIOSMITH_SMOKE_STREAM=1 asks every model to stream, to see
			// which do (the note); only "stream" models must.
			p.Stream = e.Has(catalog.CapStream) || os.Getenv("AUDIOSMITH_SMOKE_STREAM") != ""
		}, minSeconds: 1.5})
	}
	if _, ok := langOf("zh"); ok {
		cases = append(cases, tcase{name: "zh", text: smokeZH, lang: "zh", mod: base("zh"), minSeconds: 1.5})
	}
	if e.Has(catalog.CapClone) {
		cases = append(cases, tcase{name: "clone", text: smokeEN, lang: "en", mod: func(p *proto.SynthesizeParams) {
			base("en")(p)
			p.Voice = ""
			p.RefAudio, p.RefText = ref, smokeRefText
		}, minSeconds: 1.5})
	}
	if e.Has(catalog.CapDuration) {
		cases = append(cases, tcase{name: "duration", text: smokeEN, lang: "en", mod: func(p *proto.SynthesizeParams) {
			base("en")(p)
			d := 4.0
			p.Duration = &d
		}, minSeconds: 3.8})
	}
	// Voice design and instruction tags (OmniVoice, CustomVoice): gender
	// and pitch are checked on the take's F0; dialects, whisper and
	// non-verbal tags are made for listening (samples in the report dir).
	design := func(voice, instruct string) func(*proto.SynthesizeParams) {
		return func(p *proto.SynthesizeParams) {
			p.Voice, p.Instruct = voice, instruct
			if e.Has(catalog.CapLanguage) {
				p.Language = ""
			}
		}
	}
	if e.Family == "omnivoice" {
		cases = append(cases,
			tcase{name: "design-en-female", text: smokeEN, lang: "en", mod: design("female, young adult, high pitch, british accent", ""), minSeconds: 1.5, pitch: 165},
			tcase{name: "design-en-male", text: smokeEN, lang: "en", mod: design("male, elderly, low pitch", ""), minSeconds: 1.5, pitch: -175},
			tcase{name: "design-zh-male", text: smokeZH, lang: "zh", mod: design("男，老年，低音调", ""), minSeconds: 1.5, pitch: -175},
			tcase{name: "design-zh-female-en-tags", text: smokeZH, lang: "zh", mod: design("female, young adult, high pitch", ""), minSeconds: 1.5, pitch: 165},
			tcase{name: "design-whisper", text: smokeEN, lang: "en", mod: design("female, whisper", ""), minSeconds: 1.5, minScore: 0.3},
			tcase{name: "nonverbal-laughter", text: "[laughter] You really got me. I didn't see that coming at all.", lang: "en", mod: design("", ""), minSeconds: 1.5, minScore: 0.4},
		)
		for _, d := range []string{"河南话", "陕西话", "四川话", "贵州话", "云南话", "桂林话", "济南话", "石家庄话", "甘肃话", "宁夏话", "青岛话", "东北话"} {
			cases = append(cases, tcase{name: "dialect-" + d, text: smokeZH, lang: "zh", mod: design("男，中年，"+d, ""), minSeconds: 1.5, minScore: 0.2})
		}
	}
	if e.Family == "kokoro" {
		// Calibrates the pitch check: a male and a female preset voice.
		cases = append(cases,
			tcase{name: "pitch-male", text: smokeEN, lang: "en", mod: func(p *proto.SynthesizeParams) { p.Voice = "am_adam" }, minSeconds: 1.5, pitch: -150},
			tcase{name: "pitch-female", text: smokeEN, lang: "en", mod: func(p *proto.SynthesizeParams) { p.Voice = "af_heart" }, minSeconds: 1.5, pitch: 165})
	}
	if strings.Contains(strings.ToLower(e.Repo), "customvoice") {
		cases = append(cases,
			tcase{name: "instruct-zh", text: smokeZH, lang: "zh", mod: func(p *proto.SynthesizeParams) {
				p.Voice, p.Instruct, p.Language = "Vivian", "用非常开心、兴奋的语气说", "Chinese"
			}, minSeconds: 1.5},
			tcase{name: "instruct-zh-sad", text: smokeZH, lang: "zh", mod: func(p *proto.SynthesizeParams) {
				p.Voice, p.Instruct, p.Language = "Vivian", "用悲伤、低沉的语气慢慢地说", "Chinese"
			}, minSeconds: 1.5},
			tcase{name: "instruct-en", text: smokeEN, lang: "en", mod: func(p *proto.SynthesizeParams) {
				p.Voice, p.Instruct, p.Language = "Ryan", "Very excited and fast", "English"
			}, minSeconds: 1.5},
		)
	}
	if e.Has(catalog.CapVoicePrompt) {
		prompt := filepath.Join(outDir, e.ID+".voiceprompt")
		var enc proto.EncodeVoiceResult
		err := call(proto.MethodTTSEncodeVoice, proto.EncodeVoiceParams{Model: e.Repo, ModelPath: snap, RefAudio: ref, RefText: smokeRefText,
			Language: "English", PromptPath: prompt}, &enc)
		r := smokeRow{Case: "encodeVoice", Seconds: enc.Seconds}
		if err != nil {
			r.Note = err.Error()
		} else {
			r.Pass, r.Note = true, fmt.Sprintf("%s, %d KB", enc.Format, enc.Bytes>>10)
			cases = append(cases, tcase{name: "voicePrompt", text: smokeEN, lang: "en", mod: func(p *proto.SynthesizeParams) {
				base("en")(p)
				p.Voice, p.VoicePrompt = "", prompt
			}, minSeconds: 1.5})
		}
		add(r)
	}
	// Models with a hard per-request limit: find the longest English and
	// Chinese text they take, in characters and in catalog.TextLength.
	if e.Family == "kokoro" {
		for _, lang := range []string{"en", "zh"} {
			if _, ok := langOf(lang); !ok {
				continue
			}
			unit := "The engine reads this sentence aloud, again and again, to find how long a request may be. "
			if lang == "zh" {
				unit = "引擎会一遍又一遍地朗读这句话，用来测试一次请求最多能有多长。"
			}
			long := []rune(strings.Repeat(unit, 40))
			try := func(n int) error {
				p := proto.SynthesizeParams{Model: e.Repo, ModelPath: snap, Text: string(long[:n]), Output: proto.Output{Path: filepath.Join(outDir, e.ID+"-limit.wav")}}
				base(lang)(&p)
				var res proto.SynthesizeResult
				return call(proto.MethodTTSSynthesize, p, &res)
			}
			lo, hi := 20, len(long)
			var tooLong error
			for hi-lo > 4 {
				mid := (lo + hi) / 2
				if err := try(mid); err == nil {
					lo = mid
				} else {
					hi, tooLong = mid, err
				}
			}
			r := smokeRow{Case: "limit-" + lang, Pass: true,
				Note: fmt.Sprintf("longest accepted %d characters (TextLength %d)", lo, catalog.TextLength(string(long[:lo])))}
			if tooLong != nil {
				var pe *proto.Error
				if !errors.As(tooLong, &pe) || pe.Code != proto.CodeInputTooLong {
					r.Pass = false
					r.Note += "; over it: " + tooLong.Error() + " (want input_too_long)"
				} else {
					r.Note += "; over it: " + pe.Message
				}
			}
			if e.MaxChars > 0 && catalog.TextLength(string(long[:lo])) < e.MaxChars {
				r.Pass = false
				r.Note += fmt.Sprintf("; catalogue maxChars %d is too high", e.MaxChars)
			}
			add(r)
		}
	}

	type heard struct {
		r smokeRow
		c tcase
	}
	var pending []heard
	for _, c := range cases {
		out := filepath.Join(outDir, e.ID+"-"+c.name+".wav")
		p := proto.SynthesizeParams{Model: e.Repo, ModelPath: snap, Text: c.text, Output: proto.Output{Path: out}}
		seed := int64(7)
		p.Seed = &seed
		c.mod(&p)
		var res proto.SynthesizeResult
		err := call(proto.MethodTTSSynthesize, p, &res)
		r := smokeRow{Case: c.name, AudioSeconds: res.AudioSeconds, Seconds: res.Seconds, RTF: res.RTF, Output: out}
		if err != nil {
			r.Note = err.Error()
			add(r)
			continue
		}
		a, err := enginetest.ReadWAV(out)
		switch {
		case err != nil:
			r.Note = err.Error()
		case res.AudioSeconds < c.minSeconds || res.AudioSeconds > 30:
			r.Note = fmt.Sprintf("%.2f s of audio", res.AudioSeconds)
		case p.Stream && e.Has(catalog.CapStream) && (lastChunks < 2 || lastFirstChunk.Seconds() > 0.7*res.Seconds+0.3):
			// Streaming means the first audio comes well before the take
			// ends. Only the cases that ask for it (en) are held to it.
			r.Note = fmt.Sprintf("not streaming: %d chunks, the first after %.2f s of %.2f s", lastChunks, lastFirstChunk.Seconds(), res.Seconds)
		case c.name == "duration" && math.Abs(res.AudioSeconds-4) > 0.25:
			r.Note = fmt.Sprintf("duration 4 gave %.2f s", res.AudioSeconds)
		case dbfs(a.Mono()) < -45 || hasNaN(a.Samples):
			r.Note = fmt.Sprintf("silent or broken: %.1f dBFS", dbfs(a.Mono()))
		default:
			r.Pass = true
			r.Note = fmt.Sprintf("%.1f dBFS", dbfs(a.Mono()))
			if p.Stream {
				r.Note += fmt.Sprintf(", %d chunks, first after %.2f s", lastChunks, lastFirstChunk.Seconds())
			}
		}
		if r.Pass && c.pitch != 0 {
			f0 := medianF0(a.Mono(), a.Rate)
			r.Note += fmt.Sprintf(", F0 %.0f Hz", f0)
			if c.pitch > 0 && f0 < c.pitch || c.pitch < 0 && f0 > -c.pitch {
				r.Pass = false
				r.Note += fmt.Sprintf(" (want %s %.0f)", map[bool]string{true: "≥", false: "≤"}[c.pitch > 0], math.Abs(c.pitch))
			}
		}
		pending = append(pending, heard{r, c})
	}
	// Whisper hears the takes after the model's memory peak is read, so
	// that it does not count.
	peak := peakGB()
	for _, h := range pending {
		r, c := h.r, h.c
		r.PeakGB = peak
		if r.Pass {
			heard, err := hear(r.Output, c.lang)
			minScore := c.minScore
			if minScore == 0 {
				minScore = 0.6
			}
			if err != nil {
				r.Note += "; whisper: " + err.Error()
			} else if heard != "" {
				r.Heard = heard
				r.Score = similarity(strings.TrimPrefix(c.text, "[laughter] "), heard, c.lang == "zh")
				if r.Score < minScore {
					r.Pass = false
					r.Note += fmt.Sprintf("; Whisper heard %q (%.2f)", heard, r.Score)
				}
			}
		}
		add(r)
	}
	_ = refSeconds
}

// smokeSTT transcribes the reference (English) and a Kokoro take (Chinese).
func smokeSTT(e catalog.Entry, snap, ref, outDir string, call func(string, any, any) error, add func(smokeRow), kokoro catalog.Entry, kokoroPath string) {
	check := func(name, audio, want, lang, ask string) {
		var tr proto.TranscribeResult
		err := call(proto.MethodSTTTranscribe, proto.TranscribeParams{Model: e.Repo, ModelPath: snap, Audio: audio, Language: ask}, &tr)
		r := smokeRow{Case: name, Seconds: tr.Seconds, AudioSeconds: tr.AudioSeconds, RTF: tr.RTF, Heard: tr.Text}
		if err != nil {
			r.Note = err.Error()
		} else {
			r.Score = similarity(want, tr.Text, lang == "zh")
			r.Note = fmt.Sprintf("language %s, %d segments", tr.Language, len(tr.Segments))
			r.Pass = r.Score >= 0.7 && (ask != "auto" || strings.HasPrefix(tr.Language, lang))
		}
		add(r)
	}
	check("en-auto", ref, smokeRefText, "en", "auto")
	zh := filepath.Join(outDir, e.ID+"-zh-src.wav")
	var zr proto.SynthesizeResult
	if err := call(proto.MethodTTSSynthesize, proto.SynthesizeParams{Model: kokoro.Repo, ModelPath: kokoroPath, Text: smokeZH, Voice: "zf_xiaobei", Output: proto.Output{Path: zh}}, &zr); err != nil {
		add(smokeRow{Case: "zh", Note: "making the Chinese clip: " + err.Error()})
		return
	}
	check("zh", zh, smokeZH, "zh", "zh")
	if e.Has(catalog.CapTranslate) {
		var tr proto.TranscribeResult
		err := call(proto.MethodSTTTranscribe, proto.TranscribeParams{Model: e.Repo, ModelPath: snap, Audio: zh, Task: "translate"}, &tr)
		r := smokeRow{Case: "translate", Heard: tr.Text, Seconds: tr.Seconds}
		if err != nil {
			r.Note = err.Error()
		} else {
			r.Pass = tr.Text != "" && !hasHan(tr.Text)
			r.Note = "zh → en"
		}
		add(r)
	}
}

// medianF0 estimates the median pitch of the voiced 40 ms frames
// (normalised autocorrelation, 60–400 Hz), 0 when nothing is voiced.
func medianF0(x []float64, rate int) float64 {
	frame, hop := rate*40/1000, rate*10/1000
	minLag, maxLag := rate/400, rate/60
	var f0s []float64
	for start := 0; start+frame+maxLag < len(x); start += hop {
		w := x[start : start+frame]
		if dbfs(w) < -40 {
			continue
		}
		best, bestLag := 0.0, 0
		for lag := minLag; lag <= maxLag; lag++ {
			var num, e1, e2 float64
			for i := range frame {
				a, b := x[start+i], x[start+i+lag]
				num += a * b
				e1 += a * a
				e2 += b * b
			}
			if r := num / math.Sqrt(e1*e2+1e-12); r > best {
				best, bestLag = r, lag
			}
		}
		if best > 0.6 && bestLag > 0 {
			f0s = append(f0s, float64(rate)/float64(bestLag))
		}
	}
	if len(f0s) == 0 {
		return 0
	}
	slices.Sort(f0s)
	return f0s[len(f0s)/2]
}

func ordered(ws []proto.Word) bool {
	for i, w := range ws {
		if w.End < w.Start || i > 0 && w.Start < ws[i-1].Start {
			return false
		}
	}
	return len(ws) > 0
}

func hasNaN(s []float64) bool { return slices.ContainsFunc(s, math.IsNaN) }

func hasHan(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0
}

func dbfs(s []float64) float64 {
	if len(s) == 0 {
		return -120
	}
	var sum float64
	for _, v := range s {
		sum += v * v
	}
	rms := math.Sqrt(sum / float64(len(s)))
	if rms < 1e-6 {
		return -120
	}
	return 20 * math.Log10(rms)
}

// similarity compares texts as words (or characters for Chinese): the
// longest common subsequence over the longer length.
func similarity(want, got string, chars bool) float64 {
	norm := func(s string) []string {
		s = strings.ToLower(s)
		var out []string
		if chars {
			for _, r := range s {
				if unicode.IsLetter(r) || unicode.IsNumber(r) {
					out = append(out, string(r))
				}
			}
			return out
		}
		for _, w := range strings.Fields(s) {
			if w = strings.TrimFunc(w, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }); w != "" {
				out = append(out, w)
			}
		}
		return out
	}
	a, b := norm(want), norm(got)
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else {
				dp[i][j] = max(dp[i-1][j], dp[i][j-1])
			}
		}
	}
	return float64(dp[len(a)][len(b)]) / float64(max(len(a), len(b)))
}

// watchRSS samples a process's resident size every 250 ms until stop,
// which returns the largest, in bytes.
func watchRSS(pid int) (stop func() int64) {
	done := make(chan struct{})
	result := make(chan int64, 1)
	go func() {
		var peak int64
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		for {
			if out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output(); err == nil {
				if kb, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); err == nil {
					peak = max(peak, kb<<10)
				}
			}
			select {
			case <-done:
				result <- peak
				return
			case <-tick.C:
			}
		}
	}()
	var got int64 = -1
	return func() int64 {
		if got < 0 {
			close(done)
			got = <-result
		}
		return got
	}
}

func writeSmokeReport(t *testing.T, path string, rows []smokeRow) {
	if len(rows) == 0 {
		return
	}
	data, _ := json.MarshalIndent(rows, "", "  ")
	os.WriteFile(strings.TrimSuffix(path, filepath.Ext(path))+".json", data, 0o644)
	var b strings.Builder
	fmt.Fprintf(&b, "# Model smoke %s\n\n| Model | Case | Result | Audio s | Gen s | RTF | MLX peak GB | RSS GB | Heard / note |\n|---|---|---|---|---|---|---|---|---|\n",
		time.Now().Format("2006-01-02 15:04"))
	for _, r := range rows {
		res := "pass"
		if !r.Pass {
			res = "**FAIL**"
		}
		note := r.Note
		if r.Heard != "" {
			note = fmt.Sprintf("%q (%.2f); %s", r.Heard, r.Score, r.Note)
		}
		if r.LoadSeconds > 0 {
			note = fmt.Sprintf("load %.1f s; %s", r.LoadSeconds, note)
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %.2f | %.2f | %.2f | %.2f | %.2f | %s |\n", r.Model, r.Case, res, r.AudioSeconds, r.Seconds, r.RTF, r.PeakGB, r.RSSGB,
			strings.ReplaceAll(note, "|", "/"))
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Log(err)
	} else {
		t.Logf("report: %s", path)
	}
}
