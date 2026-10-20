package main

// Portable voices (thread #42): a model's built-in speaker or a designed
// voice made into a cloned voice that any model that clones can speak
// with. Its model says reference texts (#83: the best of three takes,
// tidied to −18 LUFS; a designed voice's own reference); that clip is
// saved as a new cloned voice "<name> (approximate)": a clone of a voice
// sounds close to it, not the same (#40 measured 0.64–0.70 similarity),
// so the name and the toast say so.

import (
	"context"
	"strings"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
)

// portableTag marks the voices made portable.
const portableTag = "portable"

// portableJob is what a portable copy being rendered becomes.
type portableJob struct {
	name, lang, origin string
}

// canMakePortable reports whether v can be made portable: a built-in
// speaker or a designed voice (a cloned voice already is).
func canMakePortable(v *core.Voice) bool {
	if v != nil && v.Kind == core.VoiceBuiltin && removedVoice(v) {
		return false // its model is gone: nothing can render it
	}
	return v != nil && (v.Kind == core.VoiceBuiltin || v.Kind == core.VoiceDesigned)
}

// makePortable makes a saved voice portable: a designed voice's
// reference is used as it is (made first when it has none; a locked
// voice's locked take); a built-in voice says refCandidates reference
// texts, and the best take is kept (voice_reference.go).
func (a *app) makePortable(v *core.Voice) {
	if !canMakePortable(v) {
		return
	}
	lang := v.Language
	pj := portableJob{name: v.Name, lang: lang, origin: "voice:" + v.ID}
	if v.Kind == core.VoiceDesigned {
		switch {
		case a.refUnchecked(v): // heard first (#93)
			id := v.ID
			a.checkReference(v, func() {
				if v := a.findVoice(id); v != nil && !a.refUnchecked(v) {
					a.makePortable(v)
				}
			})
		case v.RefAudio != "":
			a.savePortable(pj, portableFiles{path: a.voicePath(v, v.RefAudio), text: v.RefText, seconds: v.RefSeconds, model: v.CreatedWith, check: v.RefCheck}, nil)
		case needsReference(v):
			id := v.ID
			a.toast(i18n.T("Making a portable copy of “%s”…", v.Name))
			a.makeReference(v, func(ok bool) {
				switch v := a.findVoice(id); {
				case v == nil:
				case ok:
					a.makePortable(v)
				case v.Sample != "": // no reference: its sample, as before
					a.savePortable(pj, portableFiles{path: a.voicePath(v, v.Sample), text: v.SampleText, model: v.CreatedWith}, nil)
				}
			})
		case v.Sample != "":
			a.savePortable(pj, portableFiles{path: a.voicePath(v, v.Sample), text: v.SampleText, model: v.CreatedWith}, nil)
		}
		return
	}
	e := entryFor(v.Model)
	lang = firstOf(lang, e.SpeakerLanguage(v.Speaker), catalog.BaseLanguage(i18n.Locale()))
	pj.lang = lang
	t := core.Take{Model: e.Repo, Voice: v.Speaker, Language: modelLanguage(e, lang)}
	id := v.ID
	a.makePortableRef(pj, t, func() {
		if v := a.findVoice(id); v != nil {
			a.makePortable(v)
		}
	})
}

// makeSpeakerPortable makes model e's built-in speaker portable.
func (a *app) makeSpeakerPortable(e catalog.Entry, speaker string) {
	lang := firstOf(e.SpeakerLanguage(speaker), catalog.BaseLanguage(i18n.Locale())) // the speaker's own, else the UI's
	t := core.Take{Model: e.Repo, Voice: speaker, Language: modelLanguage(e, lang)}
	a.makePortableRef(portableJob{name: speaker, lang: lang, origin: "speaker:" + e.ID + ":" + speaker}, t, func() {
		a.makeSpeakerPortable(e, speaker)
	})
}

// portableFiles are what a portable copy is saved from: the reference
// (path, tidied), the take before tidying (original, if any), what it
// says, its length, the model that said it and the checker's findings.
type portableFiles struct {
	path, original, text string
	seconds              float64
	model                string
	check                *core.RefQuality
}

// savePortable saves a reference clip as the portable copy; then, if
// given, learns whether it was saved.
func (a *app) savePortable(pj portableJob, f portableFiles, then func(ok bool)) {
	v := core.Voice{Name: i18n.T("%s (approximate)", pj.name), Kind: core.VoiceCloned, Language: pj.lang, RefText: f.text,
		RefSeconds: f.seconds, RefCheck: f.check, CreatedWith: f.model, Origin: "portable:" + pj.origin, Tags: []string{portableTag}}
	svc := a.lib.svc.Voices
	a.background(func() {
		saved, err := svc.SaveFiles(context.Background(), v, core.VoiceFiles{Reference: f.path, Original: f.original})
		a.post(func() {
			if then != nil {
				then(err == nil)
			}
			if err != nil {
				a.toast(i18n.T("Could not save the portable copy: %v", err))
				return
			}
			a.voiceChanged(core.VoiceChanged{Voice: saved})
			a.toast(i18n.T("Saved “%s”: models that clone can speak with it. It sounds close to the original, not the same.", saved.Name))
		})
	})
}

// isPortable reports whether v is a portable copy.
func isPortable(v *core.Voice) bool {
	return v != nil && strings.HasPrefix(v.Origin, "portable:")
}

// voiceKindLabel names a voice's kind; a portable copy says it is an
// approximate clone.
func voiceKindLabel(v *core.Voice) string {
	if isPortable(v) {
		return i18n.T("Portable copy (approximate clone)")
	}
	return voiceKindText(v.Kind)
}
