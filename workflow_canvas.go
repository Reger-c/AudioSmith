package main

// The workflow canvas: the steps as cards on a plane the user pans and
// zooms (the content goes through one Transform, the view's), the links
// as curves painted under them, a minimap, and every pointer and key
// gesture in one HandleInput: press a card to select and drag it, an
// output port to draw a link to an input port (or the other way), a link
// to select it, the background to pan; scroll pans, ⌘-scroll zooms;
// arrows move the selected step, Delete removes it.

import (
	"math"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
	"github.com/hehehai/AudioSmith/internal/workflow"
)

// Card size and port geometry, in content units (DIPs at 100 %).
const (
	wfCardW   = 224
	wfCardH   = 88
	wfPortR   = 6  // a port's radius
	wfPortHit = 14 // how near a port the pointer must be
	wfMoveBy  = 8  // an arrow's move (×5 with Shift)
)

// Gestures of the canvas.
const (
	wfGestureNone = iota
	wfGesturePan
	wfGestureMove
	wfGestureLink
	wfGestureMinimap
)

// wfCanvasState is the canvas's gesture in progress.
type wfCanvasState struct {
	w, h    float32 // the canvas's size at the last build
	fit     bool    // show every card once the canvas's size is known
	gesture int
	node    string  // the step being moved
	offX    float64 // where in it the pointer holds it
	offY    float64
	moved   bool
	lastX   float32 // the pointer, in the canvas (pan)
	lastY   float32
	// A link being drawn: from a port, to the pointer.
	from     string
	fromPort string
	fromIn   bool // drawn from an input port (back to an output)
	px, py   float64
}

// Kind icons.
var wfKindIcons = map[workflow.Kind]icons.Icon{
	workflow.KindStart:      icons.FileText,
	workflow.KindSource:     icons.Soundwave,
	workflow.KindSpeak:      icons.VolumeLoud,
	workflow.KindTranscribe: icons.Microphone3,
	workflow.KindTranslate:  icons.Global,
	workflow.KindConvert:    icons.TransferHorizontal,
	workflow.KindNormalize:  icons.Pulse2,
	workflow.KindCondition:  icons.BranchingPathsUp,
	workflow.KindExport:     icons.Export,
}

// wfKindColor is a kind's color, the same in light and dark.
func wfKindColor(k workflow.Kind) ui.Color {
	switch k {
	case workflow.KindStart, workflow.KindSource:
		return ui.RGB(0x64, 0x74, 0x8B)
	case workflow.KindSpeak, workflow.KindConvert:
		return ui.RGB(0x10, 0xB9, 0x81)
	case workflow.KindTranscribe, workflow.KindTranslate:
		return ui.RGB(0x8B, 0x5C, 0xF6)
	case workflow.KindNormalize:
		return ui.RGB(0x0E, 0xA5, 0xE9)
	case workflow.KindCondition:
		return ui.RGB(0xF5, 0x9E, 0x0B)
	case workflow.KindExport:
		return ui.RGB(0x3B, 0x82, 0xF6)
	}
	return ui.RGB(0x88, 0x88, 0x88)
}

// wfTypeColor is a value type's color (links, ports).
func wfTypeColor(t workflow.Type, fallback ui.Color) ui.Color {
	switch t {
	case workflow.TypeText:
		return ui.RGB(0x3B, 0x82, 0xF6)
	case workflow.TypeAudio:
		return ui.RGB(0x10, 0xB9, 0x81)
	case workflow.TypeTranscript:
		return ui.RGB(0x8B, 0x5C, 0xF6)
	}
	return fallback
}

// --- geometry (content units) ------------------------------------------------------------

// wfPortAt is where a port of n is.
func wfPortAt(n *workflow.Node, port string, input bool) (float64, float64) {
	if input {
		return n.X, n.Y + wfCardH/2
	}
	switch port {
	case workflow.PortYes:
		return n.X + wfCardW, n.Y + wfCardH*0.36
	case workflow.PortNo:
		return n.X + wfCardW, n.Y + wfCardH*0.74
	}
	return n.X + wfCardW, n.Y + wfCardH/2
}

// wfPorts lists a step's ports: inputs, then outputs.
func wfPorts(n *workflow.Node) (in, out []string) {
	spec, _ := workflow.SpecOf(n.Kind)
	for _, p := range spec.In {
		in = append(in, p.Name)
	}
	for _, p := range spec.Out {
		out = append(out, p.Name)
	}
	return in, out
}

