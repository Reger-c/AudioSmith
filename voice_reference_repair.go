package main

// Checking references made from a voice itself (thread #93, after #91).
// Before #93 a designed voice's reference (or a portable copy's) could be
// cut off by the model's length limit and still be saved with its whole
// text; clones then said the missing words before every line. Such a
// reference is checked once, the first time it is used (where #83 makes
// a missing one: a Studio take on a model that clones, a story or book
// that needs it, a portable copy): the recognizer listens, and a
// reference that does not say its text is made again (a designed voice
// whose design model is at hand), or gets what it says as its text. The
// result is kept (RefQuality.TextChecked); without a recognizer the
// reference is used as it is and checked another time.

import (
	"context"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp/refcheck"
	"github.com/hehehai/AudioSmith/internal/i18n"
	vd "github.com/hehehai/AudioSmith/internal/voicedesign"
)

// refCheck is a reference being checked: what waits for it.
type refCheck struct {
	then []func()
}

// refUnchecked reports whether v's reference was made from the voice
// itself (a designed voice's, unlocked; a portable copy's) and has not
// been heard saying its text yet (nor could be this session).
func (a *app) refUnchecked(v *core.Voice) bool {
	if v == nil || v.RefAudio == "" || v.RefText == "" || v.RefCheck != nil && v.RefCheck.TextChecked || a.refCheckSkip[v.ID] {
		return false
	}
	return v.Kind == core.VoiceDesigned && !v.Locked || isPortable(v)
}

// refChecking reports whether v's reference is being checked.
func (a *app) refChecking(id string) bool { return a.refChecks[id] != nil }

// checkReference has the recognizer hear v's reference, then runs then:
// at once when it says its text (marked checked); after it is made again
// when it does not and can be (a designed voice); else with what it says
// as its text. A reference that can't be heard is used as it is.
func (a *app) checkReference(v *core.Voice, then func()) {
	if c := a.refChecks[v.ID]; c != nil {
		c.then = append(c.then, then)
		return
	}
	if a.refChecks == nil {
		a.refChecks = map[string]*refCheck{}
	}
	if a.refCheckSkip == nil {
		a.refCheckSkip = map[string]bool{}
	}
	c := &refCheck{then: []func(){then}}
	a.refChecks[v.ID] = c
	id, was := v.ID, core.RefHeard{Audio: v.RefAudio, Text: v.RefText}
	want := was.Text
	finish := func(checked bool) {
		if !checked {
			a.refCheckSkip[id] = true // not again this session
		}
		delete(a.refChecks, id)
		for _, f := range c.then {
			f()
		}
	}
	a.transcribeClip(a.voicePath(v, was.Audio), vd.RefTextLang(want), func(heard string, err error) {
		v := a.findVoice(id)
		switch {
		case err != nil || v == nil || !sameRef(v, was):
			finish(false)
		case textDrift(want, heard) <= refcheck.RetakeCER:
			a.saveRefText(id, was, want, finish)
		case v.Kind == core.VoiceDesigned && a.canMakeReferenceNow(v):
			a.toast(i18n.T("The reference of “%s” doesn't say its text: making it again…", v.Name))
			a.makeReference(v, func(ok bool) {
				if ok {
					finish(true)
					return
				}
				a.useHeardText(id, was, heard, finish)
			})
		default:
			a.useHeardText(id, was, heard, finish)
		}
	})
}

// sameRef reports whether v's reference is still was.
func sameRef(v *core.Voice, was core.RefHeard) bool {
	return v.RefAudio == was.Audio && v.RefText == was.Text
}

// useHeardText makes heard the text of voice id's reference was, when it
// can be the text of that clip.
func (a *app) useHeardText(id string, was core.RefHeard, heard string, finish func(bool)) {
	v := a.findVoice(id)
	if v == nil || !sameRef(v, was) || !heardFits(heard, v.RefSeconds) {
		finish(false)
		return
	}
	a.toast(i18n.T("The reference of “%s” now has the text it says.", v.Name))
	a.saveRefText(id, was, heard, finish)
}

// saveRefText records that voice id's reference was says s.
func (a *app) saveRefText(id string, was core.RefHeard, s string, finish func(bool)) {
	svc := a.lib.svc.Voices
	a.background(func() {
		saved, err := svc.ReferenceTextChecked(context.Background(), id, was, s)
		a.post(func() {
			if err == nil {
				a.voiceChanged(core.VoiceChanged{Voice: saved})
			}
			finish(err == nil)
		})
	})
}
