package main

// The shared kit of the 0.3 design (research/ui-design-spec.md §3–§4,
// docs/architecture.md §3.4): buttons by rank, the "⋯" overflow menu,
// text marks and chips, the page header, flat cards, collapsible
// sections, form rows, search and filter bars, empty states, notes on
// the page, and side panels that become drawers when the page is
// narrow. Pages build from these rather than styling MyGo's widgets one
// by one.
//
// Ranks: one primaryButton per view (the core action, in ink); ui.Button
// for the other actions that need a label (secondary: grey face); ghost
// buttons for quiet actions in toolbars and lists; iconButton, with its
// tooltip, wherever an icon says enough; and the rest in moreMenu. The
// round ink playButton plays a take or a clip; playToggle is the small
// play icon of a row in a list.
//
// Notes: banner is a line of state on the subtle fill with any icon;
// notice is a banner by kind (info, done, warning, error) with an ⓘ for
// more and an × to dismiss it.

import (
	"math"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
)

// ---- Buttons by rank ----

// labelIcon is the size of an icon beside a button's label: no taller than
// the label's line (13 pt is a 15.3 DIP line on the Mac), so a button with
// an icon stands as tall as one without.
func labelIcon(t *ui.Theme) float32 { return t.FontSize * 1.15 }

// primaryButton is the view's one core action (Generate, Export, Start):
// MyGo's primary button in ink, near-black (near-white in the dark).
//
//	if primaryButton(c.Key("generate"), i18n.T("Generate")).Disabled(!ok).Clicked() { … }
func primaryButton(c *ui.Context, label string) ui.Element {
	t := c.Theme()
	c.SetTheme(inkTheme(t))
	b := ui.PrimaryButton(c, label).Border(1, ui.Color{}) // as tall as ui.Button, which has a hairline
	c.SetTheme(t)
	return b
}

// primaryIconButton is primaryButton with an icon before its label.
func primaryIconButton(c *ui.Context, ic icons.Icon, label string) ui.Element {
	t := c.Theme()
	c.SetTheme(inkTheme(t))
	b := ui.PrimaryButton(c, "").Border(1, ui.Color{}) // as tall as ui.Button
	b.Children(func() {
		ui.Icon(c, ic.Linear).Size(labelIcon(t), labelIcon(t))
		ui.Text(c, label).SingleLine()
	})
	c.SetTheme(t)
	return b
}

// recordButton is a record or destructive action (Record, Stop
// recording, Erase…): MyGo's primary button in the red-orange.
func recordButton(c *ui.Context, label string) ui.Element {
	t := c.Theme()
	k := tok(t)
	rec := *t
	rec.Accent, rec.AccentText = k.Record, ui.Hex("#ffffff")
	rec.AccentHover, rec.AccentPressed = k.Record.Mix(ui.Hex("#000000"), 0.1), k.Record.Mix(ui.Hex("#000000"), 0.2)
	c.SetTheme(&rec)
	b := ui.PrimaryButton(c, label).Border(1, ui.Color{}) // as tall as ui.Button
	c.SetTheme(t)
	return b
}

// secondaryIconButton is a secondary button (ui.Button's look) with an
// icon before its label.
func secondaryIconButton(c *ui.Context, ic icons.Icon, label string) ui.Element {
	t := c.Theme()
	b := ui.Button(c, "")
	b.Children(func() {
		ui.Icon(c, ic.Linear).Size(labelIcon(t), labelIcon(t))
		ui.Text(c, label).SingleLine()
	})
	return b
}

// ghostButton is a quiet action: no face until the pointer is on it.
func ghostButton(c *ui.Context, label string) ui.Element {
	t := c.Theme()
	b := ghostBase(c).Padding(t.Space(1.5), t.Space(2.5))
	b.Children(func() { ui.Text(c, label).SingleLine() })
	return b
}

// ghostIconButton is a ghost button with an icon before its label.
func ghostIconButton(c *ui.Context, ic icons.Icon, label string) ui.Element {
	t := c.Theme()
	b := ghostBase(c).Padding(t.Space(1.5), t.Space(2.5)).Gap(t.Space(1.5))
	b.Children(func() {
		ui.Icon(c, ic.Linear).Size(labelIcon(t), labelIcon(t)).TextColor(tok(t).Text2)
		ui.Text(c, label).SingleLine()
	})
	return b
}

