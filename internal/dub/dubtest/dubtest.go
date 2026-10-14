// Package dubtest is a fake engine for the dubbing pipeline's tests: every
// method the pipeline calls (protocol 2's and the proposed ones in
// internal/dub/engineapi.go), answered in Go with real WAV files, so the
// whole pipeline runs on Linux.
//
// What each method does:
//
//	media.probe       WAV: its header; other files: Probes[path], else a 10 s video
//	media.decode      WAV inputs converted; others read "<input>.wav" (a video's audio)
//	separate.run      vocals = the input, background = the input × 0.1
//	diarize.run       energy VAD regions, speakers taking turns (Speakers, default 2)
//	stt.transcribe    one segment per VAD region, its text from Script (or "Line N."), with word times
//	stt.align         the text's words spread over the audio
//	vad.detect        the energy VAD
//	enhance.run       a copy
//	tts.synthesize    a 220 Hz tone, 0.07 s per character (Duration when given), 24 kHz mono
//	media.mux/encode/transcode/thumbnail  files describing what they were asked
//
// Methods in Missing answer method_not_found, as an engine without them.
package dubtest

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/analysis"
	"github.com/hehehai/AudioSmith/internal/dsp/resample"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
)

// Engine is the fake. Goroutine-safe.
type Engine struct {
	mu       sync.Mutex
	Missing  map[string]bool
	Script   []string // what the source says, line by line (stt.transcribe)
	Language string   // detected language (default "en")
	Speakers int      // diarize.run's speakers (default 2)
	Probes   map[string]proto.ProbeResult
	// Heard replaces what a check (stt.transcribe of a dubbed track's
	// line) hears: by the line's expected text.
	Heard map[string]string
	// Fail makes a method fail with this code.
	Fail  map[string]string
	Calls []Call
}

// Call is a request the fake answered.
type Call struct {
	Method string
	Params json.RawMessage
}

// New makes a fake with every method.
func New() *Engine {
	return &Engine{Missing: map[string]bool{}, Probes: map[string]proto.ProbeResult{}, Heard: map[string]string{}, Fail: map[string]string{}}
}

// Count is how many times method was called.
func (e *Engine) Count(method string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, c := range e.Calls {
		if c.Method == method {
			n++
		}
	}
	return n
}

// Last is the params of the last call of method.
func (e *Engine) Last(method string) json.RawMessage {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := len(e.Calls) - 1; i >= 0; i-- {
		if e.Calls[i].Method == method {
			return e.Calls[i].Params
		}
	}
	return nil
}

