package main

// The Studio's voice list in the shared voice picker (voice_picker.go,
// thread #60): the model's speakers, the saved voices it can speak
// with, then Clone a clip…, a portable copy of the speaker and Manage
// voices….

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/i18n"
)

// studioVoicePicker is the inspector's voice picker for model e.
func (a *app) studioVoicePicker(c *ui.Context, e catalog.Entry) {
	cur, opts, acts := a.studioVoiceChoices(e)
	o := voicePickerOpts{label: i18n.T("Voice"), text: a.studioVoiceLabel(e), voice: a.findVoice(a.studioVoice), width: 300}
	if v, ok := a.voicePicker(c, studioVoiceKey, cur, opts, o); ok {
		if act := acts[v]; act != nil {
			act()
		}
	}
}

// studioVoiceChoices are the Studio's voice rows for model e, the chosen
// one's value and what picking each does.
func (a *app) studioVoiceChoices(e catalog.Entry) (cur string, opts []voiceOption, acts map[string]func()) {
	speaker, style := splitVoice(a.voice, e.AllVoiceIDs())
	saved := a.findVoice(a.studioVoice)
	clip := a.studioClip != nil && a.studioVoice == "" && e.Has(catalog.CapClone)
	ref := !clip && saved == nil && e.Has(catalog.CapClone) && strings.TrimSpace(a.refAudio) != ""
	acts = map[string]func(){}
	clearOthers := func() { a.studioVoice, a.studioClip, a.refAudio, a.refText = "", nil, "", "" }
	add := func(op voiceOption, act func()) {
		opts = append(opts, op)
		acts[op.value] = act
	}
	switch {
	case saved != nil:
		cur = "v:" + saved.ID
	case clip:
		cur = "clip"
	case ref:
	case speaker != "":
		cur = "sp:" + speaker
	default:
		cur = "default"
	}
	if !e.NeedsReference {
		group := i18n.T("Model voices")
		ids := e.VoiceIDsIn(i18n.Locale())
		if speaker != "" && !slices.Contains(ids, speaker) {
			ids = append(ids, speaker) // a hidden speaker chosen before stays shown
		}
		if len(ids) == 0 {
			add(voiceOption{value: "default", label: i18n.T("Model default"), group: group}, clearOthers)
		}
		rec := ""
		if p, ok := catalog.PresetFor(e.ID); ok {
			rec = p.VoiceFor(i18n.Locale())
		}
		for _, id := range ids {
			sub := ""
			if id == rec {
				sub = i18n.T("Recommended")
			}
			add(voiceOption{value: "sp:" + id, label: id, sub: sub, group: group, preview: speakerPreview(e, id)}, func() {
				clearOthers()
				a.voice = joinVoice(id, style)
			})
		}
	}
	mine := i18n.T("My voices")
	if clip {
		add(voiceOption{value: "clip", label: i18n.T("Unsaved clip: %s", a.studioClip.Name), group: mine}, func() {})
	}
	for i := range a.voices {
		v := &a.voices[i]
		if ok, _ := voiceUsable(v, e); ok {
			add(voiceOption{value: "v:" + v.ID, label: v.Name, sub: voiceKindLabel(v), group: mine, voice: v, preview: a.savedVoicePreview(v)}, func() {
				a.studioVoice, a.studioClip = v.ID, nil
			})
		}
	}
	if e.Has(catalog.CapClone) {
		label := i18n.T("Clone a clip…")
		if ref {
			label = i18n.T("Clip: %s", filepath.Base(a.refAudio))
		}
		add(voiceOption{value: "ref", label: label, sub: i18n.T("A few seconds of clean speech; not saved to your voices"), action: true, on: ref}, func() {
			a.studioVoice, a.studioClip = "", nil
			a.chooseRefAudio()
		})
	}
	if speaker != "" && saved == nil && !clip && !ref && !e.Has(catalog.CapClone) {
		add(voiceOption{value: "portable", label: i18n.T("Make a portable copy of %s…", speaker), sub: i18n.T("An approximate clone of it, for models that clone"), action: true}, func() {
			a.makeSpeakerPortable(e, speaker)
		})
	}
	add(voiceOption{value: "manage", label: i18n.T("Manage voices…"), action: true}, func() { a.navigate("/voices") })
	return cur, opts, acts
}

// studioVoiceKey is the Studio's voice picker's key (tests open it).
const studioVoiceKey = "studio-voice"