// labelOrIcon is a ghost button with an icon and its label, or only the
// icon (with the label as its tooltip) when narrow.
func labelOrIcon(c *ui.Context, ic icons.Icon, label string, narrow bool) ui.Element {
	if narrow {
		return iconButton(c, ic, label)
	}
	return ghostIconButton(c, ic, label).Label(label)
}

// dropButton is a ghost button that opens a menu or a popover: its icon,
// its label and a chevron. Its label names it.
func dropButton(c *ui.Context, ic icons.Icon, label string) ui.Element {
	t := c.Theme()
	k := tok(t)
	b := ghostBase(c).Padding(t.Space(1.5), t.Space(2)).Gap(t.Space(1.5)).Label(label).MinWidth(0).Shrink(1)
	b.Children(func() {
		ui.Icon(c, ic.Linear).Size(labelIcon(t), labelIcon(t)).TextColor(k.Text2)
		ui.Text(c, label).SingleLine().Ellipsis("…").MinWidth(0)
		ui.Icon(c, icons.AltArrowDown.Linear).Size(px(t, 12), px(t, 12)).TextColor(k.Text3)
	})
	return b
}

// dropText is a ghost button that opens a menu: its label and a
// chevron, on the grey fill while on (a filter is set).
func dropText(c *ui.Context, label string, on bool) ui.Element {
	t := c.Theme()
	k := tok(t)
	b := ghostBase(c).Padding(px(t, 4), px(t, 6), px(t, 4), px(t, 10)).Gap(px(t, sp1)).AlignItems(ui.Center).Label(label).MinWidth(0).Shrink(1)
	if on {
		b.Background(k.Pressed)
	}
	b.Children(func() {
		col := k.Text2
		if on {
			col = k.Text
		}
		ui.Text(c, label).FontSize(px(t, fsCaption)).TextColor(col).SingleLine().Ellipsis("…").MinWidth(0)
		ui.Icon(c, icons.AltArrowDown.Linear).Size(px(t, 12), px(t, 12)).TextColor(k.Text3)
	})
	return b
}

// quietButton is an action in a row of dropText filters (Reset): the
// same size, type and colour as they are, so the row keeps its height
// when it appears.
func quietButton(c *ui.Context, ic icons.Icon, label string) ui.Element {
	t := c.Theme()
	k := tok(t)
	b := ghostBase(c).Padding(px(t, 4), px(t, 10), px(t, 4), px(t, 8)).Gap(px(t, sp1)).AlignItems(ui.Center).Label(label).Shrink(0)
	b.Children(func() {
		ui.Icon(c, ic.Linear).Size(px(t, 12), px(t, 12)).TextColor(k.Text3)
		ui.Text(c, label).FontSize(px(t, fsCaption)).TextColor(k.Text2).SingleLine()
	})
	return b
}

// ghostBase is the face of ghost and icon buttons.
func ghostBase(c *ui.Context) ui.Element {
	t := c.Theme()
	k := tok(t)
	// A clear hairline: ghost buttons stand as tall as ui.Button (which
	// has a visible one) and line up beside it.
	b := ui.ButtonBase(c).Radius(px(t, radiusControl)).TextColor(k.Text).Border(1, ui.Color{})
	switch {
	case b.Pressed():
		b.Background(k.Pressed)
	case b.Hovered():
		b.Background(k.Hover)
	}
	return b
}

// iconButton is a square ghost button showing only an icon; tip names it
// (for the tooltip, assistive technology and tests).
//
//	if iconButton(c.Key("close"), icons.Close, i18n.T("Close")).Clicked() { … }
func iconButton(c *ui.Context, ic icons.Icon, tip string) ui.Element {
	t := c.Theme()
	b := ghostBase(c).Size(px(t, 28), px(t, 28)).Center().Label(tip).Tooltip(tip)
	b.Children(func() {
		col := tok(t).Text2
		if b.Hovered() {
			col = tok(t).Text
		}
		ui.Icon(c, ic.Linear).Size(px(t, 16), px(t, 16)).TextColor(col)
	})
	return b
}

