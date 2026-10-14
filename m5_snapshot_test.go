package main

import (
	"image/png"

	"github.com/egoist/mygo/ui"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/api"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	mcpsrv "github.com/hehehai/AudioSmith/internal/mcp"
	"github.com/hehehai/AudioSmith/internal/settings"
	"github.com/hehehai/AudioSmith/internal/translate"
)

// TestSnapshotsM5 renders M5's views (Settings → Local API, Integrations
// and a setup page, the Tools, the API reference's page) light and dark,
// in English and Chinese:
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshots .
func TestSnapshotsM5(t *testing.T) {
	out := os.Getenv("AUDIOSMITH_SNAPSHOTS")
	if out == "" {
		t.Skip("set AUDIOSMITH_SNAPSHOTS to a directory")
	}
	os.MkdirAll(out, 0o755)
	t.Cleanup(func() { i18n.SetLocale("en") })
	useMemKeys(t)
	for _, run := range []struct {
		lang string
		dark bool
	}{{"en", false}, {"en", true}, {"zh-Hans", false}, {"zh-Hans", true}} {
		i18n.SetLocale("en")
		a, _, tt := newTestApp(t)
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
		shot := func(tt *ui.Tester, name string) {
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
		save := func(name string) { shot(tt, name) }
		// A tall window shows the long settings page whole.
		tall := ui.NewTester(a.view, 1100, 2000)
		tall.SetDark(run.dark)
		// The API, as if it ran on 3901, with a key, sharing and a binding.
		a.voices = append(a.voices, core.Voice{ID: "v1", Name: "Narrator", Kind: core.VoiceCloned})
		a.updateSettings(func(s *settings.Settings) {
			s.API.Bindings = []settings.APIBinding{{Client: "claude-code", Voice: "v1"}}
			s.API.Origins = []string{"http://localhost:5173"}
		})
		a.syncAPIForm()
		a.apiPort, a.apiHasKey, a.apiNewKey = 3901, true, "as-3k9TQm2pXvLr8sWn4bYc7HdJf6GuZa1eKo5iVqNh"
		a.apiShare = &api.Share{PIN: "482913", Port: 3902, URLs: []string{"http://192.168.1.23:3902"}}
		a.apiClients = []mcpsrv.Client{{ID: "claude-code", Name: "claude-code 2.1", LastSeen: time.Date(2026, 10, 14, 9, 30, 0, 0, time.Local), Calls: 12}}
		a.navigate("/settings/api")
		save("settings-api")
		shot(tall, "settings-api-full")
		a.apiForm.ops = true
		shot(tall, "settings-api-endpoints")
		a.apiForm.ops = false

		a.navigate("/integrations")
		save("integrations")
		a.navigate("/integrations/claude-code")
		save("integration-claude-code")
		a.navigate("/integrations/openai-sdk")
		save("integration-openai-sdk")

		a.navigate("/tools")
		a.toolTab = 0
		a.ab.runs = [2]*toolRun{{model: "Kokoro 82M", output: "/tmp/a.wav", key: "tool:a"}, {model: "Kokoro 82M", busy: true}}
		save("tools-ab")
		a.toolTab = 1
		a.cv = toolConvert{file: "/tmp/memo.m4a", name: "memo.m4a", duration: 4.2, heard: "Meet me at the station at noon, and bring the tickets.", match: true,
			note: i18n.T("The take is %.1f s and the recording %.1f s: stretched as far as speech allows (%.1f s).", 9.1, 4.2, 4.6),
			take: &core.Take{ID: "t-conv", AudioSeconds: 3.9}}
		save("tools-convert")
		a.toolTab = 2
		a.rf.text = "Nous devons partir avant que la tempête n'arrive sur la côte."
		a.rf.lang = langName("fr")
		a.rf.slot = 2.5
		fit := translate.DefaultPredictor.Fit(a.rf.text, "fr", 2.5, 1)
		a.rf.res = &translate.FitResult{Fit: fit, Rewrite: "Partons avant la tempête.", RewriteFit: &translate.Fit{Predicted: 1.6, Slot: 2.5, Ratio: 0.64, Verdict: translate.FitOK}}
		save("tools-ratefit")
		a.toolTab = 3
		a.dir.note = "more urgent, a little scared"
		d := translate.ParseDirection(a.dir.note)
		a.dir.res = &d
		save("tools-direction")
		a.toolTab = 4
		a.prb = toolProbe{file: "/Users/me/Movies/interview.mov", out: "{\n  \"format\": \"mov\",\n  \"duration\": 754.32,\n  \"hasAudio\": true,\n  \"hasVideo\": true,\n  \"codec\": \"aac\",\n  \"sampleRate\": 48000,\n  \"channels\": 2\n}"}
		save("tools-probe")

		a.navigate("/api-reference")
		save("api-reference")
	}
}
