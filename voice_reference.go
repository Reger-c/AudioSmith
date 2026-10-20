package main

// References made from a voice itself (thread #83, after #81 §4.3): a
// designed voice's reference and a built-in voice's portable copy. Clone
// and dialogue models copy a reference's intonation, pace and loudness,
// so the short greeting a designed voice's sample says is a poor one. The
// voice says refCandidates lively everyday texts (voicedesign's reference
// texts, 10–14 s, no fillers), asked to talk casually (#81's group D, the
// user's pick by ear: added to a designed voice's description, or a
// built-in speaker's instruction when its model takes one); each take is tidied (refcheck.Tidy), measured and heard
// by the recognizer, and the best is kept (refcheck.Pick: no fault, the
// pitch nearest the voice's sample). A designed voice keeps it as its
// reference (RefAudio, RefText: what it says exactly), apart from its
// sample, which stays for previews; a built-in voice's becomes its
// portable copy. The candidates' takes are deleted afterwards.
//
// A reference must say its text (thread #93, after #91: slow designed
// voices were cut off by the model's length limit and saved with the
// whole sentence, so clones said the missing half before every line):
// Qwen3-TTS takes get a generous length limit (refMaxTokens), a take that
// reaches it counts as cut off, and when no candidate says its text
// (refcheck.NeedsRetake) they are made once more with another seed. A
// kept take the recognizer hears saying something else gets what it
// heard as its text, or none is kept (refAccept).

import (
	"context"
	"errors"
	"os"
	"slices"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp/refcheck"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/licence"
	"github.com/hehehai/AudioSmith/internal/text"
	vd "github.com/hehehai/AudioSmith/internal/voicedesign"
)

// refCandidates is how many takes a reference is chosen from.
const refCandidates = 3

// refMaxTokens is the length limit of a Qwen3-TTS reference take, in
// codec frames (12.5 a second): 50 s for a 10–14 s text, room for the
// slowest voice. The engine's own limit follows the text's length (patch
// 0008: 0.8 s per text token, 0.48 s before), which slow voices reached.
const refMaxTokens = 625

// refTakeLimit is the length limit to set on model e's reference takes
// (0: the model's own).
func refTakeLimit(e catalog.Entry) int {
	if e.Family == "qwen3_tts" {
		return refMaxTokens
	}
	return 0
}

// refMake is a reference being made: its candidates and what to do with
// the best.
type refMake struct {
	key      string       // "voice:<id>" (a designed voice) or "portable:<origin>"
	voiceID  string       // the designed voice it is for
	recipe   string       // the voice's recipe key when it started: a changed design drops the result
	portable *portableJob // a built-in voice: the best becomes this portable copy
	model    string
	lang     string
	cands    []*refCand
	wantF0   float64 // the median pitch of the voice's sample (0: none)
	checking bool
	err      string
	then     []func(ok bool)
	// takes are the candidates' recipes and retry what a licence prompt
	// runs, for a second round (round 1) when none said its text.
	takes []core.Take
	retry func()
	round int
}

// refCand is one candidate: the take, then its tidied file and measures.
type refCand struct {
	text   string
	takeID string
	take   *core.Take
	err    string
	path   string // tidied
	secs   float64
	check  refcheck.Candidate
	heard  string // what the recognizer heard ("": it did not listen)
}

func (c *refCand) settled() bool { return c.take != nil || c.err != "" }

// running reports whether the reference is still being made.
func (m *refMake) running() bool { return m != nil && m.err == "" }

// progress is how many candidates are rendered, of how many.
func (m *refMake) progress() (done, total int) {
	for _, c := range m.cands {
		if c.settled() {
			done++
		}
	}
	return done, len(m.cands)
}

// refMaking is the reference being made for designed voice id, or nil.
func (a *app) refMaking(id string) *refMake {
	if m := a.refMakes["voice:"+id]; m.running() {
		return m
	}
	return nil
}

// refFailed is why the last reference of designed voice id failed, or "".
func (a *app) refFailed(id string) string {
	if m := a.refMakes["voice:"+id]; m != nil {
		return m.err
	}
	return ""
}

