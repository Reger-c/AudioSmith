package main

import (
	"os"
	"strings"
	"testing"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
)

// TestSnapshotsExport renders the Export audio dialog (thread #31) at the
// review's sizes (b1Runs): over History, as it opens for a Studio take
// (WAV; MP3, FLAC and Opus greyed out without ffmpeg), as MP3 with ffmpeg
// set up and its processing on, and for a voice's preview; and the History
// card with its Export button:
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/export go test -run TestSnapshotsExport .
//	AUDIOSMITH_B1_SIZES=1100 …
func TestSnapshotsExport(t *testing.T) {
	out := os.Getenv("AUDIOSMITH_SNAPSHOTS")
	if out == "" {
		t.Skip("set AUDIOSMITH_SNAPSHOTS to a directory")
	}
	os.MkdirAll(out, 0o755)
	t.Cleanup(func() { i18n.SetLocale("en") })
	var sizes []string
	if v := os.Getenv("AUDIOSMITH_B1_SIZES"); v != "" {
		sizes = strings.Split(v, ",")
	}
	for _, run := range b1Runs {
		if len(sizes) > 0 && !strings.Contains(","+strings.Join(sizes, ",")+",", ","+run.size+",") {
			continue
		}
		i18n.SetLocale("en")
		a, _, tt := newTestApp(t)
		s := &b1Shooter{t: t, out: out, run: run}
		s.setup(a, tt)
		tt.Frame()
		tk := exportTake(t, a, "T1", core.TakeStudio)
		if run.lang != "en" {
			// The take's script in the shot's language.
			zh := *tk
			zh.Text = "最后一班渡轮在十一点离港。[pause 500ms] 雾像羊毛一样铺在水面上。"
			a.putTake(zh)
			tk = a.findTake("T1")
		}
		a.showPage("history")
		s.save(tt, "export-history")

		a.openExport(tk)
		s.save(tt, "export-wav")

		ff, _ := fakeFFmpegLog(t)
		useFFmpeg(t, a, ff)
		a.openExport(tk)
		f := a.exp
		f.format = formatIndex("mp3")
		f.trim = true
		f.loud = loudnessLabel("podcast")
		*f.extras["script"] = true
		s.save(tt, "export-mp3")

		vp := exportTake(t, a, "V1", core.TakeVoice)
		a.openExport(vp)
		a.exp.format = formatIndex("opus")
		s.save(tt, "export-voice-opus")
		a.exp = nil
	}
}
