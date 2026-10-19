package main

// The timeline lane (M4): a source's waveform (a peaks pyramid), speech
// onsets as ticks, a playhead, and blocks — a dub's lines — to select,
// move and resize by dragging, with snapping to onsets, to the edges of
// neighbouring blocks and to the playhead (Alt turns it off), and to edit
// with the keyboard: ←/→ select, Enter enters precise mode, where ←/→
// nudge the start by 0.01 s (⌘: 0.1 s), ⇧ the end, ⌥ the whole block;
// S snaps the start to the nearest onset (⇧S the end), Delete deletes it.
// Overlapping blocks are red. Zoom 1–16×; scrolling pans.
//
// It is one element painted by Draw and driven by HandleInput: the
// pointer and keys reach it before the next frame, and a drag changes
// only the lane's state until the pointer lets go, which commits one
// change (one undo step).

import (
	"fmt"
	"math"
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
)

// Timeline limits.
const (
	tlMaxZoom   = 16
	tlSnapPx    = 8  // snapping distance
	tlEdgePx    = 5  // grab zone of a block's edge
	tlMinGrabPx = 16 // narrower blocks only select
	tlResizePx  = 24 // narrower blocks only move
)

// timelineState is a lane's view state.
type timelineState struct {
	zoom   float64 // 1 (fit) … 16
	offset float64 // seconds at the left edge
	sel    string  // the selected block
	edit   bool    // precise editing (Enter)
	drag   *tlDrag
	width  float32 // the lane's width as last drawn
	focus  ui.Handle
}

// tlDrag is a drag in progress.
type tlDrag struct {
	id         string
	mode       int // tlMove, tlStart, tlEnd, tlSeek
	x0         float32
	start, end float64 // as the drag started
	curS, curE float64 // where it is now
	moved      bool
}

const (
	tlMove = iota
	tlStart
	tlEnd
	tlSeek
)

// tlBlock is a block of the lane.
type tlBlock struct {
	ID         string
	Start, End float64
	Label      string // index number
	Overlap    bool
	Muted      bool
	Flag       bool // a problem (won't fit, verify)
}

// tlModel is what a lane shows.
type tlModel struct {
	Duration float64
	Blocks   []tlBlock // in time order
	Peaks    *dub.Peaks
	Onsets   []float64
	Playhead float64 // seconds; < 0 = none
}

// tlEvents are what a lane asks of its owner.
type tlEvents struct {
	Seek   func(t float64)
	Select func(id string)
	// Change moves a block (clamp: keep it between its neighbours).
	Change func(id string, start, end float64, clamp bool)
	Nudge  func(id, edge string, delta float64)
	Delete func(id string)
	Play   func(id string)
	// Command performs an Edit menu command (undo, redo) that came to
	// the focused lane, and reports whether it did.
	Command func(name string) bool
}

// span is the seconds the lane shows.
func (s *timelineState) span(d float64) float64 {
	return d / min(max(s.zoom, 1), tlMaxZoom)
}

// clampOffset keeps the view inside the source.
func (s *timelineState) clampOffset(d float64) {
	s.offset = min(max(s.offset, 0), max(d-s.span(d), 0))
}

// setZoom zooms around the time at.
func (s *timelineState) setZoom(z, at, d float64) {
	if d <= 0 {
		return
	}
	old := s.span(d)
	frac := 0.5
	if old > 0 {
		frac = (at - s.offset) / old
	}
	s.zoom = min(max(z, 1), tlMaxZoom)
	s.offset = at - frac*s.span(d)
	s.clampOffset(d)
}

// reveal scrolls so the block id is in view.
func (s *timelineState) reveal(m tlModel, id string) {
	for _, b := range m.Blocks {
		if b.ID == id {
			span := s.span(m.Duration)
			if b.Start < s.offset || b.End > s.offset+span {
				s.offset = b.Start - span*0.1
				s.clampOffset(m.Duration)
			}
		}
	}
}

