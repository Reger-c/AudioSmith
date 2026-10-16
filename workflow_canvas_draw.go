package main

// Drawing the workflow canvas: the view, the links, the pending link,
// the cards with their badges and summaries, and the minimap; split from
// workflow_canvas.go.

import (
	"math"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
	"github.com/hehehai/AudioSmith/internal/workflow"
)

// wfCanvasView builds the canvas.
func (a *app) wfCanvasView(c *ui.Context) {
	t := c.Theme()
	w := a.wf
	rep := a.wfReport()
	if b := a.wfCanvasH.Bounds(c); b.W > 0 {
		a.wfCanvas.w, a.wfCanvas.h = b.W, b.H
		if a.wfCanvas.fit {
			a.wfCanvas.fit = false
			if !a.wfAllShown() {
				a.wfFit()
			}
		}
	}
	v := w.View
	z := float32(v.Zoom)
	canvas := ui.Box(c.Key("wf-canvas")).Grow(1).MinWidth(0).MinHeight(0).Clip().Focusable().Bind(&a.wfCanvasH).
		Background(tok(t).Subtle).Label(i18n.T("Workflow canvas"))
	held := c.Modifiers() // the view builds again as they change
	canvas.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind == ui.InputScroll {
			ev.Mods |= held // ⌘ held on its own
		}
		return a.wfCanvasInput(ev)
	})
	if a.wfCanvas.gesture == wfGesturePan {
		canvas.Cursor(ui.CursorGrabbing)
	}
	// Steps dragged from the palette land where they are dropped.
	if _, over := ui.DragOver[workflow.Kind](canvas); over {
		canvas.Border(2, t.Accent)
	}
	if k, ok := ui.Drop[workflow.Kind](canvas); ok {
		x, y, _ := canvas.PointerPosition()
		a.wfAddStep(k, v.X+float64(x)/v.Zoom-wfCardW/2, v.Y+float64(y)/v.Zoom-wfCardH/2)
	}
	dot := tok(t).G[4]
	canvas.Draw(func(p *ui.Painter, r ui.Rect) {
		// Dots every 24 content units, moving with the view.
		gap := 24 * z
		for gap < 12 {
			gap *= 2
		}
		ox := float32(math.Mod(-v.X*float64(z), float64(gap)))
		oy := float32(math.Mod(-v.Y*float64(z), float64(gap)))
		for x := r.X + ox; x < r.X+r.W; x += gap {
			for y := r.Y + oy; y < r.Y+r.H; y += gap {
				p.Fill(ui.Rect{X: x - 0.75, Y: y - 0.75, W: 1.5, H: 1.5}, dot, 0.75)
			}
		}
	})
	_, _, x1, y1 := wfBounds(w)
	cw, ch := float32(x1)+wfCardW*2, float32(y1)+wfCardH*4
	canvas.Children(func() {
		content := ui.Box(c.Key("wf-content")).Absolute().Left(0).Top(0).Size(cw, ch).PassThrough().
			Transform(ui.Translation(float32(-v.X), float32(-v.Y)).Then(ui.Scaling(z, z))).TransformOrigin(0, 0)
		content.Draw(func(p *ui.Painter, r ui.Rect) { a.wfDrawEdges(p, r, t, rep) })
		content.DrawOver(func(p *ui.Painter, r ui.Rect) { a.wfDrawPending(p, r, t, rep) })
		content.Children(func() {
			for i := range w.Nodes {
				a.wfCard(c, &w.Nodes[i], rep)
			}
		})
		a.wfMinimap(c)
		if len(w.Nodes) == 0 {
			ui.Column(c.Key("wf-empty")).Absolute().Left(0).Top(0).Right(0).Bottom(0).Center().PassThrough().Children(func() {
				ui.Text(c, i18n.T("Add a step from the left, or start from a template.")).TextColor(tok(t).Text2)
			})
		}
	})
}

// wfDrawEdges paints the links (content coordinates from r's origin).
func (a *app) wfDrawEdges(p *ui.Painter, r ui.Rect, t *ui.Theme, rep *workflow.Report) {
	w := a.wf
	for _, e := range w.Edges {
		c, ok := wfEdgeCurve(w, e)
		if !ok {
			continue
		}
		col := wfTypeColor(rep.Out[e.From], t.TextMuted)
		width := float32(2)
		if len(rep.ForEdge(e.ID)) > 0 {
			col = t.Danger
		}
		if e.ID == a.wfSelEdge {
			width = 3.5
			col = t.Accent
		}
		// A branch not taken by the selected… keep it simple: the same.
		var path ui.Path
		path.MoveTo(r.X+float32(c[0][0]), r.Y+float32(c[0][1])).
			CubeTo(r.X+float32(c[1][0]), r.Y+float32(c[1][1]), r.X+float32(c[2][0]), r.Y+float32(c[2][1]), r.X+float32(c[3][0]), r.Y+float32(c[3][1]))
		p.StrokePath(&path, width, col)
		// The arrow into the input.
		ex, ey := r.X+float32(c[3][0])-wfPortR, r.Y+float32(c[3][1])
		var head ui.Path
		head.MoveTo(ex, ey).LineTo(ex-8, ey-4.5).LineTo(ex-8, ey+4.5).Close()
		p.FillPath(&head, col)
	}
}