// needsReference reports whether designed voice v has no reference yet
// and can get one (a locked voice keeps its locked take).
func needsReference(v *core.Voice) bool {
	return v != nil && v.Kind == core.VoiceDesigned && !v.Locked && v.RefAudio == ""
}

// canMakeReference reports whether "Make reference" applies to v.
func canMakeReference(v *core.Voice) bool {
	return v != nil && v.Kind == core.VoiceDesigned && !v.Locked
}

// makeReference makes designed voice v's reference (again); then, if
// given, runs once it is saved (true) or failed (false).
func (a *app) makeReference(v *core.Voice, then func(ok bool)) {
	key := "voice:" + v.ID
	if m := a.refMakes[key]; m.running() {
		if then != nil {
			m.then = append(m.then, then)
		}
		return
	}
	fail := func(msg string) {
		if a.refMakes == nil {
			a.refMakes = map[string]*refMake{}
		}
		a.refMakes[key] = &refMake{key: key, voiceID: v.ID, err: msg}
		a.toast(msg)
		if then != nil {
			then(false)
		}
	}
	e, ok := a.designModelFor(v.CreatedWith)
	if !ok {
		fail(i18n.T("No model that can design voices is available."))
		return
	}
	r := voiceRecipe(v)
	lang := firstOf(r.Attrs.Language(), v.Language)
	low, err := vd.Lower(designTarget(e), r, vd.Options{Lang: designTextLang(lang)})
	if err != nil {
		fail(err.Error())
		return
	}
	m := &refMake{key: key, voiceID: v.ID, recipe: r.Key(), model: e.Repo, lang: vd.RefLang(lang)}
	if then != nil {
		m.then = append(m.then, then)
	}
	// Asked to talk casually, as #81's group D (which the user preferred):
	// models that take the description as written.
	voice := low.Voice
	if eng, ok := vd.EngineFor(e.Family, e.Repo); ok && eng.Vocabulary == vd.VocabFreeform {
		voice = vd.WithStyle(voice, vd.ConversationalStyle(designTextLang(lang)))
	}
	reg := r.Attrs.Register()
	if r.Attrs.IsAuto() && r.Description != "" {
		reg = vd.Parse(r.Description).Attrs.Register()
	}
	var takes []core.Take
	for _, text := range vd.ReferenceTexts(lang, reg)[:refCandidates] {
		takes = append(takes, core.Take{Text: text, Model: e.Repo, Voice: voice, Seed: r.Seed, Language: modelLanguage(e, lang), VoiceID: v.ID,
			MaxTokens: refTakeLimit(e)})
	}
	id := v.ID
	a.startRefMake(m, takes, func() {
		if v := a.findVoice(id); v != nil {
			a.makeReference(v, then)
		}
	})
}

// samplePitch is the median pitch of a WAV (0 when it can't be read).
func samplePitch(path string) float64 {
	b, _, err := wav.ReadFile(path)
	if err != nil {
		return 0
	}
	return refcheck.Measure(b, refcheck.Options{CER: -1}).F0
}

// makePortableRef makes a built-in voice's portable copy from the best of
// refCandidates takes of recipe t.
func (a *app) makePortableRef(pj portableJob, t core.Take, retry func()) {
	key := "portable:" + pj.origin
	if m := a.refMakes[key]; m.running() {
		return
	}
	m := &refMake{key: key, portable: &pj, model: t.Model, lang: vd.RefLang(pj.lang)}
	if entryFor(t.Model).Has(catalog.CapInstruct) && t.Voice != "" { // "Speaker, instruction"
		t.Voice += ", " + vd.ConversationalStyle(pj.lang)
	}
	var takes []core.Take
	for _, text := range vd.ReferenceTexts(pj.lang, vd.RegisterAdult)[:refCandidates] {
		c := t
		c.Text, c.MaxTokens = text, refTakeLimit(entryFor(t.Model))
		takes = append(takes, c)
	}
	a.toast(i18n.T("Making a portable copy of “%s”…", pj.name))
	a.startRefMake(m, takes, retry)
}