// wfHitPort finds the port at x, y (the last card drawn wins).
func wfHitPort(w *workflow.Workflow, x, y, r float64) (node, port string, input, ok bool) {
	best := r * r
	for i := range w.Nodes {
		n := &w.Nodes[i]
		ins, outs := wfPorts(n)
		check := func(p string, isIn bool) {
			px, py := wfPortAt(n, p, isIn)
			if d := (px-x)*(px-x) + (py-y)*(py-y); d <= best {
				best, node, port, input, ok = d, n.ID, p, isIn, true
			}
		}
		for _, p := range ins {
			check(p, true)
		}
		for _, p := range outs {
			check(p, false)
		}
	}
	return
}

// wfHitNode finds the card at x, y (the topmost: the last drawn).
func wfHitNode(w *workflow.Workflow, x, y float64) string {
	for i := len(w.Nodes) - 1; i >= 0; i-- {
		n := &w.Nodes[i]
		if x >= n.X && x <= n.X+wfCardW && y >= n.Y && y <= n.Y+wfCardH {
			return n.ID
		}
	}
	return ""
}

// wfCurve is a link's curve from x0, y0 to x1, y1: its four points.
func wfCurve(x0, y0, x1, y1 float64) [4][2]float64 {
	d := math.Max(48, math.Abs(x1-x0)/2)
	return [4][2]float64{{x0, y0}, {x0 + d, y0}, {x1 - d, y1}, {x1, y1}}
}

func wfBezier(c [4][2]float64, t float64) (float64, float64) {
	u := 1 - t
	a, b, cc, d := u*u*u, 3*u*u*t, 3*u*t*t, t*t*t
	return a*c[0][0] + b*c[1][0] + cc*c[2][0] + d*c[3][0], a*c[0][1] + b*c[1][1] + cc*c[2][1] + d*c[3][1]
}

// wfEdgeCurve is a link's curve.
func wfEdgeCurve(w *workflow.Workflow, e workflow.Edge) ([4][2]float64, bool) {
	a, b := w.Node(e.From), w.Node(e.To)
	if a == nil || b == nil {
		return [4][2]float64{}, false
	}
	x0, y0 := wfPortAt(a, e.FromPort, false)
	x1, y1 := wfPortAt(b, e.ToPort, true)
	return wfCurve(x0, y0, x1, y1), true
}

// wfHitEdge finds the link nearest x, y within r.
func wfHitEdge(w *workflow.Workflow, x, y, r float64) string {
	best, id := r*r, ""
	for _, e := range w.Edges {
		c, ok := wfEdgeCurve(w, e)
		if !ok {
			continue
		}
		for i := 0; i <= 32; i++ {
			px, py := wfBezier(c, float64(i)/32)
			if d := (px-x)*(px-x) + (py-y)*(py-y); d < best {
				best, id = d, e.ID
			}
		}
	}
	return id
}

// wfBounds is the box around every card.
func wfBounds(w *workflow.Workflow) (x0, y0, x1, y1 float64) {
	if len(w.Nodes) == 0 {
		return 0, 0, wfCardW, wfCardH
	}
	x0, y0 = math.Inf(1), math.Inf(1)
	x1, y1 = math.Inf(-1), math.Inf(-1)
	for _, n := range w.Nodes {
		x0, y0 = math.Min(x0, n.X), math.Min(y0, n.Y)
		x1, y1 = math.Max(x1, n.X+wfCardW), math.Max(y1, n.Y+wfCardH)
	}
	return
}

// --- the view ---------------------------------------------------------------------------

// wfZoomAt zooms by f around the canvas point x, y.
func (a *app) wfZoomAt(f float64, x, y float32) {
	if a.wf == nil {
		return
	}
	v := &a.wf.View
	z := math.Min(math.Max(v.Zoom*f, workflow.MinZoom), workflow.MaxZoom)
	// The content point under x, y stays there.
	cx, cy := v.X+float64(x)/v.Zoom, v.Y+float64(y)/v.Zoom
	v.Zoom = z
	v.X, v.Y = cx-float64(x)/z, cy-float64(y)/z
}

// wfZoom zooms around the canvas's middle (toolbar, keys).
func (a *app) wfZoom(f float64) { a.wfZoomAt(f, a.wfCanvas.w/2, a.wfCanvas.h/2) }