// wfDrawPending paints the link being drawn.
func (a *app) wfDrawPending(p *ui.Painter, r ui.Rect, t *ui.Theme, rep *workflow.Report) {
	cs := &a.wfCanvas
	if cs.gesture != wfGestureLink {
		return
	}
	n := a.wf.Node(cs.from)
	if n == nil {
		return
	}
	x0, y0 := wfPortAt(n, cs.fromPort, cs.fromIn)
	x1, y1 := cs.px, cs.py
	if cs.fromIn {
		x0, y0, x1, y1 = x1, y1, x0, y0
	}
	c := wfCurve(x0, y0, x1, y1)
	var path ui.Path
	path.MoveTo(r.X+float32(c[0][0]), r.Y+float32(c[0][1])).
		CubeTo(r.X+float32(c[1][0]), r.Y+float32(c[1][1]), r.X+float32(c[2][0]), r.Y+float32(c[2][1]), r.X+float32(c[3][0]), r.Y+float32(c[3][1]))
	p.StrokePath(&path, 2, t.Accent)
}

// wfCard builds a step's card.
func (a *app) wfCard(c *ui.Context, n *workflow.Node, rep *workflow.Report) {
	t := c.Theme()
	diags := rep.For(n.ID)
	errs, warns := 0, 0
	for _, d := range diags {
		if d.Severity == workflow.Error {
			errs++
		} else {
			warns++
		}
	}
	sel := n.ID == a.wfSel
	border, bw := tok(t).G[4], float32(1)
	switch {
	case sel:
		border, bw = t.Accent, 2
	case errs > 0:
		border, bw = t.Danger, 1.5
	}
	name := n.StepName(n.Kind.Label())
	card := ui.Column(c.Key("wf-node-"+n.ID)).Absolute().Left(float32(n.X)).Top(float32(n.Y)).Size(wfCardW, wfCardH).
		Padding(8, 12).Gap(3).Radius(10).Background(tok(t).Bg).Border(bw, border).Shadow(0, 1, 3, 0, ui.RGBA(0, 0, 0, 0.08)).
		Label(i18n.T("Step %s", name)).Role(ui.RoleGroup)
	card.Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			wfKindTile(c, n.Kind, 24, 15)
			ui.Column(c).Grow(1).MinWidth(0).Children(func() {
				ui.Text(c, name).Bold().FontSize(13).SingleLine().Ellipsis("…")
				if n.Name != "" {
					ui.Text(c, n.Kind.Label()).FontSize(10.5).TextColor(t.TextMuted).SingleLine()
				}
			})
			switch s := a.wfStepBadge(n.ID); s {
			case "":
			case "✓":
				ui.Icon(c, icons.CheckCircle.Linear).Size(14, 14).TextColor(t.Success).Shrink(0)
			default:
				ui.Text(c, s).FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(0)
			}
		})
		ui.Text(c, a.wfSummary(n)).FontSize(11.5).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
		switch {
		case errs > 0:
			ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, icons.DangerTriangle.Linear).Size(12, 12).TextColor(t.Danger).Shrink(0)
				ui.Text(c, diags[0].Text()).FontSize(11).TextColor(t.Danger).SingleLine().Ellipsis("…").MinWidth(0)
			})
		case warns > 0:
			ui.Text(c, diags[0].Text()).FontSize(11).TextColor(t.Warning).SingleLine().Ellipsis("…")
		}
		// Ports.
		ins, outs := wfPorts(n)
		for _, p := range ins {
			col := wfTypeColor(rep.In[n.ID], t.TextMuted)
			ui.Box(c.Key("in-"+p)).Absolute().Left(-wfPortR-bw/2).Top(wfCardH/2-wfPortR-bw).Size(2*wfPortR, 2*wfPortR).Radius(wfPortR).
				Background(t.Background).Border(2, col).PassThrough()
		}
		for _, p := range outs {
			_, py := wfPortAt(n, p, false)
			top := float32(py-n.Y) - wfPortR - bw
			col := wfTypeColor(rep.Out[n.ID], t.TextMuted)
			ui.Box(c.Key("out-"+p)).Absolute().Left(wfCardW-wfPortR-bw*1.5).Top(top).Size(2*wfPortR, 2*wfPortR).Radius(wfPortR).
				Background(col).PassThrough()
			if l := workflow.PortLabel(p); l != "" {
				ui.Text(c, l).FontSize(10).TextColor(t.TextMuted).Absolute().Right(10).Top(top - 1).PassThrough()
			}
		}
	})
}

