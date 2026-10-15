package main

// The voice library as the UI sees it: a snapshot of the saved voices,
// kept current by VoiceChanged events, their portraits, the pages
// /voices (library + profile editor), /voices/new (a voice from a clip),
// /design and /gallery, the takes those pages generate (previews), and
// the Studio's seam: voicePicker and applyStudioVoice.
//
// Files of the area: voices.go (this), voices_view.go (library page),
// voice_editor.go (profile editor), voice_engine.go (what it needs of the
// engine), voice_portrait.go, voice_persona.go (.asvoice import/export),
// clone*.go (reference flow), design*.go, gallery*.go.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/licence"
	vd "github.com/hehehai/AudioSmith/internal/voicedesign"
)

// voicesState is the voice area's view state.
type voicesState struct {
	voices       []core.Voice // the library, by name
	voicesLoaded bool
	// portraits are decoded portraits by voice id; portraitFor the
	// portrait file + update time each was decoded from.
	portraits   map[string]*ui.Bitmap
	portraitFor map[string]string

	// The library page.
	voiceQuery  string
	voiceKind   int // 0 all, 1 cloned, 2 designed
	voiceGrid   ui.GridState
	deleteVoice string // id the delete confirmation asks about
	deleteOpen  bool
	edit        *voiceEdit // the profile editor; nil when closed

	clone   cloneState
	design  designState
	gallery galleryState
	persona personaState

	// The Studio's voice (applyStudioVoice): a saved voice, or a clip used
	// without saving it. Both "" / nil: the model's own voices.
	studioVoice string
	studioClip  *clipRef

	// voiceJobs are the takes the voice pages generate, by take id;
	// voiceWork the other engine jobs they wait for, by owner.
	voiceJobs map[string]*voiceJob
	voiceWork map[string]func(jobs.Job)
}

// clipRef is a reference clip used without saving it as a voice.
type clipRef struct {
	Path, Text, Name, Style string
}

// openVoices connects the voice pages to the library: it subscribes to
// voice, take and job events (once, here) and loads the first snapshot.
func (a *app) openVoices(lib *library) {
	a.portraits, a.portraitFor = map[string]*ui.Bitmap{}, map[string]string{}
	a.voiceJobs = map[string]*voiceJob{}
	a.voiceWork = map[string]func(jobs.Job){}
	a.voiceGrid.Key = func(i int) any {
		if l := a.shownVoices(); i < len(l) {
			return l[i].ID
		}
		return i
	}
	core.On(lib.bus, func(ev core.VoiceChanged) { a.post(func() { a.voiceChanged(ev) }) })
	core.On(lib.bus, func(ev core.TakeCreated) { a.post(func() { a.voiceTakeCreated(ev.Take) }) })
	core.On(lib.bus, func(u jobs.Updated) {
		switch {
		case u.Chunk != nil:
		case strings.HasPrefix(u.Job.Owner, takeOwner):
			a.post(func() { a.voiceJobUpdated(u.Job) })
		case strings.HasPrefix(u.Job.Owner, voiceWorkOwner) && u.Job.State.Terminal():
			a.post(func() { a.voiceWorkDone(u.Job) })
		}
	})
	a.openGallery(lib)
	a.design.reset()
	mygo.App.OnOpenFile(func(path string) { a.post(func() { a.openVoiceFile(path) }) })
	a.loadVoices()
}

// loadVoices reads the snapshot in the background.
func (a *app) loadVoices() {
	svc := a.lib.svc.Voices
	a.background(func() {
		vs, err := svc.List(context.Background(), core.VoiceQuery{})
		a.post(func() {
			if err != nil {
				a.toast(i18n.T("Could not read the voices: %v", err))
				return
			}
			// Keep voices that events added while the list was read.
			for _, v := range a.voices {
				if !slices.ContainsFunc(vs, func(x core.Voice) bool { return x.ID == v.ID }) {
					vs = append(vs, v)
				}
			}
			sortVoices(vs)
			a.voices, a.voicesLoaded = vs, true
			a.autoStudioModel() // a new user with saved voices
			a.autoLfModels()
			a.importStarterVoices()
		})
	})
}

