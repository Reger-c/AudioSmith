package main

// Workflow edits: the working copy's autosave, adding, placing, removing
// and connecting steps; split from workflow.go.

import (
	"context"
	"slices"

	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/store"
	"github.com/hehehai/AudioSmith/internal/workflow"
)

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
