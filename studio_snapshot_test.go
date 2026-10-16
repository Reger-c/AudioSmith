package main

import (
	"context"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/settings"
	"github.com/hehehai/AudioSmith/internal/text/lexicon"
)

// TestSnapshotsStudio renders the M1 Studio's views (composer and its
// popovers, a render in progress, the output panel, the History's
// details, Settings → Pronunciation), light and dark, English and
// Chinese, like TestSnapshots:
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshotsStudio .
func TestSnapshotsStudio(t *testing.T) { runSnapshotsPlaying(t, snapshotsStudio) }

func snapshotsStudio(t *testing.T, out string) {
	t.Cleanup(func() { i18n.SetLocale("en") })

	script := "Welcome back to the show. Today we talk about 3 things, and it takes 25 minutes.[pause 500ms]\n" +
		"First, the weather: it is 18°C in Shanghai. [laughter] Not bad for October!\n\n" +
		"Second, a word you may say wrong: [[Nguyen|win]]. And a [voice:Ann] line for the next story."
	for _, run := range []struct {
		lang string
		dark bool
	}{{"en", false}, {"en", true}, {"zh-Hans", false}, {"zh-Hans", true}} {
		i18n.SetLocale("en")
		a, eng, tt := newTestApp(t)
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
		save := func(name string) {
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

		// A take with findings.
		at := time.Date(2026, 10, 13, 21, 50, 0, 0, time.Local)
		tk := core.Take{ID: "T1", CreatedAt: at, Text: script, Model: kokoro, Voice: "af_heart", Speed: 1, SampleRate: 24000,
			AudioSeconds: 2.4, GenSeconds: 0.6, Seed: 81723, Render: &core.TakeRender{Bits: 24, Channels: 1, Bytes: 172844,
				Master: services.MasterDefault, SeedPinned: true, QCDone: true,
				Chunks: []core.TakeChunk{{Text: "Welcome back to the show.", End: 0.8}, {Text: "Today we talk about three things, and it takes twenty-five minutes.", Start: 1.05, End: 1.6, Cached: true}, {Text: "First, the weather: it is eighteen degrees Celsius in Shanghai.", Start: 1.7, End: 2.4}},
				Warnings: []core.TakeNotice{
					{Code: "plan.nonverbal_removed", Severity: "warning", Params: map[string]string{"tag": "[laughter]"}},
					{Code: services.NoticeChunkShort, Severity: "warning", Chunk: 3, Start: 1.7, End: 2.4, Value: 0.7, Params: map[string]string{"expected": "3.9"}}},
				Issues: []core.TakeNotice{{Code: "qc.clipping", Severity: "warning", Start: 1.21, End: 1.22, Value: 6}}}}
		addTake(t, a, tk, 2.4)
		a.lastClipID = "T1"
		a.checkOpen["T1"] = true
		a.text = script
		save("m1-studio")

		a.composer.insertOpen = true
		save("m1-insert")
		a.composer.insertOpen = false

		a.setAdvancedOpen("studio", true)
		a.seedPinned, a.seed = true, 81723
		save("m1-voice-controls")

		a.selectModel(entry(t, "omnivoice-bf16"))
		a.text = script
		a.steps = 48
		save("m1-audio-quality")
		a.setAdvancedOpen("studio", false)

		a.selectModel(entry(t, "qwen3-tts-0.6b-customvoice-8bit"))
		a.languageChosen("en", "English")
		a.composer.langOpen = true
		save("m1-language")
		a.composer.langOpen = false

		// Rendering: chunk 3 of 7, the preview playing.
		a.selectModel(entry(t, "kokoro-82m-bf16"))
		a.text = script
		a.autoplay = true
		a.generate()
		req := eng.last(t)
		eng.progress(req.ID, "generating", 0.38, 9.6)
		a.bus.Publish(services.RenderProgress{Job: req.ID, Owner: req.Spec.Owner, Chunk: 3, Chunks: 7, Cached: 2})
		chunkUpdate(t, eng, req.ID, 0, pcm16(24000), false)
		a.toasts = nil
		save("m1-rendering")
		eng.cancelled(req.ID)
		a.pl.Stop()
		a.toasts = nil

		// History with the details pane.
		addTake(t, a, core.Take{ID: "T2", CreatedAt: at.Add(time.Minute), Text: "你好，这是在 Mac 上用 MLX 生成的语音。", Model: "mlx-community/OmniVoice-bf16",
			Voice: "female, young adult", Steps: 48, Seed: 42, SampleRate: 24000, AudioSeconds: 3.2, GenSeconds: 1.4, Starred: true,
			Render: &core.TakeRender{Channels: 1, Bytes: 153644, Master: services.MasterDefault, QCDone: true}}, 1.5)
		a.showPage("history")
		a.history.selected = "T1"
		save("m1-history")
		a.history.selected = ""
		a.history.filter = 1
		save("m1-history-starred")
		a.history.filter = 0

		// Settings → Pronunciation.
		a.lib.svc.Pronunciation.Save(context.Background(), []lexicon.Entry{
			{Term: "Nguyen", Replacement: "win", Type: lexicon.Respelling, Scope: lexicon.Global, Enabled: true},
			{Term: "GIF", Replacement: "dʒɪf", Type: lexicon.IPA, Scope: "en", Enabled: true},
			{Term: "重庆", Replacement: "崇庆", Type: lexicon.Respelling, Scope: "zh", Enabled: true},
			{Term: "SQL", Replacement: "", Type: lexicon.Respelling, Scope: lexicon.Global, Enabled: false},
		})
		a.pron.loaded = false
		a.pron.test = "Mr. Nguyen sent a GIF at 3:30 pm."
		a.navigate("/settings/pronunciation")
		save("m1-settings-pronunciation")
		a.updateSettings(func(s *settings.Settings) { s.Studio.KeepTakes = 500 })
		a.navigate("/settings/studio")
		save("m1-settings-studio")
		a.navigate("/studio")
	}
	_ = ui.Cmd
}
