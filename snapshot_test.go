package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/settings"
	"github.com/hehehai/AudioSmith/internal/store/historyjson"
)

// TestSnapshots renders each page to PNG with MyGo's renderer, for looking
// at the layout without a Mac screen, light and dark, in English
// (<page>-light.png) and Chinese (<page>-light-zh-Hans.png). Set
// AUDIOSMITH_SNAPSHOTS to a directory:
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshots .
func TestSnapshots(t *testing.T) {
	out := os.Getenv("AUDIOSMITH_SNAPSHOTS")
	if out == "" {
		t.Skip("set AUDIOSMITH_SNAPSHOTS to a directory")
	}
	os.MkdirAll(out, 0o755)
	t.Cleanup(func() { i18n.SetLocale("en") })

	for _, run := range []struct {
		lang string
		dark bool
	}{{"en", false}, {"en", true}, {"zh-Hans", false}, {"zh-Hans", true}} {
		dark := run.dark
		i18n.SetLocale("en")
		a, eng, tt := newTestApp(t)
		if run.lang != "en" {
			a.updateSettings(func(s *settings.Settings) { s.General.Language = run.lang })
		}
		tt.SetDark(dark)
		tt.SetScale(2)
		a.loadedModel = kokoro
		texts := []string{
			"Hello from AudioSmith. This voice was generated on your Mac with MLX.",
			"你好，这是在 Mac 上用 MLX 生成的语音。",
			"The same voice, a little faster, for the trailer cut.",
		}
		for i, text := range texts {
			at := time.Date(2026, 10, 13, 21, 50+i, 0, 0, time.Local)
			c := core.Take{ID: historyjson.NewTakeID(at), CreatedAt: at, Text: text, Model: kokoro,
				Voice: []string{"af_heart", "zf_xiaobei", "am_adam"}[i], Speed: 1, SampleRate: 24000,
				AudioSeconds: 5.1 - float64(i), GenSeconds: 1.2}
			addTake(t, a, c, 1.2)
			a.lastClipID = c.ID
		}
		suffix := "light"
		if dark {
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
		save("studio")

		// Generating, half way.
		a.generate()
		half := 0.5
		eng.progress(a.job.id, "generating", half, 2.4)
		save("generating")
		a.job = nil

		a.modelLabel = "Qwen3-TTS 0.6B Base (voice clone)"
		a.modelChanged()
		a.modelDefaults = false
		a.refAudio = "/Users/guanwei/Voices/narrator-sample.wav"
		a.refText = "This is what my voice sounds like."
		save("studio-clone")

		a.showPage("history")
		save("history")
		a.showPage("engine")
		save("engine")

		// Models: Kokoro downloaded (from a fake hub), Soprano downloading.
		useFakeHub(t, a)
		a.download(entry(t, "kokoro-82m-bf16"))
		a.navigate("/models")
		dl := a.model(entry(t, "soprano-80m-bf16"))
		dl.downloading, dl.progress = true, hub.Progress{Bytes: 88e6, TotalBytes: 220645307, Mirror: "https://hf-mirror.com"}
		a.notices = nil
		save("models")
		a.askLicence(entry(t, "omnivoice-bf16"), nil)
		save("licence")
		a.licenceOpen = false
		dl.downloading = false

		at := time.Date(2026, 10, 13, 21, 58, 3, 0, time.Local)
		for i, l := range []core.LogEntry{
			{Source: "app", Message: "AudioSmith 0.1.0 started"},
			{Source: "hub", Message: "downloading mlx-community/Kokoro-82M-bf16"},
			{Source: "engine:gpu", Message: "loaded Kokoro-82M-bf16 in 1.4 s"},
			{Source: "hub", Level: core.SeverityError, Message: "acme/gated: the repo is gated: accept its terms on huggingface.co and add a token"},
		} {
			l.Time = at.Add(time.Duration(i) * time.Second)
			a.logs.Append(l)
		}
		a.navigate("/logs")
		save("logs")

		a.navigate("/settings/general")
		save("settings-general")
		a.navigate("/settings/appearance")
		save("settings-appearance")
		a.tokenInfo, a.proxyInfo = i18n.T("No token: gated models cannot download."), i18n.T("Downloads go through %s.", "http://127.0.0.1:7897")
		a.navigate("/settings/models")
		save("settings-models")

		a.navigate("/studio")
		a.openPalette()
		save("palette")
		a.paletteOpen = false
		a.sheetOpen = true
		save("shortcuts")
		a.sheetOpen = false
		a.switchOpen = true
		save("quickswitch")
		a.switchOpen = false
		a.notes.Post(core.Notification{Severity: core.SeveritySuccess, Title: i18n.T("%s is downloaded", "Kokoro 82M"), Link: "/models"})
		a.notes.Post(core.Notification{Severity: core.SeverityError, Title: i18n.T("Could not download %s", "Gated model"),
			Body: i18n.T("The model is gated: accept its terms on huggingface.co, then add an access token."), Link: "/settings/models"})
		a.toasts = nil
		a.noticesOpen = true
		a.logsPanel = true
		save("notifications")
	}
	_ = ui.Cmd
}
