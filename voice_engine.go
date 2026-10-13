package main

// What the voice library asks of the engine: decoding any audio or video
// to a WAV, finding speech, transcribing, removing noise, recording from
// a microphone and caching a voice prompt. Thread E's protocol (media.*,
// vad.detect, stt.transcribe, enhance.run, capture.*, tts.encodeVoice)
// will provide them; until it lands on main, goTools does what pure Go
// can (WAV files, energy VAD) and says the rest is unavailable, so the
// pages show why a control is off.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/analysis"
	"github.com/hehehai/AudioSmith/internal/dsp/resample"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
)

// refRate is the sample rate references are kept at (mono, 16-bit WAV).
const refRate = 24000

// errNoASR: transcription needs a speech-to-text model, and none is installed.
var errNoASR = errors.New("no speech-to-text model is installed")

// errNeedsEngine: the tool needs an engine feature this build lacks.
var errNeedsEngine = errors.New("this needs an engine feature that is not available yet")

// Tools a voiceTools may lack (voiceTools.Can).
const (
	toolDecodeAny  = "decode"     // audio other than WAV, and video
	toolTranscribe = "transcribe" // stt.transcribe (may still answer errNoASR)
	toolEnhance    = "enhance"    // enhance.run
	toolCapture    = "capture"    // capture.*
	toolEncode     = "encode"     // tts.encodeVoice
)

// span is a stretch of a clip, in seconds.
type span struct{ Start, End float64 }

func (s span) Len() float64 { return s.End - s.Start }

// captureDevice is a microphone.
type captureDevice struct{ ID, Name string }

// captureOptions choose what to record: Device "" is the system's
// default, Channels 0 lets the device decide (1 mono, 2 stereo).
type captureOptions struct {
	Device   string
	Channels int
}

// recording is a microphone recording in progress.
type recording interface {
	// Stop ends it; the WAV is complete when Stop returns.
	Stop() error
	// Cancel ends it and drops the file.
	Cancel()
}

// voiceTools is the engine's side of the voice library. Every method may
// block (call it on a goroutine); Can is cheap and may be called from a
// build pass.
type voiceTools interface {
	Can(tool string) bool
	// Decode converts src (any audio or video) to a mono refRate WAV at
	// out and returns its length in seconds.
	Decode(ctx context.Context, src, out string) (float64, error)
	// Speech finds the stretches of a WAV with speech in them.
	Speech(ctx context.Context, path string) ([]span, error)
	// Transcribe returns what a WAV says (errNoASR without a model);
	// lang "" detects it.
	Transcribe(ctx context.Context, path, lang string) (string, error)
	// Enhance removes noise and room from a WAV, writing out.
	Enhance(ctx context.Context, in, out string) error
	Devices(ctx context.Context) ([]captureDevice, error)
	// Record starts recording into out (a WAV); level gets the input
	// level (RMS, 0–1) about 20 times a second, on any goroutine.
	Record(ctx context.Context, o captureOptions, out string, level func(float32)) (recording, error)
	// EncodeVoice computes model's voice prompt for a reference into out,
	// so takes skip that step (core.ErrUnsupported: the model has none).
	EncodeVoice(ctx context.Context, model, ref, refText, out string) error
}

// goTools is voiceTools in pure Go: WAV in, WAV out.
type goTools struct{}

func (goTools) Can(tool string) bool { return tool == toolTranscribe }

func isWAVPath(p string) bool { return strings.EqualFold(filepath.Ext(p), ".wav") }

func (goTools) Decode(ctx context.Context, src, out string) (float64, error) {
	if !isWAVPath(src) {
		return 0, errNeedsEngine
	}
	return decodeWAV(src, out)
}

// decodeWAV reads a WAV and writes it as a mono refRate 16-bit WAV.
func decodeWAV(src, out string) (float64, error) {
	b, _, err := wav.ReadFile(src)
	if err != nil {
		return 0, err
	}
	b = resample.Convert(b, refRate, 1, resample.Good)
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return 0, err
	}
	if err := wav.WriteFile(out, b, wav.PCM16, wav.Options{}); err != nil {
		return 0, err
	}
	return b.Seconds(), nil
}

func (goTools) Speech(ctx context.Context, path string) ([]span, error) {
	b, _, err := wav.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []span
	for _, r := range analysis.VAD(b, analysis.VADOptions{}) {
		out = append(out, span{r.Start, r.End})
	}
	return out, nil
}

func (goTools) Transcribe(context.Context, string, string) (string, error) { return "", errNoASR }

func (goTools) Enhance(context.Context, string, string) error { return errNeedsEngine }

func (goTools) Devices(context.Context) ([]captureDevice, error) { return nil, errNeedsEngine }

func (goTools) Record(context.Context, captureOptions, string, func(float32)) (recording, error) {
	return nil, errNeedsEngine
}

func (goTools) EncodeVoice(context.Context, string, string, string, string) error {
	return core.ErrUnsupported
}

// cutWAV writes the part [s.Start, s.End) of a WAV to out, with 10 ms
// fades at the cuts so they don't click.
func cutWAV(src, out string, s span) (float64, error) {
	b, _, err := wav.ReadFile(src)
	if err != nil {
		return 0, err
	}
	from := min(max(dsp.FramesAt(s.Start, b.Rate), 0), b.Frames())
	to := min(max(dsp.FramesAt(s.End, b.Rate), from), b.Frames())
	cut := dsp.Buffer{Rate: b.Rate, Channels: b.Channels, Samples: append([]float32(nil), b.Samples[from*b.Channels:to*b.Channels]...)}
	fade := dsp.FramesAt(0.01, b.Rate)
	if from > 0 {
		dsp.FadeIn(cut, fade, dsp.Linear)
	}
	if to < b.Frames() {
		dsp.FadeOut(cut, fade, dsp.Linear)
	}
	if err := wav.WriteFile(out, cut, wav.PCM16, wav.Options{}); err != nil {
		return 0, err
	}
	return cut.Seconds(), nil
}
