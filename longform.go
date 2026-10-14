package main

// Longform (M3, thread #18): what the Stories (/stories) and Audiobook
// (/audiobook) pages share — their documents (services.LongformDoc) with
// autosaved drafts, the voices they offer and how a voice reference
// becomes a recipe for the document's model, compiling a document into a
// render, following renders (core.RenderChanged), and saving projects.
//
// Files of the area:
//
//	longform.go         this: state, drafts, voices, renders, projects
//	longform_view.go    panels both pages show: setup, production, book, project, render bar, outputs
//	longform_player.go  the outputs' player and the read-along (word timings)
//	stories*.go         /stories: review buffer, auto-cast, the line list, the cast
//	audiobook*.go       /audiobook: the manuscript editor, cast, lexicon, chapter plan
//	projects*.go        /projects: the library

import (
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/store"
)

// Draft keys of the two pages.
const (
	storiesDraftKey   = "stories"
	audiobookDraftKey = "audiobook"
)

// longformState is the state of the Stories, Audiobook and Projects pages.
type longformState struct {
	lfStories   *lfPage
	lfBook      *lfPage
	lfRenders   map[string]*lfRender // renders followed (bodies as published), by id
	lfRecent    []string             // full renders, newest first (interrupted ones offer Resume)
	lfAuditions map[string]lfAudition
	lfEncode    func() bool // whether books can be M4B (tests replace it)
	lfUndo      []lfUndoToast
	projects    projectsState
}

// lfRender is a render as the pages see it.
type lfRender struct {
	rec  core.Render
	body services.RenderBody
}

// lfAudition is what an audition render plays: a line or a chapter.
type lfAudition struct {
	kind core.ProjectKind
	what string // "line:<id>" or "chapter:<n>"
}

// lfPage is one page's document and view state.
type lfPage struct {
	kind      core.ProjectKind
	doc       services.LongformDoc
	project   string // the project it is saved as; "" = a draft
	savedJSON string // the document as saved or opened ("" for a draft never saved)
	draftJSON string // as last handed to the draft service
	render    string // the render whose progress and outputs show
	auditions map[string]string
	importing bool
	scriptRev int // bumped when the manuscript changes

	// Derived from the document when its fingerprint changes.
	fp       string
	spec     services.LongformSpec
	problems []services.Problem
	stats    services.ScriptStats

	// View state.
	lines       ui.ListState
	sel         int
	open        map[string]bool // inspector sections
	saveName    string
	confirm     string // a project id to open after confirming, "clear", "delete:<id>"
	confirmOpen bool
	projects    []core.Project // saved projects of this kind, newest first
	ed          *editor.State  // the manuscript (Audiobook)
	edSynced    string
	edVersion   uint64
	dismissed   string // fp when the warnings were dismissed
	reactions   bool
	ra          readAlong
}

func newLfPage(kind core.ProjectKind, model string) *lfPage {
	p := &lfPage{kind: kind, doc: services.NewLongformDoc(kind, model), auditions: map[string]string{}, sel: -1,
		open: map[string]bool{"setup": true, "cast": true}}
	return p
}

// lfDraft is a page as saved between runs.
type lfDraft struct {
	Version int                  `json:"version"`
	Doc     services.LongformDoc `json:"doc"`
	Project string               `json:"project,omitempty"`
	Saved   string               `json:"saved,omitempty"`
	Render  string               `json:"render,omitempty"`
}

