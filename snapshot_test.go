package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/store/historyjson"
)

// TestSnapshots renders each page to PNG with MyGo's renderer, for looking
// at the layout without a Mac screen. Set AUDIOSMITH_SNAPSHOTS to a directory:
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshots .
func TestSnapshots(t *testing.T) {
	out := os.Getenv("AUDIOSMITH_SNAPSHOTS")
	if out == "" {
		t.Skip("set AUDIOSMITH_SNAPSHOTS to a directory")
	}
	os.MkdirAll(out, 0o755)

	for _, dark := range []bool{false, true} {
		a, _, tt := newTestApp(t)
		tt.SetDark(dark)
		tt.SetScale(2)
		a.handleEvent(engine.Event{Event: "loaded", Model: kokoro})
		texts := []string{
			"Hello from AudioSmith. This voice was generated on your Mac with MLX.",
			"你好，这是在 Mac 上用 MLX 生成的语音。",
			"The same voice, a little faster, for the trailer cut.",
		}
		for i, text := range texts {
			at := time.Date(2026, 10, 13, 21, 50+i, 0, 0, time.Local)
			c := core.Take{ID: historyjson.NewTakeID(at), CreatedAt: at, Text: text, Model: kokoro,
				Voice: []string{"af_heart", "zf_xiaobei", "am_adam"}[i], Speed: 1, SampleRate: 24000,
				AudioSeconds: 5.1 - float64(i), GenSeconds: 1.2}
			addTake(t, a, c, 1.2)
			a.lastClipID = c.ID
		}
		suffix := "light"
		if dark {
			suffix = "dark"
		}
		save := func(name string) {
			tt.Frame()
			tt.Frame()
			f, err := os.Create(filepath.Join(out, name+"-"+suffix+".png"))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := png.Encode(f, tt.Image()); err != nil {
				t.Fatal(err)
			}
		}
		save("studio")

		// Generating, half way.
		a.generate()
		half := 0.5
		a.handleEvent(engine.Event{ID: a.job.id, Event: "status", Message: "Generating…"})
		a.handleEvent(engine.Event{ID: a.job.id, Event: "progress", Fraction: &half, AudioSeconds: 2.4})
		save("generating")
		a.job = nil

		a.modelLabel = "Qwen3-TTS 0.6B Base (voice clone)"
		a.modelChanged()
		a.modelDefaults = false
		a.refAudio = "/Users/guanwei/Voices/narrator-sample.wav"
		a.refText = "This is what my voice sounds like."
		save("studio-clone")

		a.page = "history"
		save("history")
		a.page = "engine"
		save("engine")
	}
	_ = ui.Cmd
}
