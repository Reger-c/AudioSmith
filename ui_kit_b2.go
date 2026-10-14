package main

// Phase B2's additions to the kit (ui_kit.go): a search field with its
// magnifier, filter chips, small status marks, empty states, banners,
// the round play button, and side panels that become drawers when the
// page is narrow. research/ui-design-spec.md §3–§4 and §8 give the rules.

import (
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
)

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

// searchField is a text input with a magnifier before its text. It is
// as wide as its row allows, up to maxW (0: no limit); the input is
// returned for Changed, Label…
func searchField(c *ui.Context, key string, value *string, placeholder string, maxW float32) ui.Element {
	t := c.Theme()
	k := tok(t)
	var in ui.Element
	box := ui.Row(c.Key(key+"-box")).Grow(1).Shrink(1).Basis(0).MinWidth(px(t, 120)).AlignItems(ui.Stretch)
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

// tagChip is a small grey label: a kind, a size, a tag.
func tagChip(c *ui.Context, text string) ui.Element {
	t := c.Theme()
	k := tok(t)
	return ui.Text(c, text).FontSize(px(t, fsMicro)).TextColor(k.Text2).SingleLine().Padding(px(t, 1), px(t, 6)).
		Radius(px(t, 4)).Background(k.Hover).Shrink(0)
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

// labelOrIcon is a ghost button with an icon and its label, or only the
// icon (with the label as its tooltip) when narrow.
func labelOrIcon(c *ui.Context, ic icons.Icon, label string, narrow bool) ui.Element {
	if narrow {
		return iconButton(c, ic, label)
	}
	return ghostIconButton(c, ic, label).Label(label)
}