// openLongform connects the longform pages to the library: render events
// (subscribed once, here), the drafts, the renders and the projects.
func (a *app) openLongform(lib *library) {
	model := a.preset().Repo
	a.lfStories = newLfPage(core.ProjectStory, model)
	a.lfBook = newLfPage(core.ProjectAudiobook, model)
	a.lfBook.ed = newManuscriptEditor()
	a.lfRenders = map[string]*lfRender{}
	a.lfAuditions = map[string]lfAudition{}
	host := a.eng
	lib.useEncoder(func() jobs.Executor {
		if host == nil || host.sup == nil || !slices.Contains(engine.ExecutorKinds, services.KindEncode) {
			return nil
		}
		return engine.Executor(host.sup, proto.RoleMedia, services.KindEncode)
	})
	if a.lfEncode == nil {
		a.lfEncode = a.engineEncodes
	}
	core.On(lib.bus, func(ev core.RenderChanged) { a.post(func() { a.renderChanged(ev) }) })
	core.On(lib.bus, func(ev core.ProjectChanged) { a.post(func() { a.projectChanged(ev) }) })
	a.openProjects(lib)
	a.followAlignments(lib)
	for _, p := range []*lfPage{a.lfStories, a.lfBook} {
		a.restoreLfDraft(p)
		a.loadLfProjects(p)
	}
	a.loadRenders()
}

func (p *lfPage) draftKey() string {
	if p.kind == core.ProjectAudiobook {
		return audiobookDraftKey
	}
	return storiesDraftKey
}

// lfPageOf is the page of a project kind.
func (a *app) lfPageOf(kind core.ProjectKind) *lfPage {
	if kind == core.ProjectAudiobook {
		return a.lfBook
	}
	return a.lfStories
}

// restoreLfDraft loads a page's draft in the background and applies it
// unless the page was edited meanwhile.
func (a *app) restoreLfDraft(p *lfPage) {
	drafts := a.lib.svc.Drafts
	key := p.draftKey()
	a.background(func() {
		var d lfDraft
		ok, err := drafts.Load(context.Background(), key, &d)
		if err != nil || !ok || d.Doc.Version == 0 {
			return
		}
		a.post(func() {
			if p.draftJSON != "" {
				return // edited before the draft came
			}
			a.setLfDoc(p, d.Doc, d.Project, d.Saved)
			p.render = d.Render
			p.draftJSON = docJSON(&p.doc)
		})
	})
}

// setLfDoc replaces a page's document (a draft, an opened project, a
// sample).
func (a *app) setLfDoc(p *lfPage, d services.LongformDoc, project, saved string) {
	d.Kind = p.kind
	if d.Production == (services.Production{}) {
		d.Production = services.DefaultProduction()
	}
	p.doc, p.project, p.savedJSON = d, project, saved
	p.saveName = d.Title
	p.sel = -1
	p.fp = ""
	p.scriptRev++
	p.render = ""
	if p.ed != nil {
		p.ed.Buffer.Reset(d.Script)
		p.edSynced, p.edVersion = d.Script, p.ed.Buffer.Version()
	}
	p.ra = readAlong{}
}

func docJSON(d *services.LongformDoc) string {
	b, _ := json.Marshal(d)
	return string(b)
}

// autosaveLf hands a page's document to the draft service when it
// changed, and recompiles it.
func (a *app) autosaveLf(p *lfPage) {
	fp := a.lfFingerprint(p)
	if fp == p.fp {
		return
	}
	p.fp = fp
	a.compileLf(p)
	d := lfDraft{Version: 1, Doc: p.doc, Project: p.project, Saved: p.savedJSON, Render: p.render}
	if js := docJSON(&p.doc) + "|" + p.project + "|" + p.render; js != p.draftJSON {
		p.draftJSON = js
		a.lib.svc.Drafts.Save(p.draftKey(), d)
	}
}

// lfFingerprint changes whenever the document, or a voice it may use,
// does: the manuscript by its revision (it is large).
func (a *app) lfFingerprint(p *lfPage) string {
	d := p.doc
	d.Script = ""
	h := fnv.New64a()
	b, _ := json.Marshal(d)
	h.Write(b)
	h.Write([]byte(strconv.Itoa(p.scriptRev) + "|" + p.project + "|" + p.render))
	for _, v := range a.voices {
		h.Write([]byte(v.ID + v.UpdatedAt.String()))
	}
	return strconv.FormatUint(h.Sum64(), 36)
}