// iconToggle is an icon button for something on or off (Star, the
// inspector): Bold and in the pressed face while on.
func iconToggle(c *ui.Context, ic icons.Icon, on bool, tip string) ui.Element {
	t := c.Theme()
	k := tok(t)
	b := ghostBase(c).Size(px(t, 28), px(t, 28)).Center().Label(tip).Tooltip(tip).Checked(on)
	if on {
		b.Background(k.Pressed)
	}
	b.Children(func() {
		col := k.Text2
		if on || b.Hovered() {
			col = k.Text
		}
		ui.Icon(c, ic.Get(on)).Size(px(t, 16), px(t, 16)).TextColor(col)
	})
	return b
}

// panelToggle is the header button that shows or hides a side panel
// (the Studio's inspector, a page's list): its icon (panel-left or
// panel-right, the side of the panel) on the pressed face while the
// panel shows.
func panelToggle(c *ui.Context, ic icons.Icon, shown bool, tip string) ui.Element {
	b := iconButton(c, ic, tip).Checked(shown)
	if shown {
		b.Background(tok(c.Theme()).Pressed)
	}
	return b
}

// moreMenu is the "⋯" overflow menu of a view, a card or a row: the
// secondary actions that do not need a button of their own.
//
//	moreMenu(c.Key("take-more"), "", func(m *ui.Menu) {
//		if m.Item(i18n.T("Show in Finder")).Chosen() { … }
//	})
func moreMenu(c *ui.Context, tip string, build func(m *ui.Menu)) ui.Element {
	if tip == "" {
		tip = i18n.T("More actions")
	}
	return iconButton(c, icons.MenuDots, tip).Menu(build)
}

// playButton is the round ink play button of a take or a clip: Pause
// (or Stop, with stop) while it plays. size is in DIPs at 100 %.
func playButton(c *ui.Context, playing, stop bool, size float32) ui.Element {
	t := c.Theme()
	k := tok(t)
	label, ic := i18n.T("Play"), icons.Play
	if playing {
		label, ic = i18n.T("Pause"), icons.Pause
		if stop {
			label, ic = i18n.T("Stop"), icons.Stop
		}
	}
	b := ui.ButtonBase(c).Size(px(t, size), px(t, size)).Radius(px(t, size/2)).Center().Label(label).Tooltip(label).Shrink(0)
	switch {
	case b.Pressed():
		b.Background(k.InkPressed)
	case b.Hovered():
		b.Background(k.InkHover)
	default:
		b.Background(k.Ink)
	}
	b.Children(func() {
		ui.Icon(c, ic.Bold).Size(px(t, size*0.44), px(t, size*0.44)).TextColor(k.OnInk)
	})
	return b
}

// playToggle is a small play / pause icon button: playTip while
// stopped, pauseTip while playing (the label follows).
func playToggle(c *ui.Context, playing bool, playTip, pauseTip string) ui.Element {
	if playing {
		return iconButton(c, icons.Pause, pauseTip)
	}
	return iconButton(c, icons.Play, playTip)
}

// ---- Text, marks and chips ----

// infoTip is a ⓘ that tells text as a tooltip, in place of a line of
// help under a control. It takes the keyboard focus, so the tip shows
// from the keyboard too.
func infoTip(c *ui.Context, text string) ui.Element {
	t := c.Theme()
	k := tok(t)
	b := ui.ButtonBase(c).Size(px(t, 18), px(t, 18)).Center().Radius(px(t, 9)).Label(text).Tooltip(text)
	b.Children(func() {
		col := k.Text3
		if b.Hovered() {
			col = k.Text2
		}
		ui.Icon(c, icons.InfoCircle.Linear).Size(px(t, 14), px(t, 14)).TextColor(col)
	})
	return b
}

// truncText is a line of text that ends in "…" when it does not fit, and
// shows all of itself as a tooltip.
func truncText(c *ui.Context, s string) ui.Element {
	return ui.Text(c, s).SingleLine().Ellipsis("…").MinWidth(0).Tooltip(s)
}

// caption is a secondary line (12, the secondary grey).
func caption(c *ui.Context, s string) ui.Element {
	t := c.Theme()
	return ui.Text(c, s).FontSize(px(t, fsCaption)).TextColor(tok(t).Text2)
}

// sectionHeading is a heading inside a page or a dialog (15, semibold).
func sectionHeading(c *ui.Context, title string) ui.Element {
	t := c.Theme()
	return ui.Text(c, title).FontSize(px(t, fsSection)).FontWeight(600).SingleLine().Ellipsis("…").MinWidth(0)
}