// Executor is the fake's executor of method (as engine.Executor).
func (e *Engine) Executor(method string) jobs.Executor {
	return jobs.ExecutorFunc(func(ctx context.Context, job jobs.Job, report func(jobs.Progress), _ func(json.RawMessage)) (json.RawMessage, error) {
		e.mu.Lock()
		e.Calls = append(e.Calls, Call{Method: method, Params: job.Params})
		missing, fail := e.Missing[method], e.Fail[method]
		e.mu.Unlock()
		if missing {
			return nil, &jobs.Error{Code: proto.CodeMethodNotFound, Message: "unknown method " + method}
		}
		if fail != "" {
			return nil, &jobs.Error{Code: fail, Message: method + " failed"}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if report != nil {
			report(jobs.Progress{Stage: "working", Fraction: 0.5})
		}
		res, err := e.run(method, job.Params)
		if err != nil {
			var je *jobs.Error
			if ok := asJobErr(err, &je); ok {
				return nil, je
			}
			return nil, &jobs.Error{Code: proto.CodeInternal, Message: err.Error()}
		}
		return json.Marshal(res)
	})
}

func asJobErr(err error, out **jobs.Error) bool {
	je, ok := err.(*jobs.Error)
	if ok {
		*out = je
	}
	return ok
}

func (e *Engine) run(method string, raw json.RawMessage) (any, error) {
	switch method {
	case proto.MethodMediaProbe:
		var p proto.ProbeParams
		json.Unmarshal(raw, &p)
		return e.probe(p.Input)
	case proto.MethodMediaDecode:
		var p proto.DecodeParams
		json.Unmarshal(raw, &p)
		b, err := e.read(p.Input)
		if err != nil {
			return nil, err
		}
		rate, ch := p.SampleRate, p.Channels
		if rate == 0 {
			rate = b.Rate
		}
		if ch == 0 {
			ch = b.Channels
		}
		b = resample.Convert(b, rate, ch, resample.Good)
		if err := write(p.Output, b); err != nil {
			return nil, err
		}
		src, _ := e.probe(p.Input)
		return proto.DecodeResult{AudioOut: out(p.Output.Path, b), Source: src}, nil
	case dub.MethodSeparate:
		var p dub.SeparateParams
		json.Unmarshal(raw, &p)
		b, err := e.read(p.Audio)
		if err != nil {
			return nil, err
		}
		b = resample.Convert(b, 44100, 2, resample.Fast)
		res := dub.SeparateResult{SampleRate: 44100, Channels: 2, Frames: int64(b.Frames()), AudioSeconds: b.Seconds(), Model: p.Model}
		if p.Vocals.Path != "" {
			if err := write(p.Vocals, b); err != nil {
				return nil, err
			}
			res.Vocals = p.Vocals.Path
		}
		if p.Background.Path != "" {
			bg := b.Clone()
			dsp.Gain(bg.Samples, 0.1)
			if err := write(p.Background, bg); err != nil {
				return nil, err
			}
			res.Background = p.Background.Path
		}
		return res, nil
	case dub.MethodDiarize:
		var p dub.DiarizeParams
		json.Unmarshal(raw, &p)
		b, err := e.read(p.Audio)
		if err != nil {
			return nil, err
		}
		n := p.Speakers
		if n == 0 {
			n = max(e.Speakers, 1)
			if e.Speakers == 0 {
				n = 2
			}
		}
		var turns []dub.Turn
		for i, r := range analysis.VAD(b, analysis.VADOptions{}) {
			turns = append(turns, dub.Turn{Start: r.Start, End: r.End, Speaker: fmt.Sprintf("SPEAKER_%02d", i%n)})
		}
		return dub.DiarizeResult{Turns: turns, Speakers: n, Model: p.Model, AudioSeconds: b.Seconds()}, nil
	case proto.MethodSTTTranscribe:
		var p proto.TranscribeParams
		json.Unmarshal(raw, &p)
		return e.transcribe(p)
	case proto.MethodSTTAlign:
		var p proto.AlignParams
		json.Unmarshal(raw, &p)
		b, err := e.read(p.Audio)
		if err != nil {
			return nil, err
		}
		return proto.AlignResult{Model: p.Model, Words: spread(p.Text, 0, b.Seconds()), AudioSeconds: b.Seconds()}, nil
	case proto.MethodVADDetect:
		var p proto.VADParams
		json.Unmarshal(raw, &p)
		b, err := e.read(p.Audio)
		if err != nil {
			return nil, err
		}
		var regions []proto.Region
		for _, r := range analysis.VAD(b, analysis.VADOptions{}) {
			regions = append(regions, proto.Region{Start: r.Start, End: r.End, Prob: 0.9})
		}
		return proto.VADResult{Model: p.Model, Regions: regions, AudioSeconds: b.Seconds()}, nil
	case proto.MethodEnhanceRun:
		var p proto.EnhanceParams
		json.Unmarshal(raw, &p)
		b, err := e.read(p.Audio)
		if err != nil {
			return nil, err
		}
		b = resample.Convert(b, 48000, 1, resample.Fast)
		if err := write(p.Output, b); err != nil {
			return nil, err
		}
		return proto.EnhanceResult{AudioOut: out(p.Output.Path, b), Model: p.Model}, nil
	case proto.MethodTTSSynthesize:
		var p proto.SynthesizeParams
		json.Unmarshal(raw, &p)
		secs := 0.07 * float64(max(utf8.RuneCountInString(p.Text), 1))
		if p.Duration != nil && *p.Duration > 0 {
			secs = *p.Duration
		}
		if p.Speed != nil && *p.Speed > 0 {
			secs /= *p.Speed
		}
		b := Tone(24000, 1, secs, 220, 0.3)
		if err := write(p.Output, b); err != nil {
			return nil, err
		}
		seed := int64(7)
		return proto.SynthesizeResult{Output: p.Output.Path, Model: p.Model, SampleRate: 24000, Channels: 1,
			Frames: int64(b.Frames()), AudioSeconds: b.Seconds(), Seconds: 0.01, RTF: 100, Seed: &seed}, nil
	case dub.MethodMux:
		var p dub.MuxParams
		json.Unmarshal(raw, &p)
		data, _ := json.MarshalIndent(p, "", "  ")
		if err := writeFile(p.Output.Path, data); err != nil {
			return nil, err
		}
		return dub.MuxResult{Output: p.Output.Path, Bytes: int64(len(data)), Duration: 10, Tracks: len(p.Audio)}, nil
	case dub.MethodEncode:
		var p dub.EncodeParams
		json.Unmarshal(raw, &p)
		data, err := os.ReadFile(p.Input)
		if err != nil {
			return nil, &jobs.Error{Code: proto.CodeFileNotFound, Message: err.Error()}
		}
		if err := writeFile(p.Output.Path, data); err != nil {
			return nil, err
		}
		return dub.EncodeResult{Output: p.Output.Path, Format: p.Output.Format, Bytes: int64(len(data))}, nil
	case dub.MethodTranscode:
		var p dub.TranscodeParams
		json.Unmarshal(raw, &p)
		if err := writeFile(p.Output.Path, []byte("fake mp4 of "+p.Input)); err != nil {
			return nil, err
		}
		return dub.TranscodeResult{Output: p.Output.Path, Width: 1280, Height: 720, Duration: 10}, nil
	case dub.MethodThumbnail:
		var p dub.ThumbnailParams
		json.Unmarshal(raw, &p)
		var res dub.ThumbnailResult
		for i, t := range p.Times {
			if i < len(p.Outputs) {
				if err := writeFile(p.Outputs[i], PNG); err != nil {
					return nil, err
				}
				res.Frames = append(res.Frames, dub.Frame{Time: t, Path: p.Outputs[i], Width: 2, Height: 2})
			}
		}
		return res, nil
	}
	return nil, &jobs.Error{Code: proto.CodeMethodNotFound, Message: "unknown method " + method}
}

func (e *Engine) probe(path string) (proto.ProbeResult, error) {
	e.mu.Lock()
	pr, ok := e.Probes[path]
	e.mu.Unlock()
	if ok {
		return pr, nil
	}
	if strings.EqualFold(filepath.Ext(path), ".wav") {
		b, _, err := wav.ReadFile(path)
		if err != nil {
			return pr, &jobs.Error{Code: proto.CodeFileNotFound, Message: err.Error()}
		}
		return proto.ProbeResult{Format: "wav", Duration: b.Seconds(), HasAudio: true, AudioTracks: 1, Codec: "lpcm", SampleRate: b.Rate, Channels: b.Channels, Bits: 16}, nil
	}
	if _, err := os.Stat(path); err != nil {
		return pr, &jobs.Error{Code: proto.CodeFileNotFound, Message: err.Error()}
	}
	d := 10.0
	if b, err := e.read(path); err == nil {
		d = b.Seconds()
	}
	return proto.ProbeResult{Format: "mp4", Duration: d, HasAudio: true, HasVideo: true, AudioTracks: 1, Codec: "aac", SampleRate: 48000, Channels: 2,
		Video: &proto.VideoInfo{Codec: "h264", Width: 1920, Height: 1080, FPS: 25}}, nil
}

// read reads a WAV, or the "<path>.wav" that stands for a video's audio.
func (e *Engine) read(path string) (dsp.Buffer, error) {
	if !strings.EqualFold(filepath.Ext(path), ".wav") {
		path += ".wav"
	}
	b, _, err := wav.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return b, &jobs.Error{Code: proto.CodeFileNotFound, Message: err.Error()}
		}
		return b, &jobs.Error{Code: proto.CodeUnsupportedMedia, Message: err.Error()}
	}
	return b, nil
}