// compileLf compiles a page's document (stats, problems, the spec).
func (a *app) compileLf(p *lfPage) {
	d := p.doc
	spec, probs := services.Compile(&d, a.lfResolver(&d))
	spec.Project = p.project
	p.spec, p.problems = spec, probs
	if p.kind == core.ProjectAudiobook {
		p.stats = services.StatsOf(d.Script, d.Speed)
	} else {
		p.stats = services.StatsOf(services.StoryScript(d.Lines), d.Speed)
	}
}

// --- the encoder -------------------------------------------------------------

// useEncoder gives books the engine's media.encode executor (nil while
// the engine has none).
func (l *library) useEncoder(fn func() jobs.Executor) {
	l.synthMu.Lock()
	l.encode = fn
	l.synthMu.Unlock()
}

func (l *library) encodeExecutor() jobs.Executor {
	l.synthMu.Lock()
	fn := l.encode
	l.synthMu.Unlock()
	if fn == nil {
		return nil
	}
	return fn()
}

// engineEncodes reports whether the engine side runs media.encode: the
// Go executors know the method and, once the media worker said hello,
// it lists it. (Thread #14 adds it; until then books are WAV.)
func (a *app) engineEncodes() bool {
	if !slices.Contains(engine.ExecutorKinds, services.KindEncode) {
		return false
	}
	for _, w := range a.workers {
		if w.Role == proto.RoleMedia && w.Hello != nil && !slices.Contains(w.Hello.Methods, services.KindEncode) {
			return false
		}
	}
	return true
}

// --- voices ----------------------------------------------------------------

// lfVoice is a choice of the voice pickers: a VoiceRef and its label.
type lfVoice struct{ ref, label string }

// lfVoices are the voices model e can speak with: the saved voices it
// can use, then its own speakers.
func (a *app) lfVoices(e catalog.Entry) []lfVoice {
	var out []lfVoice
	for i := range a.voices {
		v := &a.voices[i]
		if ok, _ := voiceUsable(v, e); ok {
			out = append(out, lfVoice{v.ID, v.Name})
		}
	}
	for _, v := range e.Voices {
		out = append(out, lfVoice{services.SpeakerRef + v.ID, v.ID})
	}
	return out
}

// lfVoiceLabel names a voice reference.
func (a *app) lfVoiceLabel(ref string) string {
	switch {
	case ref == "":
		return i18n.T("Default voice")
	case strings.HasPrefix(ref, services.SpeakerRef):
		return strings.TrimPrefix(ref, services.SpeakerRef)
	}
	if v := a.findVoice(ref); v != nil {
		return v.Name
	}
	return i18n.T("Missing voice")
}

// lfResolver resolves voice references and [voice:] names for a document's
// model: saved voices (by id or name) through the voice library's
// fillVoice, the model's own speakers.
func (a *app) lfResolver(d *services.LongformDoc) services.VoiceResolver {
	e := entryFor(d.Model)
	lang := modelLanguage(e, d.Language)
	voices := slices.Clone(a.voices)
	byName := map[string]int{}
	for i, v := range voices {
		if _, dup := byName[strings.ToLower(v.Name)]; !dup {
			byName[strings.ToLower(v.Name)] = i
		}
	}
	speakers := e.VoiceIDs()
	return func(ref string) (string, core.Take, bool) {
		t := core.Take{Language: lang}
		if id, ok := strings.CutPrefix(ref, services.SpeakerRef); ok {
			if !slices.Contains(speakers, id) && e.ID != "custom" {
				return "", t, false
			}
			t.Voice = id
			return ref, t, true
		}
		var v *core.Voice
		for i := range voices {
			if voices[i].ID == ref {
				v = &voices[i]
			}
		}
		if v == nil {
			if i, ok := byName[strings.ToLower(ref)]; ok {
				v = &voices[i]
			}
		}
		if v == nil {
			if slices.Contains(speakers, ref) {
				t.Voice = ref
				return services.SpeakerRef + ref, t, true
			}
			return "", t, false
		}
		if ok, _ := voiceUsable(v, e); !ok {
			return "", t, false
		}
		a.fillVoice(&t, v, e)
		if e.Has(catalog.CapVoicePrompt) && v.Kind == core.VoiceCloned && !v.Locked && v.Prompts[e.Repo] != "" {
			t.VoicePrompt = a.voicePath(v, v.Prompts[e.Repo])
		}
		return v.ID, t, true
	}
}

