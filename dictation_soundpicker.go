package main

// The sound picker of Settings → Dictation → Sounds (dictation_settings.go):
// a pop-up button with the chosen sound that opens a panel of the ActNow
// family's sounds (actnowkit's sounds package): a search field, chips for
// the suggested sounds, all of them, each group and macOS's system
// sounds, chips of tags, the list (▶ plays a sound, clicking its name
// chooses and plays it, None is first among the suggested) and the
// volume. The panel stays open while the user tries sounds. The same
// picker as Pik's and the other family apps', in its own tokens (ui_tokens.go).

import (
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
	"github.com/hehehai/actnowkit/sounds"
)

// soundPickerSpec is what a sound picker shows and does.
type soundPickerSpec struct {
	Name      string // the button's accessible name ("Pick Sound")
	Event     sounds.Event
	Choice    string // the event's choice now
	Off       bool   // sounds are off: the button dims (the panel still opens)
	Volume    int    // percent
	Catalog   *sounds.Catalog
	System    []string                   // macOS's system sounds
	Label     func(choice string) string // a choice's name
	Choose    func(choice string)
	Preview   func(choice string)
	SetVolume func(percent int)
}

// soundPickerState is a picker's panel: open, the chip (pickSuggested,
// pickAll, pickSystem or a group's id), the tags and the search text.
type soundPickerState struct {
	open  bool
	view  string
	tags  []string
	query string
}

const (
	pickSuggested = ""
	pickAll       = "*all"
	pickSystem    = "*macos"
)

// soundPicker is the button and its panel.
func soundPicker(c *ui.Context, sp soundPickerSpec) {
	t := c.Theme()
	k := tok(t)
	label := sp.Label(sp.Choice)
	b := ui.ButtonBase(c.Key("btn")).Width(px(t, soundPickerW)).Height(px(t, 26)).Padding(0, px(t, 8)).Gap(px(t, 6)).AlignItems(ui.Center).
		Radius(px(t, radiusControl)).Background(k.Hover).Label(sp.Name).Value(label).Tooltip(label).Shrink(0)
	st := ui.Local(b, "picker", func() soundPickerState { return soundPickerState{} })
	if b.Hovered() {
		b.Background(k.Pressed)
	}
	if b.Clicked() {
		st.open = !st.open
	}
	b.Children(func() {
		v := ui.Text(c, label).SingleLine().Ellipsis("…").MinWidth(0).Grow(1)
		mark := k.Text2
		if sp.Off {
			v.TextColor(k.Text3)
			mark = k.Text3
		}
		ui.Icon(c, icons.AltArrowDown.Linear).Size(px(t, 13), px(t, 13)).TextColor(mark)
	})
	ui.PopoverBase(c.Key("panel"), b, &st.open, func(panel ui.Element) {
		panel.Width(px(t, 360)).Padding(px(t, 10)).Radius(px(t, radiusCard)).Background(k.Bg).Border(1, k.Line).
			Shadow(0, 8, 24, 0, ui.RGBA(0, 0, 0, 0.18)).Label(i18n.T("Sounds")).Children(func() {
			ui.Column(c).Gap(px(t, 8)).Children(func() { soundPanel(c, sp, st) })
		})
	})
}

// soundPanel is the panel's content.
func soundPanel(c *ui.Context, sp soundPickerSpec, st *soundPickerState) {
	t := c.Theme()
	k := tok(t)
	lang := i18n.Locale()
	cat := sp.Catalog
	ui.Row(c.Key("search")).Children(func() { // SearchField grows along a row
		searchField(c, "sound-q", &st.query, i18n.T("Search sounds"), 0).AutoFocus() // type to search at once
	})
	if cat == nil && st.view != pickSystem {
		st.view = pickSystem // a build without the family's sounds
	}
	// The views.
	ui.Row(c.Key("views")).Wrap().Gap(px(t, 4)).Children(func() {
		chip := func(key, name string, n int) {
			if soundChip(c.Key("view-"+key), name, n, st.view == key).Clicked() {
				st.view = key
			}
		}
		if cat != nil {
			chip(pickSuggested, i18n.T("Suggested"), -1)
			chip(pickAll, i18n.T("All"), len(cat.Sounds))
			for _, g := range cat.GroupCounts(sounds.Filter{}) {
				chip(g.ID, g.Name.In(lang), g.Count)
			}
		}
		if len(sp.System) > 0 {
			chip(pickSystem, i18n.T("macOS"), len(sp.System))
		}
	})
	if cat != nil && st.view != pickSystem {
		f := soundFilter(st)
		if tags := cat.TagCounts(f, true); len(tags) > 0 {
			ui.Row(c.Key("tags")).Wrap().Gap(px(t, 4)).Children(func() {
				for _, tg := range tags {
					on := slices.Contains(st.tags, tg.ID)
					if soundChip(c.Key("tag-"+tg.ID), tg.Name.In(lang), tg.Count, on).Clicked() {
						f.ToggleTag(tg.ID)
						st.tags = f.Tags
					}
				}
			})
		}
	}
	ui.Scroll(c.Key("list")).MaxHeight(px(t, 270)).Children(func() {
		ui.Column(c).Gap(px(t, 1)).Children(func() {
			rows := 0
			row := func(choice, name, meta string) {
				rows++
				soundRow(c, sp, choice, name, meta)
			}
			switch {
			case st.view == pickSystem:
				q := strings.ToLower(strings.TrimSpace(st.query))
				for _, n := range sp.System {
					if q == "" || strings.Contains(strings.ToLower(n), q) {
						row(sounds.System(n), n, "")
					}
				}
			default:
				f := soundFilter(st)
				var list []sounds.Sound
				if st.view == pickSuggested && strings.TrimSpace(st.query) == "" {
					if len(st.tags) == 0 {
						row(sounds.None, i18n.T("None"), "")
					}
					for _, s := range cat.Suggested(sp.Event) {
						if cat.Match(s, sounds.Filter{Tags: st.tags}) {
							list = append(list, s)
						}
					}
				} else {
					list = cat.Filter(f)
				}
				for _, s := range list {
					row(s.ID, s.Name.In(lang), soundMeta(cat, s, lang))
				}
			}
			if rows == 0 {
				ui.Text(c, i18n.T("No sounds match")).FontSize(px(t, fsCaption)).TextColor(k.Text3).Padding(px(t, 10), px(t, 8))
			}
		})
	})
	// The volume.
	ui.Row(c.Key("vol")).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
		ui.Text(c, i18n.T("Volume")).FontSize(px(t, fsCaption)).TextColor(k.Text2)
		v := float64(sp.Volume)
		if ui.Slider(c.Key("vol-slider"), &v, 0, 100).Step(5).Label(i18n.T("Volume")).Grow(1).Changed() {
			sp.SetVolume(int(v))
		}
		ui.Text(c, fmt.Sprintf("%d%%", sp.Volume)).FontSize(px(t, fsCaption)).TextColor(k.Text2).Width(px(t, 34)).TextAlign(ui.End)
	})
}

