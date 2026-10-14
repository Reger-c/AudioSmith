package main

// WorkflowStudio (M6, thread #21): /workflows edits node-graph workflows
// (internal/workflow) on a canvas and runs them through services.Workflows
// (migration 006; one job per step, owner "workflow:<id>"). The page keeps
// a working copy of the open workflow: every edit changes it and saves a
// copy in the background (autosave); the service's WorkflowChanged
// brings runs back. Editing is locked while a run goes on.
//
// Files of the area:
//
//	workflow.go            this: state, wiring, the library, edits, runs
//	workflow_canvas.go     the canvas: pan and zoom, step cards, ports, links, minimap, keys
//	workflow_view.go       the page: library rail, toolbar, the right panel
//	workflow_inspector.go  the step details and inputs (scripts, clips)
//	workflow_run.go        the run panel: checks, progress, results
//	workflow_pages.go      sidebar, route, title, palette

import (
	"context"
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
	"github.com/hehehai/AudioSmith/internal/doc"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/llm"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/store"
	"github.com/hehehai/AudioSmith/internal/translate"
	"github.com/hehehai/AudioSmith/internal/workflow"
)

// wfState is the Workflows page's state.
type wfState struct {
	wfSvc    *services.Workflows
	wfList   []store.WorkflowRecord // newest change first
	wfLoaded bool
	wf       *workflow.Workflow // the open workflow (the working copy); nil = none
	wfRun    *workflow.Run      // its last run
	wfJobs   map[string]jobs.Job
	wfVer    int // bumps on every edit (wfReport follows it)
	wfRep    *workflow.Report
	wfRepVer int

	wfSaving, wfPending bool // a save in flight, and another wanted after it

	wfSel      string // the selected step
	wfSelEdge  string // the selected link
	wfPanel    int    // the right panel: wfPanelStep or wfPanelRun
	wfRenaming string // the workflow being renamed in the list
	wfRenameTx string
	wfConfirm  string // the workflow asked to delete
	wfAsk      bool
	wfErr      string // why the last action failed (translated)
	wfExpanded map[string]bool
	wfCanvas   wfCanvasState
	wfCanvasH  ui.Handle

	// wfEngine replaces the engine's executors (tests).
	wfEngine func(method string) jobs.Executor
}

// The right panel.
const (
	wfPanelStep = iota
	wfPanelRun
)

// wfSchedFor wraps the workflow service's scheduler (tests).
var wfSchedFor func(jobs.Scheduler) jobs.Scheduler

// openWorkflows makes the workflow service over the library and follows
// its events and jobs.
func (a *app) openWorkflows(lib *library) {
	a.wfJobs = map[string]jobs.Job{}
	a.wfExpanded = map[string]bool{}
	host := a.eng
	router := a.llmRouter
	sched := lib.sched
	if wfSchedFor != nil {
		sched = wfSchedFor(sched)
	}
	a.wfSvc = services.NewWorkflows(services.WorkflowsConfig{
		Store: lib.store.Workflows(), Bus: lib.bus, Scheduler: sched, Render: lib.svc.Render, Async: a.background,
		Engine: func(method string) jobs.Executor {
			if a.wfEngine != nil {
				return a.wfEngine(method)
			}
			if host == nil || host.sup == nil {
				return nil
			}
			return engine.Executor(host.sup, dub.RoleOf(method), method)
		},
		ModelPath: lib.modelSnapshot,
		STT: func() services.WorkflowSTT {
			m := dubModels(lib)
			return services.WorkflowSTT{Model: m.STT, Aligner: m.Align, Words: m.WordTimes}
		},
		Caps:  dubCaps,
		Voice: a.dubVoiceResolver(lib),
		Translator: func(ctx context.Context, name string) (translate.Translator, error) {
			return dubMakeTranslator(router, dub.Settings{Translator: name})
		},
	})
	core.On(lib.bus, func(ev services.WorkflowChanged) { a.post(func() { a.wfChanged(ev) }) })
	core.On(lib.bus, func(u jobs.Updated) {
		if u.Chunk == nil && strings.HasPrefix(u.Job.Owner, services.WorkflowOwner) {
			a.post(func() { a.wfJobUpdated(u.Job) })
		}
	})
	a.wfLoad()
}

