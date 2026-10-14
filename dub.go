package main

// Dubbing (M4, thread #19): the Dub page (/dub) works on one dub project
// at a time — source, transcript, translations, cast, generated tracks,
// exports — kept by services.Dubs (internal/dub.Project, migration 005)
// and made by its pipeline of stage jobs (owner "dub:<id>"). The page
// keeps a working copy of the open project: every edit is applied to it
// and, in the same order, to the saved document (dubSave); the saved
// document comes back with DubChanged and is adopted when no save is
// pending, which is also how the pipeline's results arrive.
//
// Files of the area:
//
//	dub.go          this: state, wiring (models, voices, translators, yt-dlp), projects, runs
//	dub_edit.go     edits of the transcript with undo, paste translation, glossary, cast
//	dub_view.go     the page: stepper, the step panels, banners, footer
//	dub_editor.go   the transcript editor (virtualised rows, badges, bulk bar)
//	dub_player.go   the A/B player over the live mix, audition, the video preview
//	dub_export.go   the export panel
//	timeline.go     the timeline lane widget
//	batch*.go       /batch: the batch queue and the watch folder
//	dub_settings.go Settings → Dubbing: URL import (yt-dlp), defaults

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
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/gallery"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/lang"
	"github.com/hehehai/AudioSmith/internal/llm"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/settings"
	"github.com/hehehai/AudioSmith/internal/store"
	"github.com/hehehai/AudioSmith/internal/translate"
	vd "github.com/hehehai/AudioSmith/internal/voicedesign"
)

// dubDefaultModel is the dub's TTS model until one is chosen: a model
// that clones (Qwen3-TTS Base, Apache-2.0).
const dubDefaultModel = "mlx-community/Qwen3-TTS-12Hz-0.6B-Base-8bit"

// dubState is the Dub page's state.
type dubState struct {
	dubSvc    *services.Dubs
	dubList   []store.DubRecord // the Dub page's projects, newest change first
	dubLoaded bool
	dub       *dub.Project // the open project (the working copy); nil = none
	dubHist   dub.History
	dubJobs   map[string]jobs.Job // unfinished jobs of the open project, by id
	dubSaving int                 // saves in flight; the saved copy is adopted at 0
	dubSaveQ  chan func()         // saves, in order (nil in tests: they run at once)
	dubStale  *dub.Project        // a saved copy that arrived while saves were in flight

	dubStep         int    // the step shown (dubSteps)
	dubLang         string // the target language the editor shows
	dubCur          string // the segment selected
	dubSel          map[string]bool
	dubEdit         string // the segment whose text is being edited ("" = none)
	dubEditL        string // its language ("" = the source)
	dubBuf          string // the text being edited
	dubOpen         string // the segment whose options are open
	dubList2        ui.ListState
	dubOpenProjects bool // the projects popover

	dubURL         string
	dubURLMore     bool
	dubCaptions    bool
	dubCookies     string
	dubErr         string // why the last action failed (translated)
	dubErrCode     string
	dubNeed        *catalog.Entry // a model the last action found missing
	dubDismissed   map[string]bool
	dubConfirm     string // "remove", "transcribe", "delete:<id>", "story"
	dubConfirmOpen bool

	dubPaste    dubPasteState
	dubGloss    dubGlossState
	dubExport   dubExportState
	dubPlayer   dubPlayerState
	dubTL       timelineState
	dubPeaks    *dub.Peaks
	dubPeaksFor string
	dubAudition dubAuditionState
	dubVideo    dubVideoState
	dubNotice   string     // a one-time message under the toolbar ("Imported 12 cues…")
	dubSide     panelState // the step's panel: beside the workspace, or a drawer
	dubNarrow   bool       // the workspace is narrow: toolbars show icons, rows fold actions
	dubVoiceCtl bool       // the Generate step's voice controls are open

	ytdlp ytdlpState
	batch batchState
	// dubEngine replaces the engine's executors (tests: dubtest's fake).
	dubEngine func(method string) jobs.Executor
}

// The steps of the stepper.
const (
	dubStepSource = iota
	dubStepTranscribe
	dubStepTranslate
	dubStepCast
	dubStepGenerate
	dubStepExport
)