func sortVoices(vs []core.Voice) {
	slices.SortStableFunc(vs, func(x, y core.Voice) int {
		if c := strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name)); c != 0 {
			return c
		}
		return strings.Compare(x.ID, y.ID)
	})
}

// voiceChanged applies a VoiceChanged event to the snapshot.
func (a *app) voiceChanged(ev core.VoiceChanged) {
	i := slices.IndexFunc(a.voices, func(v core.Voice) bool { return v.ID == ev.Voice.ID })
	switch {
	case ev.Deleted:
		if i >= 0 {
			a.voices = slices.Delete(a.voices, i, i+1)
		}
		delete(a.portraits, ev.Voice.ID)
		delete(a.portraitFor, ev.Voice.ID)
		if a.edit != nil && a.edit.id == ev.Voice.ID {
			a.edit = nil
		}
		if a.studioVoice == ev.Voice.ID {
			a.studioVoice = ""
		}
		if a.design.linked == ev.Voice.ID {
			a.design.linked = ""
		}
		return
	case i >= 0:
		a.voices[i] = ev.Voice
	default:
		a.voices = append(a.voices, ev.Voice)
	}
	sortVoices(a.voices)
	if a.edit != nil && a.edit.id == ev.Voice.ID {
		a.edit.refresh(ev.Voice)
	}
	a.autoStudioModel() // a new user's first saved voice
	a.autoLfModels()
}

// findVoice is the snapshot's voice with id, or nil.
func (a *app) findVoice(id string) *core.Voice {
	if id == "" {
		return nil
	}
	for i := range a.voices {
		if a.voices[i].ID == id {
			return &a.voices[i]
		}
	}
	return nil
}

// voicePath is the absolute path of a voice's file ("" for none).
func (a *app) voicePath(v *core.Voice, name string) string {
	return a.lib.svc.Voices.FilePath(*v, name)
}

// portrait is the decoded portrait of a voice, nil while there is none
// (or it is being read: requested here, at most once per version).
func (a *app) portrait(v *core.Voice) *ui.Bitmap {
	if v.Portrait == "" {
		return nil
	}
	version := v.Portrait + "@" + v.UpdatedAt.String()
	if a.portraitFor[v.ID] == version {
		return a.portraits[v.ID]
	}
	a.portraitFor[v.ID] = version
	id, path := v.ID, a.voicePath(v, v.Portrait)
	a.background(func() {
		data, err := os.ReadFile(path)
		var bmp *ui.Bitmap
		if err == nil {
			bmp, _ = ui.DecodeBitmap(data)
		}
		a.post(func() {
			if a.portraitFor[id] == version {
				a.portraits[id] = bmp
			}
		})
	})
	return a.portraits[v.ID]
}

// voiceKindText names a voice's kind.
func voiceKindText(k core.VoiceKind) string {
	switch k {
	case core.VoiceCloned:
		return i18n.T("Cloned voice")
	case core.VoiceDesigned:
		return i18n.T("Designed voice")
	}
	return i18n.T("Built-in voice")
}

// --- the area's pages in the router (shell.go) ---------------------------

// voicesRoute builds the voice area's page for r and reports whether it
// was one: /voices, /clone (or /voices/new), /design, /gallery.
func (a *app) voicesRoute(c *ui.Context, r *ui.Route) bool {
	switch {
	case r.Match("/voices"):
		r.Title(i18n.T("My voices"))
		a.voicesPage(c)
	case r.Match("/clone"), r.Match("/voices/new"): // the sidebar's Clone; /voices/new as before
		r.Title(i18n.T("Clone a voice"))
		a.clonePage(c)
	case r.Match("/design"):
		r.Title(i18n.T("Design"))
		a.designPage(c)
	case r.Match("/gallery"):
		r.Title(i18n.T("Discover voices"))
		a.galleryPage(c)
	default:
		return false
	}
	return true
}