// --- renders ---------------------------------------------------------------

// loadRenders reads the recent renders, and settles the ones the last run
// left as running.
func (a *app) loadRenders() {
	svc := a.lib.svc.Longform
	a.background(func() {
		ctx := context.Background()
		if err := svc.Reconcile(ctx); err != nil {
			a.post(func() { a.toast(i18n.T("Could not read the renders: %v", err)) })
		}
		l, err := svc.List(ctx, store.RenderQuery{Limit: 50})
		a.post(func() {
			if err != nil {
				return
			}
			for _, r := range l {
				if _, ok := a.lfRenders[r.ID]; !ok {
					a.putRender(r)
				}
			}
			a.sortRecent()
		})
	})
}

// putRender keeps a render's latest state.
func (a *app) putRender(r core.Render) *lfRender {
	b, err := services.DecodeRender(r)
	if err != nil {
		return nil
	}
	lr := &lfRender{rec: r, body: b}
	lr.body.Spec.Chapters, lr.body.Spec.Voices = nil, nil // the pages need the summary only
	a.lfRenders[r.ID] = lr
	return lr
}

func (a *app) sortRecent() {
	a.lfRecent = a.lfRecent[:0]
	for id, r := range a.lfRenders {
		if !r.rec.Audition {
			a.lfRecent = append(a.lfRecent, id)
		}
	}
	slices.SortFunc(a.lfRecent, func(x, y string) int { return strings.Compare(y, x) })
}

// renderChanged follows a render.
func (a *app) renderChanged(ev core.RenderChanged) {
	if ev.Deleted {
		delete(a.lfRenders, ev.Render.ID)
		a.sortRecent()
		a.projects.dirty = true
		return
	}
	old := a.lfRenders[ev.Render.ID]
	lr := a.putRender(ev.Render)
	if lr == nil {
		return
	}
	if old == nil {
		a.sortRecent()
	}
	if old != nil && old.rec.State == core.RenderRunning && lr.rec.State != core.RenderRunning {
		a.renderEnded(lr)
	}
}

// renderEnded tells how a render ended, and plays a finished audition.
func (a *app) renderEnded(r *lfRender) {
	a.projects.dirty = true
	if r.rec.Audition {
		if au, ok := a.lfAuditions[r.rec.ID]; ok && r.rec.State == core.RenderDone && len(r.body.Chapters) > 0 {
			if f := r.body.Chapters[0].File; f != "" {
				a.pl.Stop()
				a.pl.Toggle("audition:"+r.rec.ID, f)
			}
			_ = au
		} else if r.rec.State == core.RenderFailed {
			a.toast(i18n.T("The preview failed: %s", renderErrorText(&r.body)))
		}
		return
	}
	switch r.rec.State {
	case core.RenderDone:
		a.notify(core.SeveritySuccess, i18n.T("“%s” is ready", r.rec.Title), "", "/"+lfRoute(r.rec.Kind))
	case core.RenderFailed:
		a.notify(core.SeverityError, i18n.T("“%s” did not finish", r.rec.Title), renderErrorText(&r.body), "/"+lfRoute(r.rec.Kind))
	}
}

// lfRoute is the page of a project kind.
func lfRoute(k core.ProjectKind) string {
	if k == core.ProjectAudiobook {
		return "audiobook"
	}
	return "stories"
}

// activeRender is the full render running now, if any.
func (a *app) activeRender() *lfRender {
	for _, id := range a.lfRecent {
		if r := a.lfRenders[id]; r != nil && r.rec.State == core.RenderRunning {
			return r
		}
	}
	return nil
}