// tagChip is a small grey label: a kind, a size, a tag.
func tagChip(c *ui.Context, text string) ui.Element {
	t := c.Theme()
	k := tok(t)
	return ui.Text(c, text).FontSize(px(t, fsMicro)).TextColor(k.Text2).SingleLine().Padding(px(t, 1), px(t, 6)).
		Radius(px(t, 4)).Background(k.Hover).Shrink(0)
}

// chip is a filter chip: plain text, on the grey fill while on; count
// ("" for none) follows its label in the tertiary grey.
func chip(c *ui.Context, label, count string, on bool) ui.Element {
	t := c.Theme()
	k := tok(t)
	name := label
	if count != "" {
		name += " " + count
	}
	b := ui.ButtonBase(c).Padding(px(t, 4), px(t, 10)).Radius(px(t, radiusControl)).Gap(px(t, 6)).AlignItems(ui.Center).
		Label(name).Checked(on).Shrink(0)
	switch {
	case on:
		b.Background(k.Pressed)
	case b.Pressed():
		b.Background(k.Pressed)
	case b.Hovered():
		b.Background(k.Hover)
	}
	b.Children(func() {
		col := k.Text2
		if on {
			col = k.Text
		}
		tx := ui.Text(c, label).FontSize(px(t, fsCaption)).TextColor(col).SingleLine()
		if on {
			tx.FontWeight(600)
		}
		if count != "" {
			ui.Text(c, count).FontSize(px(t, fsMicro)).TextColor(k.Text3).SingleLine()
		}
	})
	return b
}

// statusDot is a 6 px dot of a state's colour.
func statusDot(c *ui.Context, col ui.Color) ui.Element {
	t := c.Theme()
	return ui.Box(c).Size(px(t, 6), px(t, 6)).Radius(px(t, 3)).Background(col).Shrink(0)
}

// statusMark is a status icon (14) in col, then a short text (12) in col:
// "✓ Verified", "⚠ Not downloaded".
func statusMark(c *ui.Context, ic icons.Icon, col ui.Color, text string) ui.Element {
	t := c.Theme()
	return ui.Row(c).Gap(px(t, 4)).AlignItems(ui.Center).MinWidth(0).Children(func() {
		ui.Icon(c, ic.Linear).Size(px(t, 14), px(t, 14)).TextColor(col).Shrink(0)
		ui.Text(c, text).FontSize(px(t, fsCaption)).TextColor(col).SingleLine().Ellipsis("…").MinWidth(0)
	})
}

// iconMark is an icon (14) that says something as its tooltip and label:
// a voice's Locked, a take's cached chunk.
func iconMark(c *ui.Context, ic icons.Icon, col ui.Color, tip string) ui.Element {
	t := c.Theme()
	b := ui.Box(c).Size(px(t, 16), px(t, 16)).Center().Label(tip).Tooltip(tip).Shrink(0)
	b.Children(func() {
		ui.Icon(c, ic.Linear).Size(px(t, 14), px(t, 14)).TextColor(col)
	})
	return b
}

// ---- Layout ----

// pageHeader is a page's title bar, which drags the window: its title,
// then on the right what trailing builds (the page's one primary action,
// icon buttons, moreMenu).
// headerInset is the room page titles leave on their left for the window
// controls: set each frame by sidebarFrame, non-zero beside the icon rail.
var headerInset float32

func pageHeader(c *ui.Context, title string, trailing func()) {
	t := c.Theme()
	ui.Row(c).Height(titleBarHeight).Shrink(0).AlignItems(ui.Center).Padding(0, px(t, sp4), 0, px(t, sp6)+headerInset).Gap(px(t, sp2)).DragWindow().Children(func() {
		ui.Text(c, title).FontSize(px(t, fsTitle)).FontWeight(600).SingleLine().Ellipsis("…").MinWidth(0).Grow(1)
		if trailing != nil {
			trailing()
		}
	})
}

// card is a flat card: the page's background inside a hairline border,
// radius 12. Give it children.
func card(c *ui.Context) ui.Element {
	t := c.Theme()
	return ui.Column(c).Padding(px(t, sp4)).Gap(px(t, sp3)).Radius(px(t, radiusCard)).Border(1, tok(t).Line)
}

// fillCard is a flat card on the subtle grey fill, without a border.
func fillCard(c *ui.Context) ui.Element {
	t := c.Theme()
	return ui.Column(c).Padding(px(t, sp4)).Gap(px(t, sp3)).Radius(px(t, radiusCard)).Background(tok(t).Subtle)
}