// openDubs makes the dub service over the library, follows its events and
// jobs, and reads the projects.
func (a *app) openDubs(lib *library) {
	a.dubSel = map[string]bool{}
	a.dubJobs = map[string]jobs.Job{}
	a.dubDismissed = map[string]bool{}
	a.dubTL.zoom = 1
	host := a.eng
	router := a.llmRouter
	ytdlpPath := func() string { return lib.settings.Get().Dubbing.YtDlp }
	a.dubSvc = services.NewDubs(services.DubsConfig{
		Store: lib.store.Dubs(), Bus: lib.bus, Scheduler: lib.sched, Render: lib.svc.Render,
		Documents: lib.store.Documents(),
		Engine: func(method string) jobs.Executor {
			if a.dubEngine != nil {
				return a.dubEngine(method)
			}
			if host == nil || host.sup == nil {
				return nil
			}
			return engine.Executor(host.sup, dub.RoleOf(method), method)
		},
		ModelPath: lib.modelSnapshot,
		Models:    func() services.DubModels { return dubModels(lib) },
		Caps:      dubCaps,
		Voice:     a.dubVoiceResolver(lib),
		Translator: func(ctx context.Context, s dub.Settings) (translate.Translator, error) {
			return dubMakeTranslator(router, s)
		},
		TranslateLane: func() jobs.Lane { return translateLane(router) }, // local_llm.go
		Extractor: func() *translate.Extractor {
			if router == nil || !router.Available(llm.SkillGlossaryExtract) {
				return nil
			}
			return &translate.Extractor{Router: router}
		},
		YtDlp: func() dub.YtDlp {
			s := lib.settings.Get()
			if !s.Dubbing.URLImport {
				return dub.YtDlp{}
			}
			return dub.YtDlp{Path: ytdlpPath(), Proxy: s.Models.Proxy}
		},
	})
	if a.win != nil {
		q := make(chan func(), 256)
		a.dubSaveQ = q
		go func() {
			for fn := range q {
				fn()
			}
		}()
	}
	core.On(lib.bus, func(ev services.DubChanged) { a.post(func() { a.dubChanged(ev) }) })
	core.On(lib.bus, func(u jobs.Updated) {
		if u.Chunk == nil && (strings.HasPrefix(u.Job.Owner, services.DubOwner) || strings.HasPrefix(u.Job.Owner, services.DubExportOwner)) {
			a.post(func() { a.dubJobUpdated(u.Job) })
		}
	})
	core.On(lib.bus, func(ev services.WatchChanged) { a.post(func() { a.batch.watch = ev.State }) })
	core.On(lib.bus, func(u jobs.Updated) {
		if u.Chunk == nil && strings.HasPrefix(u.Job.Owner, "preview:") {
			a.post(func() { a.dubAuditionUpdated(u.Job) })
		}
	})
	a.loadDubs()
	a.openBatch()
}

// startDubs resumes what needs the engine and the scheduler running: the
// watch folder, and the jobs of the open project.
func (a *app) startDubs() {
	svc := a.dubSvc
	a.background(func() { svc.RestoreWatch(context.Background()) })
}

// closeDubs stops the watch folder's loop (it resumes at the next start).
func (a *app) closeDubs() {
	if a.dubSvc != nil {
		a.dubSvc.CloseWatch()
	}
	a.dubStopPlayer()
}

// dubModels are the pipeline's models: the catalogue's defaults, and
// the first diarization and separation entries once the catalogue has
// them (thread #14).
func dubModels(lib *library) services.DubModels {
	c := catalog.Builtin()
	repo := func(id string) string {
		if e, ok := c.ByID(id); ok {
			return e.Repo
		}
		return ""
	}
	first := func(task catalog.Task) string {
		for _, e := range c.ForTask(task) {
			return e.Repo
		}
		return ""
	}
	m := services.DubModels{STT: repo(catalog.DefaultSTT), Align: repo(catalog.DefaultAlign), Enhance: repo(catalog.DefaultEnhance),
		Diarize: first(catalog.TaskDiarize), Separate: first(catalog.TaskSeparate)}
	if e, ok := c.ByID(catalog.DefaultSTT); ok {
		m.WordTimes = e.Has(catalog.CapWordTimes)
	}
	if lib != nil {
		s := lib.settings.Get()
		if s.Transcribe.AccurateModel != "" {
			if e, ok := c.ByID(s.Transcribe.AccurateModel); ok {
				m.STT, m.WordTimes = e.Repo, e.Has(catalog.CapWordTimes)
			}
		}
	}
	return m
}

// dubCaps describes a TTS model to the pipeline.
func dubCaps(repo string) services.DubModelCaps {
	e := entryFor(repo)
	c := services.DubModelCaps{Clone: e.Has(catalog.CapClone), Duration: e.Has(catalog.CapDuration),
		Instruct: e.Has(catalog.CapInstruct) || e.Has(catalog.CapVoiceStyle) && !e.Has(catalog.CapClone), Speed: e.Has(catalog.CapSpeed),
		Steps: e.Has(catalog.CapSteps), Guidance: e.Has(catalog.CapGuidance), NeedsReference: e.NeedsReference}
	if e.Has(catalog.CapLanguage) {
		c.Languages = map[string]string{}
		for _, l := range e.Languages {
			if l.Value != "" {
				c.Languages[l.Code] = l.Value
			}
		}
		if len(c.Languages) == 0 {
			c.Languages = nil
		}
	}
	return c
}

