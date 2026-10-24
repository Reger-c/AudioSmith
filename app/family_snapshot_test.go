package main

import (
	"context"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/settings"
	"github.com/hehehai/actnowkit/models"
)

// TestSnapshotsFamily renders what the ActNow family adds when Dicta (a
// release build) is installed and uses some of AudioSmith's models: a
// model another app deleted for all apps, the delete dialog of a shared
// model and its "Delete for all apps" confirmation, Clean up keeping a
// removed model Dicta uses, and Settings → Storage with shared space and
// models only an uninstalled app used; English and Chinese at 1100×720:
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshotsFamily .
func TestSnapshotsFamily(t *testing.T) { runSnapshots(t, snapshotsFamily) }

func snapshotsFamily(t *testing.T, out string) {
	t.Cleanup(func() { i18n.SetLocale("en") })
	useMemKeys(t)
	stubMachine(t, 16, true)
	for _, cfg := range []b2Config{
		{"1100-en", 1100, 720, 1, false, "en"},
		{"1100-zh", 1100, 720, 1, false, "zh-Hans"},
	} {
		a, _, tt := newTestApp(t)
		acceptLicences(t, a)
		i18n.SetLocale("en")
		a.updateSettings(func(s *settings.Settings) { s.General.Language = cfg.lang })
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
		ctx := context.Background()
		tf := useFamily(t, a, dictaID, "dev.hehehai.pik")
		cache := modelCache(a.prefs)
		dicta := &models.Store{Family: tf.Family, Hub: cache, BundleID: dictaID}
		// Dicta uses Qwen3-ASR, and deleted Kokoro for all apps.
		asr, kokoro := catalogEntry(t, "qwen3-asr-0.6b-4bit"), catalogEntry(t, "kokoro-82m-bf16")
		tf.claim(t, dictaID, asr)
		tf.claim(t, dictaID, kokoro)
		if res, err := dicta.Delete(ctx, models.Ref{Repo: kokoro.Repo, Revision: kokoro.Revision}, models.AllApps); err != nil || !res.Tombstoned {
			t.Fatalf("Dicta deletes Kokoro for all apps: %+v %v", res, err)
		}
		// Pik claimed a model, then was uninstalled.
		tf.claim(t, "dev.hehehai.pik", catalogEntry(t, "silero-vad"))
		tf.Uninstall("dev.hehehai.pik")
		tf.fam.Reread()
		a.showFamily(readFamilyView(tf.fam, nil, cache))
		// Removed models on disk: Gemma 4 E4B, which Dicta uses, and Parakeet.
		gemma, parakeet := "mlx-community/gemma-4-e4b-it-4bit", "mlx-community/parakeet-tdt-0.6b-v3"
		fakeHubRepo(t, cache, gemma, 5000)
		fakeHubRepo(t, cache, parakeet, 3000)
		if err := dicta.Claim(ctx, models.Claim{Repo: gemma, Revision: strings.Repeat("1", 40)}); err != nil {
			t.Fatal(err)
		}
		a.toasts = nil
		shoot("f01-models-deleted-in", func() {
			a.navigate("/models")
			a.refreshModels()
		})
		shoot("f02-delete-shared", func() { a.askDelete(asr) })
		shoot("f03-delete-for-all", func() { a.deleteAsk, a.deleteAllAsk = true, true })
		a.deleteAsk, a.deleteAllAsk = false, false
		shoot("f04-cleanup-kept", func() {
			a.refreshRemoved()
			a.cleanAsk = true
		})
		a.cleanAsk = false
		tt.SetSize(cfg.w, 1700) // the whole page, down to the models
		shoot("f05-storage", func() {
			a.stReport = nil
			a.navigate("/settings/storage")
		})
	}
}
