package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// TestSnapshotsPresets renders the recommended models and settings
// (thread #42, A+B): a new user's Studio, the model picker (recommended,
// all models, a dub's greyed models, Design), the Studio's restore
// recommended footer, a retired model's hint, a new user's Stories and
// Audiobook; Chinese at 1100×720 and 820×520 @125 %, English at 1100×720:
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshotsPresets .
func TestSnapshotsPresets(t *testing.T) {
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
	} {
		setup := func(a *app, tt *ui.Tester) {
			i18n.SetLocale("en")
			a.updateSettings(func(s *settings.Settings) {
				s.General.Language = cfg.lang
				s.Appearance.Scale = cfg.scale
			})
			tt.SetSize(cfg.w, cfg.h)
			tt.SetDark(cfg.dark)
			tt.SetScale(2)
			a.autoStudioModel()
			a.autoLfModels()
		}
		shoot := func(a *app, tt *ui.Tester, name string, fn func()) {
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

		a, _, tt := newTestApp(t)
		acceptLicences(t, a)
		setup(a, tt)
		a.text = "最后一班渡轮在十一点离港。[pause 500ms] 雾像羊毛一样铺在水面上。"
		if cfg.lang == "en" {
			a.text = "The last ferry left the harbour at eleven. [pause 500ms] Fog had settled on the water like wool."
		}
		a.composer.panel, a.composer.panelSet = true, true
		shoot(a, tt, "01-studio-new-user", func() { a.navigate("/studio") })
		shoot(a, tt, "02-picker", func() { a.pickerOpen = map[string]bool{"model": true} })
		shoot(a, tt, "03-picker-all", func() { a.pickerAll = map[string]bool{"model": true} })
		a.pickerOpen, a.pickerAll = nil, nil
		shoot(a, tt, "04-voice-controls-changed", func() {
			a.temperature, a.seedPinned, a.seed = 0.5, true, 81723
			a.composer.voiceOpen = true
		})
		a.composer.voiceOpen = false
		a.restoreRecommended()
		shoot(a, tt, "05-retired", func() {
			a.modelLabel = entry(t, "chatterbox-8bit").Label
			a.modelChanged()
		})
		a.selectModel(entry(t, "qwen3-tts-0.6b-customvoice-8bit"))

		shoot(a, tt, "06-audiobook-new-user", func() {
			a.navigate("/audiobook")
			a.lfBook.panel.set, a.lfBook.panel.open = true, true
		})
		shoot(a, tt, "07-stories-picker", func() {
			a.navigate("/stories")
			a.lfStories.panel.set, a.lfStories.panel.open = true, true
			a.lfStories.open["setup"] = true
			a.pickerOpen = map[string]bool{"lf-model": true}
		})
		a.pickerOpen = nil
		shoot(a, tt, "08-design-picker", func() {
			a.navigate("/design")
			a.pickerOpen = map[string]bool{"design-model": true}
			a.pickerAll = map[string]bool{"design-model": true}
		})
		a.pickerOpen, a.pickerAll = nil, nil

		d := newDubTest(t, 4)
		setup(d.a, d.tt)
		d.a.dubSourceFile(d.src)
		d.a.dubStep = dubStepGenerate
		d.a.dubSide.set, d.a.dubSide.open = true, true
		shoot(d.a, d.tt, "09-dub-picker", func() {
			d.a.pickerOpen = map[string]bool{"gen-model": true}
			d.a.pickerAll = map[string]bool{"gen-model": true}
		})
		_ = core.ProjectStory
	}
}