// dubVoiceResolver resolves the cast's voices for a model, on any
// goroutine: saved voices through the voice library's fillVoice, the
// model's speakers, designs and gallery voices lowered for design models
// (else their delivery as an instruction).
func (a *app) dubVoiceResolver(lib *library) func(ctx context.Context, ref, model, lang string) (dub.VoiceSpec, error) {
	return func(ctx context.Context, ref, model, lang string) (dub.VoiceSpec, error) {
		e := entryFor(model)
		switch {
		case strings.HasPrefix(ref, dub.SpeakerPrefix):
			return dub.VoiceSpec{Voice: strings.TrimPrefix(ref, dub.SpeakerPrefix)}, nil
		case strings.HasPrefix(ref, dub.DesignPrefix), strings.HasPrefix(ref, dub.GalleryPrefix), strings.HasPrefix(ref, dub.PresetPrefix):
			r, hint, ok := dubDesignRecipe(ref)
			if !ok {
				return dub.VoiceSpec{}, core.ErrNotFound
			}
			if canDesign(e) {
				low, err := vd.Lower(designTarget(e), r, vd.Options{Lang: designTextLang(firstOf(r.Attrs.Language(), lang))})
				if err != nil {
					return dub.VoiceSpec{}, err
				}
				return dub.VoiceSpec{Voice: low.Voice, Seed: low.Seed}, nil
			}
			// A model that cannot design: the delivery as an instruction.
			return dub.VoiceSpec{Instruct: hint}, nil
		}
		v, err := lib.svc.Voices.Get(ctx, ref)
		if err != nil {
			return dub.VoiceSpec{}, err
		}
		if ok, why := voiceUsable(&v, e); !ok {
			return dub.VoiceSpec{}, errors.New(why)
		}
		var t core.Take
		t.Language = modelLanguage(e, lang)
		a.fillVoice(&t, &v, e)
		spec := dub.VoiceSpec{Voice: t.Voice, RefAudio: t.RefAudio, RefText: t.RefText, Seed: t.Seed, Language: t.Language}
		if e.Has(catalog.CapVoicePrompt) && v.Kind == core.VoiceCloned && !v.Locked && v.Prompts[e.Repo] != "" {
			spec.VoicePrompt = lib.svc.Voices.FilePath(v, v.Prompts[e.Repo])
		}
		return spec, nil
	}
}

// dubDesignRecipe is the design recipe of a design, preset or gallery
// voice reference, and its delivery hint for models that cannot design.
func dubDesignRecipe(ref string) (vd.Recipe, string, bool) {
	switch {
	case strings.HasPrefix(ref, dub.PresetPrefix):
		p, ok := gallery.PresetByID(strings.TrimPrefix(ref, dub.PresetPrefix))
		if !ok {
			return vd.Recipe{}, "", false
		}
		return vd.Recipe{Attrs: p.Attrs}, p.StyleHint.In("en"), true
	case strings.HasPrefix(ref, dub.GalleryPrefix):
		arch, ok := gallery.Default().ByID(strings.TrimPrefix(ref, dub.GalleryPrefix))
		if !ok {
			return vd.Recipe{}, "", false
		}
		return vd.Recipe{Attrs: arch.Attrs, Seed: arch.Seed}, arch.Description.In("en"), true
	}
	desc := strings.TrimSpace(strings.TrimPrefix(ref, dub.DesignPrefix))
	if desc == "" {
		return vd.Recipe{}, "", false
	}
	return vd.Recipe{Description: desc, Attrs: vd.Parse(desc).Attrs}, desc, true
}

// dubMakeTranslator makes translators (tests replace it).
var dubMakeTranslator = dubTranslator

// dubTranslator is the translator a project's settings ask for: the
// language-model router, or an online MT service with its key in the
// Keychain (cloud services only when chosen).
func dubTranslator(router *llm.Router, s dub.Settings) (translate.Translator, error) {
	switch s.Translator {
	case "", "llm":
		if router == nil || !router.Available(llm.SkillTranslate) {
			return nil, &translate.Error{Code: translate.CodeNoLLM}
		}
		return &translate.LLM{Router: router}, nil
	}
	cfg := translate.MTConfig{Client: hub.NewHTTPClient("", 0), Secrets: llmSecrets}
	var t translate.Translator
	switch s.Translator {
	case "deepl":
		t = translate.NewDeepL(cfg)
	case "google":
		t = translate.NewGoogle(cfg)
	case "microsoft":
		t = translate.NewMicrosoft(cfg, "")
	default:
		return nil, &translate.Error{Code: translate.CodeConfig}
	}
	if router != nil && router.Available(llm.SkillFitRewrite) {
		return translate.WithFit(t, &translate.Fitter{Router: router}), nil
	}
	return translate.WithFit(t, &translate.Fitter{}), nil
}

// dubTranslatorReady says whether the project's translator can run, and
// its name.
func (a *app) dubTranslatorReady(s dub.Settings) (bool, string) {
	switch s.Translator {
	case "", "llm":
		if a.llmRouter == nil {
			return false, ""
		}
		rs := a.llmRouter.Routes(llm.SkillTranslate)
		if len(rs) == 0 {
			return false, ""
		}
		return true, a.routeName(rs[0])
	case "deepl":
		return true, "DeepL"
	case "google":
		return true, "Google Cloud Translation"
	case "microsoft":
		return true, "Microsoft Translator"
	}
	return false, ""
}

// --- the projects ------------------------------------------------------------------

// loadDubs reads the Dub page's projects, and reopens the last one.
func (a *app) loadDubs() {
	svc := a.dubSvc
	a.background(func() {
		l, err := svc.List(context.Background(), store.DubQuery{Limit: 200})
		a.post(func() {
			if err != nil {
				a.toast(i18n.T("Could not read the dubbing projects: %v", err))
				return
			}
			a.dubList, a.dubLoaded = l, true
			if a.dub == nil && len(l) > 0 && a.dubLastOpen() != "" {
				for _, r := range l {
					if r.ID == a.dubLastOpen() {
						a.openDub(r.ID)
					}
				}
			}
		})
	})
}

// dubLastOpen is the project open when the app last quit.
func (a *app) dubLastOpen() string { return a.prefs.Dubbing.Open }