// timelineLane draws a lane and handles its input.
func timelineLane(c *ui.Context, key string, s *timelineState, m tlModel, ev tlEvents) {
	t := c.Theme()
	d := m.Duration
	if d <= 0 {
		return
	}
	if s.zoom < 1 {
		s.zoom = 1
	}
	s.clampOffset(d)
	k := tok(t)
	ui.Row(c.Key(key + "-tools")).Gap(px(t, 2)).AlignItems(ui.Center).Shrink(0).Children(func() {
		// Zoom out, the zoom, zoom in, fit: the order of the Workflows canvas.
		if iconButton(c.Key("zoom-out"), icons.MagnifierZoomOut, i18n.T("Zoom out")).Disabled(s.zoom <= 1).Clicked() {
			s.setZoom(s.zoom/2, s.offset+s.span(d)/2, d)
		}
		ui.Text(c, strconv.Itoa(int(s.zoom))+"×").FontSize(px(t, fsCaption)).TextColor(k.Text2).Shrink(0).Padding(0, px(t, 4))
		if iconButton(c.Key("zoom-in"), icons.MagnifierZoomIn, i18n.T("Zoom in")).Disabled(s.zoom >= tlMaxZoom).Clicked() {
			at := s.offset + s.span(d)/2
			if m.Playhead >= 0 {
				at = m.Playhead
			}
			s.setZoom(s.zoom*2, at, d)
		}
		if iconButton(c.Key("zoom-fit"), icons.FullScreenSquare, i18n.T("Fit all")).Disabled(s.zoom <= 1).Clicked() {
			s.zoom, s.offset = 1, 0
		}
		ui.Spacer(c)
		if s.edit {
			tagChip(c, i18n.T("Precise editing: ←/→ nudge · ⇧ end · ⌥ move · S snap · Esc")).Shrink(1).MinWidth(0).Ellipsis("…")
		}
		ui.Text(c, tlClock(s.offset)+" – "+tlClock(min(s.offset+s.span(d), d))+" / "+tlClock(d)).Font(monoFont).FontSize(px(t, fsMicro)).TextColor(k.Text2).SingleLine().Shrink(0)
	})
	if s.zoom > 1 {
		// Panning.
		pos := s.offset
		if ui.Slider(c.Key(key+"-pan"), &pos, 0, max(d-s.span(d), 0.001)).Label(i18n.T("Scroll the timeline")).Changed() {
			s.offset = pos
			s.clampOffset(d)
		}
	}
	accent, danger, muted := k.Accent, k.Danger, k.Text3
	surface, border, text := k.Subtle, k.Line, k.Text
	lane := ui.Box(c.Key(key)).Height(px(t, 86)).Shrink(0).Radius(px(t, radiusControl)).Background(surface).Border(1, border).Focusable().Bind(&s.focus).
		Label(i18n.T("Timeline"))
	lane.Draw(func(p *ui.Painter, r ui.Rect) {
		s.width = r.W
		span := s.span(d)
		x := func(sec float64) float32 { return r.X + float32((sec-s.offset)/span)*r.W }
		// Grid: tenths of the view.
		for i := 1; i < 10; i++ {
			gx := r.X + r.W*float32(i)/10
			p.Line(gx, r.Y, gx, r.Y+r.H, 1, border.Alpha(0.5))
		}
		// The waveform.
		if m.Peaks != nil {
			n := max(int(r.W/2), 1)
			bars := m.Peaks.Bars(s.offset, s.offset+span, n)
			mid := r.Y + r.H/2
			w := r.W / float32(n)
			for i, v := range bars {
				h := v * (r.H - 8) / 2
				if h < 0.5 {
					continue
				}
				p.Fill(ui.Rect{X: r.X + float32(i)*w, Y: mid - h, W: max(w-0.5, 0.5), H: 2 * h}, text.Alpha(0.28), 0)
			}
		}
		// Onsets.
		for _, o := range m.Onsets {
			if o < s.offset || o > s.offset+span {
				continue
			}
			ox := x(o)
			p.Line(ox, r.Y+r.H-7, ox, r.Y+r.H-1, 1, muted)
		}
		// Blocks.
		for _, b := range m.Blocks {
			start, end := b.Start, b.End
			if s.drag != nil && s.drag.id == b.ID && s.drag.mode != tlSeek {
				start, end = s.drag.curS, s.drag.curE
			}
			if end < s.offset || start > s.offset+span {
				continue
			}
			bx, bw := x(start), max(x(end)-x(start), 1)
			// Lines are grey blocks; the chosen one is in the accent,
			// overlaps in the danger colour.
			col, fill := text, float32(0.08)
			if b.Muted {
				fill = 0.04
			}
			if b.ID == s.sel {
				col, fill = accent, 0.18
			}
			if b.Overlap {
				col, fill = danger, 0.16
			}
			rect := ui.Rect{X: bx, Y: r.Y + 10, W: bw, H: r.H - 20}
			p.Fill(rect, col.Alpha(fill), 3)
			if b.ID == s.sel {
				p.Stroke(rect, col, 3, 2)
				if s.edit {
					p.Line(bx, rect.Y-4, bx, rect.Y+rect.H+4, 2, col)
				}
			} else if b.Flag {
				p.Stroke(rect, k.Warning, 3, 1)
			} else {
				p.Stroke(rect, text.Alpha(0.14), 3, 1)
			}
			if bw >= 18 && b.Label != "" {
				p.Text(bx+3, rect.Y+2, b.Label, 10, text.Alpha(0.8))
			}
		}
		// The playhead.
		if m.Playhead >= s.offset && m.Playhead <= s.offset+span {
			px := x(m.Playhead)
			p.Line(px, r.Y, px, r.Y+r.H, 2, accent)
		}
	})
	lane.HandleInput(func(e ui.InputEvent) bool {
		return timelineInput(s, m, ev, e)
	})
}

