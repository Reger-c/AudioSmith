package main

// The workflow canvas: the steps as cards on a plane the user pans and
// zooms (the content goes through one Transform, the view's), the links
// as curves painted under them, a minimap, and every pointer and key
// gesture in one HandleInput: press a card to select and drag it, an
// output port to draw a link to an input port (or the other way), a link
// to select it, the background to pan; scroll pans, ⌘-scroll zooms;
// arrows move the selected step, Delete removes it.
//
// Files: workflow_canvas.go (this: state, geometry, zoom),
// workflow_canvas_input.go (gestures and keys), workflow_canvas_draw.go
// (the view and its drawing).

import (
	"math"

	"github.com/egoist/mygo/ui"
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

// wfKindTile is a step kind's icon on a small grey tile (size DIPs,
// unscaled: the canvas scales itself): the same for every kind, as the
// 0.3 design keeps colour for state (links and ports keep their type's).
func wfKindTile(c *ui.Context, k workflow.Kind, size, icon float32) {
	kt := tok(c.Theme())
	ui.Box(c).Size(size, size).Radius(size / 4).Background(kt.Hover).Center().Shrink(0).Children(func() {
		ui.Icon(c, wfKindIcons[k].Linear).Size(icon, icon).TextColor(kt.Text)
	})
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