// startRefMake queues the candidates' takes (asking for the model's
// licence first, once).
func (a *app) startRefMake(m *refMake, takes []core.Take, retry func()) {
	e := entryFor(m.model)
	if licence.Required(e) != nil && !licence.Covers(a.accepted, e) {
		a.askLicence(e, retry)
		return
	}
	if a.refMakes == nil {
		a.refMakes = map[string]*refMake{}
	}
	a.refMakes[m.key] = m
	m.takes, m.retry = takes, retry
	a.queueRefTakes(m, takes)
}

// queueRefTakes queues the candidates' takes.
func (a *app) queueRefTakes(m *refMake, takes []core.Take) {
	retry := m.retry
	for _, t := range takes {
		t.Render = &core.TakeRender{Bits: 24}
		c := &refCand{text: t.Text}
		m.cands = append(m.cands, c)
		c.takeID = a.voiceGenerate("reference", m.key, t, retry)
		// A take refused at once (no take, or its job already failed).
		if j := a.voiceJobs[c.takeID]; j == nil || j.err != "" {
			c.err = i18n.T("The job failed")
			if j != nil {
				c.err = j.err
			}
		}
	}
	a.refSettled(m)
}

// refTakeCreated takes a candidate's take.
func (a *app) refTakeCreated(key string, t core.Take) {
	m := a.refMakes[key]
	if !m.running() {
		return
	}
	for _, c := range m.cands {
		if c.takeID == t.ID {
			c.take = &t
		}
	}
	a.refSettled(m)
}

// refTakeFailed notes a candidate that could not be made.
func (a *app) refTakeFailed(key, takeID, err string) {
	m := a.refMakes[key]
	if !m.running() {
		return
	}
	for _, c := range m.cands {
		if c.takeID == takeID && c.take == nil {
			c.err = firstOf(err, "failed")
		}
	}
	a.refSettled(m)
}

// refSettled checks the candidates once every one is rendered or failed.
func (a *app) refSettled(m *refMake) {
	done, total := m.progress()
	if done < total || m.checking {
		return
	}
	var ok []*refCand
	for _, c := range m.cands {
		if c.take != nil {
			ok = append(ok, c)
		}
	}
	if len(ok) == 0 {
		a.refFinish(m, nil, firstOf(m.cands[0].err, i18n.T("The job failed")))
		return
	}
	m.checking = true
	dir := a.referencesDir()
	paths := make([]string, len(ok))
	for i, c := range ok {
		paths[i] = a.takeStore.AudioPath(*c.take)
	}
	sample := "" // the voice's sample: the pitch to stay near
	if v := a.findVoice(m.voiceID); v != nil && v.Sample != "" {
		sample = a.voicePath(v, v.Sample)
	}
	limit := 0.0 // seconds: a take this long was cut off
	if len(m.takes) > 0 && m.takes[0].MaxTokens > 0 {
		limit = float64(m.takes[0].MaxTokens)/12.5 - 0.5
	}
	a.background(func() {
		for i, c := range ok {
			c.path, c.secs, c.check = tidyCandidate(paths[i], dir, c.text)
			c.check.Truncated = limit > 0 && c.check.Raw.Seconds >= limit
		}
		f0 := 0.0
		if sample != "" {
			f0 = samplePitch(sample)
		}
		a.post(func() {
			m.wantF0 = f0
			a.refHear(m, ok, 0)
		})
	})
}

// tidyCandidate tidies a candidate's take into dir and measures it before
// and after.
func tidyCandidate(path, dir, text string) (string, float64, refcheck.Candidate) {
	var c refcheck.Candidate
	b, _, err := wav.ReadFile(path)
	if err != nil {
		c.Raw.CER, c.Tidy.CER = -1, -1
		return "", 0, c
	}
	o := textOptions(text)
	c.Raw = refcheck.Measure(b, o)
	t, _ := refcheck.Tidy(b, nil)
	c.Tidy = refcheck.Measure(t, o)
	os.MkdirAll(dir, 0o755)
	out := uniquePath(dir, "refcand")
	if err := wav.WriteFile(out, t, wav.PCM24, wav.Options{}); err != nil {
		return "", 0, c
	}
	return out, t.Seconds(), c
}