// openDub opens a project on the Dub page.
func (a *app) openDub(id string) {
	a.navigate("/dub")
	if a.dub != nil && a.dub.ID == id {
		return
	}
	svc := a.dubSvc
	a.background(func() {
		p, err := svc.Get(context.Background(), id)
		active, _ := svc.Active(context.Background(), id)
		a.post(func() {
			if err != nil {
				a.toast(i18n.T("Could not open the dub: %v", err))
				return
			}
			a.dubShow(p, active)
		})
	})
}

// dubShow makes p the open project.
func (a *app) dubShow(p *dub.Project, active []jobs.Job) {
	a.dubStopPlayer()
	a.dub = p
	a.dubHist.Reset()
	a.dubSel, a.dubCur, a.dubEdit, a.dubOpen = map[string]bool{}, "", "", ""
	a.dubErr, a.dubErrCode, a.dubNeed, a.dubNotice = "", "", nil, ""
	a.dubJobs = map[string]jobs.Job{}
	for _, j := range active {
		a.dubJobs[j.ID] = j
	}
	a.dubLang = p.Primary()
	a.dubStep = a.dubAutoStep()
	a.dubTL = timelineState{zoom: 1}
	a.dubPeaks, a.dubPeaksFor = nil, ""
	a.dubExport = dubExportState{}
	a.dubPaste = dubPasteState{}
	a.dubLoadPeaks()
	if a.prefs.Dubbing.Open != p.ID {
		id := p.ID
		a.updateSettings(func(s *settings.Settings) { s.Dubbing.Open = id })
	}
}

// dubAutoStep is the step a project opens at: the first not done.
func (a *app) dubAutoStep() int {
	p := a.dub
	switch {
	case p == nil || len(p.Segments) == 0:
		return dubStepSource
	case !p.AllTranslated():
		return dubStepTranslate
	case !p.TracksReady():
		return dubStepGenerate
	}
	return dubStepExport
}

// newDub starts a project from a file (or an empty one for a URL).
func (a *app) newDub(src dub.Source) {
	p := dub.New("", time.Now())
	p.Source = src
	p.Title = strings.TrimSuffix(src.Name, filepath.Ext(src.Name))
	if src.Kind == dub.SourceURL {
		p.Title = src.Name
	}
	d := a.prefs.Dubbing
	p.Settings.Model = firstOf(d.Model, a.dubDefaultModel())
	p.Settings.Review = !d.NoReview
	if len(d.Targets) > 0 {
		p.Targets = slices.Clone(d.Targets)
	}
	if d.Translator != "" {
		p.Settings.Translator = d.Translator
	}
	svc := a.dubSvc
	a.async(func() {
		created, err := svc.Create(context.Background(), p)
		a.post(func() {
			if err != nil {
				a.toast(i18n.T("Could not make the dub: %v", err))
				return
			}
			a.dubShow(created, nil)
			a.dubPutRecord(created)
		})
	})
}

// dubDefaultModel is the TTS model a new dub uses: the default if it is
// downloaded, else the first downloaded model that clones.
func (a *app) dubDefaultModel() string {
	if e, ok := catalog.Builtin().ByRepo(dubDefaultModel); ok {
		if ready, known := a.modelOnDisk(e); ready || !known {
			return e.Repo
		}
	}
	for _, e := range dubTTSModels() {
		if ready, _ := a.modelOnDisk(e); ready && e.Has(catalog.CapClone) {
			return e.Repo
		}
	}
	return dubDefaultModel
}

// dubTTSModels are the models a dub can speak with: those that clone,
// then the others.
func dubTTSModels() []catalog.Entry {
	var clone, other []catalog.Entry
	for _, e := range catalog.Builtin().ForTask(catalog.TaskTTS) {
		if e.Hidden {
			continue
		}
		if e.Has(catalog.CapClone) {
			clone = append(clone, e)
		} else {
			other = append(other, e)
		}
	}
	return append(clone, other...)
}

// dubPutRecord updates the list from a project.
func (a *app) dubPutRecord(p *dub.Project) {
	if p.Batch != "" {
		return
	}
	r := store.DubRecord{ID: p.ID, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, Title: p.Title, Source: p.Source.Path,
		Languages: p.Targets, Phase: string(p.Phase()), Duration: p.Source.Duration}
	if i := slices.IndexFunc(a.dubList, func(x store.DubRecord) bool { return x.ID == p.ID }); i >= 0 {
		a.dubList[i] = r
	} else {
		a.dubList = append([]store.DubRecord{r}, a.dubList...)
	}
	slices.SortStableFunc(a.dubList, func(x, y store.DubRecord) int { return y.UpdatedAt.Compare(x.UpdatedAt) })
}

// dubChanged applies a saved project.
func (a *app) dubChanged(ev services.DubChanged) {
	if ev.Deleted {
		a.dubList = slices.DeleteFunc(a.dubList, func(r store.DubRecord) bool { return r.ID == ev.ID })
		if a.dub != nil && a.dub.ID == ev.ID {
			a.dubStopPlayer()
			a.dub = nil
		}
		a.batchDeleted(ev.ID)
		return
	}
	p := ev.Project
	if i := slices.IndexFunc(a.dubList, func(r store.DubRecord) bool { return r.ID == p.ID }); i >= 0 && p.Batch == "" &&
		a.dubList[i].Phase == string(dub.PhaseGenerating) && p.Phase() == dub.PhaseDone {
		a.notify(core.SeveritySuccess, i18n.T("Dub finished"), firstOf(p.Title, p.Source.Name), "/dub")
	}
	a.dubPutRecord(p)
	a.batchChanged(p)
	if a.dub == nil || a.dub.ID != p.ID {
		return
	}
	if a.dubSaving > 0 {
		a.dubStale = p
		return
	}
	a.dubAdopt(p)
}

