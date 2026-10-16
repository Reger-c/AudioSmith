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

// TestSnapshotsTrim renders thread #46's trim: the Models page (All with
// the clean-up row, then the Transcription, Writing and Other tabs), the
// clean-up dialog, Settings → Dictation and Writing help (one language
// model, named), and the Transcribe page on Qwen3-ASR; Chinese and
// English at 1100×720 and 820×520 @125 %:
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshotsTrim .
func TestSnapshotsTrim(t *testing.T) { runSnapshots(t, snapshotsTrim) }

func snapshotsTrim(t *testing.T, out string) {
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
		for repo, size := range map[string]int64{parakeetRepo: 2509044141, roformerRepo: 456484296, gemmaE2BRepo: 3583086498, gemmaE4BRepo: 5179239349, qwen17Repo: 984013244} {
			dir := filepath.Join(hub.RepoDir(cache, repo), "blobs")
			os.MkdirAll(dir, 0o755)
			os.WriteFile(filepath.Join(dir, "b"), nil, 0o644)
			if err := os.Truncate(filepath.Join(dir, "b"), size); err != nil {
				t.Fatal(err)
			}
		}
		a.refreshModels()
		a.toasts = nil
		shoot("t01-models-all", func() {
			a.navigate("/models")
			a.modelsKind = 0
		})
		shoot("t02-models-transcription", func() { a.modelsKind = 2 })
		shoot("t03-models-writing", func() { a.modelsKind = 3 })
		shoot("t04-models-other", func() { a.modelsKind = 4 })
		shoot("t05-cleanup-dialog", func() { a.cleanAsk = true })
		a.cleanAsk = false
		shoot("t06-settings-dictation", func() { a.navigate("/settings/dictation") })
		shoot("t07-settings-writing", func() {
			a.navigate("/settings/writing")
			tt.Frame()
			tt.Frame()
			// Down to "On this Mac (AudioSmith)": the language model, named.
			if r, ok := tt.Find(i18n.T("Use for writing help")); ok {
				tt.Scroll(r.X+r.W/2, r.Y+r.H/2, 0, -4000)
			}
		})
		shoot("t08-transcribe", func() { a.navigate("/transcripts") })
	}
}
