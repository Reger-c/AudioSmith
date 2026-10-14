package main

// Transcribe (/transcripts, M2): speech to text from a file (any audio or
// video) or a recording, Fast (a smaller model, no word timings) or
// Accurate (the default speech-to-text model, then the forced aligner for
// word timings), in a language picked or detected. Transcripts are kept
// in the library (services.Transcripts over migration 003's table) with
// their 16 kHz audio, so a click on a word seeks the player there. Edit a
// sentence, clean the text up (deterministic polish, or an LLM:
// transcribe_llm.go), export TXT, SRT, VTT or JSON, copy, or reuse it as
// the Studio's script. Dictations (dictation.go) land here too.
//
// Files: transcribe.go (state, actions), transcribe_view.go (the page),
// transcribe_llm.go (language models: clean-up, Settings → Writing help).

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/lang"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/settings"
	"github.com/hehehai/AudioSmith/internal/translate"
)

// fastSTT is the Fast mode's model until the catalogue lists a smaller
// dictation model (thread #14): Qwen3-ASR 0.6B, then the default.
const fastSTT = "qwen3-asr-0.6b-4bit"

// transcribeState is the Transcribe page's view state.
type transcribeState struct {
	trSvc *services.Transcripts

	trList    []core.Transcript // the history, newest first, without word timings
	trLoaded  bool
	trQuery   string
	trFound   []core.Transcript // search results; nil when not searching
	trSearch  int               // bumps with each search: older answers are dropped
	trListUI  ui.ListState
	trSel     string           // the transcript shown; "" = the new-transcript panel
	trDetail  *core.Transcript // the whole transcript shown (word timings)
	trJob     *trJob           // the transcription running
	trErr     string
	trRetry   *services.TranscribeRequest
	trNeed    *catalog.Entry // a model the last attempt found missing
	trWarn    string         // about the transcript shown (word timings missing…)
	trMic     micState
	trMode    int // 0 fast, 1 accurate (bound to the Segmented)
	trLang    trLangPicker
	trEdit    int    // segment being edited; -1 none
	trEditBuf string // its text
	trOrig    bool   // the original shows under the clean-up
	trClean   *trCleaning
	trExport  bool   // the export menu is open
	trConfirm string // id the delete confirmation asks about
	trDevice  string // the recorder's device label (bound to its Select)
	trShown   int    // segments shown (long transcripts show more on demand)
}

// trPage is how many more segments "Show more" shows.
const trPage = 200

// trJob is the transcription the page follows.
type trJob struct {
	id, owner, trID string
	title           string
	stage           string
	fraction        float64
	started         time.Time
	cancelling      bool
}

// trCleaning is a clean-up of the transcript shown, running.
type trCleaning struct {
	id     string
	cancel context.CancelFunc
}

type trLangPicker struct {
	open  bool
	query string
	list  ui.ListState
}

// openTranscripts makes the transcript service over the library (its
// executor on the scheduler; engine calls through the supervisor that
// connectEngine makes) and follows its events and jobs.
func (a *app) openTranscripts(lib *library) {
	a.trEdit = -1
	a.trMode = 1
	if a.prefs.Transcribe.Mode == settings.TranscribeFast {
		a.trMode = 0
	}
	a.trSvc = services.NewTranscripts(services.TranscriptsConfig{
		Store: lib.store.Transcripts(), Bus: lib.bus, Scheduler: lib.sched, Licences: lib.licences,
		ModelPath: lib.modelSnapshot,
		Engine: func(method string) jobs.Executor {
			if a.eng == nil || a.eng.sup == nil {
				return nil
			}
			return engine.Executor(a.eng.sup, proto.RoleOf(method), method)
		},
	})
	core.On(lib.bus, func(ev core.TranscriptChanged) { a.post(func() { a.transcriptChanged(ev) }) })
	core.On(lib.bus, func(u jobs.Updated) {
		if u.Chunk == nil && strings.HasPrefix(u.Job.Owner, services.TranscriptOwner) {
			a.post(func() { a.transcriptJobUpdated(u.Job) })
		}
	})
	a.loadTranscripts()
}

// loadTranscripts reads the history in the background.
func (a *app) loadTranscripts() {
	svc := a.trSvc
	a.background(func() {
		l, err := svc.List(context.Background(), core.TranscriptQuery{})
		a.post(func() {
			if err != nil {
				a.toast(i18n.T("Could not read the transcripts: %v", err))
				return
			}
			for _, t := range a.trList { // added by events while reading
				if !slices.ContainsFunc(l, func(x core.Transcript) bool { return x.ID == t.ID }) {
					l = append(l, t)
				}
			}
			sortTranscripts(l)
			a.trList, a.trLoaded = l, true
		})
	})
}