// renderErrorText explains why a render failed.
func renderErrorText(b *services.RenderBody) string {
	if b.Error != nil {
		return jobErrorText(b.Error)
	}
	for i, c := range b.Chapters {
		if c.State == services.ChapterFailed && c.Error != nil {
			return i18n.T("Chapter %d: %s", i+1, jobErrorText(c.Error))
		}
	}
	return i18n.T("Some chapters did not render.")
}

// lfBlocker is the one reason a page cannot render now, or "".
func (a *app) lfBlocker(p *lfPage) string {
	if r := a.activeRender(); r != nil {
		if r.rec.ID == p.render {
			return ""
		}
		return i18n.T("Another book is rendering: “%s”.", r.rec.Title)
	}
	switch {
	case p.importing:
		return i18n.T("Importing… Generate unlocks when it finishes.")
	case a.engState == engineStarting:
		return i18n.T("The engine is starting…")
	case a.engState == engineFailed:
		return i18n.T("The engine is not running. Restart it on the Engine page.")
	}
	if p.doc.Model == "" {
		return i18n.T("Choose a model.")
	}
	for _, pr := range p.problems {
		if pr.Blocking {
			return problemText(pr)
		}
	}
	return ""
}

// problemText translates a compiler problem.
func problemText(p services.Problem) string {
	switch p.Code {
	case services.ProblemNoModel:
		return i18n.T("Choose a model.")
	case services.ProblemNothingToSay:
		return i18n.T("Add at least one spoken line to generate.")
	case services.ProblemNoVoice:
		if l := p.Params["line"]; l != "" {
			return i18n.T("Line %s has no voice: choose a default voice, or give it one.", l)
		}
		return i18n.T("Choose a default voice, or give every line a voice.")
	case services.ProblemMissingVoice:
		return i18n.T("The voice “%s” is gone or can't be used with this model: choose another.", p.Params["voice"])
	case services.ProblemUnknownVoice:
		return i18n.T("Unknown voice “%s”: the default voice reads it.", p.Params["voice"])
	case services.ProblemEmptyChapter:
		return i18n.T("Chapter “%s” has no spoken text.", p.Params["title"])
	case services.ProblemUnknownTag:
		return i18n.T("Unrecognised tag %s: it will be read aloud.", p.Params["tag"])
	case services.ProblemDuplicateWord:
		return i18n.T("“%s” is listed twice in the pronunciations: keep one.", p.Params["word"])
	}
	return p.Code
}

// startLf compiles a page's document and starts its render (or an
// audition of chapter or line only: what is "chapter:<n>", "line:<id>").
func (a *app) startLf(p *lfPage, what string) {
	a.compileLf(p)
	if !strings.HasPrefix(what, "line:") {
		if msg := a.lfBlocker(p); msg != "" && !(what != "" && a.activeRender() != nil) {
			a.toast(msg)
			return
		}
	}
	spec := p.spec
	spec.Project = p.project
	if spec.Title == "" {
		spec.Title = i18n.T("Untitled")
	}
	switch {
	case strings.HasPrefix(what, "chapter:"):
		n, _ := strconv.Atoi(strings.TrimPrefix(what, "chapter:"))
		if n < 0 || n >= len(spec.Chapters) {
			return
		}
		spec.Chapters = spec.Chapters[n : n+1]
		spec.Audition = true
	case strings.HasPrefix(what, "line:"):
		ch, ok := lineSpec(&spec, strings.TrimPrefix(what, "line:"))
		if !ok {
			a.toast(i18n.T("This line has nothing to say."))
			return
		}
		spec.Chapters = []services.ChapterSpec{ch}
		spec.Audition = true
	}
	if spec.Audition {
		for _, pr := range p.problems {
			if pr.Blocking && pr.Code != services.ProblemNothingToSay && (pr.Line < 0 || what == "line:"+lineID(p, pr.Line)) {
				a.toast(problemText(pr))
				return
			}
		}
	}
	svc := a.lib.svc.Longform
	kind := p.kind
	a.background(func() {
		r, err := svc.Start(context.Background(), spec)
		a.post(func() {
			if err != nil {
				switch {
				case errors.Is(err, core.ErrBusy):
					a.toast(i18n.T("Another book is rendering. Stop it first."))
				default:
					a.toast(i18n.T("Could not start: %v", err))
				}
				return
			}
			a.putRender(r)
			if spec.Audition {
				a.lfAuditions[r.ID] = lfAudition{kind: kind, what: what}
				p.auditions[what] = r.ID
				return
			}
			p.render = r.ID
			p.doc.Render = r.ID
			a.sortRecent()
		})
	})
}