// dubAdopt takes the saved copy of the open project.
func (a *app) dubAdopt(p *dub.Project) {
	old := a.dub
	a.dub = p
	if len(old.Segments) > 0 && len(p.Segments) > 0 && old.Segments[0].ID != p.Segments[0].ID && old.Stage(dub.StageTranscribe, "").At != p.Stage(dub.StageTranscribe, "").At {
		a.dubHist.Reset() // a new transcript
	}
	if p.Media.Peaks != "" && a.dubPeaksFor != p.Media.Peaks {
		a.dubLoadPeaks()
	}
	if !slices.Contains(p.Languages(), a.dubLang) {
		a.dubLang = p.Primary()
	}
	if a.dubCur != "" && p.Index(a.dubCur) < 0 {
		a.dubCur = ""
	}
	// A stage that finished moves the stepper on.
	if len(old.Segments) == 0 && len(p.Segments) > 0 && a.dubStep <= dubStepTranscribe {
		a.dubStep = dubStepTranscribe
	}
}

// dubSave applies an edit to the saved project, after the edits before it.
func (a *app) dubSave(fn func(*dub.Project)) {
	if a.dub == nil {
		return
	}
	id := a.dub.ID
	svc := a.dubSvc
	a.dubSaving++
	work := func() {
		_, err := svc.Update(context.Background(), id, func(p *dub.Project) error { fn(p); return nil })
		a.post(func() {
			a.dubSaving--
			if err != nil {
				a.toast(i18n.T("Could not save the dub: %v", err))
			}
			if a.dubSaving == 0 && a.dubStale != nil {
				p := a.dubStale
				a.dubStale = nil
				if a.dub != nil && a.dub.ID == p.ID {
					a.dubAdopt(p)
				}
			}
		})
	}
	if a.dubSaveQ == nil {
		work()
		return
	}
	a.dubSaveQ <- work
}

// dubSettings changes the open project's settings.
func (a *app) dubSettings(fn func(*dub.Settings)) {
	if a.dub == nil {
		return
	}
	fn(&a.dub.Settings)
	a.dubSave(func(p *dub.Project) { fn(&p.Settings) })
}

// dubSet changes other fields of the project (title, targets, speakers…).
func (a *app) dubSet(fn func(*dub.Project)) {
	if a.dub == nil {
		return
	}
	fn(a.dub)
	a.dubSave(fn)
}

// deleteDub deletes a project and its files.
func (a *app) deleteDub(id string) {
	svc := a.dubSvc
	a.async(func() {
		err := svc.Delete(context.Background(), id)
		a.post(func() {
			if err != nil {
				a.toast(i18n.T("Could not delete the dub: %v", err))
				return
			}
			a.toast(i18n.T("Dub deleted"))
		})
	})
}

// --- jobs and runs ----------------------------------------------------------------

// dubJobUpdated follows the open project's jobs (and the batch's).
func (a *app) dubJobUpdated(j jobs.Job) {
	a.batchJobUpdated(j)
	if strings.HasPrefix(j.Owner, services.DubExportOwner) {
		a.dubExportUpdated(j)
		return
	}
	if a.dub == nil || j.Owner != services.DubOwner+a.dub.ID {
		return
	}
	if j.State.Terminal() || j.State == jobs.StateInterrupted {
		delete(a.dubJobs, j.ID)
		if j.State == jobs.StateFailed && j.Err != nil {
			a.dubFailed(j.Err)
		}
		return
	}
	a.dubJobs[j.ID] = j
}

// dubFailed shows why a stage failed, with its fix.
func (a *app) dubFailed(e *jobs.Error) {
	a.dubErr, a.dubErrCode = dubErrorText(e), e.Code
	if e.Code == "model_not_downloaded" {
		var d struct{ Model string }
		json.Unmarshal(e.Data, &d)
		if d.Model != "" {
			en := entryFor(d.Model)
			a.dubNeed = &en
		}
	}
}

// dubBusy reports whether the open project has jobs running or queued.
func (a *app) dubBusy() bool { return len(a.dubJobs) > 0 }

// dubRunning is the job of the open project running now (zero if none).
func (a *app) dubRunning() jobs.Job {
	var best jobs.Job
	for _, j := range a.dubJobs {
		if j.State == jobs.StateRunning {
			return j
		}
		if best.ID == "" || j.Created.Before(best.Created) {
			best = j
		}
	}
	return best
}

// dubLocked reports whether the transcript may not be edited now (a stage
// that writes it runs).
func (a *app) dubLocked() bool {
	for _, j := range a.dubJobs {
		switch j.Kind {
		case services.KindDubTranscribe, services.KindDubTranslate, services.KindDubFetch, services.KindDubIngest:
			return true
		}
	}
	return false
}