// timelineInput handles the lane's pointer and keys.
func timelineInput(s *timelineState, m tlModel, ev tlEvents, e ui.InputEvent) bool {
	if e.Kind == ui.InputCommand {
		return ev.Command != nil && ev.Command(e.Text)
	}
	d := m.Duration
	if s.width <= 0 || d <= 0 {
		return false
	}
	span := s.span(d)
	toSec := func(x float32) float64 { return s.offset + float64(x/s.width)*span }
	pxPerSec := float64(s.width) / span
	switch e.Kind {
	case ui.InputPointerDown:
		if e.Button != 0 {
			return false
		}
		at := toSec(e.X)
		// The block under the pointer, its edges first.
		for i := len(m.Blocks) - 1; i >= 0; i-- {
			b := m.Blocks[i]
			bw := (b.End - b.Start) * pxPerSec
			x0 := float32((b.Start - s.offset) * pxPerSec)
			x1 := float32((b.End - s.offset) * pxPerSec)
			if e.X < x0-tlEdgePx || e.X > x1+tlEdgePx {
				continue
			}
			s.sel = b.ID
			if ev.Select != nil {
				ev.Select(b.ID)
			}
			if e.Clicks >= 2 && ev.Play != nil {
				ev.Play(b.ID)
				return true
			}
			mode := tlMove
			switch {
			case bw >= tlResizePx && math.Abs(float64(e.X-x0)) <= tlEdgePx:
				mode = tlStart
			case bw >= tlResizePx && math.Abs(float64(e.X-x1)) <= tlEdgePx:
				mode = tlEnd
			case bw < tlMinGrabPx || e.X < x0 || e.X > x1:
				return true
			}
			s.drag = &tlDrag{id: b.ID, mode: mode, x0: e.X, start: b.Start, end: b.End, curS: b.Start, curE: b.End}
			return true
		}
		s.drag = &tlDrag{mode: tlSeek}
		if ev.Seek != nil {
			ev.Seek(min(max(at, 0), d))
		}
		return true
	case ui.InputPointerMove:
		g := s.drag
		if g == nil || g.mode == tlSeek {
			return g != nil
		}
		dt := float64(e.X-g.x0) / pxPerSec
		if math.Abs(float64(e.X-g.x0)) > 2 {
			g.moved = true
		}
		snap := e.Mods&ui.Alt == 0
		switch g.mode {
		case tlMove:
			ns := min(max(g.start+dt, 0), d-(g.end-g.start))
			if snap {
				if t, ok := tlSnap(m, g.id, ns, pxPerSec); ok {
					ns = t
				} else if t, ok := tlSnap(m, g.id, ns+(g.end-g.start), pxPerSec); ok {
					ns = t - (g.end - g.start)
				}
			}
			g.curS, g.curE = ns, ns+(g.end-g.start)
		case tlStart:
			ns := min(max(g.start+dt, 0), g.end-dub.MinSegment)
			if snap {
				if t, ok := tlSnap(m, g.id, ns, pxPerSec); ok && t < g.end-dub.MinSegment {
					ns = t
				}
			}
			g.curS = ns
		case tlEnd:
			ne := max(min(g.end+dt, d), g.start+dub.MinSegment)
			if snap {
				if t, ok := tlSnap(m, g.id, ne, pxPerSec); ok && t > g.start+dub.MinSegment {
					ne = t
				}
			}
			g.curE = ne
		}
		return true
	case ui.InputPointerUp:
		g := s.drag
		s.drag = nil
		if g == nil || g.mode == tlSeek || !g.moved || ev.Change == nil {
			return g != nil
		}
		// Alt allows overlap; otherwise the block stays between its neighbours.
		ev.Change(g.id, g.curS, g.curE, e.Mods&ui.Alt == 0)
		return true
	case ui.InputScroll:
		if s.zoom > 1 {
			delta := float64(e.DX)
			if delta == 0 {
				delta = float64(e.DY)
			}
			s.offset += delta / pxPerSec
			s.clampOffset(d)
			return true
		}
	case ui.InputKeyDown:
		return timelineKey(s, m, ev, e)
	}
	return false
}

