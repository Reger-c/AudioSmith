package main

// System-wide dictation (M2): a global shortcut (⌘⇧Space by default)
// records from the microphone in the listen worker, then transcribes the
// recording (a transcript.make job, Fast: no word timings), polishes the
// text (and cleans it with the language model within 4 s, when one is set
// up and the setting asks), and types it into the app that was in front
// when the shortcut was pressed: the clipboard and a synthetic ⌘V, the
// user's clipboard restored after. Without Accessibility, or with
// "Copy only", the text stays on the clipboard. Every dictation is kept
// on the Transcribe page.
//
// Toggle mode: press to start, press again to stop. Hold mode: dictate
// while the keys are held (MyGo reports presses; internal/dictation gets
// the release from Carbon); a tap shorter than holdTap keeps listening
// until the next press. Esc cancels while the pill shows.
//
// v1 records whole utterances (capture.start → stt.transcribe); live
// partials need streaming ASR in the listen worker (stt.stream.*, a
// protocol proposal in threads/17.md).
//
// Files: dictation.go (this: the session), dictation_pill.go (the
// recorder pill, a borderless window), dictation_tray.go (menu bar icon,
// close to tray), dictation_settings.go (Settings → Dictation, the
// shortcut recorder), permissions.go (Settings → Permissions).

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dictation"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/settings"
	"github.com/hehehai/AudioSmith/internal/translate"
)

// Timings of a dictation.
const (
	holdTap        = 300 * time.Millisecond // a shorter hold keeps listening (tap to lock)
	minSpeech      = 0.3                    // seconds: shorter recordings say "No speech"
	hideInserted   = 700 * time.Millisecond
	hideCopied     = 4 * time.Second
	hideNoSpeech   = 1500 * time.Millisecond
	dictMaxSeconds = 600
)

// dictSleep is the Inserter's wait (nil: real time); tests skip it.
var dictSleep func(ctx context.Context, d time.Duration)

// dictPhase is where a dictation is.
type dictPhase int

const (
	dictIdle         dictPhase = iota
	dictStarting               // the microphone is starting
	dictListening              // recording
	dictTranscribing           // the transcription job (queued, loading the model, running)
	dictCleaning               // polish and clean-up
	dictInserting              // typing into the app
	dictInserted
	dictCopied
	dictNoSpeech
	dictError
)

// live reports whether the dictation still works (not an end state).
func (p dictPhase) live() bool { return p >= dictStarting && p <= dictInserting }

// hotkeys registers global shortcuts (mygo.GlobalShortcut; tests fake it).
type hotkeys interface {
	Register(acc string, fn func()) error
	Unregister(acc string)
}

type mygoHotkeys struct{}

func (mygoHotkeys) Register(acc string, fn func()) error {
	return mygo.GlobalShortcut.Register(acc, fn)
}
func (mygoHotkeys) Unregister(acc string) { mygo.GlobalShortcut.Unregister(acc) }

// mygoClip is the system clipboard through MyGo.
type mygoClip struct{}

func (mygoClip) Read() (transfer.Data, error) { return mygo.Clipboard.Read() }
func (mygoClip) Write(d transfer.Data) error  { return mygo.Clipboard.Write(d) }

// dictationState is dictation's state.
type dictationState struct {
	dictSys  dictation.System
	dictClip dictation.Clipboard
	dictKeys hotkeys // nil until connectDictation (or a test) sets it

	dictShortcut string // the accelerator registered; "" none
	dictRegErr   string // why it could not be registered (translated)
	dictVerified bool   // the shortcut was seen pressed since it was set
	dictEsc      bool   // Escape is registered (the pill shows)

	dict    *dictSession // the current or last dictation; nil before the first
	dictGen int

	dictPill pillWindow
	dictTray trayState
	dictSet  dictSettingsForm // dictation_settings.go
	perms    permissionsState // permissions.go

	// dictSleep waits before the pill hides (tests replace it).
	dictAfter func(d time.Duration, fn func())
}

// dictSession is one dictation.
type dictSession struct {
	gen      int
	target   dictation.Target
	phase    dictPhase
	hold     bool // hold mode: the release stops it
	pressed  time.Time
	released bool // released before the recording started
	rec      *engine.Recording
	out      string
	started  time.Time
	level    float64
	trID     string
	jobID    string
	stage    string // what the transcription does, translated
	text     string // the text delivered
	note     string // why it was copied, an error…: translated
	need     *catalog.Entry
	cancel   context.CancelFunc // clean-up and insertion
}