func (e *Engine) transcribe(p proto.TranscribeParams) (proto.TranscribeResult, error) {
	b, err := e.read(p.Audio)
	if err != nil {
		return proto.TranscribeResult{}, err
	}
	e.mu.Lock()
	script, lang, heard := e.Script, e.Language, e.Heard
	e.mu.Unlock()
	if lang == "" {
		lang = "en"
	}
	if p.Language != "" && p.Language != "auto" {
		lang = p.Language
	}
	res := proto.TranscribeResult{Model: p.Model, Language: lang, AudioSeconds: b.Seconds(), Seconds: 0.01, RTF: 100}
	regions := analysis.VAD(b, analysis.VADOptions{})
	if strings.HasPrefix(p.Prompt, "expect:") {
		// A check of one dubbed line: it heard what it was told to.
		want := strings.TrimPrefix(p.Prompt, "expect:")
		got := want
		if h, ok := heard[want]; ok {
			got = h
		}
		res.Text = got
		res.Segments = []proto.Segment{{Start: 0, End: b.Seconds(), Text: got}}
		return res, nil
	}
	var texts []string
	for i, r := range regions {
		t := fmt.Sprintf("Line %d.", i+1)
		if i < len(script) {
			t = script[i]
		}
		seg := proto.Segment{Start: round(r.Start), End: round(r.End), Text: t}
		if p.Words {
			seg.Words = spread(t, r.Start, r.End)
		}
		res.Segments = append(res.Segments, seg)
		texts = append(texts, t)
	}
	res.Text = strings.Join(texts, " ")
	return res, nil
}