// soundFilter is the catalogue filter of the panel's state.
func soundFilter(st *soundPickerState) sounds.Filter {
	f := sounds.Filter{Tags: slices.Clone(st.tags), Query: st.query}
	if st.view != pickSuggested && st.view != pickAll && st.view != pickSystem {
		f.Group = st.view
	}
	return f
}

// soundMeta is a row's second line: how long the sound lasts and its
// first featured tags.
func soundMeta(cat *sounds.Catalog, s sounds.Sound, lang string) string {
	parts := []string{fmt.Sprintf("%.1f s", s.Length().Seconds())}
	n := 0
	for _, tg := range s.Tags {
		if i := slices.IndexFunc(cat.Tags, func(x sounds.Tag) bool { return x.ID == tg && x.Featured }); i >= 0 && n < 3 {
			parts = append(parts, cat.Tags[i].Name.In(lang))
			n++
		}
	}
	return strings.Join(parts, " · ")
}

// soundRow is one choice: ▶ plays it; its name chooses and plays it.
func soundRow(c *ui.Context, sp soundPickerSpec, choice, name, meta string) {
	t := c.Theme()
	k := tok(t)
	chosen := choice == sp.Choice
	ui.Row(c.Key("row-" + choice)).Gap(px(t, 4)).AlignItems(ui.Center).Children(func() {
		if choice != sounds.None {
			if soundPlayButton(c.Key("play"), i18n.T("Play %s", name)).Clicked() {
				sp.Preview(choice)
			}
		} else {
			ui.Box(c).Size(px(t, 26), px(t, 26)).Shrink(0)
		}
		r := ui.ButtonBase(c.Key("pick")).Grow(1).MinWidth(0).Padding(px(t, 4), px(t, 8)).Gap(px(t, 8)).AlignItems(ui.Center).
			Radius(px(t, radiusControl)).Label(name).Selected(chosen)
		switch {
		case chosen:
			r.Background(k.Pressed)
		case r.Hovered():
			r.Background(k.Hover)
		}
		if r.Clicked() {
			sp.Choose(choice)
			if choice != sounds.None {
				sp.Preview(choice)
			}
		}
		r.Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Children(func() {
				ui.Text(c, name).TextColor(k.Text).SingleLine().Ellipsis("…")
				if meta != "" {
					ui.Text(c, meta).FontSize(px(t, fsMicro)).TextColor(k.Text3).SingleLine().Ellipsis("…")
				}
			})
			if chosen {
				ui.Icon(c, icons.Check.Linear).Size(px(t, 14), px(t, 14)).TextColor(k.AccentInk)
			}
		})
	})
}

// soundChip is a filter chip: its name and how many sounds it has (n < 0:
// none shown).
func soundChip(c *ui.Context, name string, n int, on bool) ui.Element {
	t := c.Theme()
	k := tok(t)
	b := ui.ButtonBase(c).Height(px(t, 22)).Padding(0, px(t, 8)).Gap(px(t, 4)).AlignItems(ui.Center).
		Radius(px(t, 11)).Label(name).Selected(on).Shrink(0)
	fg, sub := k.Text2, k.Text3
	switch {
	case on:
		b.Background(k.AccentSoft)
		fg, sub = k.AccentInk, k.AccentInk
	case b.Hovered():
		b.Background(k.Pressed)
	default:
		b.Background(k.Hover)
	}
	b.Children(func() {
		ui.Text(c, name).FontSize(px(t, fsCaption)).TextColor(fg).SingleLine()
		if n >= 0 {
			ui.Text(c, fmt.Sprint(n)).FontSize(px(t, fsMicro)).TextColor(sub).SingleLine()
		}
	})
	return b
}

// soundPickerW is the picker button's width.
const soundPickerW = 176

// soundPlayButton is ▶ on a row of the panel: a ghost button.
func soundPlayButton(c *ui.Context, tip string) ui.Element {
	t := c.Theme()
	k := tok(t)
	b := ghostBase(c).Size(px(t, 26), px(t, 26)).Center().Shrink(0).Label(tip).Tooltip(tip)
	b.Children(func() {
		col := k.Text2
		if b.Hovered() {
			col = k.Text
		}
		ui.Icon(c, icons.Play.Linear).Size(px(t, 14), px(t, 14)).TextColor(col)
	})
	return b
}