// dubRun starts the pipeline through a stage, after checking what it
// needs (the TTS model downloaded and its licence accepted, a
// translator).
func (a *app) dubRun(req services.RunRequest) {
	p := a.dub
	if p == nil || a.dubBusy() {
		return
	}
	a.dubErr, a.dubErrCode, a.dubNeed = "", "", nil
	if req.Through == dub.StageMix || req.Through == dub.StageSynthesize {
		e := entryFor(p.Settings.Model)
		if ready, known := a.modelOnDisk(e); known && !ready {
			a.dubErr = i18n.T("%s is not downloaded yet.", e.Label)
			a.dubNeed = &e
			return
		}
		if a.lib.licences != nil {
			if err := a.lib.licences.Check(context.Background(), e.Repo); err != nil {
				var lic *core.LicenceRequiredError
				if errors.As(err, &lic) {
					a.askLicence(e, func() { a.dubRun(req) })
					return
				}
			}
		}
	}
	if (req.Through == dub.StageTranslate || req.Through == dub.StageMix) && len(p.Segments) > 0 {
		if ok, _ := a.dubTranslatorReady(p.Settings); !ok {
			missing := false
			for _, l := range p.Languages() {
				if len(p.Untranslated(l)) > 0 {
					missing = true
				}
			}
			if missing {
				a.dubErr, a.dubErrCode = i18n.T("Translating needs a translation engine: a local model (Ollama, LM Studio) or a provider in Settings → Writing help."), services.DubCodeNoTranslator
				return
			}
		}
	}
	if p.Source.Kind == dub.SourceURL && a.dubCookies != "" {
		a.dubSvc.SetCookies(p.ID, a.dubCookies)
		a.dubCookies = ""
	}
	svc, sched := a.dubSvc, a.lib.sched
	id := p.ID
	a.async(func() {
		ids, err := svc.Run(context.Background(), id, req)
		active := dubUnfinished(sched, ids)
		a.post(func() {
			if err != nil {
				a.dubErr = err.Error()
				return
			}
			for _, j := range active {
				if _, ok := a.dubJobs[j.ID]; !ok {
					a.dubJobs[j.ID] = j
				}
			}
			if len(ids) == 0 {
				a.toast(i18n.T("Nothing to do: everything is up to date."))
			}
		})
	})
}

// dubUnfinished are the jobs of ids that have not ended yet (their
// updates may have come already).
func dubUnfinished(sched jobs.Scheduler, ids map[string]string) []jobs.Job {
	var out []jobs.Job
	for _, id := range ids {
		j, err := sched.Get(context.Background(), id)
		if err == nil && !j.State.Terminal() && j.State != jobs.StateInterrupted {
			out = append(out, j)
		}
	}
	return out
}

// dubStart runs the next part of the pipeline: up to the transcript with
// review on, else the whole dub.
func (a *app) dubStart() {
	p := a.dub
	if p == nil {
		return
	}
	through := dub.StageMix
	if p.Settings.Review && len(p.Segments) == 0 {
		through = dub.StageReferences
	}
	a.dubRun(services.RunRequest{Through: through})
}

// dubTranslate translates (the lines that need it).
func (a *app) dubTranslate(force bool) {
	req := services.RunRequest{Through: dub.StageTranslate}
	if force {
		req.Force = []dub.Stage{dub.StageTranslate}
	}
	a.dubRun(req)
}

// dubGenerate makes the dubbed tracks (the stale lines).
func (a *app) dubGenerate() { a.dubRun(services.RunRequest{Through: dub.StageMix}) }

// dubVerify runs the second-pass check of the language shown.
func (a *app) dubVerify() {
	if a.dub == nil {
		return
	}
	a.dubRun(services.RunRequest{Through: dub.StageQC, Langs: []string{a.dubLang}})
}

// dubCancel cancels the open project's jobs.
func (a *app) dubCancel() {
	if a.dub == nil {
		return
	}
	svc, id := a.dubSvc, a.dub.ID
	for k, j := range a.dubJobs {
		j.State = jobs.StateCancelled
		a.dubJobs[k] = j
	}
	a.async(func() {
		svc.Cancel(context.Background(), id)
		a.post(func() {
			for k, j := range a.dubJobs {
				if j.State == jobs.StateCancelled {
					delete(a.dubJobs, k)
				}
			}
		})
	})
}

// dubResume continues an interrupted run.
func (a *app) dubResume() {
	if a.dub == nil {
		return
	}
	svc, id := a.dubSvc, a.dub.ID
	a.async(func() {
		ids, err := svc.Resume(context.Background(), id)
		active := dubUnfinished(a.lib.sched, ids)
		a.post(func() {
			if err != nil {
				a.dubErr = err.Error()
				return
			}
			for _, j := range active {
				a.dubJobs[j.ID] = j
			}
		})
	})
}

// dubInterrupted reports whether the open project's last run stopped.
func (a *app) dubInterrupted() bool {
	return a.dub != nil && services.Interrupted(a.dub, nil) && !a.dubBusy()
}

// dubStaleCount is how many lines of lang changed since they were rendered.
func (a *app) dubStaleCount(l string) int {
	p := a.dub
	if p == nil {
		return 0
	}
	t := p.Track(l)
	if t.File == "" && len(t.Lines) == 0 {
		return 0
	}
	n := 0
	for _, s := range p.Segments {
		if strings.TrimSpace(p.TextFor(s, l)) == "" {
			continue
		}
		r, ok := t.Lines[s.ID]
		if !ok || r.Err != "" || r.Start != s.Start || r.Text != p.TextFor(s, l) {
			n++
		}
	}
	return n
}

// --- sources -----------------------------------------------------------------------

// dubExts are the files the Dub page takes.
var dubExts = dub.MediaExts