// collapsible is a section that folds: a header row (chevron, title, and
// what trailing adds on the right, as a summary) that opens and closes
// it, and fn below it while open.
func collapsible(c *ui.Context, title string, open *bool, trailing, fn func()) ui.Element {
	t := c.Theme()
	k := tok(t)
	root := ui.Column(c).Shrink(0)
	root.Children(func() {
		parts := ui.CollapsibleBase(c, open)
		tr := parts.Trigger.Gap(px(t, sp2)).AlignItems(ui.Center).Padding(px(t, 6), px(t, 4)).Radius(px(t, radiusControl)).Label(title)
		if tr.Hovered() {
			tr.Background(k.Hover)
		}
		tr.Children(func() {
			ui.Icon(c, icons.AltArrowRight.Linear).Size(px(t, 14), px(t, 14)).TextColor(k.Text2).Rotate(90 * parts.Progress())
			ui.Text(c, title).FontWeight(600).SingleLine().Ellipsis("…").MinWidth(0).Grow(1)
			if trailing != nil {
				trailing()
			}
		})
		// Apply a click now, in this build pass: callers often hand in a
		// local copied from a map and store it back after this returns, so a
		// click applied after the frame would land in a dead local and the
		// section would never open or close.
		tr.Changed()
		if p := parts.Panel(fn); p.Valid() {
			p.Gap(px(t, sp3)).Padding(px(t, sp2), px(t, 4), px(t, 4))
		}
	})
	return root
}

// formRow is a setting: its name, a ⓘ with what it does (info, "" for
// none), and its control on the right.
func formRow(c *ui.Context, key, title, info string, control func()) {
	t := c.Theme()
	// The control goes under the name when both do not fit on a line.
	ui.Row(c.Key(key)).Wrap().GapX(px(t, sp4)).GapY(px(t, 6)).AlignItems(ui.Center).MinHeight(px(t, 32)).Children(func() {
		ui.Row(c).Grow(1).Basis(px(t, 160)).MinWidth(px(t, 120)).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, title).MinWidth(0)
			if info != "" {
				infoTip(c, info)
			}
		})
		ui.Row(c).Wrap().Gap(px(t, sp2)).AlignItems(ui.Center).Shrink(1).MinWidth(0).Children(control)
	})
}

// fieldInfo is a labelled control with a ⓘ beside its label.
func fieldInfo(c *ui.Context, label, info string, control func()) {
	t := c.Theme()
	ui.Column(c).Gap(px(t, 6)).Children(func() {
		ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, label).MinWidth(0)
			if info != "" {
				infoTip(c, info)
			}
		})
		control()
	})
}

// searchField is a text input with a magnifier before its text. It is
// as wide as its row allows, up to maxW (0: no limit); the input is
// returned for Changed, Label…
func searchField(c *ui.Context, key string, value *string, placeholder string, maxW float32) ui.Element {
	t := c.Theme()
	k := tok(t)
	var in ui.Element
	box := ui.Row(c.Key(key + "-box")).Grow(1).Shrink(1).Basis(0).MinWidth(px(t, 120)).AlignItems(ui.Stretch)
	if maxW > 0 {
		box.MaxWidth(maxW)
	}
	box.Children(func() {
		in = ui.TextInput(c.Key(key), value).Placeholder(placeholder).Label(placeholder).Grow(1).MinWidth(0).
			Padding(t.Space(1.5), t.Space(2.5), t.Space(1.5), px(t, 30))
		ui.Column(c).Absolute().Left(0).Top(0).Bottom(0).Width(px(t, 30)).Center().PassThrough().Children(func() {
			ui.Icon(c, icons.Magnifer.Linear).Size(px(t, 14), px(t, 14)).TextColor(k.Text3)
		})
	})
	return in
}

// filterBar is the bar of a page's search field and its filters
// (segmented controls, selects, a selection button): on one line when
// the search keeps 240 DIPs beside the filters (filtersW wide, in DIPs at
// 100 %) in room (the bar's width), else the search on a line of its own
// and the filters under it. It returns whether it took two lines.
func filterBar(c *ui.Context, room, filtersW float32, search, filters func()) (twoLines bool) {
	t := c.Theme()
	if room-px(t, filtersW+sp2) >= px(t, 240) {
		ui.Row(c).Gap(px(t, sp2)).AlignItems(ui.Center).Shrink(0).Children(func() {
			search()
			filters()
		})
		return false
	}
	ui.Column(c).Gap(px(t, sp2)).Shrink(0).Children(func() {
		ui.Row(c).Gap(px(t, sp2)).AlignItems(ui.Center).Children(search)
		ui.Row(c).Gap(px(t, sp2)).AlignItems(ui.Center).Children(filters)
	})
	return true
}

