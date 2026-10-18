package main

import (
	"context"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// TestSnapshotsReferences renders the reference checker and references
// (thread #83): Clone's recording tips; a noisy clip denoised, tidied and
// graded; the editor of a clone cleaned on import, of a designed voice
// with its reference and of one without; Stories' whole dialogue with
// its characters' references; at 1100×720 and 820×520 @125 %, Chinese and
// English:
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshotsReferences .
func TestSnapshotsReferences(t *testing.T) { runSnapshots(t, snapshotsReferences) }

func snapshotsReferences(t *testing.T, out string) {
	t.Cleanup(func() { i18n.SetLocale("en") })
	useMemKeys(t)
	stubMachine(t, 24, true)
	vdm := withDesignCatalog(t)
	for _, cfg := range []b2Config{
		{"1100-zh", 1100, 720, 1, false, "zh-Hans"},
		{"min125-zh", 820, 520, 1.25, false, "zh-Hans"},
		{"1100-en", 1100, 720, 1, false, "en"},
	} {
		i18n.SetLocale("en")
		a, eng, tt := newTestApp(t)
		acceptLicences(t, a)
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
			f, err := os.Create(filepath.Join(out, fmt.Sprintf("%s-%s.png", name, cfg.name)))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := png.Encode(f, tt.Image()); err != nil {
				t.Fatal(err)
			}
		}
		shoot("r01-clone-tips", func() {
			a.startClone("")
			a.clone.tipsOpen = true
		})
		shoot("r02-clone-noisy", func() {
			clip := filepath.Join(testDir(t), "kitchen.wav")
			writeSpeech(clip, 12, []span{{0.9, 3.5}, {4, 7.2}, {7.6, 11}})
			addNoise(t, clip, -40)
			a.startClone("")
			a.cloneLoad(clip, "kitchen.wav", true)
			answerEnhance(t, eng)
			answerTranscribe(t, eng, "我常对新来的播音员说：慢一点，让句子呼吸。")
			a.clone.name = "厨房里录的"
		})
		a.resetClone("")

		ctx := context.Background()
		clone := addVoice(t, a, core.Voice{Name: "访谈", Kind: core.VoiceCloned, RefText: "慢一点，让句子呼吸。"}, 9)
		clone, _ = a.lib.svc.Voices.CleanReference(ctx, clone.ID, a.voicePath(&clone, clone.RefAudio), 8.6,
			&core.RefQuality{Grade: "ok", Hints: []string{"noisy", "flat"}, Cleaned: []string{"denoised", "tidied"}, OriginalSeconds: 9})
		a.voiceChanged(core.VoiceChanged{Voice: clone})
		shoot("r03-editor-cleaned", func() {
			a.navigate("/voices")
			a.openVoiceEditor(clone.ID)
		})
		mom := addVoice(t, a, core.Voice{Name: "妈妈", Kind: core.VoiceDesigned, Design: "一位三十多岁的年轻妈妈，声音温柔", Language: "zh", Seed: 11,
			CreatedWith: vdm.Repo, DesignAttrs: attrsOf("一位三十多岁的年轻妈妈，声音温柔")}, 0)
		ref := filepath.Join(testDir(t), "mom-ref.wav")
		writeSpeech(ref, 12, []span{{0.15, 11.85}})
		mom.RefText, mom.RefSeconds = "上周末我去爬山，本来以为一个小时就能到顶，结果走了快三个小时！你猜怎么着？山顶的风景美得不得了，一点都不后悔。", 11.9
		mom.RefCheck = &core.RefQuality{Grade: "good", Cleaned: []string{"tidied"}, OriginalSeconds: 12.6}
		mom, _ = a.lib.svc.Voices.SaveFiles(ctx, mom, core.VoiceFiles{Reference: ref})
		a.voiceChanged(core.VoiceChanged{Voice: mom})
		sample := filepath.Join(testDir(t), "kid-sample.wav")
		writeSpeech(sample, 4, []span{{0.1, 1.6}, {3, 3.9}})
		kid, _ := a.lib.svc.Voices.SaveFiles(ctx, core.Voice{Name: "孩子", Kind: core.VoiceDesigned, Design: "一个七岁的小女孩", Language: "zh", Seed: 3,
			CreatedWith: vdm.Repo, DesignAttrs: attrsOf("一个七岁的小女孩"), SampleText: "你好，这就是我的声音。"}, core.VoiceFiles{Sample: sample})
		a.voiceChanged(core.VoiceChanged{Voice: kid})
		shoot("r04-editor-designed-reference", func() { a.openVoiceEditor(mom.ID) })
		shoot("r05-editor-designed-none", func() { a.openVoiceEditor(kid.ID) })
		a.edit = nil

		soulx, _ := dialogueEntry()
		shoot("r06-stories-dialogue-refs", func() {
			a.navigate("/stories")
			p := a.lfStories
			kittenDoc(&p.doc)
			d := &p.doc
			d.Model, d.Voice = entry(t, "qwen3-tts-1.7b-base-8bit").Repo, mom.ID
			d.Cast[0].Voice, d.Cast[1].Voice = kid.ID, mom.ID
			d.Production.Dialogue, d.Production.DialogueModel = true, soulx.Repo
			p.open["setup"], p.open["cast"], p.open["scene"], p.open["dialogue-refs"] = false, false, true, true
			p.panel.set, p.panel.open = true, true
		})
	}
}
