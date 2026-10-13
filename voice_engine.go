package main

// What the voice pages ask of the engine (protocol 2, M1 methods): jobs
// for media.decode, vad.detect, stt.transcribe, enhance.run and
// tts.encodeVoice, followed by their owner "voicework:<id>"; and the
// direct capture calls of enginelink.go (captureDevices, startRecording,
// stopRecording). Pure Go does what it can without the engine: WAV files
// are decoded here, and an energy VAD finds speech at once while the
// engine's (Silero) runs.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/analysis"
	"github.com/hehehai/AudioSmith/internal/dsp/resample"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/jobs"
)

// refRate is the sample rate references are kept at (mono, 16-bit WAV).
const refRate = 24000

// voiceWorkOwner prefixes the owners of the voice pages' engine jobs.
const voiceWorkOwner = "voicework:"

// span is a stretch of a clip, in seconds.
type span struct{ Start, End float64 }

func (s span) Len() float64 { return s.End - s.Start }

// engineJob submits an engine job of kind for the voice pages, on its
// role's lane, and calls then on the main thread with its result or its
// error (a *jobs.Error, see engineErrorText).
func (a *app) engineJob(kind string, params any, then func(json.RawMessage, error)) {
	raw, err := json.Marshal(params)
	if err != nil {
		then(nil, err)
		return
	}
	owner := voiceWorkOwner + core.NewID(time.Now())
	a.voiceWork[owner] = func(j jobs.Job) {
		switch j.State {
		case jobs.StateSucceeded:
			then(j.Result, nil)
		case jobs.StateCancelled:
			then(nil, &jobs.Error{Code: jobs.CodeCancelled, Message: i18n.T("Cancelled")})
		default:
			e := j.Err
			if e == nil {
				e = &jobs.Error{Code: proto.CodeInternal, Message: i18n.T("The job failed")}
			}
			then(nil, e)
		}
	}
	spec := jobs.Spec{Kind: kind, Lane: jobs.Lane(proto.RoleOf(kind)), Priority: jobs.PriorityInteractive,
		Params: raw, Title: workTitle(kind), Owner: owner}
	sched := a.lib.sched
	a.async(func() {
		_, err := sched.Submit(context.Background(), spec)
		if err != nil {
			a.post(func() {
				if cb := a.voiceWork[owner]; cb != nil {
					delete(a.voiceWork, owner)
					then(nil, err)
				}
			})
		}
	})
}

// voiceWorkDone hands a finished job to the page waiting for it.
func (a *app) voiceWorkDone(j jobs.Job) {
	cb := a.voiceWork[j.Owner]
	if cb == nil {
		return
	}
	delete(a.voiceWork, j.Owner)
	cb(j)
}

// workTitle names a voice page's job in the jobs list.
func workTitle(kind string) string {
	switch kind {
	case proto.MethodMediaDecode:
		return i18n.T("Read a clip")
	case proto.MethodVADDetect:
		return i18n.T("Find the speech in a clip")
	case proto.MethodSTTTranscribe:
		return i18n.T("Transcribe a reference")
	case proto.MethodEnhanceRun:
		return i18n.T("Clean a clip's noise")
	case proto.MethodTTSEncodeVoice:
		return i18n.T("Prepare a voice")
	}
	return kind
}

// defaultModel is the repo of one of the catalogue's default analysis
// models (hidden entries included).
func defaultModel(id string) string {
	e, _ := catalog.Builtin().ByID(id)
	return e.Repo
}

// decodeClip makes src a mono WAV at out: WAV files here, anything else
// (compressed audio, video) with the engine's media.decode. then gets the
// length in seconds.
func (a *app) decodeClip(src, out string, then func(float64, error)) {
	if isWAVPath(src) {
		a.background(func() {
			secs, err := decodeWAV(src, out)
			if err != nil {
				// A WAV Go can't read (an exotic encoding): the engine may.
				a.post(func() { a.engineDecode(src, out, then) })
				return
			}
			a.post(func() { then(secs, nil) })
		})
		return
	}
	a.engineDecode(src, out, then)
}

