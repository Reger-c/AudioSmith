package main

// Adapted from VoxtNew's shell_logs_select.go (commit 21fe7ed7),
// Copyright 2026 ActNow and the SGLang-Omni Team, Apache License 2.0
// (see NOTICE).
//
// The logs view's lines, chosen as in Finder (actnowkit's listsel, the
// ActNow family's log-selection spec): a click copies its line, Shift-
// click selects from that line, ⌘-click adds or takes out one, dragging
// selects the lines it passes, ⌘A every line shown and Esc none. Copying
// clears the selection. The context menu (right-click, Control-click)
// copies the line, the selection or every line shown.
//
// The rows' column takes the pointer (HandleInput) and keeps its events
// for the next build, which reads where the rows were as the user acted
// (Bounds: the frame on screen) once it has built them.

import (
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/logs"
	"github.com/hehehai/actnowkit/listsel"
)

// logSelection is the logs view's selection, with the app's state.
type logSelection struct {
	m listsel.Model[uint64] // by core.LogEntry.Seq
	// events are the rows column's pointer events since the last build,
	// in its box's coordinates.
	events []logPointer
	// spans are where the rows were in the column, as built last, and
	// origin where the column was in the window.
	spans  []logSpan
	origin ui.Rect
	// scroll is the page's and the panel's scroll, which a drag near
	// their edges moves.
	scroll [2]ui.ScrollState
}

type logPointer struct {
	kind ui.InputKind
	x, y float32
	mods ui.Modifiers
}

type logSpan struct {
	seq         uint64
	top, bottom float32
}

// logSeqs are the keys of lines, in their order.
func logSeqs(lines []core.LogEntry) []uint64 {
	out := make([]uint64, len(lines))
	for i, e := range lines {
		out[i] = e.Seq
	}
	return out
}

// logsInput takes the rows column's input: the primary button's presses,
// moves and releases (the secondary one is the context menu's), and the
// Edit menu's Copy and Select All, which come as commands on macOS.
func (a *app) logsInput(svc ui.Services, lines []core.LogEntry) func(ui.InputEvent) bool {
	return func(ev ui.InputEvent) bool {
		s := &a.logSel
		switch ev.Kind {
		case ui.InputPointerDown, ui.InputPointerUp:
			if ev.Button != 0 {
				return false
			}
		case ui.InputPointerMove:
			if ev.Button != 0 {
				return false // the hover's moves (-1): the rows look at their own
			}
			if n := len(s.events); n > 0 && s.events[n-1].kind == ui.InputPointerMove {
				s.events[n-1].x, s.events[n-1].y = ev.X, ev.Y // the drag only needs the last
				return true
			}
		case ui.InputCommand:
			switch {
			case ev.Text == "copy" && !s.m.Empty():
				a.copyLogLines(svc, listsel.Items(&s.m, lines, entrySeq))
				return true
			case ev.Text == "selectAll" && len(lines) > 0:
				s.m.SelectAll(logSeqs(lines))
				return true
			}
			return false
		default:
			return false
		}
		s.events = append(s.events, logPointer{kind: ev.Kind, x: ev.X, y: ev.Y, mods: ev.Mods})
		return true
	}
}

func entrySeq(e core.LogEntry) uint64 { return e.Seq }

// logsResolve runs the pointer's events against the rows just built
// (s.spans) and reports whether the selection may have changed: the view
// then builds once more to show it. col is the rows column, for where the
// pointer is now while it presses (the log scrolled under it).
func (a *app) logsResolve(c *ui.Context, col ui.Element, lines []core.LogEntry) bool {
	s := &a.logSel
	if len(s.events) == 0 && !s.m.Pressed() {
		return false
	}
	order := logSeqs(lines)
	changed := false
	for _, ev := range s.events {
		switch ev.kind {
		case ui.InputPointerDown:
			seq, ok := s.hit(ev.y, false)
			if !ok {
				// Under the last line: nothing to copy.
				changed = changed || !s.m.Empty()
				s.m.Clear()
				continue
			}
			var mods listsel.Mods
			if ev.mods&ui.Shift != 0 {
				mods |= listsel.Extend
			}
			if ev.mods&ui.Cmd != 0 {
				mods |= listsel.Toggle
			}
			s.m.Press(seq, ev.x, ev.y, mods)
		case ui.InputPointerMove:
			seq, ok := s.hit(ev.y, true)
			changed = s.m.Move(ev.x, ev.y, seq, ok, order) || changed
		case ui.InputPointerUp:
			if !s.m.Pressed() {
				continue
			}
			seq, ok := s.hit(ev.y, true)
			s.m.Move(ev.x, ev.y, seq, ok, order) // a flick within one frame
			in, k := s.m.Release(order)
			changed = true
			if in == listsel.CopyRow {
				for _, e := range lines {
					if e.Seq == k {
						a.copyLogLines(c.Services(), []core.LogEntry{e})
					}
				}
			}
		}
	}
	s.events = s.events[:0]
	if s.m.Dragging() {
		// The log may have scrolled under a pointer at rest.
		x, y, _ := col.PointerPosition()
		seq, ok := s.hit(y, true)
		changed = s.m.Move(x, y, seq, ok, order) || changed
	}
	return changed
}

