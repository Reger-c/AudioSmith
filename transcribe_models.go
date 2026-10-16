package main

// Transcribe's models: the speech-to-text entries, the model and languages
// of a run, the speakers model, the aligner and language names; split from
// transcribe.go.

import (
	"slices"
	"strings"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/lang"
)

// sttEntries are the speech-to-text models (hidden ones included: the
// catalogue hides models the app did not run end to end yet), without
// the speakers model, which Transcribe's Speakers option and the dub use
// (too slow for dictation and calls).
func sttEntries() []catalog.Entry {
	var out []catalog.Entry
	for _, e := range catalog.Builtin().Entries {
		if slices.Contains(e.Tasks, catalog.TaskSTT) && !e.Has(catalog.CapSpeakers) {
			out = append(out, e)
		}
	}
	return out
}

// sttEntry is the speech-to-text model a setting names (id, a catalogue
// id), else the default for language (BCP-47, "" = detected): Qwen3-ASR,
// or Whisper for a language Qwen3-ASR does not know (catalog.STTFor,
// thread #46). A removed model's id (Parakeet) gets the default too, and
// so does the speakers model (only the Speakers option uses it).
func sttEntry(id, language string) catalog.Entry {
	c := catalog.Builtin()
	if e, ok := c.ByID(id); ok && slices.Contains(e.Tasks, catalog.TaskSTT) && !e.Has(catalog.CapSpeakers) {
		return e
	}
	return c.STTFor(language, false)
}

// trModel is the model of a mode for the page's language: both modes
// use the same one (Settings → Transcribe may name one for each); Fast
// skips the word timings. With Speakers on, the speakers model (thread
// #48) in both modes.
func (a *app) trModel(fast bool) catalog.Entry {
	if e, ok := a.trSpeakersModel(); ok {
		return e
	}
	if fast {
		return sttEntry(a.prefs.Transcribe.FastModel, a.prefs.Transcribe.Language)
	}
	return sttEntry(a.prefs.Transcribe.AccurateModel, a.prefs.Transcribe.Language)
}

// trLanguages are the languages the page can transcribe in a mode: the
// chosen model's, or, when it follows the language (no model in the
// settings), every language a speech-to-text model knows; nil = any.
func (a *app) trLanguages(fast bool) []string {
	if e, ok := a.trSpeakersModel(); ok {
		return sttLanguages(e)
	}
	id := a.prefs.Transcribe.AccurateModel
	if fast {
		id = a.prefs.Transcribe.FastModel
	}
	if e, ok := catalog.Builtin().ByID(id); ok && slices.Contains(e.Tasks, catalog.TaskSTT) {
		return sttLanguages(e)
	}
	var out []string
	for _, e := range sttEntries() {
		l := sttLanguages(e)
		if l == nil {
			return nil
		}
		for _, code := range l {
			if !slices.Contains(out, code) {
				out = append(out, code)
			}
		}
	}
	return out
}

// trSpeakersModel is the speakers model when the page's Speakers option
// is on.
func (a *app) trSpeakersModel() (catalog.Entry, bool) {
	if !a.prefs.Transcribe.Speakers {
		return catalog.Entry{}, false
	}
	return catalog.Builtin().SpeakersModel()
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