// openDictation sets dictation's defaults; connectDictation adds the OS
// side when the app is ready.
func (a *app) openDictation() {
	if a.dictSys == nil {
		a.dictSys = dictation.Native()
	}
	if a.dictClip == nil {
		a.dictClip = mygoClip{}
	}
	if a.dictAfter == nil {
		a.dictAfter = func(d time.Duration, fn func()) {
			if a.win == nil {
				return // tests call dictHide themselves
			}
			time.AfterFunc(d, func() { a.post(fn) })
		}
	}
	a.loadDictSettingsForm()
}

// connectDictation registers the shortcut and its release, and makes the
// menu bar icon; it runs in App.WhenReady.
func (a *app) connectDictation() {
	a.dictKeys = mygoHotkeys{}
	win := a.win
	dictation.MainThread = func(fn func()) {
		done := make(chan struct{})
		win.Update(func() {
			defer close(done)
			fn()
		})
		<-done
	}
	if err := a.dictSys.OnHotkeyReleased(func() { a.post(a.dictReleased) }); err != nil && !errors.Is(err, dictation.ErrUnsupported) {
		a.logs.Append(core.LogEntry{Level: core.SeverityWarning, Source: "dictation", Message: "hold-to-talk: " + err.Error()})
	}
	a.applyDictShortcut()
	a.openTray()
	a.connectWindowClose()
	a.recheckPermissions()
}

// closeDictation ends a dictation and releases the shortcut; on quit.
func (a *app) closeDictation() {
	if s := a.dict; s != nil && s.phase.live() {
		a.dictCancel()
	}
	if a.dictKeys != nil && a.dictShortcut != "" {
		a.dictKeys.Unregister(a.dictShortcut)
		a.dictShortcut = ""
	}
	a.closeTray()
}

// applyDictShortcut registers the settings' shortcut (or none), the new
// one before the old one goes, so a refused shortcut keeps the working one.
func (a *app) applyDictShortcut() {
	if a.dictKeys == nil {
		return
	}
	d := a.prefs.Dictation
	want := ""
	if d.Enabled {
		if s, err := dictation.ParseShortcut(d.Shortcut); err == nil {
			want = s.String()
		} else {
			a.dictRegErr = shortcutErrorText(err)
		}
	}
	if want == a.dictShortcut {
		return
	}
	if want != "" {
		if err := a.dictKeys.Register(want, a.dictHotkey); err != nil {
			a.dictRegErr = i18n.T("%s could not be registered: another app may use it. Choose another shortcut.", dictation.GlyphsOf(want))
			return
		}
	}
	if a.dictShortcut != "" {
		a.dictKeys.Unregister(a.dictShortcut)
	}
	a.dictShortcut, a.dictRegErr, a.dictVerified = want, "", false
	a.updateTrayMenu()
}

// dictHotkey is the shortcut's callback (main thread): the app in front
// is captured before anything else shows.
func (a *app) dictHotkey() {
	target := a.dictSys.Frontmost()
	a.post(func() { a.dictPressed(target) })
}

// dictModel is the model dictation transcribes with.
func (a *app) dictModel() catalog.Entry {
	return sttEntry(a.prefs.Dictation.Model, catalog.DefaultSTT)
}

// dictPressed handles a press of the shortcut (or Start in the tray).
func (a *app) dictPressed(target dictation.Target) {
	a.dictVerified = true
	s := a.dict
	if s != nil {
		switch s.phase {
		case dictStarting, dictListening:
			if !s.hold {
				a.dictStop()
			}
			return
		case dictTranscribing, dictCleaning, dictInserting:
			return // one at a time: this one finishes first
		}
	}
	a.dictStart(target, a.prefs.Dictation.Mode == settings.DictationHold)
}

// dictReleased handles the release of the shortcut's keys (hold mode).
func (a *app) dictReleased() {
	s := a.dict
	if s == nil || !s.hold {
		return
	}
	if time.Since(s.pressed) < holdTap {
		s.hold = false // a tap: listen until the next press
		return
	}
	switch s.phase {
	case dictStarting:
		s.released = true
	case dictListening:
		a.dictStop()
	}
}

// dictToggle starts or stops dictation from the tray or the palette.
func (a *app) dictToggle() {
	if s := a.dict; s != nil && (s.phase == dictStarting || s.phase == dictListening) {
		a.dictStop()
		return
	}
	a.dictPressed(a.dictSys.Frontmost())
}

