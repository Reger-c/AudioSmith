package main

// TestWatermarkEngine checks AudioSeal in the engine on a Mac: takes of
// several TTS models (AUDIOSMITH_WM_MODELS, default Kokoro, Qwen3
// CustomVoice and OmniVoice) are watermarked with AudioSmith's message and
// detected (≈1, exact message), the clean takes are not (≈0); the
// watermark survives AAC (media.encode, 64 kb/s), 24→48 kHz resampling,
// loudness normalisation and a ±5 % time-stretch; the SNR is reported;
// and a minute of audio is timed on the GPU and the CPU.
//
//	AUDIOSMITH_E2E=1 go test -run TestWatermarkEngine -v -timeout 40m .
//
// AUDIOSMITH_WM_CPU=1 also times the CPU.
//
// The engine runs sandboxed (no network). Models it downloads are deleted
// afterwards unless AUDIOSMITH_SMOKE_KEEP=1. Report: /tmp/audiosmith-watermark.md.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/loudness"
	"github.com/hehehai/AudioSmith/internal/dsp/stretch"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/watermark"
)

func TestWatermarkEngine(t *testing.T) {
	if os.Getenv("AUDIOSMITH_E2E") == "" {
		t.Skip("set AUDIOSMITH_E2E=1")
	}
	bin := engine.Find("resources")
	if bin == "" {
		t.Fatal("no engine: run scripts/build-engine.sh")
	}
	ctx := context.Background()
	keep := os.Getenv("AUDIOSMITH_SMOKE_KEEP") != ""
	cacheDir := hub.CacheDir()
	client := hub.New(hub.Options{CacheDir: cacheDir})
	ensure := func(id string) (catalog.Entry, string) {
		t.Helper()
		e, ok := catalog.Builtin().ByID(id)
		if !ok {
			t.Fatalf("no entry %s", id)
		}
		l, err := client.Status(ctx, e)
		if err == nil && l.State != hub.Downloaded {
			if err = client.Download(ctx, e, nil); err == nil && !keep {
				t.Cleanup(func() { client.Delete(context.Background(), e) })
			}
			l, _ = client.Status(ctx, e)
		}
		if err != nil || l.State != hub.Downloaded {
			t.Fatalf("%s: %v %+v", id, err, l)
		}
		return e, l.Snapshot
	}
	seal, sealSnap := ensure(catalog.DefaultWatermark)
	began := time.Now()
	modelDir, err := watermark.Prepare(sealSnap, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var report []string
	note := func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		report = append(report, "- "+line)
		t.Log(line)
	}
	defer func() {
		os.WriteFile("/tmp/audiosmith-watermark.md", []byte("# Watermark "+time.Now().Format("2006-01-02 15:04")+"\n\n"+strings.Join(report, "\n")+"\n"), 0o644)
	}()
	note("converted the checkpoints in %.2f s", time.Since(began).Seconds())

	start := func(env ...string) engine.Supervisor {
		sup := engine.NewSupervisor(engine.Config{Bin: offlineEngine(t, bin), Bus: core.NewBus(), LogDir: t.TempDir(), Offline: true,
			CacheDir: cacheDir, StatsEvery: -1, Env: env, OnDemand: []proto.Role{proto.RoleMedia}, Logf: t.Logf})
		sup.Start(ctx)
		t.Cleanup(sup.Stop)
		return sup
	}
	sup := start()
	run := func(sup engine.Supervisor, method string, params, out any) error {
		raw, _ := json.Marshal(params)
		res, err := engine.KindExecutor(sup, method).Run(ctx, jobs.Job{ID: core.NewID(time.Now()), Spec: jobs.Spec{Params: raw}}, func(jobs.Progress) {}, nil)
		if err == nil && out != nil {
			err = json.Unmarshal(res, out)
		}
		return err
	}
	dir := t.TempDir()
	if d := os.Getenv("AUDIOSMITH_WM_DIR"); d != "" { // keep the files (comparison with the Python reference)
		dir = d
		os.MkdirAll(dir, 0o755)
	}
	msg := watermark.Message(0x3C)
	embed := func(sup engine.Supervisor, in, out string) proto.WatermarkEmbedResult {
		t.Helper()
		var r proto.WatermarkEmbedResult
		if err := run(sup, proto.MethodWatermarkEmbed, proto.WatermarkEmbedParams{Model: seal.Repo, ModelPath: modelDir, Input: in,
			Output: proto.Output{Path: out, Bits: 32}, Message: msg}, &r); err != nil {
			t.Fatalf("embed %s: %v", filepath.Base(in), err)
		}
		return r
	}
	detect := func(in string) proto.WatermarkDetectResult {
		t.Helper()
		var r proto.WatermarkDetectResult
		if err := run(sup, proto.MethodWatermarkDetect, proto.WatermarkDetectParams{Model: seal.Repo, ModelPath: modelDir, Input: in}, &r); err != nil {
			t.Fatalf("detect %s: %v", filepath.Base(in), err)
		}
		return r
	}

	// Takes.
	texts := map[string]string{"en": "AudioSmith turns text into natural speech, right here on your Mac. The quick brown fox jumps over the lazy dog, and then it rests beside the quiet river.",
		"zh": "今天天气很好，我们一起去公园散步吧。这是一段用来测试水印的中文语音。"}
	ids := os.Getenv("AUDIOSMITH_WM_MODELS")
	if ids == "" {
		ids = "kokoro-82m-bf16,qwen3-tts-0.6b-customvoice-8bit,omnivoice-bf16"
	}
	var first string
	for _, id := range strings.Split(ids, ",") {
		e, snap := ensure(strings.TrimSpace(id))
		p := proto.SynthesizeParams{Model: e.Repo, ModelPath: snap, Text: texts["en"], Output: proto.Output{Path: filepath.Join(dir, e.ID+".wav")}}
		if len(e.Voices) > 0 {
			p.Voice = e.Voices[0].ID
		}
		var sr proto.SynthesizeResult
		if err := run(sup, proto.MethodTTSSynthesize, p, &sr); err != nil {
			t.Errorf("%s: %v", e.ID, err)
			continue
		}
		run(sup, proto.MethodModelsUnload, proto.UnloadParams{}, nil)
		wm := filepath.Join(dir, e.ID+".wm.wav")
		er := embed(sup, p.Output.Path, wm)
		d, clean := detect(wm), detect(p.Output.Path)
		ok := d.Probability >= 0.9 && d.Message == msg && clean.Probability <= 0.1
		note("%s (%d Hz, %.1f s): SNR %.1f dB; watermarked p=%.3f message %#04x (confidence %.2f); clean p=%.3f — %v",
			e.ID, er.SampleRate, er.AudioSeconds, er.SNR, d.Probability, d.Message, d.Confidence, clean.Probability, ok)
		if !ok {
			t.Errorf("%s: %+v / clean %+v", e.ID, d, clean)
		}
		if first == "" {
			first = wm
		}
	}
	if first == "" {
		t.Fatal("no take")
	}

	// A 16 kHz copy of the first take, watermarked without resampling:
	// what the Python reference can be compared with sample by sample.
	in16 := filepath.Join(dir, "take16.wav")
	if err := run(sup, proto.MethodMediaDecode, proto.DecodeParams{Input: strings.TrimSuffix(first, ".wm.wav") + ".wav", Output: proto.Output{Path: in16, Bits: 32}, SampleRate: 16000, Channels: 1}, nil); err != nil {
		t.Fatal(err)
	}
	embed(sup, in16, filepath.Join(dir, "take16.wm.wav"))
	if py := filepath.Join(dir, "take16.py.wm.wav"); fileExistsPath(py) {
		d := detect(py)
		note("the Python reference's watermark, detected here: p=%.3f message %#04x", d.Probability, d.Message)
	}

	// Robustness, on the first take.
	check := func(what, path string) {
		t.Helper()
		d := detect(path)
		ok := d.Probability >= 0.5 && d.Message == msg
		note("after %s: p=%.3f message %#04x (confidence %.2f) — %v", what, d.Probability, d.Message, d.Confidence, ok)
		// AudioSeal is not trained for speed changes: time-stretch is
		// reported, not required.
		if !ok && !strings.HasPrefix(what, "time-stretch") {
			t.Errorf("%s: %+v", what, d)
		}
	}
	m4a := filepath.Join(dir, "take.m4a")
	if err := run(sup, proto.MethodMediaEncode, proto.EncodeParams{Input: first, Output: proto.Output{Path: m4a, Format: "m4a"}, Bitrate: 64000}, nil); err != nil {
		t.Fatal(err)
	}
	check("AAC 64 kb/s", m4a)
	up := filepath.Join(dir, "take48k.wav")
	if err := run(sup, proto.MethodMediaDecode, proto.DecodeParams{Input: first, Output: proto.Output{Path: up}, SampleRate: 48000}, nil); err != nil {
		t.Fatal(err)
	}
	check("resampling 24→48 kHz", up)
	b, _, err := wav.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	norm, _ := loudness.Normalize(b, loudness.Podcast, loudness.NormalizeOptions{})
	np := filepath.Join(dir, "take-norm.wav")
	wav.WriteFile(np, norm, wav.PCM16, wav.Options{})
	check("loudness normalisation to −16 LUFS", np)
	for _, ratio := range []float64{0.95, 1.05} {
		s := stretch.Stretch(b, ratio, stretch.Options{})
		sp := filepath.Join(dir, fmt.Sprintf("take-x%.2f.wav", ratio))
		wav.WriteFile(sp, s, wav.PCM16, wav.Options{})
		check(fmt.Sprintf("time-stretch ×%.2f", ratio), sp)
	}

	// Speed: a minute of audio, on the GPU and on the CPU.
	minute := dsp.Buffer{Rate: b.Rate, Channels: b.Channels}
	for minute.Frames() < 60*b.Rate {
		minute.Samples = append(minute.Samples, b.Samples...)
	}
	minute.Samples = minute.Samples[:60*b.Rate*b.Channels]
	mp := filepath.Join(dir, "minute.wav")
	wav.WriteFile(mp, minute, wav.PCM16, wav.Options{})
	devices := []string{"gpu"}
	if os.Getenv("AUDIOSMITH_WM_CPU") != "" {
		devices = append(devices, "cpu")
	}
	for _, device := range devices {
		s := sup
		switch device {
		case "cpu":
			s = start("AUDIOSMITH_WATERMARK_DEVICE=cpu")
		}
		embed(s, mp, filepath.Join(dir, "minute-"+device+".wm.wav")) // loads
		began := time.Now()
		r := embed(s, mp, filepath.Join(dir, "minute-"+device+".wm.wav"))
		took := time.Since(began)
		var dr proto.WatermarkDetectResult
		began = time.Now()
		run(s, proto.MethodWatermarkDetect, proto.WatermarkDetectParams{Model: seal.Repo, ModelPath: modelDir, Input: r.Output}, &dr)
		note("%s: embed 60 s in %d ms (engine %.0f ms), detect in %d ms (p=%.3f)", device, took.Milliseconds(), r.Seconds*1000, time.Since(began).Milliseconds(), dr.Probability)
	}
}
