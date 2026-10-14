package main

// A voice from a clip (/voices/new): upload or record a reference, keep
// its best part, transcribe it, clean its noise, then save it as a voice
// (name + portrait), use it once without saving, or replace the reference
// of a saved voice. The engine does the work (voice_engine.go); this file
// holds the flow and the rules about lengths.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/audio"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/i18n"
)

// Reference lengths, in seconds.
const (
	refMin       = 1.0  // shorter is refused
	refIdealLow  = 5.0  // 5–15 s is what models clone best from
	refIdealHigh = 15.0 // longer clips start at their best 15 s
	refMaxBare   = 75.0 // longer needs a transcript
)

// cloneState is the reference flow's view state.
type cloneState struct {
	replaceFor string // voice whose reference this replaces; "" = a new voice
	gen        int    // bumps with each clip: results for older ones are dropped
	srcName    string // the file picked, or "Recording"
	work       string // the working WAV (mono) in referencesDir
	original   string // the clip before cleaning (Undo)
	seconds    float64
	peaks      []float32
	speech     []span
	auto       span    // the part the app chose; the user's own choice is kept
	selLo      float64 // the part kept, bound to the trimmer
	selHi      float64
	busy       string // what runs, translated; "" when idle
	err        string
	cleaned    bool
	cleanErr   string

	transcript string
	autoText   string // the machine transcript last put in transcript
	asr        string // "", "running", "failed", "noasr", "done"
	asrErr     string
	style      string
	language   string

	name        string
	portrait    []byte
	portraitBmp *ui.Bitmap
	saving      bool

	mic           micState
	devices       []proto.CaptureDevice
	devicesLoaded bool
	devicesErr    string
}

// sel is the part of the clip kept.
func (s *cloneState) sel() span { return span{s.selLo, s.selHi} }

// hasClip reports whether a clip is loaded.
func (s *cloneState) hasClip() bool { return s.work != "" }

// startClone opens the reference flow for a new voice, or to replace the
// reference of voice replaceFor.
func (a *app) startClone(replaceFor string) {
	devices, loaded, mic := a.clone.devices, a.clone.devicesLoaded, a.clone.mic
	a.micCancel(&a.clone.mic)
	a.clone = cloneState{replaceFor: replaceFor, gen: a.clone.gen + 1, devices: devices, devicesLoaded: loaded,
		mic: micState{device: mic.device, channels: mic.channels}}
	if v := a.findVoice(replaceFor); v != nil {
		a.clone.name, a.clone.style, a.clone.language = v.Name, v.Style, v.Language
	}
	a.navigate("/clone")
	a.loadDevices()
}

// loadDevices lists the microphones once.
func (a *app) loadDevices() {
	if a.clone.devicesLoaded {
		return
	}
	a.clone.devicesLoaded = true
	a.captureDevices(func(ds []proto.CaptureDevice, err error) {
		if err != nil {
			a.clone.devicesErr = engineErrorText(err)
			return
		}
		a.clone.devices, a.clone.devicesErr = ds, ""
	})
}

// workPath is a new file in the references directory.
func (a *app) workPath(prefix string) string {
	return filepath.Join(a.referencesDir(), prefix+"-"+core.NewID(time.Now())+".wav")
}

// chooseCloneFile asks for an audio or video file.
func (a *app) chooseCloneFile() {
	exts := []string{"wav", "mp3", "m4a", "aac", "flac", "ogg", "opus", "aiff", "aif", "caf", "webm", "mp4", "mov", "m4v"}
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent:  a.win,
			Title:   i18n.T("Reference audio"),
			Filters: []mygo.FileFilter{{Name: i18n.T("Audio or video"), Extensions: exts}},
		})
		if err != nil || len(paths) == 0 {
			return
		}
		a.post(func() { a.cloneLoad(paths[0], filepath.Base(paths[0]), false) })
	}()
}

// cloneDropped takes files dropped on the reference flow: the first
// audio or video file becomes the clip, an image the portrait.
func (a *app) cloneDropped(paths []string) {
	for _, p := range paths {
		if isPortraitPath(p) {
			a.clonePortrait(p)
			continue
		}
		a.cloneLoad(p, filepath.Base(p), false)
		return
	}
}

