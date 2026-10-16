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

// TestSnapshotsRemoved renders thread #43's removal of six speech models:
// the Models page with the clean-up row and its dialog, a Studio draft on
// a removed model with its notice, and the Studio's picker with every
// model left; Chinese at 1100×720 and 820×520 @125 %, English at
// 1100×720:
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshotsRemoved .
func TestSnapshotsRemoved(t *testing.T) { runSnapshots(t, snapshotsRemoved) }

func snapshotsRemoved(t *testing.T, out string) {
	t.Cleanup(func() { i18n.SetLocale("en") })
	useMemKeys(t)
	stubMachine(t, 16, true)
	for _, cfg := range []b2Config{
		{"1100-zh", 1100, 720, 1, false, "zh-Hans"},
		{"min125-zh", 820, 520, 1.25, false, "zh-Hans"},
		{"1100-en", 1100, 720, 1, false, "en"},
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
		// What a user who tried them has on disk (sparse files: their
		// real sizes, no space taken).
		cache := a.hubClient().Options().CacheDir
		for repo, size := range map[string]int64{kittenRepo: 292427770, pocketRepo: 240322540, "kyutai/moshiko-pytorch-bf16": 384644900, cbMultiRepo: 1425486273} {
			dir := filepath.Join(hub.RepoDir(cache, repo), "blobs")
			os.MkdirAll(dir, 0o755)
			if err := os.Truncate(filepath.Join(dir, "b"), 0); err != nil {
				os.WriteFile(filepath.Join(dir, "b"), nil, 0o644)
			}
			if err := os.Truncate(filepath.Join(dir, "b"), size); err != nil {
				t.Fatal(err)
			}
		}
		a.toasts = nil
		shoot("r01-models-cleanup", func() {
			a.navigate("/models")
			a.modelsKind = 1
		})
		shoot("r02-cleanup-dialog", func() { a.cleanAsk = true })
		a.cleanAsk = false
		shoot("r03-studio-removed-draft", func() {
			a.navigate("/studio")
			a.applyStudioDraft(studioDraft{Version: 3, Text: "Hello from AudioSmith.", ModelLabel: "KittenTTS mini", Voice: "Bella"})
		})
		a.toasts = nil
		shoot("r04-picker-all", func() {
			a.pickerOpen = map[string]bool{"model": true}
			a.pickerAll = map[string]bool{"model": true}
		})
		a.pickerOpen, a.pickerAll = nil, nil
	}
}
