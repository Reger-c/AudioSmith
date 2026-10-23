package main

import (
	"fmt"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/ffmpeg"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// TestSnapshotsSettingsPages renders every Settings section whole (the
// dialog as tall as its section) at 1100 wide and at 820 wide @125 %, in
// Chinese and English, for reviewing Settings' rhythm (thread #96: card
// padding, rows, notes, titles):
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshotsSettingsPages .
func TestSnapshotsSettingsPages(t *testing.T) { runSnapshots(t, snapshotsSettingsPages) }

func snapshotsSettingsPages(t *testing.T, out string) {
	t.Cleanup(func() { i18n.SetLocale("en") })
	old := settingsH
	settingsH = 1 << 14 // the whole section, without scrolling
	t.Cleanup(func() { settingsH = old })
	savedSys, savedReach := readSysInfo, reach
	t.Cleanup(func() { readSysInfo, reach = savedSys, savedReach })
	readSysInfo = func() sysInfo {
		return sysInfo{OS: "macOS 15.4", Chip: "Apple M4", Memory: 16 << 30, Cores: 10, Arch: "arm64"}
	}
	reach = func(_ *http.Client, host string) netCheck {
		return netCheck{host: host, ok: true, latency: 120 * time.Millisecond}
	}
	ffmpegFind = func() []string { return []string{"/opt/homebrew/bin/ffmpeg"} }
	t.Cleanup(func() { ffmpegFind = ffmpeg.FindSystem })
	for _, cfg := range []struct {
		name  string
		w, h  int
		scale float64
		lang  string
	}{
		{"1100-zh", 1100, 3200, 1, "zh-Hans"},
		{"min125-zh", 820, 3600, 1.25, "zh-Hans"},
		{"1100-en", 1100, 3200, 1, "en"},
	} {
		i18n.SetLocale("en")
		a, _, tt := newTestApp(t)
		a.updateSettings(func(s *settings.Settings) {
			s.General.Language = cfg.lang
			s.Appearance.Scale = cfg.scale
		})
		tt.SetSize(cfg.w, cfg.h)
		tt.SetScale(2)
		a.llmOllama = []string{"qwen3:4b"}
		for _, s := range settingsSections() {
			a.navigate("/settings/" + s.id)
			switch s.id {
			case "about":
				a.runSelfCheck()
			case "models":
				a.testNetwork(nil)
				tt.SetSize(cfg.w, 7600) // the list of models, then its groups
			}
			a.toasts = nil
			tt.Frame()
			tt.Frame()
			tt.Frame()
			f, err := os.Create(filepath.Join(out, fmt.Sprintf("sp-%s-%s.png", s.id, cfg.name)))
			if err != nil {
				t.Fatal(err)
			}
			if err := png.Encode(f, tt.Image()); err != nil {
				t.Fatal(err)
			}
			f.Close()
			tt.SetSize(cfg.w, cfg.h)
		}
	}
}