// emptyState is a page's or a list's empty state: a large icon, a title,
// a line of text and actions (one primary at most).
func emptyState(c *ui.Context, ic icons.Icon, title, body string, actions func()) ui.Element {
	t := c.Theme()
	k := tok(t)
	return ui.Column(c).Grow(1).Center().Gap(px(t, sp2)).Padding(px(t, sp6)).Children(func() {
		ui.Icon(c, ic.Linear).Size(px(t, 36), px(t, 36)).TextColor(k.Text3)
		ui.Text(c, title).FontSize(px(t, fsSection)).FontWeight(600).TextAlign(ui.Center)
		if body != "" {
			ui.Text(c, body).TextColor(k.Text2).TextAlign(ui.Center).MaxWidth(px(t, 420))
		}
		if actions != nil {
			ui.Row(c).Gap(px(t, sp2)).Wrap().Justify(ui.Center).Margin(px(t, sp2), 0, 0).Children(actions)
		}
	})
}

// ---- Notes on the page ----

// banner is a line of state the user should read now (a warning, a
// locked voice, a licence) on the subtle fill: an icon in col, the text,
// and trailing actions.
func banner(c *ui.Context, ic icons.Icon, col ui.Color, text string, trailing func()) ui.Element {
	t := c.Theme()
	k := tok(t)
	return ui.Row(c).Gap(px(t, sp2)).AlignItems(ui.Center).Padding(px(t, sp2), px(t, sp3)).Radius(px(t, radiusControl)).
		Background(k.Subtle).Wrap().Children(func() {
		ui.Icon(c, ic.Linear).Size(px(t, 16), px(t, 16)).TextColor(col).Shrink(0)
		ui.Text(c, text).FontSize(px(t, fsCaption)).Grow(1).Basis(px(t, 160)).MinWidth(0)
		if trailing != nil {
			trailing()
		}
	})
}

// noticeKind is what a notice is about: it picks the icon and its colour.
type noticeKind int

const (
	noticeInfo noticeKind = iota
	noticeOK
	noticeWarn
	noticeError
)

// notice is a note on the page (a step's result, a warning): a
// banner with an ⓘ telling more ("" for none), the actions (buttons),
// and × when onClose is set.
func notice(c *ui.Context, key string, kind noticeKind, text, more string, actions, onClose func()) {
	k := tok(c.Theme())
	ic, col := icons.InfoCircle, k.Text2
	switch kind {
	case noticeOK:
		ic, col = icons.CheckCircle, k.Success
	case noticeWarn:
		ic, col = icons.DangerTriangle, k.Warning
	case noticeError:
		ic, col = icons.DangerCircle, k.Danger
	}
	banner(c.Key(key), ic, col, text, func() {
		if more != "" {
			infoTip(c.Key("more"), more)
		}
		if actions != nil {
			actions()
		}
		if onClose != nil && iconButton(c.Key("close"), icons.Close, i18n.T("Dismiss")).Clicked() {
			onClose()
		}
	}).Shrink(0)
}

// ---- Side panels ----

// pageWidth is the width of the page beside the sidebar (or the rail).
func (a *app) pageWidth(c *ui.Context) float32 {
	t := c.Theme()
	w, _ := c.Size()
	side := a.sideW
	if a.railMode() {
		side = px(t, railWidth)
	}
	return w - side
}

// panelFits says whether a side panel w wide fits beside the page while
// the page's main column keeps 480 DIPs; when it does not, the panel
// shows as a drawer over the page (sidePanel).
func (a *app) panelFits(c *ui.Context, w float32) bool {
	return a.pageWidth(c)-w >= px(c.Theme(), 480)
}