// cloneLoad makes src the clip: decoded to a mono WAV, its best part
// selected (by energy at once, then by the engine's VAD), then cleaned
// (recordings) and transcribed. A newer clip wins over a slow older one.
func (a *app) cloneLoad(src, name string, recorded bool) {
	s := &a.clone
	s.gen++
	gen := s.gen
	s.busy, s.err, s.srcName = i18n.T("Reading the clip…"), "", name
	s.work, s.original, s.peaks, s.speech, s.cleaned, s.cleanErr = "", "", nil, nil, false, ""
	s.asr, s.asrErr = "", ""
	if s.transcript == s.autoText { // a new clip: a machine transcript of the old one goes
		s.transcript = ""
	}
	s.autoText = ""
	out := a.workPath("clip")
	a.decodeClip(src, out, func(secs float64, err error) {
		if s.gen != gen {
			os.Remove(out)
			return
		}
		if err != nil {
			s.busy, s.err = "", i18n.T("Could not read the clip: %s", engineErrorText(err))
			return
		}
		a.background(func() {
			peaks, _ := audio.Peaks(out, peakCount)
			speech := energySpeech(out)
			a.post(func() {
				if s.gen != gen {
					return
				}
				s.busy = ""
				s.work, s.seconds, s.speech, s.peaks = out, secs, speech, peaks
				s.auto = bestWindow(speech, secs, refIdealHigh)
				s.selLo, s.selHi = s.auto.Start, s.auto.End
				a.cloneRefine(gen, recorded)
			})
		})
	})
}

// cloneRefine asks the engine's VAD where the speech is (keeping a part
// the user chose), then cleans a recording and transcribes.
func (a *app) cloneRefine(gen int, recorded bool) {
	s := &a.clone
	next := func() {
		if recorded {
			a.cloneClean(true)
			return
		}
		a.cloneTranscribe()
	}
	if s.seconds <= refIdealHigh {
		next() // all of it is kept: no need to look for the best part
		return
	}
	a.detectSpeech(s.work, func(speech []span, err error) {
		if s.gen != gen {
			return
		}
		if err == nil && len(speech) > 0 {
			s.speech = speech
			if s.sel() == s.auto {
				s.auto = bestWindow(speech, s.seconds, refIdealHigh)
				s.selLo, s.selHi = s.auto.Start, s.auto.End
			}
		}
		next()
	})
}

// cloneClean removes the clip's noise (keeping the original for Undo);
// then, when asked, transcribes it.
func (a *app) cloneClean(thenTranscribe bool) {
	s := &a.clone
	if !s.hasClip() || s.cleaned || s.busy != "" {
		return
	}
	gen, in, out := s.gen, s.work, a.workPath("clean")
	s.busy, s.cleanErr = i18n.T("Cleaning the noise…"), ""
	a.enhanceClip(in, out, func(err error) {
		if s.gen != gen {
			os.Remove(out)
			return
		}
		if err != nil {
			s.busy, s.cleanErr = "", engineErrorText(err)
			if thenTranscribe {
				a.cloneTranscribe()
			}
			return
		}
		a.background(func() {
			peaks, _ := audio.Peaks(out, peakCount)
			a.post(func() {
				if s.gen != gen {
					return
				}
				s.busy = ""
				s.original, s.work, s.peaks, s.cleaned = s.work, out, peaks, true
				if thenTranscribe {
					a.cloneTranscribe()
				}
			})
		})
	})
}

// cloneUndoClean goes back to the clip as it was before cleaning.
func (a *app) cloneUndoClean() {
	s := &a.clone
	if !s.cleaned || s.original == "" {
		return
	}
	s.work, s.original, s.cleaned = s.original, "", false
	path, gen := s.work, s.gen
	a.background(func() {
		peaks, _ := audio.Peaks(path, peakCount)
		a.post(func() {
			if s.gen == gen {
				s.peaks = peaks
			}
		})
	})
}