// dictStart records a new dictation for target.
func (a *app) dictStart(target dictation.Target, hold bool) {
	a.dictGen++
	gen := a.dictGen
	out := filepath.Join(a.lib.store.Dir(), "dictation", core.NewID(time.Now())+".wav")
	s := &dictSession{gen: gen, target: target, phase: dictStarting, hold: hold, pressed: time.Now(), out: out, level: -120}
	a.dict = s
	if a.dictSys.Microphone() == dictation.PermissionDenied || a.dictSys.Microphone() == dictation.PermissionRestricted {
		a.dictFail(s, i18n.T("AudioSmith may not use the microphone. Allow it in System Settings → Privacy & Security → Microphone."))
		a.dictShow()
		return
	}
	a.dictShow()
	if a.prefs.Dictation.Sounds {
		a.dictSys.PlaySound("Tink")
	}
	a.dictPreload()
	os.MkdirAll(filepath.Dir(out), 0o755)
	p := proto.CaptureParams{Device: a.prefs.Dictation.Device, Channels: 1, SampleRate: services.TranscriptRate,
		Output: proto.Output{Path: out, Format: "wav", Bits: 16}, MaxSeconds: dictMaxSeconds}
	a.startRecording(p, func(l proto.LevelParams) {
		if a.dict == s {
			s.level = l.RMS
		}
	}, func(rec *engine.Recording, err error) {
		if a.dict != s || s.phase != dictStarting {
			if rec != nil {
				a.background(rec.Cancel)
			}
			return
		}
		if err != nil {
			a.dictFail(s, engineErrorText(err))
			return
		}
		s.rec, s.phase, s.started = rec, dictListening, time.Now()
		a.dictRefresh()
		if s.released {
			a.dictStop()
		}
	})
}

// dictPreload loads the dictation model while the user speaks, so the
// transcription does not wait for it (a models.load job, skipped when the
// model is resident or not downloaded).
func (a *app) dictPreload() {
	e := a.dictModel()
	if e.Repo == "" || e.Repo == a.loadedModel || containsString(a.loaded, e.Repo) || a.engState != engineReady {
		return
	}
	if ready, known := a.modelOnDisk(e); known && !ready {
		return
	}
	params, _ := json.Marshal(proto.LoadParams{Model: e.Repo, Task: "stt"})
	sched := a.lib.sched
	a.async(func() {
		sched.Submit(context.Background(), jobs.Spec{Kind: proto.MethodModelsLoad, Lane: jobs.LaneGPU, Priority: jobs.PriorityInteractive,
			Params: params, Title: i18n.T("Load model"), Owner: "dictation:load"})
	})
}