// lineID is the id of story line i ("" for an audiobook).
func lineID(p *lfPage, i int) string {
	if i < 0 || i >= len(p.doc.Lines) {
		return ""
	}
	return p.doc.Lines[i].ID
}

// lineSpec is a chapter holding only the line id of spec.
func lineSpec(spec *services.LongformSpec, id string) (services.ChapterSpec, bool) {
	for _, ch := range spec.Chapters {
		var lines []services.LineSpec
		for _, l := range ch.Lines {
			if l.ID == id {
				l.Paragraph, l.PauseAfter = false, 0
				lines = append(lines, l)
			}
		}
		if len(lines) > 0 {
			return services.ChapterSpec{Lines: lines}, true
		}
	}
	return services.ChapterSpec{}, false
}

// stopLf stops a page's render.
func (a *app) stopLf(p *lfPage) {
	id := p.render
	svc := a.lib.svc.Longform
	a.background(func() {
		if err := svc.Stop(context.Background(), id); err != nil {
			a.post(func() { a.toast(i18n.T("Could not stop: %v", err)) })
		}
	})
}

// resumeLf renders what a stopped or failed render lacks.
func (a *app) resumeLf(p *lfPage, id string) {
	svc := a.lib.svc.Longform
	a.background(func() {
		r, err := svc.Resume(context.Background(), id)
		a.post(func() {
			if err != nil {
				if errors.Is(err, core.ErrBusy) {
					a.toast(i18n.T("Another book is rendering. Stop it first."))
				} else {
					a.toast(i18n.T("Could not resume: %v", err))
				}
				return
			}
			a.putRender(r)
			if p != nil {
				p.render = id
			}
		})
	})
}

// auditionState says how a page's audition of what goes: "" (none),
// "running", "done", "failed".
func (a *app) auditionState(p *lfPage, what string) (string, *lfRender) {
	id := p.auditions[what]
	r := a.lfRenders[id]
	if r == nil {
		return "", nil
	}
	switch r.rec.State {
	case core.RenderRunning:
		return "running", r
	case core.RenderDone:
		return "done", r
	}
	return "failed", r
}

// playAudition plays (or pauses) a finished audition.
func (a *app) playAudition(r *lfRender) {
	if len(r.body.Chapters) == 0 || r.body.Chapters[0].File == "" {
		return
	}
	if err := a.pl.Toggle("audition:"+r.rec.ID, r.body.Chapters[0].File); err != nil {
		a.toast(i18n.T("Can't play: %v", err))
	}
}

// --- projects --------------------------------------------------------------

// loadLfProjects reads a page's saved projects.
func (a *app) loadLfProjects(p *lfPage) {
	svc := a.lib.svc.Projects
	kind := p.kind
	a.background(func() {
		l, err := svc.List(context.Background(), kind, "")
		if err != nil {
			return
		}
		for i := range l {
			l[i].Body = nil
		}
		a.post(func() { p.projects = l })
	})
}

