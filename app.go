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
	modelsKind int          // the Models page's tab (modelKinds)
	win        *mygo.Window // nil in tests
	toasts     []string     // shown by the next build of the view
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
	longformState // M3 (thread #18): longform.go — Stories, Audiobook, Projects
	dubState      // M4 (thread #19): dub*.go, batch*.go, timeline.go
	// M5 (thread #20): api*.go (Local API, MCP), integrations*.go, tools*.go.
	apiState
	toolsState
	wfState     // M6 (thread #21): workflow*.go — WorkflowStudio
	watermarkUI // watermark_app.go (thread #14): Settings → Privacy, Tools → Watermark
	// M6b (thread #23): calls*.go, twilio*.go — AI phone calls.
	callsState
	exportState                       // export_dialog.go (thread #31): the Export audio dialog
	modelPickerState                  // model_picker.go (thread #42): the creation pages' model picker
	removedState                      // models_removed.go (thread #43): removed models, their clean-up
	voicePickers     voicePickerState // voice_picker.go (thread #60): the voice pickers' previews
	familyState                       // family_app.go (thread #72): the ActNow family directory
	familyCoord                       // family_coord.go (thread #73): shortcuts and memory with the family
	familyKeys                        // family_keys.go (thread #74): API keys shared with the family
	familyReuse                       // family_reuse.go (thread #75): reusing other apps' models, licences
	familyShared                      // family_shared.go (thread #76): the shared settings, moving the models
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
	a.openLongform(lib)    // M3: longform.go (thread #18)
	a.openDubs(lib)        // M4: dub.go (thread #19)
	a.openTools(lib)       // M5: tools.go (thread #20)
	a.openWorkflows(lib)   // M6: workflow.go (thread #21)
	a.openWatermark(lib)   // watermark_app.go (thread #14)
	a.openCalls(lib)       // M6b: calls.go (thread #23)
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
	a.stopAPI()        // M5: api.go (thread #20)
	a.closeDictation() // M2: dictation.go
	a.closeDubs()      // M4: dub.go
	a.closeCalls()     // M6b: calls.go (thread #23)
	a.closeFamily()    // family_app.go (thread #72)
	// family_coord.go: OnWillQuit did it already; for quits without it.
	a.quitFamilyRuntime()
	a.eng.Stop()
	a.closeShell()
}