// wfLoad reads the library, and opens the workflow shown last.
func (a *app) wfLoad() {
	svc := a.wfSvc
	a.background(func() {
		list, err := svc.List(context.Background())
		a.post(func() {
			a.wfLoaded = true
			if err != nil {
				a.toast(i18n.T("Could not read the workflows: %v", err))
				return
			}
			a.wfList = list
			if a.wf == nil && len(list) > 0 {
				a.wfOpen(list[0].ID)
			}
		})
	})
}

// wfOpen opens a workflow, with its run and its unfinished jobs.
func (a *app) wfOpen(id string) {
	svc := a.wfSvc
	a.background(func() {
		ctx := context.Background()
		w, run, err := svc.Get(ctx, id)
		var active []jobs.Job
		if err == nil {
			active, _ = svc.Active(ctx, id)
		}
		a.post(func() {
			if err != nil {
				a.toast(i18n.T("Could not open the workflow: %v", err))
				return
			}
			a.wfShow(w, run, active)
		})
	})
}

// wfShow makes w the open workflow.
func (a *app) wfShow(w *workflow.Workflow, run *workflow.Run, active []jobs.Job) {
	a.wf, a.wfRun = w, run
	a.wfJobs = map[string]jobs.Job{}
	for _, j := range active {
		a.wfJobs[j.ID] = j
	}
	a.wfSel, a.wfSelEdge, a.wfErr = "", "", ""
	a.wfCanvas = wfCanvasState{}
	a.wfVer++
	if a.wfBusy() {
		a.wfPanel = wfPanelRun
	}
}

// wfChanged follows the service: the list, and the open workflow's run.
func (a *app) wfChanged(ev services.WorkflowChanged) {
	i := slices.IndexFunc(a.wfList, func(r store.WorkflowRecord) bool { return r.ID == ev.ID })
	// The Projects page lists names and run states.
	if i < 0 || ev.Deleted || a.wfList[i].Name != ev.Record.Name || a.wfList[i].State != ev.Record.State {
		a.projects.dirty = true
	}
	if ev.Deleted {
		if i >= 0 {
			a.wfList = slices.Delete(a.wfList, i, i+1)
		}
		if a.wf != nil && a.wf.ID == ev.ID {
			a.wf, a.wfRun = nil, nil
			if len(a.wfList) > 0 {
				a.wfOpen(a.wfList[0].ID)
			}
		}
		return
	}
	if ev.Record.ID != "" {
		if i >= 0 {
			a.wfList[i] = ev.Record
		} else {
			a.wfList = append(a.wfList, ev.Record)
		}
		slices.SortStableFunc(a.wfList, func(x, y store.WorkflowRecord) int { return y.UpdatedAt.Compare(x.UpdatedAt) })
	}
	if a.wf != nil && a.wf.ID == ev.ID && ev.Run != nil {
		a.wfRun = ev.Run
	}
}

// wfJobUpdated follows the open workflow's jobs.
func (a *app) wfJobUpdated(j jobs.Job) {
	if a.wf == nil || j.Owner != services.WorkflowOwner+a.wf.ID {
		return
	}
	if j.State.Terminal() || j.State == jobs.StateInterrupted {
		delete(a.wfJobs, j.ID)
		if j.State == jobs.StateFailed && j.Err != nil && j.Err.Code != jobs.CodeShutdown {
			a.wfErr = wfErrorText(j.Err.Code, j.Err.Message)
		}
		if !a.wfBusy() && j.State == jobs.StateSucceeded && a.wfRun != nil {
			done, failed, total := a.wfRun.Counts()
			if failed == 0 && done == total {
				a.notify(core.SeveritySuccess, i18n.T("Workflow finished"), i18n.T("%s: %d of %d items ready.", a.wf.Name, done, total), "/workflows")
			}
		}
		return
	}
	a.wfJobs[j.ID] = j
}

// wfBusy reports whether the open workflow is running.
func (a *app) wfBusy() bool { return len(a.wfJobs) > 0 }

// wfLocked reports whether the open workflow can't be edited now.
func (a *app) wfLocked() bool { return a.wf == nil || a.wfBusy() }

// --- the library ----------------------------------------------------------------------

// wfNames are the names in the library.
func (a *app) wfNames() []string {
	var out []string
	for _, r := range a.wfList {
		out = append(out, r.Name)
	}
	return out
}