// voicePageTitle translates the titles of the area's pages.
func voicePageTitle(path string) (string, bool) {
	switch path {
	case "/voices":
		return i18n.T("My voices"), true
	case "/design":
		return i18n.T("Design"), true
	case "/gallery":
		return i18n.T("Discover voices"), true
	case "/clone":
		return i18n.T("Clone"), true
	}
	return "", false
}

var (
	voicesIcon  = icons.UserSpeakRounded
	designIcon  = icons.MagicStick3
	galleryIcon = icons.Widget
	cloneIcon   = icons.UserPlusRounded
)

// --- takes the voice pages generate -------------------------------------

// voiceJob is a take a voice page asked for: a "Try this voice" phrase, a
// design preview, or a voice's sample. It is followed until the take is
// saved (TakeCreated) or its job fails.
type voiceJob struct {
	takeID   string
	purpose  string // "try", "design", "sample"
	voiceID  string // the voice it is for; "" for an unsaved design
	model    string
	jobID    string
	fraction float64
	status   string
	err      string
	// needDownload is the model to offer for download after the error.
	needDownload *catalog.Entry
	take         *core.Take // the result
	started      time.Time
}

// running reports whether the job is still going.
func (j *voiceJob) running() bool { return j != nil && j.take == nil && j.err == "" }

// voiceJob returns the newest job of purpose for voice voiceID.
func (a *app) voiceJob(purpose, voiceID string) *voiceJob {
	var out *voiceJob
	for _, j := range a.voiceJobs {
		if j.purpose == purpose && j.voiceID == voiceID && (out == nil || j.started.After(out.started)) {
			out = j
		}
	}
	return out
}

// voiceGenerate queues a take for a voice page through TakeService, as
// the Studio does: retry runs again after the licence dialog accepts.
func (a *app) voiceGenerate(purpose, voiceID string, t core.Take, retry func()) {
	e := entryFor(t.Model)
	if licence.Required(e) != nil && !licence.Covers(a.accepted, e) {
		a.askLicence(e, retry)
		return
	}
	for id, j := range a.voiceJobs { // one at a time per purpose and voice
		if j.purpose == purpose && j.voiceID == voiceID && !j.running() {
			delete(a.voiceJobs, id)
		}
	}
	now := time.Now()
	t.ID = core.NewID(now)
	t.Origin = core.TakeVoice // a preview or a sample (History, the Export dialog)
	j := &voiceJob{takeID: t.ID, purpose: purpose, voiceID: voiceID, model: t.Model, fraction: -1,
		status: i18n.T("Starting…"), started: now}
	a.voiceJobs[t.ID] = j
	takes := a.lib.svc.Takes
	a.async(func() {
		id, err := takes.Generate(context.Background(), core.TakeRequest{Recipe: t})
		a.post(func() {
			if err == nil {
				if j.jobID == "" {
					j.jobID = id
				}
				return
			}
			var lic *core.LicenceRequiredError
			switch {
			case errors.As(err, &lic):
				delete(a.voiceJobs, j.takeID)
				a.askLicence(e, retry)
			case errors.Is(err, core.ErrNotDownloaded):
				j.err, j.needDownload = i18n.T("%s is not downloaded yet.", e.Label), &e
			default:
				j.err = err.Error()
			}
		})
	})
}