func sortTranscripts(l []core.Transcript) {
	slices.SortStableFunc(l, func(x, y core.Transcript) int {
		if c := y.CreatedAt.Compare(x.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(y.ID, x.ID)
	})
}

// briefTranscript is t without word timings, as the list keeps it.
func briefTranscript(t core.Transcript) core.Transcript {
	segs := slices.Clone(t.Segments)
	for i := range segs {
		segs[i].Words = nil
	}
	t.Segments = segs
	return t
}

// transcriptChanged applies a saved or deleted transcript.
func (a *app) transcriptChanged(ev core.TranscriptChanged) {
	t := ev.Transcript
	i := slices.IndexFunc(a.trList, func(x core.Transcript) bool { return x.ID == t.ID })
	switch {
	case ev.Deleted:
		if i >= 0 {
			a.trList = slices.Delete(a.trList, i, i+1)
		}
		a.trFound = slices.DeleteFunc(a.trFound, func(x core.Transcript) bool { return x.ID == t.ID })
		if a.trSel == t.ID {
			a.trSel, a.trDetail, a.trEdit = "", nil, -1
		}
	case i >= 0:
		a.trList[i] = briefTranscript(t)
	default:
		a.trList = append(a.trList, briefTranscript(t))
		sortTranscripts(a.trList)
	}
	if !ev.Deleted {
		if j := slices.IndexFunc(a.trFound, func(x core.Transcript) bool { return x.ID == t.ID }); j >= 0 {
			a.trFound[j] = briefTranscript(t)
		}
		if a.trSel == t.ID {
			full := t
			a.trDetail = &full
		}
	}
	a.dictTranscriptChanged(ev) // dictation.go
}

// shownTranscripts is the history list: search results while searching.
func (a *app) shownTranscripts() []core.Transcript {
	if strings.TrimSpace(a.trQuery) != "" && a.trFound != nil {
		return a.trFound
	}
	return a.trList
}

// searchTranscripts runs the history search in the background.
func (a *app) searchTranscripts() {
	q := strings.TrimSpace(a.trQuery)
	a.trSearch++
	gen := a.trSearch
	if q == "" {
		a.trFound = nil
		return
	}
	svc := a.trSvc
	a.background(func() {
		l, err := svc.List(context.Background(), core.TranscriptQuery{Search: q})
		a.post(func() {
			if gen != a.trSearch || err != nil {
				return
			}
			if l == nil {
				l = []core.Transcript{}
			}
			a.trFound = l
		})
	})
}

// selectTranscript shows a transcript ("" = the new-transcript panel),
// reading the whole of it (word timings) in the background.
func (a *app) selectTranscript(id string) {
	if a.trSel == id && (id == "" || a.trDetail != nil) {
		return
	}
	a.stopTranscriptPlayback()
	a.trSel, a.trDetail, a.trEdit, a.trOrig, a.trExport, a.trWarn = id, nil, -1, false, false, ""
	a.trShown = trPage
	if id == "" {
		return
	}
	svc := a.trSvc
	a.background(func() {
		t, err := svc.Get(context.Background(), id)
		a.post(func() {
			if a.trSel != id {
				return
			}
			if err != nil {
				a.toast(i18n.T("Could not read the transcript: %v", err))
				return
			}
			a.trDetail = &t
		})
	})
}

// --- models --------------------------------------------------------------

// sttEntries are the speech-to-text models (hidden ones included: the
// catalogue hides models the app did not run end to end yet).
func sttEntries() []catalog.Entry {
	var out []catalog.Entry
	for _, e := range catalog.Builtin().Entries {
		if slices.Contains(e.Tasks, catalog.TaskSTT) {
			out = append(out, e)
		}
	}
	return out
}

// sttEntry is the catalogue entry of id, else of fallback.
func sttEntry(id, fallback string) catalog.Entry {
	c := catalog.Builtin()
	if e, ok := c.ByID(id); ok && slices.Contains(e.Tasks, catalog.TaskSTT) {
		return e
	}
	if e, ok := c.ByID(fallback); ok {
		return e
	}
	e, _ := c.ByID(catalog.DefaultSTT)
	return e
}

// trModel is the model of a mode: Fast's smaller one, or Accurate's.
func (a *app) trModel(fast bool) catalog.Entry {
	if fast {
		return sttEntry(a.prefs.Transcribe.FastModel, fastSTT)
	}
	return sttEntry(a.prefs.Transcribe.AccurateModel, catalog.DefaultSTT)
}

// trAligner is the aligner Accurate runs after a model that does not
// time words itself; "" when none is needed.
func trAligner(e catalog.Entry) catalog.Entry {
	if e.Has(catalog.CapWordTimes) {
		return catalog.Entry{}
	}
	al, _ := catalog.Builtin().ByID(catalog.DefaultAlign)
	return al
}

// modelReady reports whether e is on disk, as the Models page last read it.
func (a *app) modelOnDisk(e catalog.Entry) (ready, known bool) {
	m := a.model(e)
	return m.known && m.local.State == hub.Downloaded, m.known
}

// sttLanguages are the codes a speech-to-text model takes; nil = any.
func sttLanguages(e catalog.Entry) []string {
	if e.Family == "whisper" {
		return lang.WhisperCodes()
	}
	var out []string
	for _, l := range e.Languages {
		out = append(out, l.Code)
	}
	return out
}

// langName shows a language: flag and name, or the code.
func langName(code string) string {
	if code == "" {
		return i18n.T("Auto-detect")
	}
	if l, ok := lang.Lookup(code); ok {
		return strings.TrimSpace(l.Flag() + " " + l.NameIn(i18n.Locale()))
	}
	return code
}

// --- transcribing ------------------------------------------------------------

// transcribeExts are the files the page takes.
var transcribeExts = []string{"wav", "mp3", "m4a", "aac", "flac", "ogg", "opus", "aiff", "aif", "caf", "webm", "mp4", "mov", "m4v", "mkv"}

// chooseTranscribeFile asks for an audio or video file.
func (a *app) chooseTranscribeFile() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent:  a.win,
			Title:   i18n.T("Transcribe a file"),
			Filters: []mygo.FileFilter{{Name: i18n.T("Audio or video"), Extensions: transcribeExts}},
		})
		if err != nil || len(paths) == 0 {
			return
		}
		a.post(func() { a.transcribeFile(paths[0]) })
	}()
}