// wfAllShown reports whether the view shows every card.
func (a *app) wfAllShown() bool {
	v := a.wf.View
	x0, y0, x1, y1 := wfBounds(a.wf)
	return x0 >= v.X && y0 >= v.Y && x1 <= v.X+float64(a.wfCanvas.w)/v.Zoom && y1 <= v.Y+float64(a.wfCanvas.h)/v.Zoom
}

// wfFit shows every card (once the canvas is laid out).
func (a *app) wfFit() {
	if a.wf == nil {
		return
	}
	if a.wfCanvas.w == 0 {
		a.wfCanvas.fit = true
		return
	}
	x0, y0, x1, y1 := wfBounds(a.wf)
	const pad = 40
	z := math.Min(float64(a.wfCanvas.w)/(x1-x0+2*pad), float64(a.wfCanvas.h)/(y1-y0+2*pad))
	z = math.Min(math.Max(z, workflow.MinZoom), 1.2)
	v := &a.wf.View
	v.Zoom = z
	v.X = (x0+x1)/2 - float64(a.wfCanvas.w)/2/z
	v.Y = (y0+y1)/2 - float64(a.wfCanvas.h)/2/z
}

// wfResetZoom shows the content at 100 %, keeping the middle.
func (a *app) wfResetZoom() {
	if a.wf != nil {
		a.wfZoom(1 / a.wf.View.Zoom)
	}
}

// --- input --------------------------------------------------------------------------------

// wfCanvasInput takes the canvas's pointer and keys.
func (a *app) wfCanvasInput(ev ui.InputEvent) bool {
	w := a.wf
	if w == nil {
		return false
	}
	cs := &a.wfCanvas
	v := &w.View
	cx, cy := v.X+float64(ev.X)/v.Zoom, v.Y+float64(ev.Y)/v.Zoom
	locked := a.wfLocked()
	switch ev.Kind {
	case ui.InputPointerDown:
		if ev.Button != 0 {
			return false
		}
		a.wfCanvasH.Focus()
		cs.moved = false
		if node, port, in, ok := wfHitPort(w, cx, cy, wfPortHit/math.Min(v.Zoom, 1)); ok && !locked {
			cs.gesture, cs.from, cs.fromPort, cs.fromIn, cs.px, cs.py = wfGestureLink, node, port, in, cx, cy
			return true
		}
		if id := wfHitNode(w, cx, cy); id != "" {
			n := w.Node(id)
			a.wfSel, a.wfSelEdge = id, ""
			a.wfPanel = wfPanelStep
			if !locked {
				cs.gesture, cs.node, cs.offX, cs.offY = wfGestureMove, id, cx-n.X, cy-n.Y
			}
			return true
		}
		if id := wfHitEdge(w, cx, cy, 6/v.Zoom+2); id != "" {
			a.wfSelEdge, a.wfSel = id, ""
			return true
		}
		a.wfSel, a.wfSelEdge = "", ""
		cs.gesture, cs.lastX, cs.lastY = wfGesturePan, ev.X, ev.Y
		return true
	case ui.InputPointerMove:
		switch cs.gesture {
		case wfGestureMove:
			if n := w.Node(cs.node); n != nil {
				x, y := math.Max(cx-cs.offX, 0), math.Max(cy-cs.offY, 0)
				if x != n.X || y != n.Y {
					n.X, n.Y = math.Round(x), math.Round(y)
					cs.moved = true
				}
			}
		case wfGestureLink:
			cs.px, cs.py = cx, cy
		case wfGesturePan:
			v.X -= float64(ev.X-cs.lastX) / v.Zoom
			v.Y -= float64(ev.Y-cs.lastY) / v.Zoom
			cs.lastX, cs.lastY = ev.X, ev.Y
		default:
			return false
		}
		return true
	case ui.InputPointerUp:
		g := cs.gesture
		cs.gesture = wfGestureNone
		switch g {
		case wfGestureMove:
			if cs.moved {
				a.wfEdit(func(*workflow.Workflow) {}) // save the new place
			}
		case wfGestureLink:
			node, port, in, ok := wfHitPort(w, cx, cy, wfPortHit/math.Min(v.Zoom, 1))
			if !ok {
				// Dropped on a card: its input (or its only output).
				if id := wfHitNode(w, cx, cy); id != "" && id != cs.from {
					ins, outs := wfPorts(w.Node(id))
					switch {
					case !cs.fromIn && len(ins) == 1:
						node, port, in, ok = id, ins[0], true, true
					case cs.fromIn && len(outs) == 1:
						node, port, in, ok = id, outs[0], false, true
					}
				}
			}
			if ok && in != cs.fromIn && node != cs.from {
				if cs.fromIn {
					a.wfConnect(node, port, cs.from, cs.fromPort)
				} else {
					a.wfConnect(cs.from, cs.fromPort, node, port)
				}
			}
			cs.from = ""
		case wfGestureNone:
			return false
		}
		return true
	case ui.InputScroll:
		if ev.Mods&ui.Cmd != 0 {
			a.wfZoomAt(math.Exp(-float64(ev.DY)*0.01), ev.X, ev.Y)
		} else {
			v.X += float64(ev.DX) / v.Zoom
			v.Y += float64(ev.DY) / v.Zoom
		}
		return true
	case ui.InputKeyDown:
		return a.wfCanvasKey(ev.Mods, ev.Key)
	}
	return false
}