// sidePanel shows fn in a side panel w wide, at the right of the page:
// beside the main column when inline (a hairline, then a scrolling
// column; call it inside the page's body row), or else as a drawer over
// the page, under its header, which a click outside or Escape closes
// (close may be nil: then only the panel's own controls close it).
func sidePanel(c *ui.Context, key string, w float32, inline bool, label string, close func(), fn func()) {
	t := c.Theme()
	k := tok(t)
	if inline {
		ui.Box(c).Width(1).Background(k.Line).Shrink(0)
		ui.Scroll(c.Key(key)).Width(w).Shrink(0).Label(label).Children(fn)
		return
	}
	ui.Overlay(c, func() {
		_, h := c.Size()
		panel := ui.Scroll(c.Key(key+"-drawer")).Absolute().Right(0).Top(titleBarHeight).Width(w).
			Height(h-titleBarHeight-px(t, statusBarHeight)-1).Background(k.Bg).BorderWidth(0, 0, 0, 1).BorderColor(k.Line).
			Shadow(-8, 0, 24, 0, ui.RGBA(0, 0, 0, 0.12)).Label(label)
		panel.Children(fn)
		if close != nil && (panel.PressedOutside() || panel.OverlayShortcut(0, ui.KeyEscape)) {
			close()
		}
	})
}

// panelHeader is a side panel's title row: the title (15, semibold) and
// a close icon button.
func panelHeader(c *ui.Context, title string, close func()) {
	t := c.Theme()
	ui.Row(c).Gap(px(t, sp2)).AlignItems(ui.Center).Children(func() {
		sectionHeading(c, title).Grow(1)
		if iconButton(c.Key("panel-close"), icons.Close, i18n.T("Close")).Clicked() {
			close()
		}
	})
}

// panelState is a page's side panel (an inspector, a list, a step's
// settings): shown beside the main column when both fit, else hidden and
// opened by its toggle as a drawer over the page.
type panelState struct {
	open, set bool // the toggle's choice, once it was used
}

// layout says whether the panel, width wide, fits beside a main column
// of at least minMain (inline), and whether it shows.
func (sp *panelState) layout(a *app, c *ui.Context, width, minMain float32) (inline, shown bool) {
	inline = a.pageWidth(c)-width >= minMain
	shown = inline
	if sp.set {
		shown = sp.open
	}
	return inline, shown
}

// toggle is the panel's header button (like the Studio's inspector
// toggle): ic on the pressed face while the panel shows.
func (sp *panelState) toggle(c *ui.Context, ic icons.Icon, shown bool, showTip, hideTip string) {
	tip := showTip
	if shown {
		tip = hideTip
	}
	if panelToggle(c, ic, shown, tip).Clicked() {
		sp.open, sp.set = !shown, true
	}
}

// view builds the panel when it shows: sidePanel on the
// right (beside the main column, or a drawer), or the same on the left
// (call it where the panel goes in the page's row).
func (sp *panelState) view(c *ui.Context, key, label string, inline, shown, left bool, width float32, build func()) {
	if !shown {
		return
	}
	hide := func() { sp.open, sp.set = false, true }
	if !left {
		sidePanel(c, key, width, inline, label, hide, build)
		return
	}
	t := c.Theme()
	k := tok(t)
	if inline {
		ui.Scroll(c.Key(key)).Width(width).Shrink(0).Label(label).Children(build)
		ui.Box(c).Width(1).Background(k.Line).Shrink(0)
		return
	}
	ui.Overlay(c, func() {
		w, h := c.Size()
		panel := ui.Scroll(c.Key(key+"-drawer")).Absolute().Left(0).Top(titleBarHeight).Width(min(width, w-px(t, 48))).
			Height(h-titleBarHeight-px(t, statusBarHeight)-1).Background(k.Bg).BorderWidth(0, 1, 0, 0).BorderColor(k.Line).
			Shadow(8, 0, 24, 0, ui.RGBA(0, 0, 0, 0.12)).Label(label)
		panel.Children(build)
		if panel.PressedOutside() || panel.OverlayShortcut(0, ui.KeyEscape) {
			hide()
		}
	})
}

// segmentedFit is ui.Segmented as wide as its segments, for a form's
// column: there a plain Segmented stretches across, its segments keeping
// their width and leaving the rest of the track empty.
func segmentedFit(c *ui.Context, selected *int, labels ...string) ui.Element {
	return ui.Segmented(c, selected, labels...).AlignSelf(ui.Start)
}

// numOpts configure numberInput (set before it is built: Disabled also
// disables its steps). Width is the whole control's.
type numOpts struct {
	Label, Tooltip string
	Width          float32 // of the whole control; 0 for the default
	Disabled       bool
	Grow           bool // take the width the row leaves
}