// refHear has the recognizer hear candidates from k on, for their error
// rates; without one, they stay unknown.
func (a *app) refHear(m *refMake, cs []*refCand, k int) {
	if k >= len(cs) || !m.running() {
		a.refPick(m, cs)
		return
	}
	c := cs[k]
	if c.path == "" {
		a.refHear(m, cs, k+1)
		return
	}
	a.transcribeClip(c.path, m.lang, func(heard string, err error) {
		if errors.Is(err, errNoASR) {
			a.refPick(m, cs)
			return
		}
		if err == nil {
			cer := textDrift(c.text, heard)
			c.check.Raw.CER, c.check.Tidy.CER, c.heard = cer, cer, heard
		}
		a.refHear(m, cs, k+1)
	})
}

// refPick keeps the best candidate.
func (a *app) refPick(m *refMake, cs []*refCand) {
	var usable []*refCand
	var checks []refcheck.Candidate
	for _, c := range cs {
		if c.path != "" {
			usable, checks = append(usable, c), append(checks, c.check)
		}
	}
	if m.round == 0 && refcheck.NeedsRetake(checks) && len(m.takes) > 0 {
		a.refRetake(m)
		return
	}
	best := refcheck.Pick(checks, m.wantF0)
	if best < 0 {
		a.refFinish(m, nil, i18n.T("Could not read the takes."))
		return
	}
	if !refAccept(usable[best]) {
		a.refFinish(m, nil, i18n.T("The voice did not say its reference text, even in a second try: try another description, or make the reference again."))
		return
	}
	a.refFinish(m, usable[best], "")
}

// refAccept makes the kept candidate's text what it says: a take that
// says its text keeps it; one the recognizer heard saying something else
// (or cut off) gets what it heard, when that is long enough to clone
// from; else false: a reference whose text does not match its audio
// makes clones say the difference.
func refAccept(c *refCand) bool {
	if !c.check.Mismatched() {
		return true
	}
	if c.check.Tidy.Speech < refcheck.ShortSpeech || c.check.Tidy.Seconds > 2*refcheck.PickMaxLength || !heardFits(c.heard, c.check.Tidy.Speech) {
		return false
	}
	c.text = c.heard
	c.check.Raw.CER, c.check.Tidy.CER, c.check.Truncated = 0, 0, false
	return true
}

// heardFits reports whether heard (a transcript) can be the text of
// seconds of speech: at least a word or a character for every two
// seconds (a recognizer that heard "嗯" in 12 s of speech did not hear
// it), and five.
func heardFits(heard string, seconds float64) bool {
	w, c := text.Count(heard)
	return max(w, c) >= max(5, int(seconds/2))
}

// refRetake makes the candidates again, once, each with the next seed:
// none said its text.
func (a *app) refRetake(m *refMake) {
	a.refCleanup(m, nil)
	m.round, m.checking, m.cands = 1, false, nil
	takes := slices.Clone(m.takes)
	for i := range takes {
		if s := takes[i].Seed; s > 0 {
			takes[i].Seed = s%vd.MaxSeed + 1
		}
	}
	m.takes = takes
	a.queueRefTakes(m, takes)
}

// refCleanup deletes the candidates' takes and their tidied files, but
// keep's file.
func (a *app) refCleanup(m *refMake, keep *refCand) {
	var takeIDs, files []string
	for _, c := range m.cands {
		if c.take != nil {
			takeIDs = append(takeIDs, c.take.ID)
		}
		if c.path != "" && c != keep {
			files = append(files, c.path)
		}
	}
	svcT := a.lib.svc.Takes
	a.background(func() {
		for _, id := range takeIDs {
			svcT.Delete(context.Background(), id)
		}
		for _, f := range files {
			os.Remove(f)
		}
	})
}