func containsString(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// dictStop ends the recording and transcribes it.
func (a *app) dictStop() {
	s := a.dict
	if s == nil {
		return
	}
	if s.phase == dictStarting {
		s.released = true
		return
	}
	if s.phase != dictListening || s.rec == nil {
		return
	}
	s.phase, s.stage = dictTranscribing, i18n.T("Transcribing…")
	if a.prefs.Dictation.Sounds {
		a.dictSys.PlaySound("Pop")
	}
	a.dictRefresh()
	rec := s.rec
	a.stopRecording(rec, func(res proto.CaptureResult, err error) {
		if a.dict != s || s.phase != dictTranscribing {
			return
		}
		s.rec = nil
		path := firstOf(res.Output, s.out)
		switch {
		case err != nil:
			a.dictFail(s, engineErrorText(err))
		case res.AudioSeconds < minSpeech:
			os.Remove(path)
			a.dictEnd(s, dictNoSpeech, i18n.T("Nothing was heard. Check the microphone in Settings → Dictation."))
		default:
			s.out = path
			a.dictTranscribe(s, path)
		}
	})
}

// dictTranscribe queues the transcription of a dictation's recording.
func (a *app) dictTranscribe(s *dictSession, path string) {
	d := a.prefs.Dictation
	e := a.dictModel()
	s.trID = core.NewID(time.Now())
	req := services.TranscribeRequest{ID: s.trID, Input: path, Source: core.SourceDictation, RemoveInput: true,
		Mode: settings.TranscribeFast, Model: e.Repo, Language: d.Language, Prompt: d.Vocabulary, App: s.target.Name}
	svc := a.trSvc
	a.async(func() {
		_, id, err := svc.Transcribe(context.Background(), req)
		a.post(func() {
			if a.dict != s {
				return
			}
			if err != nil {
				os.Remove(path)
				if errors.Is(err, core.ErrNotDownloaded) {
					s.need = &e
					a.dictFail(s, i18n.T("%s is not downloaded yet.", e.Label))
					return
				}
				var lic *core.LicenceRequiredError
				if errors.As(err, &lic) {
					a.dictFail(s, i18n.T("The model's licence has not been accepted. Review it on the Models page."))
					return
				}
				a.dictFail(s, err.Error())
				return
			}
			if s.jobID == "" {
				s.jobID = id
			}
		})
	})
}

// dictJobUpdated follows a dictation's transcription; it reports whether
// the job was dictation's.
func (a *app) dictJobUpdated(u jobs.Job) bool {
	s := a.dict
	if s == nil || s.trID == "" || u.Owner != services.TranscriptOwner+s.trID {
		return false
	}
	if s.phase != dictTranscribing {
		return true
	}
	s.jobID = u.ID
	switch u.State {
	case jobs.StateWaiting, jobs.StateQueued:
		s.stage = i18n.T("Waiting for the engine…")
	case jobs.StateRunning:
		s.stage = i18n.T("Transcribing…")
		if u.Progress.Stage == "loading" {
			s.stage = i18n.T("Loading model…")
		}
	case jobs.StateSucceeded:
		var res services.TranscribeResult
		json.Unmarshal(u.Result, &res)
		a.dictDeliver(s, res.Transcript)
	case jobs.StateCancelled:
		os.Remove(s.out)
		a.dictEnd(s, dictIdle, "")
	default:
		os.Remove(s.out) // the recording, if the job did not take it
		if u.Err != nil && u.Err.Code == proto.CodeModelNotDownloaded {
			e := a.dictModel()
			s.need = &e
		}
		a.dictFail(s, jobErrorText(u.Err))
	}
	a.dictRefresh()
	return true
}

// dictDeliver polishes (and cleans) the text, then types it.
func (a *app) dictDeliver(s *dictSession, t core.Transcript) {
	raw := strings.TrimSpace(t.Text())
	if raw == "" {
		svc := a.trSvc
		a.async(func() { svc.Delete(context.Background(), t.ID) })
		a.dictEnd(s, dictNoSpeech, i18n.T("Nothing was heard. Check the microphone in Settings → Dictation."))
		return
	}
	d := a.prefs.Dictation
	s.phase = dictCleaning
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	cleaner := &translate.Cleaner{Router: a.llmRouter}
	_, who := a.llmAvailable()
	req := translate.CleanupRequest{Text: raw, Lang: t.Language, Vocabulary: vocabulary(d.Vocabulary), NoLLM: !d.Cleanup}
	in := dictation.Inserter{Sys: a.dictSys, Clip: a.dictClip, CopyOnly: d.Output == settings.OutputCopy, Sleep: dictSleep}
	target := s.target
	svc := a.trSvc
	a.async(func() {
		res := cleaner.Clean(ctx, req)
		text := strings.TrimSpace(res.Text)
		if text == "" {
			text = raw
		}
		a.post(func() {
			if a.dict == s && s.phase == dictCleaning {
				s.phase = dictInserting
				a.dictRefresh()
			}
		})
		if ctx.Err() != nil {
			return
		}
		out := in.Insert(ctx, target, text)
		by := "polish"
		if res.LLM {
			by = who
		}
		svc.Update(context.Background(), t.ID, func(t *core.Transcript) {
			if text != t.Text() {
				t.Clean, t.CleanBy = text, by
			}
			t.Inserted = out.Outcome == dictation.Inserted
		})
		a.post(func() {
			if a.dict != s {
				return
			}
			s.text = text
			if out.Outcome == dictation.Inserted {
				a.dictEnd(s, dictInserted, "")
				return
			}
			a.dictEnd(s, dictCopied, copiedReason(out))
		})
	})
}

// copiedReason says why a dictation was copied rather than typed.
func copiedReason(r dictation.Result) string {
	switch r.Reason {
	case dictation.ReasonCopyOnly:
		return ""
	case dictation.ReasonNoAccess:
		return i18n.T("To type into apps, allow AudioSmith in Privacy & Security → Accessibility.")
	case dictation.ReasonNoTarget:
		return i18n.T("No app to type into: paste it where you want it.")
	case dictation.ReasonNotActivated:
		return i18n.T("The app did not come back to the front: paste it there.")
	case dictation.ReasonPasteFailed:
		return i18n.T("Typing into the app failed: paste it there.")
	case dictation.ReasonClipboardError:
		return i18n.T("The clipboard could not be written.")
	}
	return ""
}

// dictFail ends a dictation with an error.
func (a *app) dictFail(s *dictSession, msg string) {
	if s.rec != nil {
		rec := s.rec
		s.rec = nil
		a.background(rec.Cancel)
	}
	if a.prefs.Dictation.Sounds {
		a.dictSys.PlaySound("Basso")
	}
	a.dictEnd(s, dictError, msg)
}

// dictEnd puts a dictation in an end state; the pill hides by itself
// (errors stay until closed or the next dictation).
func (a *app) dictEnd(s *dictSession, phase dictPhase, note string) {
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.phase, s.note = phase, note
	a.dictEscape(false)
	a.updateTray()
	a.dictRefresh()
	var wait time.Duration
	switch phase {
	case dictIdle:
		a.dictHide()
		return
	case dictInserted:
		wait = hideInserted
	case dictCopied:
		wait = hideCopied
	case dictNoSpeech:
		wait = hideNoSpeech
	default:
		return
	}
	gen := s.gen
	a.dictAfter(wait, func() {
		if a.dict == s && s.gen == gen && !s.phase.live() {
			a.dictHide()
		}
	})
}

// dictCancel drops the dictation: the recording, its transcription, its
// insertion if it has not started typing.
func (a *app) dictCancel() {
	s := a.dict
	if s == nil {
		return
	}
	if s.rec != nil {
		rec := s.rec
		s.rec = nil
		a.background(rec.Cancel)
	}
	if s.jobID != "" && s.phase == dictTranscribing {
		a.cancelJob(s.jobID)
	}
	if s.phase == dictStarting || s.phase == dictListening {
		os.Remove(s.out)
	}
	a.dictEnd(s, dictIdle, "")
}

// dictTranscriptChanged: nothing to do yet (the pill shows the text it
// delivered); kept as the place for live updates of a dictation's entry.
func (a *app) dictTranscriptChanged(core.TranscriptChanged) {}

// dictShow shows the pill and the recording state of the tray, and lets
// Escape cancel.
func (a *app) dictShow() {
	a.showPill()
	a.dictEscape(true)
	a.updateTray()
}

// dictHide hides the pill.
func (a *app) dictHide() {
	a.dictEscape(false)
	a.hidePill()
	a.updateTray()
}

// dictEscape registers Escape (cancel) while a dictation works: the pill
// never takes the keyboard, so Escape has to be a global shortcut then.
func (a *app) dictEscape(on bool) {
	if a.dictKeys == nil || on == a.dictEsc {
		return
	}
	if on {
		if err := a.dictKeys.Register("Escape", func() { a.post(a.dictCancel) }); err == nil {
			a.dictEsc = true
		}
		return
	}
	a.dictKeys.Unregister("Escape")
	a.dictEsc = false
}

// dictRefresh redraws the pill after a change.
func (a *app) dictRefresh() {
	if w := a.dictPill.win; w != nil {
		w.Invalidate()
	}
}

// dictStatus is a dictation's state as the pill says it.
func (s *dictSession) status() string {
	switch s.phase {
	case dictStarting:
		return i18n.T("Starting…")
	case dictListening:
		return i18n.T("Listening…")
	case dictTranscribing:
		return s.stage
	case dictCleaning:
		return i18n.T("Cleaning up…")
	case dictInserting:
		return i18n.T("Typing…")
	case dictInserted:
		return i18n.T("Inserted")
	case dictCopied:
		return i18n.T("Copied to the clipboard")
	case dictNoSpeech:
		return i18n.T("No speech detected")
	case dictError:
		return i18n.T("Dictation failed")
	}
	return ""
}

// shortcutErrorText explains a shortcut the parser refused.
func shortcutErrorText(err error) string {
	switch {
	case errors.Is(err, dictation.ErrNoModifier):
		return i18n.T("Add a modifier: ⌘, ⌃ or ⌥ with another key.")
	case errors.Is(err, dictation.ErrEmpty):
		return i18n.T("No shortcut is set.")
	}
	return i18n.T("This key cannot be a global shortcut.")
}