// cloneSelection writes the kept part to a file of its own and calls
// done with it on the main thread (the whole clip when all of it is kept).
func (a *app) cloneSelection(done func(path string, seconds float64, err error)) {
	s := &a.clone
	work, sel, total := s.work, s.sel(), s.seconds
	out := a.workPath("ref")
	a.background(func() {
		var err error
		secs := total
		if sel.Start <= 0.05 && sel.End >= total-0.05 {
			err = copyWAV(work, out)
		} else {
			secs, err = cutWAV(work, out, sel)
		}
		a.post(func() { done(out, secs, err) })
	})
}

// copyWAV copies a file.
func copyWAV(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

// cloneTranscribe transcribes the kept part. The transcript never
// replaces what the user typed: only an empty one, or the machine's own.
func (a *app) cloneTranscribe() {
	s := &a.clone
	if !s.hasClip() || s.asr == "running" {
		return
	}
	s.asr, s.asrErr = "running", ""
	gen, lang := s.gen, s.language
	a.cloneSelection(func(path string, _ float64, err error) {
		if err != nil {
			s.asr, s.asrErr = "failed", err.Error()
			return
		}
		a.transcribeClip(path, lang, func(text string, err error) {
			os.Remove(path)
			if s.gen != gen {
				return
			}
			switch {
			case errors.Is(err, errNoASR):
				s.asr = "noasr"
			case err != nil:
				s.asr, s.asrErr = "failed", engineErrorText(err)
			default:
				s.asr = "done"
				if s.transcript == s.autoText {
					s.transcript = text
				}
				s.autoText = text
			}
		})
	})
}

// clonePortrait makes an image the new voice's portrait.
func (a *app) clonePortrait(path string) {
	a.background(func() {
		png, err := portraitPNG(path)
		var bmp *ui.Bitmap
		if err == nil {
			bmp, _ = ui.DecodeBitmap(png)
		}
		a.post(func() {
			if err != nil {
				a.toast(portraitErrorText(err))
				return
			}
			a.clone.portrait, a.clone.portraitBmp = png, bmp
		})
	})
}

// portraitErrorText explains why an image can't be a portrait.
func portraitErrorText(err error) string {
	switch {
	case errors.Is(err, errPortraitLarge):
		return i18n.T("Choose an image under 5 MB.")
	case errors.Is(err, errPortraitType):
		return i18n.T("Choose a PNG, JPEG or WebP image.")
	}
	return i18n.T("Could not use the image: %v", err)
}

// choosePortrait asks for an image and hands it to use.
func (a *app) choosePortrait(use func(path string)) {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent:  a.win,
			Title:   i18n.T("Portrait"),
			Filters: []mygo.FileFilter{{Name: i18n.T("Images"), Extensions: []string{"png", "jpg", "jpeg", "webp"}}},
		})
		if err != nil || len(paths) == 0 {
			return
		}
		a.post(func() { use(paths[0]) })
	}()
}

// cloneProblem says why the clip can't be used yet ("" when it can).
func (a *app) cloneProblem() string {
	s := &a.clone
	switch {
	case !s.hasClip():
		return i18n.T("Add a clip first.")
	case s.busy != "":
		return s.busy
	case s.sel().Len() < refMin:
		return i18n.T("The part kept is too short: keep at least 1 second.")
	case s.sel().Len() > refMaxBare && strings.TrimSpace(s.transcript) == "":
		return i18n.T("Clips over 75 seconds need a transcript. Keep a shorter part, or type what it says.")
	}
	return ""
}

// refAdvice is the note on the kept part's length and its tone: "ok",
// "warning" or "danger".
func refAdvice(seconds float64, transcript bool) (string, string) {
	switch {
	case seconds < refMin:
		return i18n.T("Too short: keep at least 1 second; 5–15 seconds of clean speech works best."), "danger"
	case seconds < refIdealLow:
		return i18n.T("Short: 5–15 seconds of clean speech works best."), "warning"
	case seconds <= refIdealHigh+0.05:
		return i18n.T("Good length: %.1f seconds.", seconds), "ok"
	case seconds <= refMaxBare:
		return i18n.T("Longer than 15 seconds: most models clone best from 5–15 seconds. Keep the best part, or all of it."), "warning"
	case !transcript:
		return i18n.T("Over 75 seconds: keep a shorter part, or add the transcript."), "danger"
	}
	return i18n.T("Over 75 seconds: long references are slow, and a shorter part usually sounds as good."), "warning"
}