// wfStepBadge is a card's run status: done/total, or the failures.
func (a *app) wfStepBadge(id string) string {
	run := a.wfRun
	if run == nil || len(run.Items) == 0 {
		return ""
	}
	done, failed, running, total := 0, 0, false, 0
	for _, it := range run.Items {
		s := it.Steps[id]
		if s == nil {
			continue
		}
		total++
		switch s.Status {
		case workflow.StepDone, workflow.StepSkipped:
			done++
		case workflow.StepFailed:
			failed++
		case workflow.StepRunning:
			running = true
		}
	}
	switch {
	case total == 0:
		return ""
	case failed > 0:
		return i18n.T("%d failed", failed)
	case running:
		return "⋯ " + itoa(done) + "/" + itoa(total)
	case done == total:
		return "✓"
	}
	return itoa(done) + "/" + itoa(total)
}

// wfSummary is a card's line: what the step is set to.
func (a *app) wfSummary(n *workflow.Node) string {
	p := n.P
	switch n.Kind {
	case workflow.KindStart:
		items := 0
		for _, s := range p.Scripts {
			items += len(workflow.SplitScript(s.Text))
		}
		return i18n.N(items, "%d script", "%d scripts", items)
	case workflow.KindSource:
		return i18n.N(len(p.Clips), "%d clip", "%d clips", len(p.Clips))
	case workflow.KindSpeak, workflow.KindConvert:
		if p.Model == "" {
			return i18n.T("Choose a voice model")
		}
		parts := []string{modelName(p.Model)}
		if p.Voice != "" {
			parts = append(parts, a.wfVoiceLabel(p.Voice))
		}
		if p.Speed != 0 && p.Speed != 1 {
			parts = append(parts, speedRange(p.Speed, p.Speed))
		}
		return strings.Join(parts, " · ")
	case workflow.KindTranscribe:
		return langName(p.From)
	case workflow.KindTranslate:
		return dubLangName(p.From) + " → " + dubLangName(p.To)
	case workflow.KindNormalize:
		if p.Level == workflow.LevelPeak {
			return i18n.T("Peak %.0f dBFS", p.Target)
		}
		return i18n.T("Loudness %.0f LUFS", p.Target)
	case workflow.KindCondition:
		if strings.TrimSpace(p.Text) == "" {
			return i18n.T("Add details")
		}
		return p.Match.Label() + " · “" + workflow.Excerpt(p.Text, 24) + "”"
	case workflow.KindExport:
		f := strings.ToUpper(workflow.ExportFormat(p.Format, a.wfReport().In[n.ID]))
		if p.Folder == "" {
			return f + " · " + i18n.T("Results only")
		}
		return f + " · " + shortPath(p.Folder)
	}
	return ""
}

// wfMinimap is the overview in the canvas's corner: every card, the part
// the view shows; pressing or dragging in it moves the view there.
func (a *app) wfMinimap(c *ui.Context) {
	t := c.Theme()
	w := a.wf
	if len(w.Nodes) < 2 {
		return
	}
	const mw, mh = 168, 108
	x0, y0, x1, y1 := wfBounds(w)
	v := w.View
	vw, vh := float64(a.wfCanvas.w)/v.Zoom, float64(a.wfCanvas.h)/v.Zoom
	// The map shows the cards and the view.
	x0, y0 = math.Min(x0, v.X), math.Min(y0, v.Y)
	x1, y1 = math.Max(x1, v.X+vw), math.Max(y1, v.Y+vh)
	s := math.Min((mw-12)/(x1-x0), (mh-12)/(y1-y0))
	ox, oy := 6-x0*s, 6-y0*s
	m := ui.Box(c.Key("wf-minimap")).Absolute().Right(10).Bottom(10).Size(mw, mh).Radius(8).Background(tok(t).Bg).Border(1, tok(t).Line).
		Opacity(0.95).Label(i18n.T("Minimap"))
	m.HandleInput(func(ev ui.InputEvent) bool {
		center := func() {
			cx, cy := (float64(ev.X)-ox)/s, (float64(ev.Y)-oy)/s
			a.wf.View.X, a.wf.View.Y = cx-vw/2, cy-vh/2
		}
		switch ev.Kind {
		case ui.InputPointerDown:
			a.wfCanvas.gesture = wfGestureMinimap
			center()
			return true
		case ui.InputPointerMove:
			if a.wfCanvas.gesture == wfGestureMinimap {
				center()
				return true
			}
		case ui.InputPointerUp:
			if a.wfCanvas.gesture == wfGestureMinimap {
				a.wfCanvas.gesture = wfGestureNone
				return true
			}
		}
		return false
	})
	sel := a.wfSel
	m.Draw(func(p *ui.Painter, r ui.Rect) {
		for _, n := range w.Nodes {
			col := tok(t).G[5]
			if n.ID == sel {
				col = t.Accent
			}
			p.Fill(ui.Rect{X: r.X + float32(ox+n.X*s), Y: r.Y + float32(oy+n.Y*s), W: float32(wfCardW * s), H: float32(wfCardH * s)}, col, 2)
		}
		p.Stroke(ui.Rect{X: r.X + float32(ox+v.X*s), Y: r.Y + float32(oy+v.Y*s), W: float32(vw * s), H: float32(vh * s)}, t.Accent, 2, 1.5)
	})
}