func (a *app) engineDecode(src, out string, then func(float64, error)) {
	os.MkdirAll(filepath.Dir(out), 0o755)
	a.engineJob(proto.MethodMediaDecode, proto.DecodeParams{Input: src, Output: proto.Output{Path: out, Format: "wav"}, SampleRate: refRate, Channels: 1},
		func(raw json.RawMessage, err error) {
			if err != nil {
				then(0, err)
				return
			}
			var res proto.DecodeResult
			if err := json.Unmarshal(raw, &res); err != nil {
				then(0, err)
				return
			}
			then(res.AudioSeconds, nil)
		})
}

// isWAVPath reports whether a file is a WAV by its name.
func isWAVPath(p string) bool { return strings.EqualFold(filepath.Ext(p), ".wav") }

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

// energySpeech finds speech in a WAV by energy (no model, at once).
func energySpeech(path string) []span {
	b, _, err := wav.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []span
	for _, r := range analysis.VAD(b, analysis.VADOptions{}) {
		out = append(out, span{r.Start, r.End})
	}
	return out
}

// detectSpeech asks the engine's VAD (Silero) for the speech in a WAV.
func (a *app) detectSpeech(path string, then func([]span, error)) {
	a.engineJob(proto.MethodVADDetect, proto.VADParams{Model: defaultModel(catalog.DefaultVAD), Audio: path},
		func(raw json.RawMessage, err error) {
			if err != nil {
				then(nil, err)
				return
			}
			var res proto.VADResult
			if err := json.Unmarshal(raw, &res); err != nil {
				then(nil, err)
				return
			}
			var out []span
			for _, r := range res.Regions {
				out = append(out, span{r.Start, r.End})
			}
			then(out, nil)
		})
}

// errNoASR: transcription needs a speech-to-text model, and none is
// downloaded.
var errNoASR = errors.New("no speech-to-text model is installed")

// transcribeClip asks the engine for what a WAV says; lang "" detects it.
func (a *app) transcribeClip(path, lang string, then func(string, error)) {
	a.engineJob(proto.MethodSTTTranscribe, proto.TranscribeParams{Model: defaultModel(catalog.DefaultSTT), Audio: path, Language: lang},
		func(raw json.RawMessage, err error) {
			var je *jobs.Error
			if errors.As(err, &je) && je.Code == proto.CodeModelNotDownloaded {
				err = errNoASR
			}
			if err != nil {
				then("", err)
				return
			}
			var res proto.TranscribeResult
			if err := json.Unmarshal(raw, &res); err != nil {
				then("", err)
				return
			}
			then(strings.TrimSpace(res.Text), nil)
		})
}

// enhanceClip removes the noise of a WAV into out.
func (a *app) enhanceClip(in, out string, then func(error)) {
	a.engineJob(proto.MethodEnhanceRun, proto.EnhanceParams{Model: defaultModel(catalog.DefaultEnhance), Audio: in, Output: proto.Output{Path: out, Format: "wav"}},
		func(_ json.RawMessage, err error) { then(err) })
}

// encodeVoice computes model e's voice prompt for a reference into out.
func (a *app) encodeVoice(e catalog.Entry, ref, refText, lang, out string, then func(error)) {
	a.engineJob(proto.MethodTTSEncodeVoice, proto.EncodeVoiceParams{Model: e.Repo, RefAudio: ref, RefText: refText,
		Language: modelLanguage(e, lang), PromptPath: out}, func(_ json.RawMessage, err error) { then(err) })
}

// levelFraction maps an input level (dBFS) to a meter's 0–1: −60 dB is
// empty, 0 dB full.
func levelFraction(db float64) float32 {
	return float32(min(max((db+60)/60, 0), 1))
}

// inputDetected reports whether a level (dBFS RMS) is more than room noise.
func inputDetected(db float64) bool { return db > -40 }

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
