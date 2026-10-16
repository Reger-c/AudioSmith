package main

import (
	"testing"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/services"
)

// TestSnapshotsVoicePicker renders the shared voice picker open, with
// its ▶ previews (thread #60), in a Stories cast and a dub's cast,
// Chinese at 1100×720 and 820×520 @125 %, English at 1100×720 (the
// Studio's list is c02-voice-list, TestSnapshotsStudioC):
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshotsVoicePicker .
func TestSnapshotsVoicePicker(t *testing.T) { runSnapshots(t, snapshotsVoicePicker) }

func snapshotsVoicePicker(t *testing.T, out string) {
	t.Cleanup(func() { i18n.SetLocale("en") })
	for _, run := range []b1Run{
		{"1100", 1100, 720, 1, "zh-Hans", false},
		{"min125", 820, 520, 1.25, "zh-Hans", false},
		{"1100", 1100, 720, 1, "en", false},
	} {
		s := &b1Shooter{t: t, out: out, run: run}

		// Stories: the first character's voice.
		i18n.SetLocale("en")
		a, _, tt := newTestApp(t)
		useLfSynth(a)
		s.setup(a, tt)
		addVoice(t, a, core.Voice{Name: i18n.T("Interview voice"), Kind: core.VoiceCloned, Language: "zh"}, 8)
		a.navigate("/stories")
		p := a.lfStories
		a.loadSampleStory(p)
		// A model with speakers: Qwen3-TTS CustomVoice in Chinese, Kokoro in English.
		model, voices := entry(t, "qwen3-tts-0.6b-customvoice-8bit").Repo, []string{"Vivian", "Serena", "Uncle_Fu"}
		if run.lang == "en" {
			model, voices = kokoro, []string{"af_bella", "af_heart", "am_michael"}
		}
		p.doc.Model, p.doc.Voice = model, services.SpeakerRef+voices[0]
		for i := range p.doc.Cast {
			p.doc.Cast[i].Voice = services.SpeakerRef + voices[1+i%2]
		}
		p.open["cast"], p.open["production"] = true, false
		p.panel.set, p.panel.open = true, true // a drawer at the minimum size
		tt.Frame()
		a.voicePickerOpen("cast-voice-"+p.doc.Cast[0].ID, true)
		a.toasts = nil
		s.save(tt, "vp01-stories-cast")

		// Dub: a speaker's voice.
		i18n.SetLocale("en")
		d := newDubTest(t, 6)
		a, tt = d.a, d.tt
		s.setup(a, tt)
		addVoice(t, a, core.Voice{Name: i18n.T("Interview voice"), Kind: core.VoiceCloned, Language: "zh"}, 8)
		a.navigate("/dub")
		a.dubSourceFile(d.src)
		a.dubSet(func(p *dub.Project) { p.Targets = []string{"es"} })
		a.dubStart()
		a.dubTranslate(false)
		a.dubLang = "es"
		a.dubStep = dubStepCast
		a.dubSide.set, a.dubSide.open = true, true
		tt.Frame()
		a.voicePickerOpen("dub-cast-"+a.dub.SpeakerLabels()[0], true)
		a.toasts = nil
		s.save(tt, "vp02-dub-cast")
	}
}
