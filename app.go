package main

import (
	"github.com/egoist/mygo"
)

// app is AudioSmith's UI state: the view state of every page, and handles
// to the domain (store, engine). Every field is read and written on the
// main thread: work on other goroutines reaches it through post.
//
// Each area keeps its fields in a struct of its own, embedded here, in the
// file of its area (docs/architecture.md §1):
//
//	shellState   shell.go      window chrome, navigation, settings, models, jobs
//	studioState  studio.go     the TTS form
//	takesState   takes.go      the library (domain), take snapshot, player, peaks
//	engineLink   enginelink.go engines and the Studio's current job
//	voicesState  voices.go     voice library, clone flow, design, gallery
//
// Add fields to your area's struct, not here.
type app struct {
	win    *mygo.Window // nil in tests
	toasts []string     // shown by the next build of the view
	// postQueue replaces the window's queue in tests that run real
	// goroutines (scheduler, supervisor): they pump it on the test's
	// goroutine. nil: post runs fn at once (other tests).
	postQueue func(func())

	shellState
	studioState
	takesState
	engineLink
	voicesState // voices.go (thread V): voice library, design, gallery
	// M2 (thread #17): transcribe*.go, dictation*.go, permissions.go.
	transcribeState
	dictationState
	llmState
}

func newApp(eng *engineHost, lib *library) *app {
	a := &app{
		shellState:  newShellState(),
		studioState: newStudioState(lib.settings.Get()),
	}
	a.engineLink.eng = eng
	a.openShell(lib)
	a.openTakes(lib)
	a.openVoices(lib) // voices.go (thread V)
	// M2 (thread #17).
	a.openTranscripts(lib) // transcribe.go
	a.openLLM()            // transcribe_llm.go
	a.openDictation()      // dictation.go
	return a
}

// post runs fn on the main thread and redraws; directly in tests.
func (a *app) post(fn func()) {
	if a.win == nil {
		if a.postQueue != nil {
			a.postQueue(fn)
			return
		}
		fn()
		return
	}
	a.win.Update(fn)
}

// async runs fn on a goroutine; in tests (no window) at once, so that
// views see its results without waiting.
func (a *app) async(fn func()) {
	if a.win == nil {
		fn()
		return
	}
	go fn()
}

// toast shows msg (already translated) as a toast.
func (a *app) toast(msg string) {
	a.toasts = append(a.toasts, msg)
}

// quit releases what the app holds; it runs in App.OnQuit.
func (a *app) quit() {
	a.pl.Stop()
	a.closeDictation() // M2: dictation.go
	a.eng.Stop()
	a.closeShell()
}