// transcribeDropped takes the first audio or video file dropped.
func (a *app) transcribeDropped(paths []string) {
	for _, p := range paths {
		if slices.Contains(transcribeExts, strings.ToLower(strings.TrimPrefix(filepath.Ext(p), "."))) {
			a.transcribeFile(p)
			return
		}
	}
	a.toast(i18n.T("Drop an audio or video file."))
}

// transcribeFile transcribes a file in the page's mode and language.
func (a *app) transcribeFile(path string) {
	a.startTranscription(services.TranscribeRequest{Input: path, Source: core.SourceFile, Title: filepath.Base(path)})
}

// transcribeRecording transcribes what the page's recorder recorded.
func (a *app) transcribeRecording(path string) {
	a.startTranscription(services.TranscribeRequest{Input: path, Source: core.SourceRecording, RemoveInput: true})
}

// startTranscription fills the request with the page's mode, model,
// aligner and language and queues it; the page then follows its job.
func (a *app) startTranscription(req services.TranscribeRequest) {
	if a.trJob != nil {
		a.toast(i18n.T("A transcription is running. Wait for it, or cancel it."))
		return
	}
	fast := a.trMode == 0
	e := a.trModel(fast)
	req.Model, req.Mode, req.Language = e.Repo, settings.TranscribeFast, a.prefs.Transcribe.Language
	req.Words = e.Has(catalog.CapWordTimes)
	if !fast {
		req.Mode = settings.TranscribeAccurate
		if al := trAligner(e); al.Repo != "" {
			if ready, known := a.modelOnDisk(al); ready || !known {
				req.Aligner = al.Repo
			}
		}
	}
	a.queueTranscription(req, false)
}

// queueTranscription submits req; retry says it is the same request again.
func (a *app) queueTranscription(req services.TranscribeRequest, retry bool) {
	if req.ID == "" || retry {
		req.ID = core.NewID(time.Now())
	}
	a.trErr, a.trRetry, a.trNeed, a.trWarn = "", nil, nil, ""
	title := req.Title
	if title == "" {
		title = i18n.T("Recording")
	}
	owner := services.TranscriptOwner + req.ID
	a.trJob = &trJob{owner: owner, trID: req.ID, title: title, stage: i18n.T("Starting…"), fraction: -1, started: time.Now()}
	a.selectTranscript("")
	svc := a.trSvc
	a.async(func() {
		_, id, err := svc.Transcribe(context.Background(), req)
		a.post(func() {
			j := a.trJob
			if j == nil || j.owner != owner {
				return
			}
			if err != nil {
				a.trJob = nil
				a.transcriptRefused(req, err)
				return
			}
			if j.id == "" {
				j.id = id
			}
			if j.cancelling {
				a.cancelJob(id)
			}
		})
	})
}

