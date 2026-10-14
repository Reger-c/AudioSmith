package main

import (
	"context"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/ffmpeg"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// TestSnapshotsM7 renders M7a's views (the icon rail, Settings → Audio
// tools, Storage, Diagnostics, Network, Usage, Privacy, Appearance, the
// first-run wizard) light and dark, in English and Chinese:
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshots .
func TestSnapshotsM7(t *testing.T) {
	out := os.Getenv("AUDIOSMITH_SNAPSHOTS")
	if out == "" {
		t.Skip("set AUDIOSMITH_SNAPSHOTS to a directory")
	}
	os.MkdirAll(out, 0o755)
	t.Cleanup(func() { i18n.SetLocale("en") })
	fake := fakeFFmpeg(t)
	ffmpegFind = func() []string { return []string{"/opt/homebrew/bin/ffmpeg"} }
	t.Cleanup(func() { ffmpegFind = ffmpeg.FindSystem })
	for _, run := range []struct {
		lang string
		dark bool
	}{{"en", false}, {"en", true}, {"zh-Hans", false}, {"zh-Hans", true}} {
		i18n.SetLocale("en")
		a, _, tt := newTestApp(t)
		if run.lang != "en" {
			a.updateSettings(func(s *settings.Settings) { s.General.Language = run.lang })
		}
		tt.SetDark(run.dark)
		tt.SetScale(2)
		suffix := "light"
		if run.dark {
			suffix = "dark"
		}
		if run.lang != "en" {
			suffix += "-" + run.lang
		}
		shot := func(tt *ui.Tester, name string) {
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
		save := func(name string) { shot(tt, name) }
		tall := ui.NewTester(a.view, 1100, 1600)
		tall.SetDark(run.dark)

		// Audio tools: not set up, then ready.
		a.navigate("/settings/audio-tools")
		save("settings-audio-tools-none")
		tool, _ := ffmpeg.Inspect(context.Background(), fake)
		tool.FFmpeg = "/opt/homebrew/bin/ffmpeg"
		a.updateSettings(func(s *settings.Settings) {
			s.Media.FFmpeg, s.Media.Origin = "/opt/homebrew/bin/ffmpeg", settings.MediaSystem
		})
		a.ffPath, a.ffChecking = "/opt/homebrew/bin/ffmpeg", false
		a.ffTool.Store(tool)
		shot(tall, "settings-audio-tools")

		// The icon rail.
		a.navigate("/studio")
		a.setRail(true)
		save("rail")
		a.setRail(false)
	}
}