// wfTemplateOptions are the Studio's model and voice, for new steps.
func (a *app) wfTemplateOptions() workflow.TemplateOptions {
	e := a.preset()
	o := workflow.TemplateOptions{Model: e.Repo}
	if a.studioVoice != "" {
		if v := a.findVoice(a.studioVoice); v != nil {
			if ok, _ := voiceUsable(v, e); ok {
				o.Voice = v.ID
			}
		}
	}
	return o
}

// wfNew makes a workflow: Start → Speak → Export, or a template ("" =
// the blank one), and opens it.
func (a *app) wfNew(template string) {
	now := time.Now()
	var w *workflow.Workflow
	if tp, ok := workflow.TemplateByID(template); ok {
		w = tp.Make(core.NewID(now), workflow.TemplateName(tp.ID), now, a.wfTemplateOptions())
	} else {
		w = workflow.New(core.NewID(now), i18n.T("Untitled workflow"), now)
		o := a.wfTemplateOptions()
		for i := range w.Nodes {
			if w.Nodes[i].Kind == workflow.KindSpeak {
				w.Nodes[i].P.Model, w.Nodes[i].P.Voice = o.Model, o.Voice
			}
		}
	}
	if slices.Contains(a.wfNames(), w.Name) {
		w.Name = workflow.CopyName(w.Name, a.wfNames())
	}
	w.Normalize()
	a.wfAdopt(w)
}

// wfAdopt opens a new workflow and saves it.
func (a *app) wfAdopt(w *workflow.Workflow) {
	a.wfShow(w, nil, nil)
	a.wfList = append([]store.WorkflowRecord{{ID: w.ID, Name: w.Name, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt}}, a.wfList...)
	a.wfPanel = wfPanelStep
	if in := w.InputNode(); in != nil {
		a.wfSel = in.ID
	}
	a.wfSave()
}

// wfDuplicate copies the open workflow (without its run).
func (a *app) wfDuplicate() {
	if a.wf == nil {
		return
	}
	now := time.Now()
	a.wfAdopt(a.wf.Duplicate(core.NewID(now), workflow.CopyName(a.wf.Name, a.wfNames()), now))
}

// wfRename renames a workflow of the list.
func (a *app) wfRename(id, name string) {
	name = strings.TrimSpace(name)
	a.wfRenaming = ""
	if name == "" {
		return
	}
	if a.wf != nil && a.wf.ID == id {
		a.wfEdit(func(w *workflow.Workflow) { w.Name = name })
		return
	}
	svc := a.wfSvc
	a.background(func() {
		ctx := context.Background()
		w, _, err := svc.Get(ctx, id)
		if err == nil {
			w.Name = name
			_, err = svc.Save(ctx, w)
		}
		if err != nil {
			a.post(func() { a.toast(i18n.T("Could not rename the workflow: %v", err)) })
		}
	})
}

// wfDelete deletes a workflow, its run and its files.
func (a *app) wfDelete(id string) {
	svc := a.wfSvc
	a.background(func() {
		if err := svc.Delete(context.Background(), id); err != nil {
			a.post(func() { a.toast(i18n.T("Could not delete the workflow: %v", err)) })
		}
	})
}

// --- edits ------------------------------------------------------------------------------

// wfEdit changes the open workflow and saves it.
func (a *app) wfEdit(fn func(w *workflow.Workflow)) {
	if a.wf == nil {
		return
	}
	fn(a.wf)
	a.wfVer++
	if i := slices.IndexFunc(a.wfList, func(r store.WorkflowRecord) bool { return r.ID == a.wf.ID }); i >= 0 {
		a.wfList[i].Name = a.wf.Name
	}
	a.wfSave()
}

// wfSave saves a copy of the open workflow in the background, one save
// at a time (edits made meanwhile are saved after it).
func (a *app) wfSave() {
	if a.wf == nil {
		return
	}
	if a.wfSaving {
		a.wfPending = true
		return
	}
	a.wfSaving = true
	w := a.wf.Clone()
	svc := a.wfSvc
	a.background(func() {
		_, err := svc.Save(context.Background(), w)
		a.post(func() {
			a.wfSaving = false
			if err != nil {
				a.toast(i18n.T("Could not save the workflow: %v", err))
			}
			if a.wfPending {
				a.wfPending = false
				a.wfSave()
			}
		})
	})
}

// wfNode is the open workflow's step id, or nil.
func (a *app) wfNode(id string) *workflow.Node {
	if a.wf == nil {
		return nil
	}
	return a.wf.Node(id)
}