// transcriptRefused explains why a transcription did not start, and
// offers the fix (a download, the licence).
func (a *app) transcriptRefused(req services.TranscribeRequest, err error) {
	var lic *core.LicenceRequiredError
	switch {
	case errors.As(err, &lic):
		e := entryFor(lic.Model)
		a.askLicence(e, func() { a.queueTranscription(req, true) })
	case errors.Is(err, core.ErrNotDownloaded):
		repo := req.Model
		if strings.HasPrefix(err.Error(), "aligner:") {
			repo = req.Aligner
		}
		e := entryFor(repo)
		a.trErr = i18n.T("%s is not downloaded yet.", e.Label)
		a.trNeed = &e
		a.trRetry = &req
	default:
		a.trErr = err.Error()
		a.trRetry = &req
	}
	if req.RemoveInput && a.trRetry == nil {
		os.Remove(req.Input)
	}
}

// transcriptJobUpdated follows transcription jobs: the page's, and
// dictation's (dictation.go).
func (a *app) transcriptJobUpdated(u jobs.Job) {
	if a.dictJobUpdated(u) { // dictation.go
		return
	}
	j := a.trJob
	if j == nil || u.Owner != j.owner {
		return
	}
	j.id = u.ID
	switch u.State {
	case jobs.StateWaiting, jobs.StateQueued:
		if !j.cancelling {
			j.stage = i18n.T("Waiting for the engine…")
		}
	case jobs.StateRunning:
		if !j.cancelling && u.Progress.Stage != "" {
			j.stage = stageText(u.Progress.Stage)
		}
		j.fraction = u.Progress.Fraction
	case jobs.StateSucceeded:
		a.trJob = nil
		var res services.TranscribeResult
		if json.Unmarshal(u.Result, &res) == nil && res.Transcript.ID != "" {
			a.transcriptChanged(core.TranscriptChanged{Transcript: res.Transcript})
			a.selectTranscript("")
			full := res.Transcript
			a.trSel, a.trDetail = full.ID, &full
			if res.AlignError != nil {
				a.trWarn = i18n.T("Word timings are missing: %s", jobErrorText(res.AlignError))
			}
		}
	case jobs.StateCancelled:
		a.trJob = nil
		a.toast(i18n.T("Cancelled"))
	case jobs.StateFailed, jobs.StateInterrupted:
		a.trJob = nil
		a.trErr = jobErrorText(u.Err)
		var req services.TranscribeRequest
		if json.Unmarshal(u.Params, &req) == nil {
			if req.RemoveInput {
				// The recording is still there unless its audio was kept.
				if _, err := os.Stat(req.Input); err == nil {
					a.trRetry = &req
				}
			} else {
				a.trRetry = &req
			}
			if u.Err != nil && u.Err.Code == proto.CodeModelNotDownloaded {
				var d struct{ Model string }
				json.Unmarshal(u.Err.Data, &d)
				if d.Model == "" {
					d.Model = req.Model
				}
				e := entryFor(d.Model)
				a.trNeed = &e
			}
		}
	}
}

// cancelTranscription cancels the page's transcription.
func (a *app) cancelTranscription() {
	j := a.trJob
	if j == nil {
		return
	}
	j.cancelling, j.stage = true, i18n.T("Cancelling…")
	if j.id != "" {
		a.cancelJob(j.id)
	}
}

// --- the transcript shown ----------------------------------------------------

// updateTranscript changes the transcript shown and saves it.
func (a *app) updateTranscript(fn func(*core.Transcript)) {
	t := a.trDetail
	if t == nil {
		return
	}
	fn(t)
	id := t.ID
	svc := a.trSvc
	a.async(func() {
		if _, err := svc.Update(context.Background(), id, fn); err != nil {
			a.post(func() { a.toast(i18n.T("Could not save the transcript: %v", err)) })
		}
	})
}

// editSegment opens segment i for editing.
func (a *app) editSegment(i int) {
	if a.trDetail == nil || i < 0 || i >= len(a.trDetail.Segments) {
		return
	}
	a.saveSegmentEdit()
	a.trEdit, a.trEditBuf = i, a.trDetail.Segments[i].Text
}