// timelineKey handles the lane's keys.
func timelineKey(s *timelineState, m tlModel, ev tlEvents, e ui.InputEvent) bool {
	i := -1
	for k, b := range m.Blocks {
		if b.ID == s.sel {
			i = k
		}
	}
	selectAt := func(k int) {
		if k >= 0 && k < len(m.Blocks) {
			s.sel = m.Blocks[k].ID
			s.reveal(m, s.sel)
			if ev.Select != nil {
				ev.Select(s.sel)
			}
		}
	}
	switch e.Key {
	case ui.KeyLeft, ui.KeyRight:
		dir := 1.0
		if e.Key == ui.KeyLeft {
			dir = -1
		}
		if !s.edit || i < 0 {
			if i < 0 {
				selectAt(0)
			} else {
				selectAt(i + int(dir))
			}
			return true
		}
		step := 0.01
		if e.Mods&ui.Cmd != 0 {
			step = 0.1
		}
		edge := "start"
		switch {
		case e.Mods&ui.Alt != 0:
			edge = "move"
		case e.Mods&ui.Shift != 0:
			edge = "end"
		}
		if ev.Nudge != nil {
			ev.Nudge(s.sel, edge, dir*step)
		}
		return true
	case ui.KeyEnter:
		if i >= 0 {
			s.edit = !s.edit
		}
		return true
	case ui.KeyEscape:
		if s.edit {
			s.edit = false
			return true
		}
	case ui.KeyS:
		if i < 0 || ev.Change == nil || len(m.Onsets) == 0 {
			return false
		}
		b := m.Blocks[i]
		if e.Mods&ui.Shift != 0 {
			if t, ok := tlNearest(m.Onsets, b.End); ok && t > b.Start+dub.MinSegment {
				ev.Change(b.ID, b.Start, t, true)
			}
		} else if t, ok := tlNearest(m.Onsets, b.Start); ok && t < b.End-dub.MinSegment {
			ev.Change(b.ID, t, b.End, true)
		}
		return true
	case ui.KeyDelete, ui.KeyBackspace:
		if i >= 0 && ev.Delete != nil {
			ev.Delete(s.sel)
			if i+1 < len(m.Blocks) {
				s.sel = m.Blocks[i+1].ID
			} else if i > 0 {
				s.sel = m.Blocks[i-1].ID
			}
			return true
		}
	case ui.KeyEqual:
		s.setZoom(s.zoom*2, s.offset+s.span(m.Duration)/2, m.Duration)
		return true
	case ui.KeyMinus:
		s.setZoom(s.zoom/2, s.offset+s.span(m.Duration)/2, m.Duration)
		return true
	}
	return false
}

// tlSnap finds what t snaps to (onsets, other blocks' edges, the
// playhead) within tlSnapPx.
func tlSnap(m tlModel, id string, t, pxPerSec float64) (float64, bool) {
	best, bestD := 0.0, float64(tlSnapPx)/pxPerSec
	found := false
	try := func(c float64) {
		if dd := math.Abs(c - t); dd <= bestD {
			best, bestD, found = c, dd, true
		}
	}
	for _, o := range m.Onsets {
		try(o)
	}
	for _, b := range m.Blocks {
		if b.ID != id {
			try(b.Start)
			try(b.End)
		}
	}
	if m.Playhead >= 0 {
		try(m.Playhead)
	}
	return best, found
}

// tlNearest is the onset nearest t (within 1 s).
func tlNearest(onsets []float64, t float64) (float64, bool) {
	best, bestD := 0.0, 1.0
	found := false
	for _, o := range onsets {
		if d := math.Abs(o - t); d < bestD {
			best, bestD, found = o, d, true
		}
	}
	return best, found
}

// tlClock is m:ss.s.
func tlClock(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	m := int(sec) / 60
	return fmt.Sprintf("%d:%04.1f", m, sec-float64(m*60))
}
