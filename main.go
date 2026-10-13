// AudioSmith is a TTS workbench for macOS: write a script, pick a model and
// voice, generate takes on the GPU with MLX, and listen, compare and export
// them. The window is MyGo native UI in Go; speech comes from
// audiosmith-engine, a Swift helper on mlx-audio-swift (see engine/).
//
// Package main is the UI (views and their state); the domain lives in
// internal/ (see docs/architecture.md). main only wires them together:
// each step is a function in the file of the area that owns it.
package main

import (
	"log"

	"github.com/egoist/mygo"
)

func main() {
	// Dev builds are "AudioSmith Dev"; share data with the installed app.
	mygo.App.SetName("AudioSmith")

	dataDir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		log.Fatal(err)
	}
	resources, _ := mygo.App.Path(mygo.PathResources)
	logDir, _ := mygo.App.Path(mygo.PathLogs)

	takes := openTakeStore(dataDir)            // takes.go
	eng := newEngineProcess(resources, logDir) // enginelink.go
	a := newApp(eng, takes)                    // app.go
	a.connectEngine(eng)                       // enginelink.go

	mygo.App.WhenReady(func() {
		a.openWindow() // shell.go
		a.restartEngine()
	})
	mygo.App.OnQuit(a.quit)
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