// refFinish saves the best candidate (nil: none, err says why), cleans up
// the rest and runs what waited.
func (a *app) refFinish(m *refMake, best *refCand, err string) {
	cleanup := func() { a.refCleanup(m, best) }
	done := func(ok bool, msg string) {
		m.checking = false
		if !ok {
			m.err = firstOf(msg, i18n.T("The job failed"))
		} else {
			delete(a.refMakes, m.key)
		}
		for _, f := range m.then {
			f(ok)
		}
		m.then = nil
	}
	if best == nil {
		cleanup()
		done(false, err)
		return
	}
	q := refQuality(best.check.Tidy, []string{"tidied"}, best.check.Raw.Seconds)
	q.TextChecked = best.check.Tidy.CER >= 0 // heard saying its text (refAccept)
	raw := a.takeStore.AudioPath(*best.take)
	if pj := m.portable; pj != nil {
		a.savePortable(*pj, portableFiles{path: best.path, original: raw, text: best.text, seconds: best.secs, model: m.model, check: q}, func(ok bool) {
			os.Remove(best.path)
			cleanup()
			done(ok, "")
		})
		return
	}
	v := a.findVoice(m.voiceID)
	if v == nil || voiceRecipe(v).Key() != m.recipe {
		os.Remove(best.path)
		cleanup()
		done(false, i18n.T("The voice changed while its reference was made."))
		return
	}
	nv := *v
	nv.RefText, nv.RefSeconds, nv.RefCheck = best.text, best.secs, q
	svc := a.lib.svc.Voices
	a.background(func() {
		saved, err := svc.SaveFiles(context.Background(), nv, core.VoiceFiles{Reference: best.path, Original: raw})
		os.Remove(best.path)
		a.post(func() {
			cleanup()
			if err != nil {
				done(false, i18n.T("Could not save the reference: %v", err))
				return
			}
			a.voiceChanged(core.VoiceChanged{Voice: saved})
			done(true, "")
		})
	})
}

// canMakeReferenceNow reports whether v's reference can be made without
// asking anything: its design model's licence is accepted (or needs none)
// and the model is not known to be missing.
func (a *app) canMakeReferenceNow(v *core.Voice) bool {
	e, ok := a.designModelFor(v.CreatedWith)
	if !ok || licence.Required(e) != nil && !licence.Covers(a.accepted, e) {
		return false
	}
	m := a.model(e)
	return !m.known || m.local.State == hub.Downloaded
}

// refToMake reports whether ensureReferences makes v's reference: it has
// none, it can be made without asking anything, and it did not fail.
func (a *app) refToMake(v *core.Voice) bool {
	return needsReference(v) && a.refFailed(v.ID) == "" && a.canMakeReferenceNow(v)
}

// refWork reports whether ensureReferences has work for v: a reference to
// make or to check.
func (a *app) refWork(v *core.Voice) bool { return a.refToMake(v) || a.refUnchecked(v) }

// ensureReferences makes the references the voices lack (needsReference),
// when that asks nothing of the user (canMakeReferenceNow) and did not
// fail before, and checks those not yet heard saying their text
// (refUnchecked, thread #93); it runs then once every one is done or
// failed; at once when there is none. It says how many it makes and how
// many it checks.
func (a *app) ensureReferences(vs []*core.Voice, then func()) (making, checking int) {
	var need, check []*core.Voice
	has := func(xs []*core.Voice, v *core.Voice) bool {
		return slices.ContainsFunc(xs, func(x *core.Voice) bool { return x.ID == v.ID })
	}
	for _, v := range vs {
		switch {
		case v == nil || has(need, v) || has(check, v):
		case a.refToMake(v):
			need = append(need, v)
		case a.refUnchecked(v):
			check = append(check, v)
		}
	}
	if len(need)+len(check) == 0 {
		then()
		return 0, 0
	}
	left := len(need) + len(check)
	done := func() {
		if left--; left == 0 {
			then()
		}
	}
	for _, v := range need {
		a.makeReference(v, func(bool) { done() })
	}
	for _, v := range check {
		a.checkReference(v, done)
	}
	return len(need), len(check)
}

// clonesDesign reports whether model e speaks designed voices by cloning
// them (their reference, else their sample).
func clonesDesign(e catalog.Entry) bool { return e.Has(catalog.CapClone) && !canDesign(e) }