// wfAddStep adds a step of kind k at x, y (content coordinates; the
// view's free spot when x < 0), linked after the selected step when it
// can take its value, and selects it.
func (a *app) wfAddStep(k workflow.Kind, x, y float64) {
	if a.wfLocked() {
		return
	}
	from := a.wfSel
	a.wfEdit(func(w *workflow.Workflow) {
		if x < 0 {
			x, y = a.wfFreeSpot()
		}
		id := w.AddNode(k, max(x, 0), max(y, 0))
		if id == "" {
			a.toast(i18n.T("A workflow holds at most %d steps.", workflow.MaxNodes))
			return
		}
		n := w.Node(id)
		if k == workflow.KindSpeak || k == workflow.KindConvert {
			o := a.wfTemplateOptions()
			n.P.Model, n.P.Voice = o.Model, o.Voice
		}
		if k == workflow.KindStart {
			n.P.Scripts = []workflow.Script{{}}
		}
		if f := w.Node(from); f != nil && from != id {
			spec, _ := workflow.SpecOf(f.Kind)
			to, _ := workflow.SpecOf(k)
			rep := workflow.Validate(w, nil)
			if len(spec.Out) == 1 && len(to.In) == 1 && len(w.Outgoing(from)) == 0 {
				if t, ok := rep.Out[from]; ok && slices.Contains(to.In[0].Accepts, t) {
					w.Connect(from, spec.Out[0].Name, id, workflow.PortIn)
				}
			}
		}
		a.wfSel, a.wfSelEdge = id, ""
	})
	a.wfPanel = wfPanelStep
}

// wfFreeSpot is a free place for a new step near the middle of the view.
func (a *app) wfFreeSpot() (float64, float64) {
	w := a.wf
	v := w.View
	cx := v.X + float64(a.wfCanvas.w)/2/v.Zoom - wfCardW/2
	cy := v.Y + float64(a.wfCanvas.h)/2/v.Zoom - wfCardH/2
	if a.wfCanvas.w == 0 {
		return w.FreeSpot()
	}
	for i := range 40 {
		x := cx + float64(i%5)*24
		y := cy + float64(i/5)*wfCardH*1.4
		free := true
		for _, n := range w.Nodes {
			if n.X < x+wfCardW && x < n.X+wfCardW && n.Y < y+wfCardH && y < n.Y+wfCardH {
				free = false
				break
			}
		}
		if free {
			return max(x, 0), max(y, 0)
		}
	}
	return w.FreeSpot()
}

// wfRemoveSelected removes the selected step or link.
func (a *app) wfRemoveSelected() {
	if a.wfLocked() {
		return
	}
	switch {
	case a.wfSelEdge != "":
		id := a.wfSelEdge
		a.wfEdit(func(w *workflow.Workflow) { w.RemoveEdge(id) })
		a.wfSelEdge = ""
	case a.wfSel != "":
		id := a.wfSel
		a.wfEdit(func(w *workflow.Workflow) { w.RemoveNode(id) })
		a.wfSel = ""
	}
}

// wfConnect links two ports, from an output to an input.
func (a *app) wfConnect(from, fromPort, to, toPort string) {
	if a.wfLocked() {
		return
	}
	var err error
	var id string
	a.wfEdit(func(w *workflow.Workflow) { id, err = w.Connect(from, fromPort, to, toPort) })
	if err != nil {
		a.toast(i18n.T("Can't link these steps."))
		return
	}
	a.wfSelEdge, a.wfSel = id, ""
}

// wfReport is the open workflow's validation, kept until it changes.
func (a *app) wfReport() *workflow.Report {
	if a.wf == nil {
		return &workflow.Report{}
	}
	if a.wfRep == nil || a.wfRepVer != a.wfVer {
		a.wfRep, a.wfRepVer = workflow.Validate(a.wf, a.wfCheck), a.wfVer
	}
	return a.wfRep
}

// App-side diagnostics (workflow.Check).
const (
	wfCodeNotDownloaded = "app.not_downloaded"
	wfCodeVoice         = "app.voice"
	wfCodeTranslator    = "app.translator"
	wfCodeSTT           = "app.stt"
	wfCodeEncode        = "app.encode"
)

