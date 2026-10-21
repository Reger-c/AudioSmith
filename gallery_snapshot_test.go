package main

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/gallery"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// TestSnapshotsCuratedGallery renders thread #94's changes: Discover
// voices with the curated voices (all, a use case with a gender, a
// search, the use-case menu, a built-in voice's card), Design's eight
// starting points, and the Studio's speaker list in Chinese (no Ryan); at
// 1100×720 and 820×520 @125 %, Chinese and English:
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshotsCuratedGallery .
func TestSnapshotsCuratedGallery(t *testing.T) { runSnapshots(t, snapshotsCuratedGallery) }

func snapshotsCuratedGallery(t *testing.T, out string) {
	t.Cleanup(func() { i18n.SetLocale("en") })
	useMemKeys(t)
	stubMachine(t, 24, true)
	withDesignCatalog(t)
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
		g := &a.gallery
		shoot := func(name string, fn func()) {
			fn()
			a.toasts = nil
			tt.Frame()
			tt.Frame()
			tt.Frame()
			f, err := os.Create(filepath.Join(out, fmt.Sprintf("%s-%s.png", name, cfg.name)))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := png.Encode(f, tt.Image()); err != nil {
				t.Fatal(err)
			}
		}
		reset := func() {
			g.text, g.tab, g.favOnly = "", 0, false
			clear(g.filters)
		}
		shoot("g94-01-gallery", func() { a.navigate("/gallery") })
		shoot("g94-02-narration-female", func() {
			reset()
			g.tab = 1 // narration
			g.filters[gallery.FacetGender] = "female"
		})
		shoot("g94-03-search", func() {
			reset()
			g.text = "四川"
		})
		shoot("g94-04-uses-menu", func() {
			reset()
			tt.Frame()
			tt.Click(i18n.T("Use"))
		})
		tt.Key(0, ui.KeyEscape)
		shoot("g94-05-english", func() {
			reset()
			g.filters[gallery.FacetLanguage] = "en"
		})
		shoot("g94-06-builtin", func() {
			reset()
			g.text = "Kokoro"
		})
		reset()
		shoot("g94-07-design-presets", func() { a.navigate("/design") })
		shoot("g94-08-studio-speakers", func() {
			a.selectModel(entry(t, "qwen3-tts-0.6b-customvoice-8bit"))
			a.navigate("/studio")
			tt.Frame()
			a.voicePickerOpen(studioVoiceKey, true)
		})
		a.voicePickerOpen(studioVoiceKey, false)
	}
}