// voiceJobUpdated follows the jobs of the voice pages' takes.
func (a *app) voiceJobUpdated(u jobs.Job) {
	j := a.voiceJobs[strings.TrimPrefix(u.Owner, takeOwner)]
	if j == nil || j.take != nil {
		return
	}
	j.jobID = u.ID
	switch u.State {
	case jobs.StateWaiting, jobs.StateQueued:
		j.status = i18n.T("Waiting for the engine…")
	case jobs.StateRunning:
		if u.Progress.Stage != "" {
			j.status = stageText(u.Progress.Stage)
		}
		j.fraction = u.Progress.Fraction
	case jobs.StateCancelled:
		j.err = i18n.T("Cancelled")
	case jobs.StateFailed, jobs.StateInterrupted:
		j.err = jobErrorText(u.Err)
		if u.Err != nil && u.Err.Code == "model_not_downloaded" {
			e := entryFor(j.model)
			j.needDownload = &e
		}
	}
}

// voiceTakeCreated finishes a voice page's take: a sample is attached to
// its voice; "try" and design previews play.
func (a *app) voiceTakeCreated(t core.Take) {
	j := a.voiceJobs[t.ID]
	if j == nil {
		return
	}
	j.take = &t
	switch j.purpose {
	case "sample":
		a.attachSample(j.voiceID, t)
	default:
		a.play(&t)
	}
}

// attachSample saves a take's audio as a voice's sample, and plays it.
func (a *app) attachSample(voiceID string, t core.Take) {
	v := a.findVoice(voiceID)
	if v == nil {
		return
	}
	nv := *v
	nv.SampleText = t.Text
	if nv.CreatedWith == "" {
		nv.CreatedWith = t.Model
	}
	path := a.takeStore.AudioPath(t)
	svc := a.lib.svc.Voices
	a.background(func() {
		saved, err := svc.SaveFiles(context.Background(), nv, core.VoiceFiles{Sample: path})
		a.post(func() {
			if err != nil {
				a.toast(i18n.T("Could not save the voice's sample: %v", err))
				return
			}
			a.playVoice(&saved)
		})
	})
}

// --- playing voices -----------------------------------------------------

// voiceAudio is what plays for a voice: its sample, else its reference.
func (a *app) voiceAudio(v *core.Voice) string {
	if v.Sample != "" {
		return a.voicePath(v, v.Sample)
	}
	return a.voicePath(v, v.RefAudio)
}

// playVoice plays (or stops) a voice's audio; a designed voice without a
// sample renders one first.
func (a *app) playVoice(v *core.Voice) {
	path := a.voiceAudio(v)
	if path == "" {
		a.renderSample(v)
		return
	}
	if err := a.pl.Toggle("voice:"+v.ID, path); err != nil {
		a.toast(i18n.T("Could not play: %v", err))
	}
}

// voicePlaying reports whether a voice's audio plays now.
func (a *app) voicePlaying(v *core.Voice) bool { return a.pl.Playing("voice:" + v.ID) }

// sampleLine is the fixed line a voice's sample says, in its language, so
// the same recipe always renders the same sample.
func sampleLine(lang string) string {
	if strings.HasPrefix(lang, "zh") {
		return "你好，这就是我的声音。很高兴认识你，希望你喜欢。"
	}
	return "Hello, this is how I sound. It's nice to meet you, and I hope you like my voice."
}

// renderSample generates a designed voice's sample with a model that can
// design; the take becomes the voice's sample when it is saved.
func (a *app) renderSample(v *core.Voice) {
	if v.Kind != core.VoiceDesigned {
		a.toast(i18n.T("This voice has no audio."))
		return
	}
	e, ok := a.designModelFor(v.CreatedWith)
	if !ok {
		a.toast(i18n.T("No model that can design voices is available."))
		return
	}
	r := voiceRecipe(v)
	lang := r.Attrs.Language()
	if lang == "" {
		lang = v.Language
	}
	low, err := vd.Lower(designTarget(e), r, vd.Options{Lang: designTextLang(lang)})
	if err != nil {
		a.toast(err.Error())
		return
	}
	id := v.ID
	t := core.Take{Text: sampleLine(lang), Model: e.Repo, Voice: low.Voice, Seed: r.Seed, Language: modelLanguage(e, lang), VoiceID: id}
	a.voiceGenerate("sample", id, t, func() {
		if v := a.findVoice(id); v != nil {
			a.renderSample(v)
		}
	})
}