// bestWindow picks the stretch of at most maxLen seconds with the most
// speech in it. Windows start just before a stretch of speech and, when a
// word would be cut at their end, end before that word; they are compared
// after that. Clips no longer than maxLen are kept whole.
func bestWindow(speech []span, total, maxLen float64) span {
	if total <= maxLen {
		return span{0, total}
	}
	window := func(start float64) span {
		w := span{start, min(start+maxLen, total)}
		if w.Len() < maxLen {
			w.Start = max(0, w.End-maxLen)
		}
		for _, s := range speech {
			if s.Start < w.End && s.End > w.End && s.Start-w.Start >= refIdealLow {
				w.End = s.Start
				break
			}
		}
		return w
	}
	best := window(0)
	bestSpeech := speechIn(speech, best)
	for _, s := range speech {
		w := window(max(0, s.Start-0.1))
		if v := speechIn(speech, w); v > bestSpeech+1e-9 {
			best, bestSpeech = w, v
		}
	}
	return best
}

// speechIn is how much speech lies inside w.
func speechIn(speech []span, w span) float64 {
	var n float64
	for _, s := range speech {
		n += max(0, min(s.End, w.End)-max(s.Start, w.Start))
	}
	return n
}

// cloneSave saves the clip: as a new voice, or as the new reference of
// the voice it replaces. A voice prompt for the Studio's model is cached
// afterwards when the engine can make one.
func (a *app) cloneSave() {
	s := &a.clone
	if a.cloneProblem() != "" || s.saving {
		return
	}
	name := strings.TrimSpace(s.name)
	if name == "" {
		a.toast(i18n.T("Give the voice a name."))
		return
	}
	s.saving = true
	v := core.Voice{Name: name, Kind: core.VoiceCloned, RefText: strings.TrimSpace(s.transcript), Style: s.style, Language: s.language}
	if old := a.findVoice(s.replaceFor); old != nil {
		v = *old
		v.Name, v.RefText, v.Style, v.Language = name, strings.TrimSpace(s.transcript), s.style, s.language
	}
	portrait := s.portrait
	svc, gen := a.lib.svc.Voices, s.gen
	a.cloneSelection(func(path string, secs float64, err error) {
		if err != nil {
			s.saving = false
			a.toast(i18n.T("Could not save the voice profile: %v", err))
			return
		}
		v.RefSeconds = secs
		a.background(func() {
			saved, err := svc.SaveFiles(context.Background(), v, core.VoiceFiles{Reference: path, Portrait: portrait})
			os.Remove(path)
			a.post(func() {
				if s.gen == gen {
					s.saving = false
				}
				if err != nil {
					a.toast(i18n.T("Could not save the voice profile: %v", err))
					return
				}
				if s.replaceFor != "" {
					a.toast(i18n.T("Reference replaced"))
				} else {
					a.toast(i18n.T("Voice profile saved"))
				}
				a.voiceChanged(core.VoiceChanged{Voice: saved})
				a.encodePrompt(saved, a.preset())
				a.navigate("/voices")
				a.openVoiceEditor(saved.ID)
			})
		})
	})
}

// cloneUseOnce hands the clip to the Studio without saving a voice.
func (a *app) cloneUseOnce() {
	s := &a.clone
	if a.cloneProblem() != "" {
		return
	}
	text, style, name := strings.TrimSpace(s.transcript), s.style, s.srcName
	a.cloneSelection(func(path string, _ float64, err error) {
		if err != nil {
			a.toast(i18n.T("Could not use the clip: %v", err))
			return
		}
		a.studioClip = &clipRef{Path: path, Text: text, Name: name, Style: style}
		a.studioVoice = ""
		if p := a.preset(); !p.Has(catalog.CapClone) {
			if ms := cloneModels(); len(ms) > 0 {
				a.selectModel(ms[0])
			}
		}
		a.navigate("/studio")
	})
}

