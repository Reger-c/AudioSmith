// AudioSmith is a TTS workbench for macOS: write a script, pick a model and
// voice, generate takes on the GPU with MLX, and listen, compare and export
// them. The window is MyGo native UI in Go; speech comes from
// audiosmith-engine, a Swift helper on mlx-audio-swift (see engine/).
package main

import (
	"log"
	"path/filepath"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func main() {
	// Dev builds are "AudioSmith Dev"; share data with the installed app.
	mygo.App.SetName("AudioSmith")

	dataDir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		log.Fatal(err)
	}
	lib, err := loadLibrary(dataDir)
	if err != nil {
		log.Printf("history: %v", err)
	}
	resources, _ := mygo.App.Path(mygo.PathResources)
	logDir, _ := mygo.App.Path(mygo.PathLogs)

	eng := &engineProcess{
		path:    findEngine(resources),
		logPath: filepath.Join(logDir, "engine.log"),
	}
	a := newApp(eng, lib)
	a.logPath = eng.logPath
	eng.onEvent = func(ev engineEvent) { a.post(func() { a.handleEvent(ev) }) }
	eng.onExit = func(err error, tail string) { a.post(func() { a.engineExited(err, tail) }) }

	mygo.App.WhenReady(func() {
		a.win = mygo.NewWindow(mygo.WindowOptions{
			Title:          "AudioSmith",
			Width:          1100,
			Height:         720,
			MinWidth:       820,
			MinHeight:      520,
			StateKey:       "main",
			TitleBarStyle:  mygo.TitleBarHiddenInset,
			TitleBarHeight: titleBarHeight,
			Content:        ui.View(a.view),
		})
		a.restartEngine()
	})
	mygo.App.OnQuit(func() {
		a.pl.stop()
		eng.Stop()
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