// wfCanvasKey takes a key pressed on the canvas.
func (a *app) wfCanvasKey(mods ui.Modifiers, key ui.Key) bool {
	step := float64(wfMoveBy)
	if mods&ui.Shift != 0 {
		step *= 5
	}
	move := func(dx, dy float64) bool {
		if a.wfSel == "" || a.wfLocked() {
			return false
		}
		id := a.wfSel
		a.wfEdit(func(w *workflow.Workflow) {
			if n := w.Node(id); n != nil {
				n.X, n.Y = math.Max(n.X+dx, 0), math.Max(n.Y+dy, 0)
			}
		})
		return true
	}
	if mods&ui.Cmd != 0 {
		switch key {
		case ui.KeyEqual:
			a.wfZoom(1.2)
			return true
		case ui.KeyMinus:
			a.wfZoom(1 / 1.2)
			return true
		case ui.Key0:
			a.wfResetZoom()
			return true
		}
		return false
	}
	switch key {
	case ui.KeyLeft:
		return move(-step, 0)
	case ui.KeyRight:
		return move(step, 0)
	case ui.KeyUp:
		return move(0, -step)
	case ui.KeyDown:
		return move(0, step)
	case ui.KeyDelete, ui.KeyBackspace:
		if a.wfSel == "" && a.wfSelEdge == "" {
			return false
		}
		a.wfRemoveSelected()
		return true
	case ui.KeyEscape:
		if a.wfCanvas.gesture != wfGestureNone {
			a.wfCanvas.gesture, a.wfCanvas.from = wfGestureNone, ""
			return true
		}
		if a.wfSel != "" || a.wfSelEdge != "" {
			a.wfSel, a.wfSelEdge = "", ""
			return true
		}
	}
	return false
}

// --- drawing ------------------------------------------------------------------------------

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
	kc := wfKindColor(n.Kind)
	name := n.StepName(n.Kind.Label())
	card := ui.Column(c.Key("wf-node-"+n.ID)).Absolute().Left(float32(n.X)).Top(float32(n.Y)).Size(wfCardW, wfCardH).
		Padding(8, 12).Gap(3).Radius(10).Background(tok(t).Bg).Border(bw, border).Shadow(0, 1, 3, 0, ui.RGBA(0, 0, 0, 0.08)).
		Label(i18n.T("Step %s", name)).Role(ui.RoleGroup)
	card.Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Size(24, 24).Radius(6).Background(kc).Center().Shrink(0).Children(func() {
				ui.Icon(c, wfKindIcons[n.Kind].Linear).Size(15, 15).TextColor(ui.RGB(255, 255, 255))
			})
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
			col := wfKindColor(n.Kind)
			if n.ID == sel {
				col = t.Accent
			}
			p.Fill(ui.Rect{X: r.X + float32(ox+n.X*s), Y: r.Y + float32(oy+n.Y*s), W: float32(wfCardW * s), H: float32(wfCardH * s)}, col, 2)
		}
		p.Stroke(ui.Rect{X: r.X + float32(ox+v.X*s), Y: r.Y + float32(oy+v.Y*s), W: float32(vw * s), H: float32(vh * s)}, t.Accent, 2, 1.5)
	})
}
