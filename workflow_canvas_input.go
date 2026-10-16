package main

// The workflow canvas's input: pointer gestures (select, drag, link, pan,
// zoom, the minimap) and keys; split from workflow_canvas.go.

import (
	"math"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/workflow"
)

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
