package main

// The model picker the creation pages share (Studio, Stories, Audiobook,
// Dub, Design): the chosen model with its one-line "best for"; a list
// with the page's recommended models first (the catalogue's presets for
// the interface's language, thread #41), each with a speed and a quality
// hint, and the others behind "Show all models", where the ones that
// cannot do the page's job are greyed out with the reason. A retired
// model (still running for old projects) says what replaces it.

import (
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
)

// modelPickerState is the pickers' view state, by picker key.
type modelPickerState struct {
	pickerOpen map[string]bool
	pickerAll  map[string]bool // "Show all models" open (remembered while the app runs)
}

// pickerOpts tunes a picker.
type pickerOpts struct {
	Locked bool
	Custom bool // offer a custom Hugging Face repo (the Studio)
	// Only, when set, is the list (Design: the models that design);
	// otherwise every speech model is offered.
	Only []catalog.Entry
}

// whyNotFor says why model e cannot do workflow w ("" = it can).
func whyNotFor(e catalog.Entry, w catalog.Workflow) string {
	switch w {
	case catalog.WorkflowDub:
		if !e.Has(catalog.CapClone) {
			return i18n.T("Can't clone: the dub won't sound like the speakers")
		}
	case catalog.WorkflowDesign:
		if !e.Has(catalog.CapDesign) {
			return i18n.T("Can't design voices from a description")
		}
	case catalog.WorkflowStory, catalog.WorkflowAudiobook:
		if e.NeedsReference {
			return i18n.T("Only clones: every voice must be a cloned one")
		}
	}
	return ""
}

// englishOnly reports whether e speaks only English (Kokoro without its
// hidden Chinese voices, Pocket, Soprano…), for a hint in other languages.
func englishOnly(e catalog.Entry) bool {
	p, _ := catalog.PresetFor(e.ID)
	langs := 0
	for _, l := range e.Languages {
		base := catalog.BaseLanguage(l.Code)
		if slices.Contains(p.HideLanguages, base) {
			continue
		}
		if base != "en" {
			return false
		}
		langs++
	}
	return langs > 0 || len(p.Languages) == 1 && p.Languages[0] == "en"
}

// ratingDots is a 1–3 hint as dots: ●●○.
func ratingDots(n int) string {
	n = min(max(n, 0), 3)
	return strings.Repeat("●", n) + strings.Repeat("○", 3-n)
}

// bestFor is model e's one line: its preset's "best for", else nothing.
func bestFor(e catalog.Entry) string {
	if p, ok := catalog.PresetFor(e.ID); ok && p.BestFor != "" {
		return i18n.T(p.BestFor)
	}
	return ""
}