// chooseDubSource asks for a video or audio file.
func (a *app) chooseDubSource() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.win, Title: i18n.T("Choose a video or audio file"),
			Filters: []mygo.FileFilter{{Name: i18n.T("Video or audio"), Extensions: dubExts}}})
		if err != nil || len(paths) == 0 {
			return
		}
		a.post(func() { a.dubSourceFile(paths[0]) })
	}()
}

// dubDropped takes the first video or audio file dropped.
func (a *app) dubDropped(paths []string) {
	for _, p := range paths {
		if dub.IsMedia(p, dubExts) {
			a.dubSourceFile(p)
			return
		}
	}
	a.toast(i18n.T("Drop a video or audio file."))
}

// dubSourceFile starts a dub of a file, or replaces the source of the
// open one when it has none.
func (a *app) dubSourceFile(path string) {
	src := dub.Source{Kind: dub.SourceFile, Path: path, Name: filepath.Base(path)}
	if fi, err := os.Stat(path); err == nil {
		src.Size = fi.Size()
	}
	if a.dub != nil && a.dub.Source.Path == "" && a.dub.Source.URL == "" {
		a.dubSet(func(p *dub.Project) {
			p.Source = src
			p.Title = strings.TrimSuffix(src.Name, filepath.Ext(src.Name))
		})
		return
	}
	a.newDub(src)
}

// dubImportURL starts a dub of a URL (yt-dlp).
func (a *app) dubImportURL() {
	u := strings.TrimSpace(a.dubURL)
	if _, err := dub.CheckURL(context.Background(), u, nil); err != nil {
		a.dubErr = dubURLErrorText(err)
		return
	}
	if !a.prefs.Dubbing.URLImport || a.prefs.Dubbing.YtDlp == "" {
		a.dubErr, a.dubErrCode = i18n.T("Importing from a URL is off. Turn it on in Settings → Dubbing (it downloads yt-dlp, about 35 MB)."), dub.CodeNoYtDlp
		return
	}
	a.dubErr = ""
	a.newDub(dub.Source{Kind: dub.SourceURL, URL: u, Name: u, WantCaptions: a.dubCaptions})
	a.dubURL = ""
}

// dubRemoveSource clears the open project's source and everything made
// from it (it asks first when there is work to lose).
func (a *app) dubRemoveSource() {
	if a.dub == nil {
		return
	}
	a.dubStopPlayer()
	a.dubSet(func(p *dub.Project) {
		p.Source, p.Segments, p.Tracks, p.Stages, p.References, p.RefTried, p.Media, p.Run, p.Detected = dub.Source{}, nil, nil, nil, nil, nil, dub.Media{}, nil, ""
	})
	a.dubHist.Reset()
	a.dubStep = dubStepSource
	a.dubPeaks, a.dubPeaksFor = nil, ""
}

// dubLoadPeaks reads the open project's waveform in the background.
func (a *app) dubLoadPeaks() {
	p := a.dub
	if p == nil || p.Media.Peaks == "" {
		return
	}
	path := a.dubSvc.Path(p.ID, p.Media.Peaks)
	id, name := p.ID, p.Media.Peaks
	a.dubPeaksFor = name
	a.background(func() {
		pk, err := dub.ReadPeaks(path)
		a.post(func() {
			if err == nil && a.dub != nil && a.dub.ID == id {
				a.dubPeaks = pk
			}
		})
	})
}

// --- texts ---------------------------------------------------------------------------

// dubErrorText explains a failed stage.
func dubErrorText(e *jobs.Error) string {
	if e == nil {
		return ""
	}
	switch e.Code {
	case services.DubCodeNoTranslator:
		return i18n.T("Translating needs a translation engine: a local model (Ollama, LM Studio) or a provider in Settings → Writing help.")
	case services.DubCodeNoSpeech:
		return i18n.T("No speech was found in the source.")
	case services.DubCodeNoModel:
		return i18n.T("Choose a voice model for the dub.")
	case services.DubCodeNoLines:
		return i18n.T("Nothing to say in this language: translate it first.")
	case services.DubCodeNeedsEngine:
		return i18n.T("The engine cannot do this yet. It comes with a later engine update.")
	case services.DubCodeNeedsFFmpeg:
		return i18n.T("This needs ffmpeg: set it up in Settings → Audio tools.") // M7a
	case services.DubCodeNotMixed:
		return i18n.T("That track is not generated yet.")
	case "dub.no_audio":
		return i18n.T("The file has no sound to dub.")
	case "dub.no_video":
		return i18n.T("The source has no video: export WAV or M4A.")
	case dub.CodeNoYtDlp, dub.CodeURLScheme, dub.CodeURLPrivate, dub.CodeURLPlaylist, dub.CodeURLLive, dub.CodeURLManifest, dub.CodeURLInvalid, dub.CodeYtDlp, dub.CodeCookies:
		var d struct{ Detail string }
		json.Unmarshal(e.Data, &d)
		return dubURLErrorText(&dub.URLError{Code: e.Code, Detail: d.Detail})
	case translate.CodeAuth, translate.CodeNoKey:
		return i18n.T("The translation service refused the key. Check it in Settings → Writing help.")
	case translate.CodeQuota:
		return i18n.T("The translation service's quota is used up. Try again later.")
	case translate.CodeNetwork:
		return i18n.T("The translation service could not be reached.")
	}
	return jobErrorText(e)
}