// voiceRecipe is a designed voice's recipe.
func voiceRecipe(v *core.Voice) vd.Recipe {
	r := vd.Recipe{Description: v.Design, Seed: v.Seed}
	if len(v.DesignAttrs) > 0 {
		json.Unmarshal(v.DesignAttrs, &r.Attrs)
	}
	return r
}

// modelLanguage is the value of a model's language parameter for a
// BCP-47 code ("" when the model takes none or doesn't speak it).
func modelLanguage(e catalog.Entry, code string) string {
	if code == "" || !e.Has(catalog.CapLanguage) {
		return ""
	}
	for _, l := range e.Languages {
		if l.Code == code || strings.HasPrefix(l.Code, code+"-") {
			if l.Value != "" {
				return l.Value
			}
			return l.Code
		}
	}
	if len(e.Languages) == 0 {
		return code
	}
	return ""
}

// --- models -------------------------------------------------------------

// voiceCatalog is the catalogue the voice pages offer models from (tests
// swap it for one with more design-capable models).
var voiceCatalog = catalog.Builtin

// canDesign reports whether a model makes voices from descriptions.
func canDesign(e catalog.Entry) bool {
	_, ok := vd.EngineFor(e.Family, e.Repo)
	return ok && e.Has(catalog.CapDesign)
}

// designModels are the models that can design voices, in catalogue order.
func designModels() []catalog.Entry {
	var out []catalog.Entry
	for _, e := range voiceCatalog().ForTask(catalog.TaskTTS) {
		if canDesign(e) {
			out = append(out, e)
		}
	}
	return out
}

// cloneModels are the models that clone from a reference.
func cloneModels() []catalog.Entry {
	var out []catalog.Entry
	for _, e := range voiceCatalog().ForTask(catalog.TaskTTS) {
		if e.Has(catalog.CapClone) {
			out = append(out, e)
		}
	}
	return out
}

// designModelFor is the model a designed voice renders with: repo when it
// can design, else the first that can.
func (a *app) designModelFor(repo string) (catalog.Entry, bool) {
	ms := designModels()
	for _, e := range ms {
		if e.Repo == repo {
			return e, true
		}
	}
	if len(ms) == 0 {
		return catalog.Entry{}, false
	}
	return ms[0], true
}

// designTarget is the lowering of a design-capable model.
func designTarget(e catalog.Entry) vd.Target {
	eng, _ := vd.EngineFor(e.Family, e.Repo)
	return eng.Target
}

// designTextLang is the language a design description is written in for
// speech in lang: Chinese for Chinese, English otherwise.
func designTextLang(lang string) string {
	if strings.HasPrefix(lang, "zh") {
		return "zh"
	}
	return "en"
}

// --- the Studio's seam ---------------------------------------------------

// voiceUsable reports whether model e can speak with v, and why not.
func voiceUsable(v *core.Voice, e catalog.Entry) (bool, string) {
	if e.NeedsReference && !voiceHasReference(v) {
		return false, i18n.T("%s only clones voices, and this voice has no reference clip.", e.Label)
	}
	switch {
	case v.Kind == core.VoiceBuiltin:
		if v.Model == "" || v.Model == e.Repo {
			return true, ""
		}
		return false, i18n.T("This voice belongs to %s.", catalog.Builtin().DisplayName(v.Model))
	case v.Locked && v.Sample != "" && e.Has(catalog.CapClone):
		return true, ""
	case v.Kind == core.VoiceCloned:
		if e.Has(catalog.CapClone) {
			return true, ""
		}
		return false, i18n.T("%s can't clone voices. Choose a model that clones, such as Qwen3-TTS Base or OmniVoice.", e.Label)
	case v.Kind == core.VoiceDesigned:
		if canDesign(e) {
			return true, ""
		}
		if v.Sample != "" && e.Has(catalog.CapClone) {
			return true, ""
		}
		return false, i18n.T("%s can't design voices. Choose a model that can, such as OmniVoice.", e.Label)
	}
	return false, ""
}