// encodePrompt caches the voice's prompt for the Studio's model, when
// the model can reuse one (tts.encodeVoice): its takes then skip
// encoding the reference.
func (a *app) encodePrompt(v core.Voice, e catalog.Entry) {
	if !e.Has(catalog.CapVoicePrompt) || !e.Has(catalog.CapClone) || v.RefAudio == "" {
		return
	}
	svc := a.lib.svc.Voices
	ref, out := a.voicePath(&v, v.RefAudio), a.workPath("prompt")
	a.encodeVoice(e, ref, v.RefText, v.Language, out, func(err error) {
		if err != nil {
			os.Remove(out)
			return // the engine encodes the reference with each take instead
		}
		a.background(func() {
			ctx := context.Background()
			if cur, err := svc.Get(ctx, v.ID); err == nil {
				svc.SaveFiles(ctx, cur, core.VoiceFiles{Prompt: out, PromptModel: e.Repo})
			}
			os.Remove(out)
		})
	})
}

// --- microphone ---------------------------------------------------------

// micState is a microphone recording: the reference flow's and the
// consent statement's.
type micState struct {
	rec      *engine.Recording
	out      string
	phase    string // "", "starting", "recording", "stopping"
	started  time.Time
	level    float64 // input RMS, dBFS
	err      string
	device   string // capture device id; "" = the system's default
	channels int    // 0 auto, 1 mono, 2 stereo
	onStop   func(path string)
}

// micStart starts recording into a new file; done gets it when stopped.
func (a *app) micStart(m *micState, done func(path string)) {
	if m.phase != "" {
		return
	}
	out := a.workPath("rec")
	m.phase, m.err, m.level, m.out, m.onStop = "starting", "", -120, out, done
	os.MkdirAll(filepath.Dir(out), 0o755)
	p := proto.CaptureParams{Device: m.device, Channels: m.channels, Output: proto.Output{Path: out, Format: "wav"}, MaxSeconds: 600}
	a.startRecording(p, func(l proto.LevelParams) {
		if m.out == out {
			m.level = l.RMS
		}
	}, func(rec *engine.Recording, err error) {
		if m.out != out || m.phase != "starting" {
			if rec != nil {
				a.background(rec.Cancel)
			}
			return
		}
		if err != nil {
			m.phase, m.err = "", engineErrorText(err)
			return
		}
		m.rec, m.phase, m.started = rec, "recording", time.Now()
	})
}

// micStop ends the recording and hands the file on.
func (a *app) micStop(m *micState) {
	if m.phase != "recording" || m.rec == nil {
		return
	}
	m.phase = "stopping"
	rec, out, done := m.rec, m.out, m.onStop
	a.stopRecording(rec, func(res proto.CaptureResult, err error) {
		if m.out != out {
			return
		}
		m.rec, m.phase, m.level = nil, "", -120
		switch {
		case err != nil:
			m.err = engineErrorText(err)
		case res.AudioSeconds < 0.5:
			m.err = i18n.T("Recording too short.")
		default:
			if res.Reason == proto.CaptureDeviceLost {
				a.toast(i18n.T("The microphone went away: what was recorded is kept."))
			}
			if done != nil {
				done(firstOf(res.Output, out))
			}
		}
	})
}

// micEnded notices a recording that ended by itself (the device went
// away, the time limit) and finishes it as Stop does.
func (a *app) micEnded(m *micState) {
	if m.phase != "recording" || m.rec == nil {
		return
	}
	select {
	case <-m.rec.Done():
		a.micStop(m)
	default:
	}
}

// micCancel drops a recording in progress.
func (a *app) micCancel(m *micState) {
	rec := m.rec
	m.rec, m.phase, m.out = nil, "", ""
	if rec != nil {
		a.background(rec.Cancel)
	}
}