// projectChanged updates the pages' project lists.
func (a *app) projectChanged(ev core.ProjectChanged) {
	a.projects.dirty = true
	for _, p := range []*lfPage{a.lfStories, a.lfBook} {
		if ev.Project.Kind != p.kind {
			continue
		}
		i := slices.IndexFunc(p.projects, func(x core.Project) bool { return x.ID == ev.Project.ID })
		switch {
		case ev.Deleted:
			if i >= 0 {
				p.projects = slices.Delete(p.projects, i, i+1)
			}
			if p.project == ev.Project.ID {
				p.project, p.savedJSON = "", ""
			}
		case i >= 0:
			pr := ev.Project
			pr.Body = nil
			p.projects[i] = pr
		default:
			pr := ev.Project
			pr.Body = nil
			p.projects = append([]core.Project{pr}, p.projects...)
		}
		if !ev.Deleted && p.project == ev.Project.ID && p.doc.Title != ev.Project.Title {
			// Renamed elsewhere (the Projects page).
			p.doc.Title, p.saveName = ev.Project.Title, ev.Project.Title
			p.savedJSON = docJSON(&p.doc)
		}
	}
}

// unsaved reports whether a page has changes its project lacks.
func (p *lfPage) unsaved() bool {
	if p.savedJSON == "" {
		return !p.empty()
	}
	return docJSON(&p.doc) != p.savedJSON
}

// empty reports whether a page has nothing worth keeping.
func (p *lfPage) empty() bool {
	return len(p.doc.Lines) == 0 && strings.TrimSpace(p.doc.Script) == "" && strings.TrimSpace(p.doc.ImportText) == ""
}

// saveLf saves a page as its project, or as a new one (copy).
func (a *app) saveLf(p *lfPage, copy bool) {
	name := strings.TrimSpace(p.saveName)
	if name == "" {
		name = strings.TrimSpace(p.doc.Title)
	}
	if name == "" {
		a.toast(i18n.T("Name the project first."))
		return
	}
	doc := p.doc
	if doc.Title == "" {
		doc.Title = name
	}
	pr := core.Project{ID: p.project, Kind: p.kind, Title: name}
	if copy {
		pr.ID = ""
	}
	lib := a.lib.svc.Library
	a.background(func() {
		saved, err := lib.SaveDoc(context.Background(), pr, doc)
		a.post(func() {
			if err != nil {
				a.toast(i18n.T("Could not save: %v", err))
				return
			}
			p.project = saved.ID
			p.doc.Title = doc.Title
			p.saveName = saved.Title
			p.savedJSON = docJSON(&p.doc)
			a.toast(i18n.T("Saved “%s”", saved.Title))
		})
	})
}

// openLfProject opens a saved project in its page (replacing the draft:
// callers ask first when it has unsaved changes).
func (a *app) openLfProject(id string) {
	lib := a.lib.svc.Library
	a.background(func() {
		pr, d, err := lib.LoadDoc(context.Background(), id)
		a.post(func() {
			if err != nil {
				a.toast(i18n.T("Could not open: %v", err))
				return
			}
			p := a.lfPageOf(pr.Kind)
			if a.activeRender() != nil && a.activeRender().rec.ProjectID == p.project && p.project != "" && p.project != pr.ID {
				a.toast(i18n.T("Longform work is active: stop the render first."))
				return
			}
			a.setLfDoc(p, d, pr.ID, "")
			p.savedJSON = docJSON(&p.doc)
			p.saveName = pr.Title
			p.render = d.Render
			a.navigate("/" + lfRoute(pr.Kind))
		})
	})
}

// deleteLfProject deletes a saved project; its renders stay.
func (a *app) deleteLfProject(id string) {
	svc := a.lib.svc.Projects
	a.background(func() {
		err := svc.Delete(context.Background(), id)
		a.post(func() {
			if err != nil {
				a.toast(i18n.T("Could not delete: %v", err))
			} else {
				a.toast(i18n.T("Project deleted"))
			}
		})
	})
}

// newLf starts a new, empty document on a page.
func (a *app) newLf(p *lfPage) {
	model := p.doc.Model
	if model == "" {
		model = a.preset().Repo
	}
	d := services.NewLongformDoc(p.kind, model)
	d.Voice = p.doc.Voice
	d.Language = p.doc.Language
	a.setLfDoc(p, d, "", "")
}

// --- seams for the voice pages ---------------------------------------------