// voiceHasReference reports whether fillVoice gives a clone of v a
// reference clip: a cloned voice's reference, or the sample of a locked
// or designed voice (what models that only clone can speak with).
func voiceHasReference(v *core.Voice) bool {
	switch {
	case v.Kind == core.VoiceBuiltin:
		return false
	case v.Sample != "" && (v.Locked || v.Kind == core.VoiceDesigned):
		return true
	case v.Kind == core.VoiceCloned:
		return v.RefAudio != ""
	}
	return false
}

// studioVoiceOptions are the picker's choices for model e: labels and the
// voice id each stands for ("" = the model's own voices, "clip" = the
// unsaved clip).
func (a *app) studioVoiceOptions(e catalog.Entry) (labels, ids []string) {
	labels, ids = []string{i18n.T("The model's own voices")}, []string{""}
	if a.studioClip != nil {
		labels, ids = append(labels, i18n.T("Unsaved clip: %s", a.studioClip.Name)), append(ids, "clip")
	}
	seen := map[string]int{}
	for i := range a.voices {
		v := &a.voices[i]
		if ok, _ := voiceUsable(v, e); !ok && v.ID != a.studioVoice {
			continue
		}
		label := v.Name
		if n := seen[v.Name]; n > 0 {
			label = v.Name + " (" + itoa(n+1) + ")"
		}
		seen[v.Name]++
		labels, ids = append(labels, label), append(ids, v.ID)
	}
	return labels, ids
}

// studioUsesSavedVoice reports whether the Studio's voice comes from the
// picker (a saved voice or an unsaved clip) for model e, so the Studio
// can hide its own voice and reference controls.
func (a *app) studioUsesSavedVoice(e catalog.Entry) bool {
	if v := a.findVoice(a.studioVoice); v != nil {
		ok, _ := voiceUsable(v, e)
		return ok
	}
	return a.studioClip != nil && e.Has(catalog.CapClone)
}

// applyStudioVoice fills the voice of a take for model e from the
// picker's choice: VoiceID, and what the model takes to speak with it —
// a reference and its transcript (clones, locked voices), the design
// lowered to the model's vocabulary and the seed (designed voices), a
// style instruction, a built-in speaker. The language is filled only when
// the take has none. A choice the model can't use leaves t as it is.
//
// Thread G calls it where the Studio builds a take's recipe:
// a.applyStudioVoice(&t, p)
func (a *app) applyStudioVoice(t *core.Take, e catalog.Entry) {
	if a.studioVoice == "" && a.studioClip != nil {
		if e.Has(catalog.CapClone) {
			t.RefAudio, t.RefText = a.studioClip.Path, a.studioClip.Text
			if s := styleFor(e, a.studioClip.Style); s != "" {
				t.Voice = s
			}
		}
		return
	}
	v := a.findVoice(a.studioVoice)
	if v == nil {
		return
	}
	if ok, _ := voiceUsable(v, e); !ok {
		return
	}
	a.fillVoice(t, v, e)
}

