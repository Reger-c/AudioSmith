package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// TestSettingRowsControlsBesideText: in every Settings page, a group
// row's controls (switches, pickers, buttons) never overlap its text, at
// the width the user looked at Software updates (about 1000 wide,
// Chinese) and at the smallest window at 125 %: the row of controls stays
// beside the text column or wraps under it, and nothing labelled but the
// row's own title and note reaches into the text column.
func TestSettingRowsControlsBesideText(t *testing.T) {
	t.Cleanup(func() { i18n.SetLocale("en"); groupRowLaid = nil })
	type row struct {
		text, ctrl ui.Rect
		own        []ui.Rect
	}
	rows := map[string]row{}
	groupRowLaid = func(key string, text, ctrl ui.Element, texts [2]ui.Element) {
		r := row{text: text.Bounds(), ctrl: ctrl.Bounds()}
		for _, e := range texts {
			if e.Valid() {
				r.own = append(r.own, e.Bounds())
			}
		}
		rows[key] = r
	}
	for _, run := range []struct {
		lang  string
		w     int
		scale float64
	}{{"zh-Hans", 1000, 1}, {"en", 1000, 1}, {"zh-Hans", 820, 1.25}, {"en", 820, 1.25}} {
		i18n.SetLocale("en")
		// A copy that cannot update itself (built on the Mac), as the
		// user's: its update switches are off and disabled.
		e := newUpdatesApp(t, "0.4.0-beta.6", false)
		e.a.updateSettings(func(s *settings.Settings) {
			if run.lang != "en" {
				s.General.Language = run.lang
			}
			s.Appearance.Scale = run.scale
		})
		e.tt.SetSize(run.w, 4000)
		// The page under the dialog: its labels are not the rows'.
		e.a.navigate("/studio")
		e.tt.Frame()
		e.tt.Frame()
		under := map[ui.Rect]bool{}
		for _, s := range e.tt.Texts() {
			if r, ok := e.tt.Find(s); ok {
				under[r] = true
			}
		}
		pages := 0
		for _, s := range settingsSections() {
			e.a.navigate("/settings/" + s.id)
			clear(rows)
			e.tt.Frame()
			e.tt.Frame()
			e.tt.Frame()
			if len(rows) == 0 {
				continue
			}
			pages++
			var labels []ui.Rect
			for _, s := range e.tt.Texts() {
				if r, ok := e.tt.Find(s); ok && !under[r] {
					labels = append(labels, r)
				}
			}
			for key, r := range rows {
				if r.text.W <= 0 {
					continue // not laid out (a folded part)
				}
				if overlaps(r.text, r.ctrl) {
					t.Errorf("%s %d@%v %s: row %q: controls %v over the text %v", run.lang, run.w, run.scale, s.id, key, r.ctrl, r.text)
				}
				for _, l := range labels {
					// A control spilling out of the row of controls.
					if l.Y < r.ctrl.Y+r.ctrl.H && l.Y+l.H > r.ctrl.Y && l.X >= r.ctrl.X-0.5 && l.X <= r.ctrl.X+r.ctrl.W && !inside(l, r.ctrl) && !spans(l, r.text) {
						t.Errorf("%s %d@%v %s: row %q: %v out of its row of controls %v", run.lang, run.w, run.scale, s.id, key, l, r.ctrl)
					}
				}
			labels:
				for _, l := range labels {
					if !overlaps(l, r.text) || spans(l, r.text) {
						continue
					}
					for _, o := range r.own {
						if near(l, o) {
							continue labels
						}
					}
					t.Errorf("%s %d@%v %s: row %q: %v reaches into the text %v", run.lang, run.w, run.scale, s.id, key, l, r.text)
				}
			}
		}
		if pages < 8 {
			t.Errorf("%s %d@%v: group rows on %d pages only", run.lang, run.w, run.scale, pages)
		}
	}
}

// overlaps tells whether two rectangles share more than an edge (half a
// DIP of slack for rounding).
func overlaps(a, b ui.Rect) bool {
	const e = 0.5
	return a.X+a.W > b.X+e && b.X+b.W > a.X+e && a.Y+a.H > b.Y+e && b.Y+b.H > a.Y+e
}

// inside tells whether a lies within b (half a DIP of slack).
func inside(a, b ui.Rect) bool {
	const e = 0.5
	return a.X >= b.X-e && a.Y >= b.Y-e && a.X+a.W <= b.X+b.W+e && a.Y+a.H <= b.Y+b.H+e
}

// near tells whether two rectangles are the same, but for rounding.
func near(a, b ui.Rect) bool {
	d := func(x, y float32) bool { return x-y < 0.5 && y-x < 0.5 }
	return d(a.X, b.X) && d(a.Y, b.Y) && d(a.W, b.W) && d(a.H, b.H)
}

// spans tells whether a reaches across b from side to side: a labelled
// container (the dialog, its scroll area) that holds b.
func spans(a, b ui.Rect) bool { return a.X <= b.X && a.X+a.W >= b.X+b.W }
