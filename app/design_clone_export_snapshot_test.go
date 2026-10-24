package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// TestSnapshotsF renders Design, Clone and the Export dialog simplified
// (thread #42, phase F) and the portable voices: the Studio's offer to
// make a speaker or a saved voice portable, and a portable copy in My
// voices; Chinese at 1100×720 and 820×520 @125 %, English at 1100×720:
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshotsF .
func TestSnapshotsF(t *testing.T) { runSnapshots(t, snapshotsF) }

func snapshotsF(t *testing.T, out string) {
	t.Cleanup(func() { i18n.SetLocale("en") })
	useMemKeys(t)
	stubMachine(t, 16, true)
	for _, cfg := range []b2Config{
		{"1100-zh", 1100, 720, 1, false, "zh-Hans"},
		{"min125-zh", 820, 520, 1.25, false, "zh-Hans"},
		{"1100-en", 1100, 720, 1, false, "en"},
	} {
		a, eng, tt := newTestApp(t)
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
		shoot("f01-design", func() {
			a.navigate("/design")
			a.design.description = "慈祥的老奶奶，语速慢，略带沙哑"
			a.design.model = "OmniVoice"
			a.design.seed = 1234567
		})
		shoot("f02-clone", func() {
			clip := filepath.Join(testDir(t), "interview.wav")
			writeSpeech(clip, 32, []span{{0.4, 3}, {4, 11}, {11.6, 19}, {20, 27}, {28, 31}})
			a.startClone("")
			a.cloneLoad(clip, "interview.wav", false)
			if r := eng.lastOf(t, proto.MethodVADDetect); r.ID != "" {
				eng.fail(r.ID, proto.CodeModelNotDownloaded, "no VAD")
			}
			answerTranscribe(t, eng, "我常对新来的播音员说：慢一点，让句子呼吸。")
			a.clone.name = "访谈的声音"
			a.setAdvancedOpen("clone", true)
		})
		tk := exportTake(t, a, "T1", core.TakeStudio)
		shoot("f03-export", func() {
			a.navigate("/history")
			a.openExport(tk)
		})
		shoot("f04-export-advanced", func() { a.setAdvancedOpen("export", true); a.exp.trim = true })
		a.exp = nil

		kok := entry(t, "kokoro-82m-bf16")
		v := addVoice(t, a, core.Voice{Name: "Heart", Kind: core.VoiceBuiltin, Model: kok.Repo, Speaker: "af_heart", Language: "en"}, 0)
		addVoice(t, a, core.Voice{Name: "Serena (approximate)", Kind: core.VoiceCloned, Language: "zh", Origin: "portable:speaker:qwen3-tts-0.6b-customvoice-8bit:Serena", Tags: []string{portableTag}}, 10)
		shoot("f05-studio-speaker-portable", func() {
			a.navigate("/studio")
			a.selectModel(entry(t, "qwen3-tts-0.6b-customvoice-8bit"))
			a.studioVoice, a.voice = "", "Serena"
			a.composer.panel, a.composer.panelSet = true, true
			a.voicePickerOpen(studioVoiceKey, true)
		})
		a.voicePickerOpen(studioVoiceKey, false)
		shoot("f06-studio-make-portable", func() {
			a.selectModel(entry(t, "qwen3-tts-1.7b-base-8bit"))
			a.studioVoice = v.ID
		})
		shoot("f07-voices", func() { a.navigate("/voices") })
		_ = ui.Cmd
	}
}