// numberInput is a field for *value in [lo, hi] by step, with a − and +
// as one stepper beside it, the field's height and in Solar's icons (the
// kit's replacement for ui.NumberInput, whose small text buttons do not
// line up with its field). Up and Down step while it has the focus. It
// reports whether typing, the arrows or a step changed the value.
func numberInput(c *ui.Context, value *float64, lo, hi, step float64, o numOpts) bool {
	t := c.Theme()
	k := tok(t)
	decimals := 0
	if s := strconv.FormatFloat(step, 'f', -1, 64); strings.Contains(s, ".") {
		decimals = len(s) - strings.IndexByte(s, '.') - 1
	}
	format := func(v float64) string { return strconv.FormatFloat(v, 'f', decimals, 64) }
	changed := false
	set := func(v float64) {
		v = math.Max(lo, math.Min(hi, v))
		p := math.Pow(10, float64(decimals))
		v = math.Round(v*p) / p
		if v != *value {
			*value, changed = v, true
		}
	}
	row := ui.Row(c).Gap(px(t, 4)).AlignItems(ui.Stretch).Shrink(0)
	text := ui.Local(row, "text", func() string { return format(*value) })
	row.Children(func() {
		// Width is the whole control's, as ui.NumberInput's was: the field
		// takes what the stepper leaves; it grows when the control does.
		in := ui.TextInput(c.Key("field"), text).Label(o.Label).Disabled(o.Disabled).Grow(1).Shrink(1).MinWidth(px(t, 56))
		if o.Width > 0 {
			in.Width(max(o.Width-px(t, 4+28*2+3), px(t, 56)))
		} else {
			in.Width(px(t, 80))
		}
		if in.Changed() {
			if v, err := strconv.ParseFloat(strings.TrimSpace(*text), 64); err == nil && v >= lo && v <= hi {
				set(v)
			}
		}
		if in.Shortcut(0, ui.KeyUp) {
			set(*value + step)
			*text = format(*value)
		}
		if in.Shortcut(0, ui.KeyDown) {
			set(*value - step)
			*text = format(*value)
		}
		if !in.Focused() {
			*text = format(*value) // what the app set, or what was typed, in full
		}
		ui.Row(c.Key("steps")).AlignItems(ui.Stretch).Radius(px(t, radiusControl)).Border(1, k.Line).Shrink(0).Children(func() {
			step1 := func(key string, ic icons.Icon, label string, delta float64, off bool) {
				b := ui.ButtonBase(c.Key(key)).Width(px(t, 28)).Center().Label(label).Tooltip(label).Disabled(o.Disabled || off)
				switch {
				case b.Pressed():
					b.Background(k.Pressed)
				case b.Hovered():
					b.Background(k.Hover)
				}
				b.Children(func() {
					col := k.Text2
					if o.Disabled || off {
						col = k.Text3
					}
					ui.Icon(c, ic.Linear).Size(px(t, 14), px(t, 14)).TextColor(col)
				})
				if b.Clicked() {
					set(*value + delta)
					*text = format(*value)
				}
			}
			step1("minus", icons.Minus, i18n.T("Decrease"), -step, *value <= lo)
			ui.Box(c).Width(1).Background(k.Line)
			step1("plus", icons.Add, i18n.T("Increase"), step, *value >= hi)
		})
	})
	if o.Tooltip != "" {
		row.Tooltip(o.Tooltip)
	}
	if o.Grow {
		row.Grow(1).MinWidth(0)
	}
	return changed
}

// popoverWithFooter is a popover's content: body in a scroll as tall as
// the window allows, and footer (Reset…) pinned under it, past a hairline,
// so it shows however long the body is.
func popoverWithFooter(c *ui.Context, key string, footer, body func()) {
	t := c.Theme()
	_, h := c.Size()
	maxH := max(px(t, 180), min(px(t, 420), h*0.4)) // room above or below the button it opens from
	ui.Column(c.Key(key + "-pop-frame")).Children(func() {
		ui.Scroll(c.Key(key + "-pop-scroll")).MaxHeight(maxH).Children(body)
		ui.Divider(c)
		ui.Column(c.Key(key + "-pop-footer")).Padding(px(t, 10)).AlignItems(ui.Stretch).Children(footer)
	})
}
