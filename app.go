package main

import (
	"github.com/egoist/mygo"
)

// app is AudioSmith's UI state: the view state of every page, and handles
// to the domain (store, engine). Every field is read and written on the
// main thread: work on other goroutines reaches it through post.
//
// Each area keeps its fields in a struct of its own, embedded here, in the
// file its owner thread edits (docs/architecture.md §Ownership):
//
//	shellState   shell.go      window chrome, navigation      (thread C)
//	studioState  studio.go     the TTS form                   (thread A)
//	takesState   takes.go      take library, player, peaks    (thread A)
//	engineLink   enginelink.go engine process and current job (thread B)
//
// Add fields to your area's struct, not here.
type app struct {
	win    *mygo.Window // nil in tests
	toasts []string     // shown by the next build of the view

	shellState
	studioState
	takesState
	engineLink
}

func newApp(eng *engineHost, takes *library) *app {
	a := &app{
		shellState:  newShellState(),
		studioState: newStudioState(),
	}
	a.engineLink.eng = eng
	a.openTakes(takes)
	a.openShell(takes)
	return a
}

// post runs fn on the main thread and redraws; directly in tests.
func (a *app) post(fn func()) {
	if a.win == nil {
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
	a.eng.Stop()
	a.closeShell()
}
