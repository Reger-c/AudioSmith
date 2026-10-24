package main

// Painting the timeline lane and its overview strip (timeline.go).

import (
	"github.com/egoist/mygo/ui"
)

// timelinePaint paints the lane in r.
func timelinePaint(p *ui.Painter, r ui.Rect, k *tokens, s *timelineState, m tlModel) {
	d := m.Duration
	s.width = r.W
	s.zoom = min(s.zoom, s.maxZoom(d))
	s.clampOffset(d)
	span := s.span(d)
	accent, danger, muted := k.Accent, k.Danger, k.Text3
	border, text := k.Line, k.Text
	x := func(sec float64) float32 { return r.X + float32((sec-s.offset)/span)*r.W }
	// Grid: tenths of the view.
	for i := 1; i < 10; i++ {
		gx := r.X + r.W*float32(i)/10
		p.Line(gx, r.Y, gx, r.Y+r.H, 1, border.Alpha(0.5))
	}
	// The waveform: a bar every 2 DIPs from the pyramid's level for the
	// zoom, kept while the view stays.
	if m.Peaks != nil {
		n := max(int(r.W/2), 1)
		bars := s.bars.get(m.Peaks, s.offset, s.offset+span, n)
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
	// Blocks, then their labels, the ones that have room.
	var lx, lw, lroom []float32
	var lbl []string
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
		if b.Label != "" {
			// A block cut by the lane's edge keeps its number in the part that shows.
			vx0, vx1 := max(bx, r.X), min(bx+bw, r.X+r.W)
			lx, lw, lroom, lbl = append(lx, vx0+3), append(lw, float32(len(b.Label)*tlLabelCharPx)), append(lroom, vx1-vx0), append(lbl, b.Label)
		}
	}
	for i, ok := range tlLabels(lx, lw, lroom) {
		if ok {
			p.Text(lx[i], r.Y+12, lbl[i], 10, text.Alpha(0.8))
		}
	}
	// The playhead.
	if m.Playhead >= s.offset && m.Playhead <= s.offset+span {
		px := x(m.Playhead)
		p.Line(px, r.Y, px, r.Y+r.H, 2, accent)
	}
}

// overviewPaint paints the overview strip: the whole source's waveform,
// the lines, the window the lane shows and the playhead.
func overviewPaint(p *ui.Painter, r ui.Rect, k *tokens, s *timelineState, m tlModel) {
	d := m.Duration
	s.ovW = r.W
	x := func(sec float64) float32 { return r.X + float32(sec/d)*r.W }
	if m.Peaks != nil {
		n := max(int(r.W/2), 1)
		mid, w := r.Y+r.H/2, r.W/float32(n)
		for i, v := range s.ovBars.get(m.Peaks, 0, d, n) {
			if h := v * (r.H - 4) / 2; h >= 0.5 {
				p.Fill(ui.Rect{X: r.X + float32(i)*w, Y: mid - h, W: max(w-0.5, 0.5), H: 2 * h}, k.Text.Alpha(0.18), 0)
			}
		}
	}
	for _, b := range m.Blocks {
		col := k.Text.Alpha(0.35)
		if b.ID == s.sel {
			col = k.Accent
		}
		p.Fill(ui.Rect{X: x(b.Start), Y: r.Y + r.H - 4, W: max(x(b.End)-x(b.Start), 1), H: 2}, col, 0)
	}
	win := ui.Rect{X: x(s.offset), Y: r.Y + 1, W: max(x(s.offset+s.span(d))-x(s.offset), 4), H: r.H - 2}
	p.Fill(win, k.Accent.Alpha(0.16), 3)
	p.Stroke(win, k.Accent, 3, 1.5)
	if m.Playhead >= 0 {
		px := x(m.Playhead)
		p.Line(px, r.Y, px, r.Y+r.H, 1.5, k.Accent)
	}
}