// wfCheck is what only the app knows about a step: its model downloaded,
// its voice there and usable, a translator and a speech-to-text model
// set up. It reads memory only (the models' state, the voices).
func (a *app) wfCheck(n *workflow.Node, _ workflow.Type) []workflow.Diagnostic {
	var out []workflow.Diagnostic
	add := func(sev workflow.Severity, code, msg string) {
		out = append(out, workflow.Diagnostic{Severity: sev, Code: code, Node: n.ID, Arg: msg})
	}
	stt := func() {
		m := dubModels(a.lib)
		if m.STT == "" {
			add(workflow.Error, wfCodeSTT, i18n.T("No speech-to-text model is available: download one on the Models page."))
			return
		}
		if e, ok := catalog.Builtin().ByRepo(m.STT); ok {
			if ready, known := a.modelOnDisk(e); known && !ready {
				add(workflow.Warning, wfCodeNotDownloaded, i18n.T("%s is not downloaded yet.", e.Label))
			}
		}
	}
	switch n.Kind {
	case workflow.KindSpeak, workflow.KindConvert:
		if n.P.Model == "" {
			break
		}
		e := entryFor(n.P.Model)
		if ready, known := a.modelOnDisk(e); known && !ready {
			add(workflow.Warning, wfCodeNotDownloaded, i18n.T("%s is not downloaded yet.", e.Label))
		}
		if ref := n.P.Voice; ref != "" && !strings.Contains(ref, ":") {
			v := a.findVoice(ref)
			switch {
			case v == nil:
				add(workflow.Error, wfCodeVoice, i18n.T("Its voice was deleted: choose another."))
			default:
				if ok, why := voiceUsable(v, e); !ok {
					add(workflow.Error, wfCodeVoice, why)
				}
			}
		}
		if n.Kind == workflow.KindConvert {
			stt()
		}
	case workflow.KindTranscribe:
		stt()
	case workflow.KindTranslate:
		if ok, _ := a.dubTranslatorReady(dub.Settings{Translator: n.P.Translator}); !ok {
			add(workflow.Error, wfCodeTranslator, i18n.T("Translating needs a translation engine: a local model (Ollama, LM Studio) or a provider in Settings → Writing help."))
		}
	case workflow.KindExport:
		if n.P.Format == workflow.FormatM4A && !a.wfCanEncode() {
			add(workflow.Warning, wfCodeEncode, i18n.T("M4A needs the speech engine; it is not running."))
		}
	}
	return out
}

// wfCanEncode reports whether M4A can be written now.
func (a *app) wfCanEncode() bool {
	return a.wfEngine != nil || a.eng != nil && a.eng.sup != nil
}

// --- runs ---------------------------------------------------------------------------------

// wfStart runs the open workflow (again: every result anew).
func (a *app) wfStart(again bool) {
	if a.wf == nil || a.wfBusy() {
		return
	}
	rep := a.wfReport()
	a.wfPanel = wfPanelRun
	if !rep.OK() {
		a.wfErr = i18n.T("Fix the steps marked in red first.")
		return
	}
	a.wfErr = ""
	w := a.wf.Clone()
	svc, sched := a.wfSvc, a.lib.sched
	a.background(func() {
		ctx := context.Background()
		// The working copy first: the run reads the saved one.
		_, err := svc.Save(ctx, w)
		var run *workflow.Run
		if err == nil {
			run, err = svc.Run(ctx, w.ID, services.RunOptions{Again: again, Priority: jobs.PriorityNormal})
		}
		var active []jobs.Job
		if err == nil && sched != nil {
			active, _ = svc.Active(ctx, w.ID)
		}
		a.post(func() {
			if a.wf == nil || a.wf.ID != w.ID {
				return
			}
			if err != nil {
				var inv *services.WorkflowInvalidError
				switch {
				case errors.As(err, &inv):
					a.wfErr = i18n.T("Fix the steps marked in red first.")
				case errors.Is(err, core.ErrBusy):
					a.wfErr = i18n.T("This workflow is running already.")
				default:
					a.wfErr = i18n.T("Could not start the run: %v", err)
				}
				return
			}
			// Events may have brought a later state of this run already.
			if a.wfRun == nil || a.wfRun.ID != run.ID {
				a.wfRun = run
			}
			for _, j := range active {
				if !j.State.Terminal() {
					a.wfJobs[j.ID] = j
				}
			}
			if len(run.Steps) == 0 {
				a.toast(i18n.T("Nothing changed since the last run: every result is ready."))
			}
		})
	})
}