// hit returns the row at y in the column (a row takes the gap above it).
// Above the first row and under the last, clamp gives those (a drag's
// rows); else there is none.
func (s *logSelection) hit(y float32, clamp bool) (uint64, bool) {
	n := len(s.spans)
	if n == 0 {
		return 0, false
	}
	if y < s.spans[0].top {
		return s.spans[0].seq, clamp
	}
	for _, sp := range s.spans {
		if y < sp.bottom {
			return sp.seq, true
		}
	}
	return s.spans[n-1].seq, clamp
}

// logsAutoScroll scrolls st while a drag holds the pointer within 24 DIPs
// of the edges of view (or past them), faster farther on, and reports
// whether it scrolled: the next frames go on, and the drag's rows follow
// the pointer (logsResolve).
func logsAutoScroll(t *ui.Theme, col ui.Element, view ui.Rect, st *ui.ScrollState) bool {
	if view.H <= 0 {
		return false
	}
	_, y, _ := col.PointerPosition()
	y += col.Bounds().Y // in the window
	edge := px(t, 24)
	var dy float32
	switch {
	case y < view.Y+edge:
		dy = y - (view.Y + edge)
	case y > view.Y+view.H-edge:
		dy = y - (view.Y + view.H - edge)
	default:
		return false
	}
	step := min(max(abs32(dy)/3, 2), 24)
	if dy < 0 {
		step = -step
	}
	to := min(max(st.Y+step, 0), st.MaxY)
	if to == st.Y {
		return false
	}
	st.Y = to
	return true
}

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// copyLogLines copies lines (as the log file has them), says how many,
// and clears the selection.
func (a *app) copyLogLines(svc ui.Services, lines []core.LogEntry) {
	if len(lines) == 0 {
		return
	}
	svc.WriteClipboard(listsel.Text(lines, logs.Format))
	a.toast(i18n.N(len(lines), "Copied %d line", "Copied %d lines", len(lines)))
	a.logSel.m.Clear()
}

// logsKeys gives the rows column ⌘A, ⌘C and Esc while it has the focus
// (on macOS the Edit menu takes ⌘A and ⌘C: logsInput).
func (a *app) logsKeys(c *ui.Context, col ui.Element, lines []core.LogEntry) {
	s := &a.logSel
	if col.Shortcut(ui.Cmd, ui.KeyA) && len(lines) > 0 {
		s.m.SelectAll(logSeqs(lines))
	}
	if col.Shortcut(ui.Cmd, ui.KeyC) && !s.m.Empty() {
		a.copyLogLines(c.Services(), listsel.Items(&s.m, lines, entrySeq))
	}
	// Esc only while there is something to give up: else it is the
	// window's (a dialog, the palette).
	if (s.m.Pressed() || !s.m.Empty()) && col.Shortcut(0, ui.KeyEscape) {
		if !s.m.Cancel() {
			s.m.Clear()
		}
		s.events = s.events[:0]
	}
}

// logRowMenu is a row's context menu: this line, the lines selected, every
// line shown.
func (a *app) logRowMenu(c *ui.Context, row ui.Element, e core.LogEntry, lines []core.LogEntry) {
	svc := c.Services()
	row.ContextMenu(func(m *ui.Menu) {
		s := &a.logSel
		if m.Item(i18n.T("Copy this line")).Chosen() {
			a.copyLogLines(svc, []core.LogEntry{e})
		}
		if n := s.m.Count(); n > 0 {
			if m.Item(i18n.N(n, "Copy %d selected line", "Copy %d selected lines", n)).Shortcut(ui.Cmd, ui.KeyC).Chosen() {
				a.copyLogLines(svc, listsel.Items(&s.m, lines, entrySeq))
			}
		}
		if m.Item(i18n.T("Copy all lines shown")).Chosen() {
			a.copyLogLines(svc, lines)
		}
	})
}
