package main

import (
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
)

// TestSnapshotsStudioC renders the simplified Studio (thread #42, phase
// C): the inspector's model, voice and speed, the voice list, 「高级」 on
// CustomVoice and OmniVoice, a clip to clone, a voice described in words,
// and the narrow window with its drawer; Chinese at 1100×720 and 820×520
// @125 %, English at 1100×720:
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshotsStudioC .
func TestSnapshotsStudioC(t *testing.T) { runSnapshots(t, snapshotsStudioC) }

func snapshotsStudioC(t *testing.T, out string) {
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
		a.autoStudioModel()
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
		text := "最后一班渡轮在十一点离港。[pause 500ms] 雾像羊毛一样铺在水面上。"
		if cfg.lang == "en" {
			text = "The last ferry left the harbour at eleven. [pause 500ms] Fog had settled on the water like wool."
		}
		at := time.Date(2026, 10, 15, 21, 50, 0, 0, time.Local)
		addTake(t, a, core.Take{ID: "T1", CreatedAt: at, Text: text, Model: a.preset().Repo, Voice: a.voice, SampleRate: 24000,
			AudioSeconds: 4.2, GenSeconds: 1.1, Render: &core.TakeRender{Bits: 24, Channels: 1, Master: services.MasterDefault, QCDone: true}}, 4.2)
		a.lastClipID = "T1"
		addVoice(t, a, core.Voice{Name: "访谈的声音", Kind: core.VoiceCloned, Language: "zh"}, 8)
		a.studioVoice = ""
		home := entry(t, "qwen3-tts-0.6b-customvoice-8bit")
		if cfg.lang == "en" {
			home = entry(t, "kokoro-82m-bf16")
		}
		a.selectModel(home)
		a.navigate("/studio")
		a.text = text
		reset := func() {
			a.inspectorUI = studioInspectorState{}
			a.setAdvancedOpen("studio", false)
			a.composer.panel, a.composer.panelSet = false, false
		}
		shoot("c01-studio", func() {})
		shoot("c02-voice-list", func() { a.inspectorUI.voiceListOpen = true })
		reset()
		shoot("c03-advanced", func() {
			a.seedPinned, a.seed = true, 81723
			a.voice = joinVoice(a.voice, "平静温暖")
			if cfg.lang == "en" {
				a.speed = 1.1
			}
			a.setAdvancedOpen("studio", true)
			a.composer.panel, a.composer.panelSet = true, true
		})
		a.restoreRecommended()
		reset()
		shoot("c04-advanced-omnivoice", func() {
			a.selectModel(entry(t, "omnivoice-bf16"))
			a.setAdvancedOpen("studio", true)
			a.composer.panel, a.composer.panelSet = true, true
		})
		reset()
		shoot("c05-clone-clip", func() {
			a.selectModel(entry(t, "qwen3-tts-1.7b-base-8bit"))
			a.studioVoice = ""
			a.refAudio, a.refText = "/Users/me/Voices/narrator-sample.wav", "这是我的声音听起来的样子。"
			a.setAdvancedOpen("studio", true)
			a.composer.panel, a.composer.panelSet = true, true
		})
		a.refAudio, a.refText = "", ""
		reset()
		shoot("c06-voice-design", func() {
			a.selectModel(entry(t, "qwen3-tts-1.7b-voicedesign-4bit"))
			a.voice = "三十多岁、平静温暖的女声旁白"
			a.composer.panel, a.composer.panelSet = true, true
		})
		reset()
		shoot("c07-saved-voice", func() {
			a.selectModel(entry(t, "qwen3-tts-1.7b-base-8bit"))
			a.studioVoice = a.savedVoices()[0].ID
			a.composer.panel, a.composer.panelSet = true, true
		})
		_ = ui.Cmd
	}
}