// wfCancel stops the open workflow's run (it can resume later).
func (a *app) wfCancel() {
	if a.wf == nil {
		return
	}
	svc, id := a.wfSvc, a.wf.ID
	a.background(func() {
		if err := svc.Cancel(context.Background(), id); err != nil {
			a.post(func() { a.toast(i18n.T("Could not stop the run: %v", err)) })
		}
	})
}

// wfStale reports whether the workflow changed since its last run.
func (a *app) wfStale() bool {
	if a.wf == nil || a.wfRun == nil || a.wfRun.Signature == "" {
		return false
	}
	rep := a.wfReport()
	if !rep.OK() {
		return false
	}
	items := a.wf.Items()
	return workflow.RunSignature(workflow.StepKeys(a.wf, rep, items, a.wfSalt()), items) != a.wfRun.Signature
}

// wfSalt is the service's salt (the speech-to-text model).
func (a *app) wfSalt() workflow.Salt {
	m := dubModels(a.lib)
	return func(n *workflow.Node) string {
		switch n.Kind {
		case workflow.KindTranscribe, workflow.KindConvert:
			return m.STT + "|" + m.Align
		}
		return ""
	}
}

// wfExportAll copies every result into a folder the user picks.
func (a *app) wfExportAll() {
	if a.wf == nil {
		return
	}
	svc, id := a.wfSvc, a.wf.ID
	run := func(dir string) {
		a.background(func() {
			files, err := svc.ExportAll(context.Background(), id, dir, "")
			a.post(func() {
				if err != nil {
					a.toast(i18n.T("Could not export: %v", err))
					return
				}
				a.toast(i18n.N(len(files), "Exported %d file.", "Exported %d files.", len(files)))
			})
		})
	}
	if wfExportDir != "" {
		run(wfExportDir)
		return
	}
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.win, Title: i18n.T("Export every result to a folder"), Directory: true})
		if err != nil || len(paths) == 0 {
			return
		}
		run(paths[0])
	}()
}

// wfExportDir replaces the folder dialogs (tests).
var wfExportDir string

// --- inputs ----------------------------------------------------------------------------

// wfScriptExts are the documents a Start step imports.
var wfScriptExts = []string{"txt", "md", "markdown", "docx", "epub"}

// wfAudioExts are the clips a Source step takes.
var wfAudioExts = []string{"wav", "mp3", "m4a", "aac", "aiff", "aif", "flac", "ogg", "caf", "mp4", "mov"}

// wfImportScripts asks for documents and adds each as a script.
func (a *app) wfImportScripts(node string) {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.win, Title: i18n.T("Import scripts"), Multiple: true,
			Filters: []mygo.FileFilter{{Name: i18n.T("Documents"), Extensions: wfScriptExts}}})
		if err != nil || len(paths) == 0 {
			return
		}
		a.wfScriptFiles(node, paths)
	}()
}

// wfScriptFiles reads documents into a Start step's scripts, one each
// (on a goroutine: doc.Open reads files).
func (a *app) wfScriptFiles(node string, paths []string) {
	a.background(func() {
		var scripts []workflow.Script
		var failed []string
		for _, p := range paths {
			d, err := doc.Open(context.Background(), p, doc.Limits{MaxChars: 4 * workflow.MaxScriptChars})
			if err != nil {
				failed = append(failed, filepath.Base(p))
				continue
			}
			scripts = append(scripts, workflow.Script{Name: filepath.Base(p), Text: d.Text()})
		}
		a.post(func() {
			if len(failed) > 0 {
				a.toast(i18n.T("Could not read %s.", strings.Join(failed, ", ")))
			}
			if len(scripts) == 0 || a.wfLocked() || a.wfNode(node) == nil {
				return
			}
			a.wfEdit(func(w *workflow.Workflow) {
				n := w.Node(node)
				// An empty first script gives way to the imports.
				if len(n.P.Scripts) == 1 && strings.TrimSpace(n.P.Scripts[0].Text) == "" {
					n.P.Scripts = nil
				}
				n.P.Scripts = append(n.P.Scripts, scripts...)
				if len(n.P.Scripts) > workflow.MaxScripts {
					n.P.Scripts = n.P.Scripts[:workflow.MaxScripts]
					a.toast(i18n.T("A Start step takes at most %d scripts.", workflow.MaxScripts))
				}
			})
		})
	})
}