// saveSegmentEdit keeps the segment being edited: its words lose their
// timings when its text changed (exports estimate them).
func (a *app) saveSegmentEdit() {
	i := a.trEdit
	a.trEdit = -1
	t := a.trDetail
	if t == nil || i < 0 || i >= len(t.Segments) {
		return
	}
	text := strings.TrimSpace(a.trEditBuf)
	if text == t.Segments[i].Text {
		return
	}
	a.updateTranscript(func(t *core.Transcript) {
		if i < len(t.Segments) {
			t.Segments[i].Text, t.Segments[i].Edited = text, true
		}
	})
}

// deleteTranscript deletes a transcript and its audio.
func (a *app) deleteTranscript(id string) {
	a.trConfirm = ""
	if a.trSel == id {
		a.stopTranscriptPlayback()
	}
	svc := a.trSvc
	a.async(func() {
		err := svc.Delete(context.Background(), id)
		a.post(func() {
			if err != nil {
				a.toast(i18n.T("Could not delete the transcript: %v", err))
				return
			}
			a.toast(i18n.T("Transcript deleted"))
		})
	})
}

// transcriptTitle names a transcript in lists.
func transcriptTitle(t core.Transcript) string {
	switch {
	case t.Title != "":
		return t.Title
	case t.Source == core.SourceDictation && t.App != "":
		return i18n.T("Dictation in %s", t.App)
	case t.Source == core.SourceDictation:
		return i18n.T("Dictation")
	}
	return i18n.T("Recording")
}

// exportName is a file name for a transcript's export.
func transcriptFileName(t core.Transcript, ext string) string {
	base := strings.TrimSuffix(t.Title, filepath.Ext(t.Title))
	if base == "" {
		base = "transcript-" + t.CreatedAt.Format("2006-01-02-1504")
	}
	return base + "." + ext
}

// exportTranscript saves the transcript shown as format.
func (a *app) exportTranscript(format string) {
	a.trExport = false
	t := a.trDetail
	if t == nil {
		return
	}
	data, err := services.Export(*t, format)
	if err != nil {
		a.toast(i18n.T("Export failed: %v", err))
		return
	}
	a.saveFile(i18n.T("Export the transcript"), transcriptFileName(*t, format), format, data)
}

// exportAllTranscripts saves every transcript as one text file.
func (a *app) exportAllTranscripts() {
	svc := a.trSvc
	a.background(func() {
		l, err := svc.List(context.Background(), core.TranscriptQuery{})
		a.post(func() {
			if err != nil {
				a.toast(i18n.T("Export failed: %v", err))
				return
			}
			data := services.ExportAll(l, i18n.DateTime)
			a.saveFile(i18n.T("Export all transcripts"), "transcripts_"+time.Now().Format("2006-01-02")+".txt", "txt", data)
		})
	})
}

// saveFile asks where to save data, then writes it (a dialog blocks: on
// a goroutine of its own). In tests (no window) it writes to
// a.trExportDir.
func (a *app) saveFile(title, name, ext string, data []byte) {
	write := func(dst string) {
		err := os.WriteFile(dst, data, 0o644)
		a.post(func() {
			if err != nil {
				a.toast(i18n.T("Export failed: %v", err))
			} else {
				a.toast(i18n.T("Exported %s", filepath.Base(dst)))
			}
		})
	}
	if a.win == nil {
		if trExportDir != "" {
			write(filepath.Join(trExportDir, name))
		}
		return
	}
	go func() {
		dst, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
			Parent: a.win, Title: title, DefaultPath: name,
			Filters: []mygo.FileFilter{{Name: strings.ToUpper(ext), Extensions: []string{ext}}},
		})
		if err != nil || dst == "" {
			return
		}
		write(dst)
	}()
}

// trExportDir is where exports go in tests.
var trExportDir string

// reuseTranscript makes the transcript the Studio's script.
func (a *app) reuseTranscript() {
	t := a.trDetail
	if t == nil {
		return
	}
	a.text = t.Preferred()
	a.script.set(a.text)
	if t.Language != "" {
		// The Studio's model takes its own language values.
		p := a.preset()
		codes, valueOf, all := languageSupport(p)
		if mc, ok := lang.Match(codes, t.Language); ok || all {
			if all {
				mc = t.Language
			}
			a.languageChosen(t.Language, valueOf(mc))
		}
	}
	a.navigate("/studio")
	a.toast(i18n.T("The transcript is the Studio's script."))
}

