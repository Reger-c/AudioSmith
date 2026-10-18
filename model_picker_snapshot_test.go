package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// TestSnapshotsModelPickerFit renders thread #84: the Stories model
// picker with "Show all models" open, at its top and scrolled to its end
// (inside the window, the last model whole); its whole-dialogue section
// (on a 24 GB Mac, then with whole dialogue on, then greyed out on an
// 8 GB Mac); the Studio's picker scrolled to its end; and a story's
// voice picker. Chinese and English at 1100×720 and 820×520 @125 %:
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshotsModelPickerFit .
func TestSnapshotsModelPickerFit(t *testing.T) { runSnapshots(t, snapshotsModelPickerFit) }

func snapshotsModelPickerFit(t *testing.T, out string) {
	t.Cleanup(func() { i18n.SetLocale("en") })
	useMemKeys(t)
	for _, cfg := range []b2Config{
		{"1100-zh", 1100, 720, 1, false, "zh-Hans"},
		{"min125-zh", 820, 520, 1.25, false, "zh-Hans"},
		{"1100-en", 1100, 720, 1, false, "en"},
		{"min125-en", 820, 520, 1.25, false, "en"},
	} {
		i18n.SetLocale("en")
		a, _, tt := newTestApp(t)
		acceptLicences(t, a)
		a.updateSettings(func(s *settings.Settings) {
			s.General.Language = cfg.lang
			s.Appearance.Scale = cfg.scale
		})
		tt.SetSize(cfg.w, cfg.h)
		tt.SetScale(2)
		a.sys = &sysInfo{Memory: 24 << 30}
		for _, id := range []string{"qwen3-tts-0.6b-customvoice-8bit", "qwen3-tts-0.6b-base-8bit", "soulx-podcast-1.7b-8bit"} {
			m := a.model(entry(t, id))
			m.known, m.local.State = true, hub.Downloaded
		}
		for _, id := range []string{"qwen3-tts-1.7b-base-8bit", "omnivoice-bf16", "moss-ttsd-v1.0-8bit"} {
			m := a.model(entry(t, id))
			m.known, m.local.State = true, hub.Missing
		}
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
		// scrollEnd scrolls the open picker's list to its end.
		scrollEnd := func() {
			tt.Frame()
			tt.Frame()
			r, ok := tt.Find(i18n.T("Search models"))
			if !ok {
				t.Fatalf("%s: no picker", cfg.name)
			}
			tt.Scroll(r.X+r.W/2, r.Y+r.H+60, 0, 4000)
			tt.Move(1, float32(cfg.h)-1) // no row under the pointer
		}
		a.navigate("/stories")
		p := a.lfStories
		kittenDoc(&p.doc)
		p.panel.set, p.panel.open = true, true // the inspector, a drawer at the minimum size
		shoot("q01-story-picker", func() { a.pickerOpen = map[string]bool{"lf-model": true} })
		shoot("q02-story-picker-all", func() { a.pickerAll = map[string]bool{"lf-model": true} })
		shoot("q03-story-picker-all-end", scrollEnd)
		a.pickerOpen = map[string]bool{}
		shoot("q04-story-dialogue-on", func() { // the button, and Performance's switch in step
			p.doc.Production.Dialogue, p.doc.Production.DialogueModel = true, entry(t, "soulx-podcast-1.7b-8bit").Repo
			p.open["cast"], p.open["scene"] = false, true
		})
		shoot("q05-story-picker-dialogue-on", func() { a.pickerOpen = map[string]bool{"lf-model": true} })
		a.pickerOpen = map[string]bool{}
		p.doc.Production.Dialogue = false
		p.open["cast"], p.open["scene"] = true, false
		a.sys = &sysInfo{Memory: 8 << 30}
		shoot("q06-story-picker-8gb", func() { a.pickerOpen = map[string]bool{"lf-model": true} })
		a.pickerOpen, a.pickerAll = map[string]bool{}, map[string]bool{}
		a.sys = &sysInfo{Memory: 24 << 30}
		a.navigate("/studio")
		a.composer.panel, a.composer.panelSet = true, true
		shoot("q07-studio-picker-all-end", func() {
			a.pickerOpen = map[string]bool{"model": true}
			a.pickerAll = map[string]bool{"model": true}
			scrollEnd()
		})
		a.pickerOpen, a.pickerAll = map[string]bool{}, map[string]bool{}
		// A voice picker low in the inspector: it fits too.
		a.navigate("/stories")
		p.open["setup"], p.open["cast"], p.open["scene"] = false, true, false
		shoot("q08-story-cast-voice", func() { a.voicePickerOpen("cast-voice-mom", true) })
		a.voicePickerOpen("cast-voice-mom", false)
		i18n.SetLocale("en")
	}
}