// wfAddClips asks for audio files.
func (a *app) wfAddClips(node string) {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.win, Title: i18n.T("Add audio"), Multiple: true,
			Filters: []mygo.FileFilter{{Name: i18n.T("Audio"), Extensions: wfAudioExts}}})
		if err != nil || len(paths) == 0 {
			return
		}
		a.post(func() { a.wfClipFiles(node, paths) })
	}()
}

// wfClipFiles adds audio files to a Source step (it stats them: small,
// local files, as the Dub page does on drop).
func (a *app) wfClipFiles(node string, paths []string) {
	if a.wfLocked() || a.wfNode(node) == nil {
		return
	}
	var clips []workflow.Clip
	var skipped []string
	for _, p := range paths {
		fi, err := os.Stat(p)
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(p)), ".")
		switch {
		case err != nil || fi.IsDir() || !slices.Contains(wfAudioExts, ext):
			skipped = append(skipped, filepath.Base(p))
		case fi.Size() > workflow.MaxClipBytes:
			skipped = append(skipped, filepath.Base(p))
			a.toast(i18n.T("%s is over 64 MiB.", filepath.Base(p)))
		default:
			clips = append(clips, workflow.Clip{Name: filepath.Base(p), Path: p, Size: fi.Size(), Mod: fi.ModTime()})
		}
	}
	if len(skipped) > 0 && len(clips) == 0 {
		a.toast(i18n.T("Add audio files: %s can't be used.", strings.Join(skipped, ", ")))
	}
	if len(clips) == 0 {
		return
	}
	a.wfEdit(func(w *workflow.Workflow) {
		n := w.Node(node)
		n.P.Clips = append(n.P.Clips, clips...)
		if len(n.P.Clips) > workflow.MaxClips {
			n.P.Clips = n.P.Clips[:workflow.MaxClips]
			a.toast(i18n.T("A Source clips step takes at most %d clips.", workflow.MaxClips))
		}
	})
}

// wfChooseFolder asks for an export step's folder.
func (a *app) wfChooseFolder(node string) {
	set := func(dir string) {
		a.wfEdit(func(w *workflow.Workflow) {
			if n := w.Node(node); n != nil {
				n.P.Folder = dir
			}
		})
	}
	if wfExportDir != "" {
		set(wfExportDir)
		return
	}
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.win, Title: i18n.T("Choose the export folder"), Directory: true})
		if err != nil || len(paths) == 0 {
			return
		}
		a.post(func() { set(paths[0]) })
	}()
}

// --- texts --------------------------------------------------------------------------------

// wfErrorText explains a failed step or run.
func wfErrorText(code, msg string) string {
	switch code {
	case "":
		return msg
	case "model_not_downloaded":
		return i18n.T("A model this workflow uses is not downloaded: download it on the Models page.")
	case "licence_required":
		return i18n.T("A model this workflow uses needs its licence accepted on the Models page.")
	case services.CodeEngineUnavailable:
		return i18n.T("The speech engine is not running. Start it on the Engine page.")
	case services.WorkflowCodeNoSTT:
		return i18n.T("No speech-to-text model is available: download one on the Models page.")
	case services.WorkflowCodeNoSpeech:
		return i18n.T("No speech was found.")
	case services.WorkflowCodeVoice:
		return i18n.T("The voice can't be used: %s", msg)
	case services.WorkflowCodeEncode:
		return i18n.T("This engine can't write M4A: choose WAV.")
	case services.WorkflowCodeFile:
		return i18n.T("A file could not be read or written: %s", msg)
	case services.CodeNothingToSay:
		return i18n.T("There is nothing to say.")
	case translate.CodeNoLLM, translate.CodeNoKey, translate.CodeConfig:
		return i18n.T("Translating needs a translation engine: a local model (Ollama, LM Studio) or a provider in Settings → Writing help.")
	case translate.CodeLineFailed, translate.CodeMissing:
		return i18n.T("Some text could not be translated.")
	}
	return msg
}

// wfWarningText explains a step's warning.
func wfWarningText(code string) string {
	switch code {
	case services.WorkflowWarnSilent:
		return i18n.T("The audio is silent: kept as it is.")
	case services.WorkflowWarnNoFolder:
		return i18n.T("No folder: kept in the Results list.")
	case services.WarnNoWordTimes:
		return i18n.T("Without word timings: subtitles are timed from the text.")
	}
	return code
}

// wfLLMReady reports whether a language model can translate.
func (a *app) wfLLMReady() bool {
	return a.llmRouter != nil && a.llmRouter.Available(llm.SkillTranslate)
}
