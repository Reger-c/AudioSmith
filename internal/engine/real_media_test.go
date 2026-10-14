package engine

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine/enginetest"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
)

// tool finds a command in PATH or Homebrew's bin.
func tool(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	if p := "/opt/homebrew/bin/" + name; fileExists(p) {
		return p
	}
	return ""
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// TestRealEngineMedia runs the media and listen workers of the built
// engine: media.probe and media.decode of WAV, AIFF (macOS `say`), M4A
// (afconvert), MP3 and MOV (ffmpeg, when installed), the refusals, and the
// microphone: capture.devices, a short recording when macOS allows this
// process the microphone (over SSH it usually does not: then the refusal
// must be permission_denied). AUDIOSMITH_E2E=1, macOS.
func TestRealEngineMedia(t *testing.T) {
	if os.Getenv("AUDIOSMITH_E2E") == "" {
		t.Skip("set AUDIOSMITH_E2E=1 to run the real engine")
	}
	bin := Find(filepath.Join("..", "..", "resources"))
	if bin == "" {
		t.Fatal("no engine: run scripts/build-engine.sh")
	}
	dir := t.TempDir()
	bus := core.NewBus()
	sup := NewSupervisor(Config{Bin: bin, Bus: bus, Roles: []proto.Role{proto.RoleMedia}, OnDemand: []proto.Role{proto.RoleListen},
		LogDir: dir, StatsEvery: -1, Logf: t.Logf})
	sup.Start(context.Background())
	defer sup.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// The inputs.
	speech := filepath.Join(dir, "speech.aiff")
	if out, err := exec.Command("say", "-o", speech, "This is a reference recording for AudioSmith, about five seconds long.").CombinedOutput(); err != nil {
		t.Fatalf("say: %v %s", err, out)
	}
	wav := filepath.Join(dir, "tone.wav")
	enginetest.WriteWAV(wav, enginetest.Tone(44100, 2, 3, 440, 0.3), 24)
	inputs := map[string]float64{speech: 0, wav: 3}
	m4a := filepath.Join(dir, "speech.m4a")
	if out, err := exec.Command("afconvert", "-f", "m4af", "-d", "aac", speech, m4a).CombinedOutput(); err != nil {
		t.Fatalf("afconvert: %v %s", err, out)
	}
	inputs[m4a] = 0
	if ff := tool("ffmpeg"); ff != "" {
		mp3 := filepath.Join(dir, "speech.mp3")
		mov := filepath.Join(dir, "clip.mov")
		if out, err := exec.Command(ff, "-loglevel", "error", "-i", speech, "-codec:a", "libmp3lame", "-b:a", "128k", mp3).CombinedOutput(); err == nil {
			inputs[mp3] = 0
		} else {
			t.Logf("no mp3: %s", out)
		}
		if out, err := exec.Command(ff, "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=25", "-i", speech,
			"-shortest", "-c:v", "h264_videotoolbox", "-c:a", "aac", mov).CombinedOutput(); err == nil {
			inputs[mov] = 0
		} else {
			t.Logf("no mov: %s", out)
		}
	} else {
		t.Log("ffmpeg not installed: MP3 and MOV are not covered")
	}

	var speechSeconds float64
	for path, want := range inputs {
		info, err := Probe(ctx, sup, path)
		if err != nil {
			t.Errorf("probe %s: %v", filepath.Base(path), err)
			continue
		}
		t.Logf("probe %s: %+v", filepath.Base(path), info)
		if !info.HasAudio || info.SampleRate == 0 || info.Channels == 0 || info.Codec == "" {
			t.Errorf("probe %s: %+v", filepath.Base(path), info)
		}
		if path == speech {
			speechSeconds = info.Duration
		}
		if want > 0 && math.Abs(info.Duration-want) > 0.05 {
			t.Errorf("%s: duration %.3f, want %.3f", filepath.Base(path), info.Duration, want)
		}
		if filepath.Ext(path) == ".mov" && (!info.HasVideo || info.Video == nil || info.Video.Width != 320) {
			t.Errorf("mov: %+v", info)
		}
	}
	// Decode each to 16 kHz mono, and a window to 24 kHz stereo.
	dec := Executor(sup, proto.RoleMedia, proto.MethodMediaDecode)
	for path := range inputs {
		out := filepath.Join(dir, filepath.Base(path)+".wav")
		res, err := runJob(ctx, dec, proto.DecodeParams{Input: path, Output: proto.Output{Path: out, Bits: 16}, SampleRate: 16000, Channels: 1})
		if err != nil {
			t.Errorf("decode %s: %v", filepath.Base(path), err)
			continue
		}
		var r proto.DecodeResult
		decodeInto(t, res, &r)
		a, err := enginetest.ReadWAV(out)
		if err != nil || a.Rate != 16000 || a.Channels != 1 || int64(a.Frames()) != r.Frames || math.Abs(a.Seconds()-r.Source.Duration) > 0.1 {
			t.Errorf("decode %s: %+v, file %d Hz %d ch %.3f s, %v", filepath.Base(path), r, a.Rate, a.Channels, a.Seconds(), err)
		}
	}
	win := filepath.Join(dir, "window.wav")
	res, err := runJob(ctx, dec, proto.DecodeParams{Input: m4a, Output: proto.Output{Path: win, Bits: 32}, SampleRate: 24000, Channels: 2, Start: 1, Duration: 2})
	var r proto.DecodeResult
	if err == nil {
		decodeInto(t, res, &r)
	}
	if a, rerr := enginetest.ReadWAV(win); err != nil || rerr != nil || a.Channels != 2 || !a.Float || math.Abs(a.Seconds()-2) > 0.03 {
		t.Errorf("window: %+v %v %v", r, err, rerr)
	}
	t.Logf("speech %.2f s; window %+v", speechSeconds, r.AudioOut)

	// media.encode (M3): an M4B with chapters, tags and a cover, read back by ffprobe.
	coverPath := filepath.Join(dir, "cover.png")
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for i := range img.Pix {
		img.Pix[i] = byte(i)
	}
	cf, _ := os.Create(coverPath)
	png.Encode(cf, img)
	cf.Close()
	book := filepath.Join(dir, "book.m4b")
	enc := Executor(sup, proto.RoleMedia, proto.MethodMediaEncode)
	res, err = runJob(ctx, enc, proto.EncodeParams{Input: speech, Output: proto.Output{Path: book, Format: "m4b"}, Codec: "aac",
		Chapters: []proto.EncodeChapter{{Title: "Opening", Start: 0, End: 2}, {Title: "第一章 Chapter One", Start: 2, End: speechSeconds}},
		Metadata: proto.EncodeMetadata{Title: "Smoke Book", Author: "A. Writer", Narrator: "Kokoro", Year: "2026", Genre: "Fiction", Description: "A test."},
		Cover:    coverPath})
	var er proto.EncodeResult
	if err == nil {
		decodeInto(t, res, &er)
	}
	t.Logf("encode: %+v %v", er, err)
	if err != nil || er.Bytes == 0 || math.Abs(er.Duration-speechSeconds) > 0.1 || er.Bitrate != 64000 {
		t.Errorf("encode: %+v %v", er, err)
	} else if fp := tool("ffprobe"); fp != "" {
		out, err := exec.Command(fp, "-v", "error", "-show_chapters", "-show_format", "-show_streams", "-of", "json", book).Output()
		var probe struct {
			Chapters []struct {
				Start string            `json:"start_time"`
				Tags  map[string]string `json:"tags"`
			} `json:"chapters"`
			Format struct {
				Duration string            `json:"duration"`
				Tags     map[string]string `json:"tags"`
			} `json:"format"`
			Streams []struct {
				Codec string `json:"codec_name"`
				Type  string `json:"codec_type"`
			} `json:"streams"`
		}
		json.Unmarshal(out, &probe)
		t.Logf("ffprobe: %+v %v", probe, err)
		var codecs []string
		for _, s := range probe.Streams {
			codecs = append(codecs, s.Type+":"+s.Codec)
		}
		if len(probe.Chapters) != 2 || probe.Chapters[1].Tags["title"] != "第一章 Chapter One" || probe.Format.Tags["title"] != "Smoke Book" ||
			probe.Format.Tags["artist"] != "A. Writer" || probe.Format.Tags["composer"] != "Kokoro" || !slices.Contains(codecs, "audio:aac") ||
			!slices.ContainsFunc(codecs, func(c string) bool { return strings.HasPrefix(c, "video:") }) { // the cover shows as an attached picture
			t.Errorf("m4b as ffprobe reads it: chapters %+v, tags %v, streams %v", probe.Chapters, probe.Format.Tags, codecs)
		}
	}
	if _, err := runJob(ctx, enc, proto.EncodeParams{Input: speech, Output: proto.Output{Path: filepath.Join(dir, "b.mp3"), Format: "mp3"}}); !errors.As(err, new(*jobs.Error)) {
		t.Errorf("mp3: %v", err)
	}

	// Refusals.
	var je *jobs.Error
	if _, err := Probe(ctx, sup, filepath.Join(dir, "gone.m4a")); !errors.As(err, &je) || je.Code != proto.CodeFileNotFound {
		t.Errorf("missing: %v", err)
	}
	txt := filepath.Join(dir, "notes.txt")
	os.WriteFile(txt, []byte("not audio"), 0o644)
	if _, err := Probe(ctx, sup, txt); !errors.As(err, &je) || je.Code != proto.CodeUnsupportedMedia {
		t.Errorf("text file: %v", err)
	}

	// The microphone.
	devs, err := Devices(ctx, sup)
	if err != nil {
		t.Fatalf("devices: %v", err)
	}
	t.Logf("inputs: %+v", devs)
	if len(devs) == 0 {
		t.Skip("no audio input on this Mac")
	}
	rec, err := StartCapture(ctx, sup, "", proto.CaptureParams{Output: proto.Output{Path: filepath.Join(dir, "rec.wav")}, MaxSeconds: 1.5}, func(l proto.LevelParams) {})
	switch {
	case errors.As(err, &je) && je.Code == proto.CodePermissionDenied:
		t.Logf("recording refused, as expected without microphone access: %v", err)
	case err != nil:
		t.Fatalf("capture: %v", err)
	default:
		res, err := rec.Result()
		if err != nil || res.Reason != proto.CaptureMaxSeconds || math.Abs(res.AudioSeconds-1.5) > 0.1 {
			t.Errorf("recording: %+v %v", res, err)
		}
		t.Logf("recorded %+v", res)
	}
}

func runJob(ctx context.Context, ex jobs.Executor, params any) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return ex.Run(ctx, jobs.Job{ID: core.NewID(time.Now()), Spec: jobs.Spec{Params: raw}}, func(jobs.Progress) {}, nil)
}

func decodeInto(t *testing.T, raw json.RawMessage, v any) {
	t.Helper()
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatal(err)
	}
}