// dubURLErrorText explains a refused URL.
func dubURLErrorText(err error) string {
	var ue *dub.URLError
	if !errors.As(err, &ue) {
		return err.Error()
	}
	switch ue.Code {
	case dub.CodeURLScheme:
		return i18n.T("Only http and https links can be imported.")
	case dub.CodeURLPrivate:
		return i18n.T("Links to this Mac or the local network cannot be imported.")
	case dub.CodeURLPlaylist:
		return i18n.T("That is a playlist: paste the link of one video.")
	case dub.CodeURLLive:
		return i18n.T("Live streams and premieres cannot be imported.")
	case dub.CodeURLManifest:
		return i18n.T("Stream manifests (HLS, DASH) cannot be imported: paste the video page's link.")
	case dub.CodeNoYtDlp:
		return i18n.T("Importing from a URL is off. Turn it on in Settings → Dubbing (it downloads yt-dlp, about 35 MB).")
	case dub.CodeCookies:
		return i18n.T("The cookie export must be 1 MB or smaller.")
	case dub.CodeChecksum:
		return i18n.T("The download of yt-dlp did not match its checksum, so it was not installed.")
	case dub.CodeYtDlp:
		if ue.Detail != "" {
			return i18n.T("The download failed: %s", ue.Detail)
		}
		return i18n.T("The download failed.")
	}
	return i18n.T("That is not a link to a video.")
}

// dubStageName names a stage.
func dubStageName(s dub.Stage) string {
	switch s {
	case dub.StageFetch:
		return i18n.T("Downloading the video…")
	case dub.StageIngest:
		return i18n.T("Reading the source…")
	case dub.StageSeparate:
		return i18n.T("Separating voices and music…")
	case dub.StageTranscribe:
		return i18n.T("Transcribing…")
	case dub.StageReferences:
		return i18n.T("Picking each speaker's voice…")
	case dub.StageTranslate:
		return i18n.T("Translating…")
	case dub.StageSynthesize:
		return i18n.T("Generating speech…")
	case dub.StageMix:
		return i18n.T("Mixing the track…")
	case dub.StageQC:
		return i18n.T("Checking the dub…")
	}
	return i18n.T("Working…")
}

// dubStageOfKind is the stage a job kind runs.
func dubStageOfKind(kind string) dub.Stage {
	return dub.Stage(strings.TrimPrefix(kind, "dub."))
}

// dubWarningText explains a stage's warning code.
func dubWarningText(code string) string {
	switch code {
	case services.DubWarnNoSeparation:
		return i18n.T("Voices and music could not be separated (the engine cannot yet): the original audio plays quietly under the dub.")
	case services.NoticeWatermarkFailed: // thread #14
		return i18n.T("The invisible watermark could not be added to the dubbed track.")
	case services.DubWarnNoDiarization:
		return i18n.T("Speakers could not be told apart (the engine cannot yet): every line is one speaker's. Set speakers by hand.")
	case services.DubWarnTooManySpk:
		return i18n.T("The speaker model tells at most 4 speakers apart: set the others by hand.")
	case services.DubWarnNoPoster:
		return i18n.T("No preview frame: the engine cannot make one yet.")
	case services.DubWarnNoProxy:
		return i18n.T("This video's format cannot play in the preview; the audio still dubs.")
	case services.DubWarnNoEnhance:
		return i18n.T("The voice references were not cleaned of noise.")
	case services.DubWarnCaptions:
		return i18n.T("The video's own captions were used as the transcript.")
	case services.DubWarnNoReference:
		return i18n.T("A speaker says too little to clone: their lines use the default voice unless cast.")
	case services.DubWarnVoice:
		return i18n.T("A cast voice could not be used with this model: those lines use the default voice.")
	case services.DubWarnTranslateFailed:
		return i18n.T("Some lines could not be translated: retry them, or write them.")
	case services.DubWarnLinesFailed:
		return i18n.T("Some lines could not be generated: they stay silent.")
	case services.WarnNoWordTimes:
		return i18n.T("Word timings are missing: karaoke subtitles spread the words evenly.")
	}
	return code
}

// dubPhaseText names a phase.
func dubPhaseText(ph dub.Phase) string {
	switch ph {
	case dub.PhaseNew:
		return i18n.T("No source")
	case dub.PhaseReady:
		return i18n.T("Ready")
	case dub.PhasePreparing:
		return i18n.T("Preparing")
	case dub.PhaseTranscribed:
		return i18n.T("Transcribed")
	case dub.PhaseTranslating:
		return i18n.T("Translating")
	case dub.PhaseTranslated:
		return i18n.T("Translated")
	case dub.PhaseGenerating:
		return i18n.T("Generating")
	case dub.PhaseDone:
		return i18n.T("Done")
	case dub.PhaseFailed:
		return i18n.T("Failed")
	}
	return string(ph)
}

// dubLangName names a language for lists: flag and name.
func dubLangName(code string) string {
	if code == "" {
		return i18n.T("Auto-detect")
	}
	if l, ok := lang.Lookup(code); ok {
		name := l.NameIn(i18n.Locale())
		if base, region, ok := strings.Cut(code, "-"); ok && len(region) == 2 {
			if bl, ok := lang.Lookup(base); ok {
				name = bl.NameIn(i18n.Locale()) + " (" + region + ")"
			}
		}
		return strings.TrimSpace(l.Flag() + " " + name)
	}
	return code
}
