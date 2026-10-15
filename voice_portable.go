package main

// Portable voices (thread #42): a model's built-in speaker or a designed
// voice made into a cloned voice that any model that clones can speak
// with. Its model says a ~10 s line, evened out to −18 LUFS; that take is
// saved as a new cloned voice "<name> (approximate)": a clone of a voice
// sounds close to it, not the same (#40 measured 0.64–0.70 similarity),
// so the name and the toast say so.

import (
	"context"
	"strings"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/services"
	vd "github.com/hehehai/AudioSmith/internal/voicedesign"
)

// portableTag marks the voices made portable.
const portableTag = "portable"

// portableJob is what a portable copy being rendered becomes.
type portableJob struct {
	name, lang, origin string
}

// portableLine is the line a portable copy says (about 10 s), in its
// language: varied sounds, calm, without pauses, for a clean reference.
func portableLine(lang string) string {
	if strings.HasPrefix(lang, "zh") {
		return "这是一段用来克隆音色的参考录音。我会用平稳自然的语气，把这几句话清楚地读完，让每个字的声音都能被听见。"
	}
	return "This is a reference recording for cloning a voice. I will read these few sentences in a calm and natural way, so that every sound comes through clearly."
}

// canMakePortable reports whether v can be made portable: a built-in
// speaker or a designed voice (a cloned voice already is).
func canMakePortable(v *core.Voice) bool {
	return v != nil && (v.Kind == core.VoiceBuiltin || v.Kind == core.VoiceDesigned)
}

// makePortable makes a saved voice portable: a designed voice's sample is
// used as it is; otherwise its model renders the line first.
func (a *app) makePortable(v *core.Voice) {
	if !canMakePortable(v) {
		return
	}
	lang := v.Language
	if v.Kind == core.VoiceDesigned && v.Sample != "" {
		a.savePortable(portableJob{name: v.Name, lang: lang, origin: "voice:" + v.ID}, a.voicePath(v, v.Sample), v.SampleText, 0, v.CreatedWith)
		return
	}
	var t core.Take
	switch v.Kind {
	case core.VoiceBuiltin:
		e := entryFor(v.Model)
		t = core.Take{Model: e.Repo, Voice: v.Speaker, Language: modelLanguage(e, lang)}
	case core.VoiceDesigned:
		e, ok := a.designModelFor(v.CreatedWith)
		if !ok {
			a.toast(i18n.T("No model that can design voices is available."))
			return
		}
		r := voiceRecipe(v)
		if lang == "" {
			lang = r.Attrs.Language()
		}
		low, err := vd.Lower(designTarget(e), r, vd.Options{Lang: designTextLang(lang)})
		if err != nil {
			a.toast(err.Error())
			return
		}
		t = core.Take{Model: e.Repo, Voice: low.Voice, Seed: r.Seed, Language: modelLanguage(e, lang)}
	}
	id := v.ID
	a.renderPortable(portableJob{name: v.Name, lang: lang, origin: "voice:" + v.ID}, t, func() {
		if v := a.findVoice(id); v != nil {
			a.makePortable(v)
		}
	})
}

// makeSpeakerPortable makes model e's built-in speaker portable.
func (a *app) makeSpeakerPortable(e catalog.Entry, speaker string) {
	lang := ""
	for _, v := range e.Voices {
		if v.ID == speaker {
			lang = catalog.BaseLanguage(v.Language)
		}
	}
	if lang == "" {
		lang = catalog.BaseLanguage(i18n.Locale())
	}
	t := core.Take{Model: e.Repo, Voice: speaker, Language: modelLanguage(e, lang)}
	a.renderPortable(portableJob{name: speaker, lang: lang, origin: "speaker:" + e.ID + ":" + speaker}, t, func() {
		a.makeSpeakerPortable(e, speaker)
	})
}

// renderPortable renders the portable line with recipe t, evened out.
func (a *app) renderPortable(pj portableJob, t core.Take, retry func()) {
	t.Text = portableLine(pj.lang)
	t.Render = &core.TakeRender{Bits: 24, Master: services.MasterDefault}
	if a.portableJobs == nil {
		a.portableJobs = map[string]portableJob{}
	}
	a.toast(i18n.T("Making a portable copy of “%s”…", pj.name))
	a.voiceGenerate("portable", pj.origin, t, retry)
	// The job just queued is the newest of its purpose.
	if j := a.voiceJob("portable", pj.origin); j != nil {
		a.portableJobs[j.takeID] = pj
	}
}

// portableTakeCreated saves a rendered portable line as the new voice.
func (a *app) portableTakeCreated(t core.Take) bool {
	pj, ok := a.portableJobs[t.ID]
	if !ok {
		return false
	}
	delete(a.portableJobs, t.ID)
	a.savePortable(pj, a.takeStore.AudioPath(t), t.Text, t.AudioSeconds, t.Model)
	return true
}

// savePortable saves a reference clip as the portable copy.
func (a *app) savePortable(pj portableJob, path, text string, seconds float64, model string) {
	v := core.Voice{Name: i18n.T("%s (approximate)", pj.name), Kind: core.VoiceCloned, Language: pj.lang, RefText: text,
		RefSeconds: seconds, CreatedWith: model, Origin: "portable:" + pj.origin, Tags: []string{portableTag}}
	svc := a.lib.svc.Voices
	a.background(func() {
		saved, err := svc.SaveFiles(context.Background(), v, core.VoiceFiles{Reference: path})
		a.post(func() {
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
