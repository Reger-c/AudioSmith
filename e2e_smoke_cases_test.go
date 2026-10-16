package main

// TestModelSmoke's cases: text to speech (with streaming) and speech to
// text; split from e2e_smoke_test.go.

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/engine/enginetest"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
)

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
