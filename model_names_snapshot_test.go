package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// TestSnapshotsModelNames renders thread #45's friendly model names: the
// Studio's picker (recommended, then every model, then a search), the
// dub's picker, the Models page (with a card's Details open), ⌘E, a
// History take's details and the settings that choose a model; Chinese
// and English at 1100×720 and 820×520 @125 %:
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshotsModelNames .
func TestSnapshotsModelNames(t *testing.T) {
	out := os.Getenv("AUDIOSMITH_SNAPSHOTS")
	if out == "" {
		t.Skip("set AUDIOSMITH_SNAPSHOTS to a directory")
	}
	os.MkdirAll(out, 0o755)
	t.Cleanup(func() { i18n.SetLocale("en") })
	useMemKeys(t)
	stubMachine(t, 16, true)
	for _, cfg := range []b2Config{
		{"1100-zh", 1100, 720, 1, false, "zh-Hans"},
		{"min125-zh", 820, 520, 1.25, false, "zh-Hans"},
		{"1100-en", 1100, 720, 1, false, "en"},
		{"min125-en", 820, 520, 1.25, false, "en"},
	} {
		a, _, tt := newTestApp(t)
		acceptLicences(t, a)
		i18n.SetLocale("en")
		a.updateSettings(func(s *settings.Settings) {
			s.General.Language = cfg.lang
			s.Appearance.Scale = cfg.scale
		})
		tt.SetSize(cfg.w, cfg.h)
		tt.SetScale(2)
		shoot := func(name string, fn func()) {
			fn()
			a.toasts = nil
			tt.Frame()
			tt.Frame()
			tt.Frame()
			f, err := os.Create(filepath.Join(out, name+"-"+cfg.name+".png"))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := png.Encode(f, tt.Image()); err != nil {
				t.Fatal(err)
			}
		}
		// A few downloaded, the others not (the picker shows their size).
		for _, id := range []string{"kokoro-82m-bf16", "qwen3-tts-0.6b-customvoice-8bit", "qwen3-tts-0.6b-base-8bit"} {
			m := a.model(entry(t, id))
			m.known, m.local.State = true, hub.Downloaded
		}
		for _, id := range []string{"qwen3-tts-1.7b-base-8bit", "omnivoice-bf16", "chatterbox-turbo-8bit", "qwen3-tts-1.7b-voicedesign-4bit"} {
			m := a.model(entry(t, id))
			m.known, m.local.State = true, hub.Missing
		}
		a.navigate("/studio")
		a.composer.panel, a.composer.panelSet = true, true // the inspector, over the script when narrow
		a.text = "你好，欢迎使用 AudioSmith。Hello from AudioSmith."
		shoot("n01-studio-picker", func() { a.pickerOpen = map[string]bool{"model": true} })
		shoot("n02-studio-picker-all", func() { a.pickerAll = map[string]bool{"model": true} })
		shoot("n03-picker-search", func() { a.pickerFind = map[string]string{"model": "qwen 1.7b"} })
		a.pickerOpen, a.pickerAll, a.pickerFind = map[string]bool{}, map[string]bool{}, map[string]string{}
		shoot("n04-studio", func() {})
		shoot("n05-models", func() { a.navigate("/models") })
		shoot("n06-models-details", func() { a.modelsDetail = map[string]bool{"kokoro-82m-bf16": true} })
		a.modelsDetail = nil
		shoot("n07-models-search", func() { a.modelsFind = "qwen3" })
		a.modelsFind = ""
		shoot("n08-models-asr", func() { a.modelsKind = 2 })
		a.modelsKind = 0
		shoot("n09-quick-switch", func() { a.navigate("/studio"); a.switchOpen = true })
		a.switchOpen = false
		at := time.Date(2026, 10, 15, 21, 50, 0, 0, time.Local)
		tk := core.Take{ID: core.NewID(at), CreatedAt: at, Text: "你好，这是在 Mac 上用 MLX 生成的语音。", Model: entry(t, "qwen3-tts-0.6b-customvoice-8bit").Repo,
			Voice: "Serena", Speed: 1, SampleRate: 24000, AudioSeconds: 3.4, GenSeconds: 1.1, Seed: 81723, Temperature: 0.7, TopP: 0.8,
			Render: &core.TakeRender{Bits: 24, Channels: 1, Bytes: 172844, Master: services.MasterDefault}}
		addTake(t, a, tk, 1.1)
		shoot("n10-history-details", func() { a.showPage("history"); a.history.selected = tk.ID })
		a.history.selected = ""
		shoot("n11-settings-studio", func() { a.navigate("/settings/studio") })
		shoot("n12-settings-dictation", func() {
			a.navigate("/settings/dictation")
			tt.Frame()
			tt.Frame()
			if cfg.scale > 1 { // the model is below the fold
				tt.Scroll(float32(cfg.w)*0.6, float32(cfg.h)*0.6, 0, 230)
			}
		})
		shoot("n13-settings-writing", func() { a.navigate("/settings/writing") })
		i18n.SetLocale("en")
	}
}