// modelPicker is the picker for workflow w; set receives the model chosen
// (or the replacement of a retired one). key must be unique on the page.
func (a *app) modelPicker(c *ui.Context, key string, w catalog.Workflow, cur catalog.Entry, set func(catalog.Entry), o pickerOpts) {
	t := c.Theme()
	k := tok(t)
	if a.pickerOpen == nil {
		a.pickerOpen, a.pickerAll = map[string]bool{}, map[string]bool{}
	}
	lang := i18n.Locale()
	recs := catalog.Builtin().Recommended(w, lang)
	if o.Only != nil {
		recs = slices.DeleteFunc(recs, func(e catalog.Entry) bool {
			return !slices.ContainsFunc(o.Only, func(x catalog.Entry) bool { return x.ID == e.ID })
		})
	}
	isRec := func(e catalog.Entry) bool {
		return slices.ContainsFunc(recs, func(x catalog.Entry) bool { return x.ID == e.ID })
	}
	label := cur.Label
	if cur.ID == "custom" {
		label = i18n.T("Custom Hugging Face repo…")
	}
	btn := ui.ButtonBase(c.Key(key)).Padding(px(t, 6), px(t, 10)).Gap(px(t, sp2)).Radius(px(t, radiusControl)).
		Background(t.Surface).Border(1, t.Border).AlignItems(ui.Center).Label(i18n.T("Model")).Tooltip(label).Disabled(o.Locked)
	if btn.Hovered() && !o.Locked {
		btn.Background(t.SurfaceHover)
	}
	btn.Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(px(t, 1)).Children(func() {
			ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
				ui.Text(c, label).SingleLine().Ellipsis("…").MinWidth(0).Shrink(1)
				if isRec(cur) {
					ui.Text(c, i18n.T("Recommended")).FontSize(px(t, fsMicro)).TextColor(k.AccentInk).Padding(px(t, 1), px(t, 5)).
						Radius(px(t, 4)).Background(k.AccentSoft).SingleLine().Shrink(0)
				}
			})
			if b := bestFor(cur); b != "" && cur.Retired == nil {
				ui.Text(c, b).FontSize(px(t, fsMicro)).TextColor(k.Text2).SingleLine().Ellipsis("…").MinWidth(0)
			}
		})
		ui.Icon(c, icons.AltArrowDown.Linear).Size(px(t, 14), px(t, 14)).TextColor(k.Text2).Shrink(0)
	})
	if btn.Clicked() {
		a.pickerOpen[key] = !a.pickerOpen[key]
	}
	open := a.pickerOpen[key]
	ui.Popover(c, btn, &open, func() {
		a.pickerList(c, key, w, cur, recs, set, o)
	})
	a.pickerOpen[key] = a.pickerOpen[key] && open && !o.Locked // closed by a pick, outside, Escape
	if r, ok := catalog.Builtin().Replacement(cur); ok {
		ui.Row(c.Key(key + "-retired")).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
			iconMark(c, icons.DangerTriangle, t.Warning, "")
			caption(c, i18n.T("%s is retired: it misread too much in our tests. %s is better.", cur.Label, r.Label)).
				TextColor(t.Warning).Grow(1).MinWidth(0)
			if ghostButton(c.Key(key+"-switch"), i18n.T("Switch")).Disabled(o.Locked).Tooltip(i18n.T("Use %s", r.Label)).Clicked() {
				set(r)
			}
		})
	}
}

// pickerList is the picker's popover: recommended, then all the others.
func (a *app) pickerList(c *ui.Context, key string, w catalog.Workflow, cur catalog.Entry, recs []catalog.Entry, set func(catalog.Entry), o pickerOpts) {
	t := c.Theme()
	k := tok(t)
	pick := func(e catalog.Entry) {
		a.pickerOpen[key] = false
		if e.ID != cur.ID || e.ID == "custom" {
			set(e)
		}
	}
	others := o.Only
	if others == nil {
		others = catalog.Builtin().ForTask(catalog.TaskTTS)
	}
	others = slices.DeleteFunc(slices.Clone(others), func(e catalog.Entry) bool {
		return slices.ContainsFunc(recs, func(x catalog.Entry) bool { return x.ID == e.ID })
	})
	if o.Only == nil && w == catalog.WorkflowDesign {
		// Design lists only what designs.
		others = slices.DeleteFunc(others, func(e catalog.Entry) bool { return whyNotFor(e, w) != "" })
	}
	// The ones that can do the job first.
	slices.SortStableFunc(others, func(x, y catalog.Entry) int {
		xn, yn := whyNotFor(x, w) != "", whyNotFor(y, w) != ""
		switch {
		case xn == yn:
			return 0
		case yn:
			return -1
		}
		return 1
	})
	_, h := c.Size()
	ui.Scroll(c.Key(key + "-scroll")).MaxHeight(max(px(t, 220), h*0.7)).Children(func() {
		ui.Column(c.Key(key + "-list")).Width(px(t, 340)).Gap(px(t, 2)).Padding(px(t, 6)).Children(func() {
			if len(recs) > 0 {
				ui.Text(c, i18n.T("Recommended")).FontSize(px(t, fsMicro)).FontWeight(600).TextColor(k.Text3).Padding(px(t, 4), px(t, sp2), px(t, 2))
				for _, e := range recs {
					a.pickerRow(c, e, cur, bestFor(e), "", true, pick)
				}
			}
			if len(others) == 0 && !o.Custom {
				return
			}
			all := a.pickerAll[key] || len(recs) == 0
			if len(recs) > 0 {
				ui.Divider(c)
				label := i18n.T("Show all models (%d)", len(others))
				tg := ui.ButtonBase(c.Key(key+"-all")).Padding(px(t, 6), px(t, sp2)).Radius(px(t, radiusControl)).Gap(px(t, 6)).AlignItems(ui.Center).Label(label)
				if tg.Hovered() {
					tg.Background(k.Hover)
				}
				if tg.Clicked() {
					a.pickerAll[key] = !all
					all = !all
				}
				tg.Children(func() {
					ic := icons.AltArrowRight
					if all {
						ic = icons.AltArrowDown
					}
					ui.Icon(c, ic.Linear).Size(px(t, 14), px(t, 14)).TextColor(k.Text2)
					ui.Text(c, label).FontSize(px(t, fsCaption)).TextColor(k.Text2)
				})
			}
			if !all {
				return
			}
			for _, e := range others {
				why := whyNotFor(e, w)
				note := ""
				if why == "" && englishOnly(e) && catalog.BaseLanguage(i18n.Locale()) != "en" {
					note = i18n.T("English only")
				}
				a.pickerRow(c, e, cur, i18n.T(e.About), firstOf(why, note), why == "", pick)
			}
			if o.Custom {
				custom := catalog.Custom("")
				a.pickerRow(c, custom, cur, i18n.T("Any speech model on Hugging Face that the engine can load (type its repo)."), "", true, pick)
			}
		})
	})
}