// polishTranscript cleans the text without a language model.
func (a *app) polishTranscript() {
	t := a.trDetail
	if t == nil {
		return
	}
	clean := translate.Polish(t.Text(), t.Language, translate.PolishOptions{})
	a.updateTranscript(func(t *core.Transcript) { t.Clean, t.CleanBy = clean, "polish" })
	a.trOrig = false
}

// dropClean removes the clean-up: the transcript reads as its segments.
func (a *app) dropClean() {
	a.updateTranscript(func(t *core.Transcript) { t.Clean, t.CleanBy = "", "" })
}

// --- playback ----------------------------------------------------------------

// trPlayID is the player's id for a transcript's audio.
func trPlayID(id string) string { return "transcript:" + id }

// playTranscriptAt plays the transcript shown from second s.
func (a *app) playTranscriptAt(s float64) {
	t := a.trDetail
	if t == nil || t.Audio == "" || t.Duration <= 0 {
		return
	}
	id := trPlayID(t.ID)
	if a.pl.Current() != id {
		if err := a.pl.Toggle(id, a.trSvc.AudioPath(*t)); err != nil {
			a.toast(i18n.T("Cannot play: %v", err))
			return
		}
	} else if !a.pl.Playing(id) {
		a.pl.Toggle(id, a.trSvc.AudioPath(*t))
	}
	a.pl.Seek(id, float32(min(max(s/t.Duration, 0), 1)))
}

// toggleTranscriptPlayback plays or pauses the transcript shown.
func (a *app) toggleTranscriptPlayback() {
	t := a.trDetail
	if t == nil || t.Audio == "" {
		return
	}
	if err := a.pl.Toggle(trPlayID(t.ID), a.trSvc.AudioPath(*t)); err != nil {
		a.toast(i18n.T("Cannot play: %v", err))
	}
}

// stopTranscriptPlayback stops the transcript's audio, if it plays.
func (a *app) stopTranscriptPlayback() {
	if strings.HasPrefix(a.pl.Current(), "transcript:") {
		a.pl.Stop()
	}
}

// trPosition is where the transcript shown is playing, in seconds; -1
// when it is not loaded in the player.
func (a *app) trPosition() float64 {
	t := a.trDetail
	if t == nil || a.pl.Current() != trPlayID(t.ID) {
		return -1
	}
	return float64(a.pl.Progress(trPlayID(t.ID))) * t.Duration
}

// trRecMaxSeconds bounds a recording (4 hours).
const trRecMaxSeconds = 4 * 3600

// trRecStart records for a transcript, mono at 16 kHz, into the
// transcript directory.
func (a *app) trRecStart() {
	m := &a.trMic
	if m.phase != "" || a.trJob != nil {
		return
	}
	a.loadDevices()
	out := filepath.Join(a.lib.store.Transcripts().AudioDir(), "rec-"+core.NewID(time.Now())+".wav")
	m.phase, m.err, m.level, m.out = "starting", "", -120, out
	os.MkdirAll(filepath.Dir(out), 0o755)
	p := proto.CaptureParams{Device: a.prefs.Transcribe.Device, Channels: 1, SampleRate: services.TranscriptRate,
		Output: proto.Output{Path: out, Format: "wav", Bits: 16}, MaxSeconds: trRecMaxSeconds}
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

// trRecStop ends the recording and transcribes it.
func (a *app) trRecStop() {
	m := &a.trMic
	if m.phase != "recording" || m.rec == nil {
		return
	}
	m.phase = "stopping"
	rec, out := m.rec, m.out
	a.stopRecording(rec, func(res proto.CaptureResult, err error) {
		if m.out != out {
			return
		}
		m.rec, m.phase, m.level = nil, "", -120
		path := firstOf(res.Output, out)
		switch {
		case err != nil:
			m.err = engineErrorText(err)
		case res.AudioSeconds < 0.5:
			os.Remove(path)
			m.err = i18n.T("Recording too short.")
		default:
			if res.Reason == proto.CaptureDeviceLost {
				a.toast(i18n.T("The microphone went away: what was recorded is kept."))
			}
			a.transcribeRecording(path)
		}
	})
}

// trRecEnded notices a recording that ended by itself (the device went
// away, the time limit) and finishes it as Stop does.
func (a *app) trRecEnded() {
	m := &a.trMic
	if m.phase != "recording" || m.rec == nil {
		return
	}
	select {
	case <-m.rec.Done():
		a.trRecStop()
	default:
	}
}