// spread times text's words evenly over start–end.
func spread(text string, start, end float64) []proto.Word {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}
	step := (end - start) / float64(len(words))
	out := make([]proto.Word, len(words))
	for i, w := range words {
		out[i] = proto.Word{Text: strings.Trim(w, ".,!?"), Start: round(start + step*float64(i)), End: round(start + step*float64(i+1))}
	}
	return out
}

func round(x float64) float64 { return math.Round(x*1000) / 1000 }

func out(path string, b dsp.Buffer) proto.AudioOut {
	return proto.AudioOut{Output: path, SampleRate: b.Rate, Channels: b.Channels, Frames: int64(b.Frames()), AudioSeconds: b.Seconds()}
}

func write(o proto.Output, b dsp.Buffer) error {
	if err := os.MkdirAll(filepath.Dir(o.Path), 0o755); err != nil {
		return err
	}
	enc := wav.PCM16
	switch o.Bits {
	case 24:
		enc = wav.PCM24
	case 32:
		enc = wav.Float32
	}
	return wav.WriteFile(o.Path, b, enc, wav.Options{})
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// Tone is a sine.
func Tone(rate, ch int, seconds, hz, amp float64) dsp.Buffer {
	n := int(float64(rate) * seconds)
	b := dsp.New(rate, ch, n)
	for i := range n {
		v := float32(amp * math.Sin(2*math.Pi*hz*float64(i)/float64(rate)))
		for c := range ch {
			b.Samples[i*ch+c] = v
		}
	}
	return b
}

// Speech writes a source-like WAV: bursts of tone (one per line, 1.5–3 s,
// with 0.8 s pauses) over a quiet hum, at 48 kHz stereo, and returns the
// burst times.
func Speech(path string, lines int) ([]dsp.Range, error) {
	rate := 48000
	var spans []dsp.Range
	at := 0.5
	for i := range lines {
		d := 1.5 + float64(i%3)*0.75
		spans = append(spans, dsp.Range{Start: at, End: at + d})
		at += d + 0.8
	}
	total := at + 0.5
	b := dsp.New(rate, 2, int(total*float64(rate)))
	for i := range b.Frames() {
		t := float64(i) / float64(rate)
		v := 0.003 * math.Sin(2*math.Pi*60*t)
		for k, s := range spans {
			if t >= s.Start && t < s.End {
				v += 0.3 * math.Sin(2*math.Pi*(150+20*float64(k%4))*t) * (0.6 + 0.4*math.Sin(2*math.Pi*4*t))
			}
		}
		b.Samples[2*i], b.Samples[2*i+1] = float32(v), float32(v)
	}
	return spans, write(proto.Output{Path: path, Bits: 16}, b)
}

// PNG is a 1×1 transparent PNG.
var PNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\rIDATx\x9cc\xf8\x0f\x00\x00\x01\x01\x00\x05\x18\xd8N\x00\x00\x00\x00IEND\xaeB`\x82")