// useVoiceInStories adds v to the story's cast (once) and opens Stories:
// the gallery's and the voice editor's "Use in Stories".
func (a *app) useVoiceInStories(v *core.Voice) {
	p := a.lfStories
	if !slices.ContainsFunc(p.doc.Cast, func(m services.CastMember) bool { return m.Voice == v.ID }) {
		p.doc.Cast = append(p.doc.Cast, services.CastMember{ID: core.NewID(time.Now()), Name: v.Name, Voice: v.ID})
	}
	a.lfFitModel(p, v)
	p.open["cast"] = true
	a.navigate("/stories")
}

// useVoiceInAudiobook makes v the audiobook's narrator ("Set as audiobook
// default") and opens the Audiobook page.
func (a *app) useVoiceInAudiobook(v *core.Voice) {
	p := a.lfBook
	p.doc.Voice = v.ID
	a.lfFitModel(p, v)
	a.navigate("/audiobook")
}

// lfFitModel switches a page to a model that can speak with v when its
// own can't.
func (a *app) lfFitModel(p *lfPage, v *core.Voice) {
	if ok, _ := voiceUsable(v, entryFor(p.doc.Model)); ok {
		return
	}
	var pick []catalog.Entry
	if v.Kind == core.VoiceDesigned {
		pick = designModels()
	}
	for _, e := range append(pick, cloneModels()...) {
		if ok, _ := voiceUsable(v, e); ok {
			p.doc.Model = e.Repo
			return
		}
	}
}

// longformVoiceItems adds Use in Stories and Narrate an audiobook to a
// voice menu; use hands the voice (saved first, for an archetype) on.
func (a *app) longformVoiceItems(m *ui.Menu, use func(then func(*core.Voice))) {
	if m.Item(i18n.T("Use in Stories")).Chosen() {
		use(a.useVoiceInStories)
	}
	if m.Item(i18n.T("Narrate an audiobook")).Chosen() {
		use(a.useVoiceInAudiobook)
	}
}

// voiceLongformMenu is the voice editor's "More" menu with them.
func (a *app) voiceLongformMenu(c *ui.Context, v *core.Voice) {
	id := v.ID
	ui.Button(c.Key("editor-longform"), "⋯").Label(i18n.T("More actions")).Menu(func(m *ui.Menu) {
		a.longformVoiceItems(m, func(then func(*core.Voice)) {
			if v := a.findVoice(id); v != nil {
				then(v)
			}
		})
	})
}

// projectUsageLink is a project in a voice's "Where this voice has been
// used": it opens the project (voice_editor.go).
func (a *app) projectUsageLink(c *ui.Context, u core.VoiceProjectUsage) {
	t := c.Theme()
	label := u.Title + " · " + i18n.N(u.Segments, "%d segment", "%d segments", u.Segments)
	if u.Kind != core.ProjectStory && u.Kind != core.ProjectAudiobook {
		ui.Text(c, label).FontSize(px(t, 12))
		return
	}
	if ui.ButtonBase(c.Key("usage-"+u.ID)).Label(label).Children(func() {
		ui.Text(c, label).FontSize(px(t, 12)).TextColor(t.Accent)
	}).Clicked() {
		a.askOpenProject(u.ID, u.Kind, u.Title)
	}
}

// askOpenProject opens a project, asking first when its page has unsaved
// changes.
func (a *app) askOpenProject(id string, kind core.ProjectKind, title string) {
	p := a.lfPageOf(kind)
	if p.project == id {
		a.navigate("/" + lfRoute(kind))
		return
	}
	if p.unsaved() {
		// The page asks (lfConfirm), over the draft it would replace.
		a.navigate("/" + lfRoute(kind))
		p.confirm, p.confirmOpen = id, true
		if !slices.ContainsFunc(p.projects, func(x core.Project) bool { return x.ID == id }) {
			p.projects = append(p.projects, core.Project{ID: id, Kind: kind, Title: title})
		}
		return
	}
	a.openLfProject(id)
}