// fillVoice sets a take's voice fields for v on model e (v usable on e).
func (a *app) fillVoice(t *core.Take, v *core.Voice, e catalog.Entry) {
	t.VoiceID = v.ID
	if t.Language == "" {
		t.Language = modelLanguage(e, v.Language)
	}
	switch {
	case v.Kind == core.VoiceBuiltin:
		t.Voice = v.Speaker
	case v.Kind == core.VoiceDesigned && canDesign(e) && !(v.Locked && v.Sample != "" && e.Has(catalog.CapClone)):
		r := voiceRecipe(v)
		low, err := vd.Lower(designTarget(e), r, vd.Options{Lang: designTextLang(firstOf(r.Attrs.Language(), v.Language))})
		if err == nil {
			t.Voice, t.Seed = low.Voice, low.Seed
		}
	case v.Locked && v.Sample != "":
		t.RefAudio, t.RefText, t.Seed = a.voicePath(v, v.Sample), v.SampleText, v.Seed
		if s := styleFor(e, v.Style); s != "" {
			t.Voice = s
		}
	case v.Kind == core.VoiceDesigned: // its sample, cloned
		t.RefAudio, t.RefText = a.voicePath(v, v.Sample), v.SampleText
		t.Seed = v.Seed
	default:
		t.RefAudio, t.RefText = a.voicePath(v, v.RefAudio), v.RefText
		if v.Seed != 0 {
			t.Seed = v.Seed
		}
		if s := styleFor(e, v.Style); s != "" {
			t.Voice = s
		}
	}
}

// studioVoicePrompt is the cached voice prompt (tts.encodeVoice) of the
// Studio's voice for model e, an absolute path, or "". The engine prefers
// it to the reference, which applyStudioVoice still sets as the fallback.
//
// Thread G sends it as SynthesizeParams.VoicePrompt once core.Take
// carries one: t.VoicePrompt = a.studioVoicePrompt(p)
func (a *app) studioVoicePrompt(e catalog.Entry) string {
	v := a.findVoice(a.studioVoice)
	if v == nil || !e.Has(catalog.CapVoicePrompt) || v.Prompts[e.Repo] == "" {
		return ""
	}
	if ok, _ := voiceUsable(v, e); !ok || (v.Locked && v.Sample != "") || v.Kind != core.VoiceCloned {
		return "" // the prompt is of the reference, not of a locked take or a design
	}
	return a.voicePath(v, v.Prompts[e.Repo])
}

// styleFor is a clone's style hint as model e takes it: tag engines get
// it reduced to their vocabulary, free-form ones as written, others none.
func styleFor(e catalog.Entry, style string) string {
	style = strings.TrimSpace(style)
	if style == "" || !(e.Has(catalog.CapVoiceStyle) || e.Has(catalog.CapInstruct)) {
		return ""
	}
	eng, ok := vd.EngineFor(e.Family, e.Repo)
	if ok && eng.Vocabulary == vd.VocabTags {
		r := vd.Recipe{Attrs: vd.Parse(style).Attrs}
		low, err := vd.Lower(eng.Target, r, vd.Options{Lang: vd.TextLang(style)})
		if err != nil {
			return ""
		}
		return low.Voice
	}
	if ok && eng.Vocabulary == vd.VocabSpeaker {
		return "" // CustomVoice speaks only its own speakers: no clones
	}
	return style
}

func firstOf(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

// useVoiceInStudio makes v the Studio's voice and opens the Studio,
// switching to a model that can speak with it when the Studio's can't.
func (a *app) useVoiceInStudio(v *core.Voice) {
	a.studioVoice, a.studioClip = v.ID, nil
	if ok, _ := voiceUsable(v, a.preset()); !ok {
		// The recommended models first: a designed voice speaks best
		// cloned from its sample on Qwen3-TTS Base (thread #41).
		pick := catalog.Builtin().Recommended(catalog.WorkflowStudio, i18n.Locale())
		if v.Kind == core.VoiceDesigned {
			pick = append(pick, designModels()...)
		}
		pick = append(pick, cloneModels()...)
		for _, e := range pick {
			if ok, _ := voiceUsable(v, e); ok {
				a.selectModel(e)
				break
			}
		}
	}
	a.navigate("/studio")
}

// referencesDir holds clips used without saving them ("Use without
// saving") and the working copies of the reference flow.
func (a *app) referencesDir() string { return filepath.Join(a.lib.store.Dir(), "references") }

// voiceCacheDir is the cache of rendered gallery previews.
func (a *app) voiceCacheDir() string {
	return filepath.Join(filepath.Dir(a.lib.peaks.Dir), "voice-previews")
}