// pickerRow is one model of the list: its name (and "non-commercial"),
// one line about it (or why it can't be used, or a note), the speed and
// quality hint of a preset, and on the right ✓ for the chosen one or the
// download size of one not downloaded.
func (a *app) pickerRow(c *ui.Context, e, cur catalog.Entry, about, note string, usable bool, pick func(catalog.Entry)) {
	t := c.Theme()
	k := tok(t)
	name := e.Label
	if e.ID == "custom" {
		name = i18n.T("Custom Hugging Face repo…")
	}
	chosen := e.ID == cur.ID && (e.ID != "custom" || cur.ID == "custom")
	b := ui.ButtonBase(c.Key("pm-"+e.ID)).Padding(px(t, 6), px(t, sp2)).Radius(px(t, radiusControl)).Gap(px(t, sp2)).
		AlignItems(ui.Center).Label(name).Disabled(!usable)
	switch {
	case chosen:
		b.Background(k.Pressed)
	case b.Hovered() && usable:
		b.Background(k.Hover)
	}
	if b.Clicked() && usable {
		pick(e)
	}
	b.Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(px(t, 1)).Children(func() {
			col := k.Text
			if !usable {
				col = k.Text3
			}
			ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
				ui.Text(c, name).TextColor(col).SingleLine().Ellipsis("…").MinWidth(0).Shrink(1)
				if !e.Licence.Commercial {
					ui.Text(c, i18n.T("non-commercial")).FontSize(px(t, fsMicro)).TextColor(t.Warning).SingleLine().Shrink(0)
				}
			})
			line, lineCol := about, k.Text2
			if note != "" {
				line = note
				if !usable {
					lineCol = k.Text2
				} else {
					lineCol = k.Text3
				}
			}
			if line != "" {
				ui.Text(c, line).FontSize(px(t, fsMicro)).TextColor(lineCol).MaxLines(2).Ellipsis("…").MinWidth(0)
			}
			if p, ok := catalog.PresetFor(e.ID); ok && usable {
				ui.Text(c, i18n.T("Speed %s · Quality %s", ratingDots(p.Speed), ratingDots(p.Quality))).FontSize(px(t, fsMicro)).TextColor(k.Text3).SingleLine()
			}
		})
		switch {
		case chosen:
			iconMark(c, icons.CheckCircle, k.AccentInk, i18n.T("Chosen"))
		case e.ID != "custom":
			if ready, known := a.modelOnDisk(e); known && !ready {
				ui.Text(c, catalog.HumanSize(e.Size)).FontSize(px(t, fsMicro)).TextColor(k.Text3).SingleLine().Shrink(0).
					Tooltip(i18n.T("Not downloaded yet"))
			}
		}
	})
}
