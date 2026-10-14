package main

// Phase-B1 additions to the kit (ui_kit.go), for Stories, Audiobook,
// Dub, Batch, Transcribe and Projects: side panels that turn into
// drawers when the window is narrow, the round play button, play
// toggles, status marks and dots, grey chips, one-line notices, empty
// states and the stepper.

import (
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
)

// mainWidth is the width of the page beside the sidebar (or the rail).
func (a *app) mainWidth(c *ui.Context) float32 {
	t := c.Theme()
	w, _ := c.Size()
	side := a.sideW
	if a.railMode() {
		side = px(t, railWidth)
	}
	return w - side
}

// sidePanel is a page's side panel (an inspector, a list, a setup
// column): shown beside the main column when both fit, else hidden and
// opened by its toggle as a drawer over the page.
type sidePanel struct {
	open, set bool // the toggle's choice, once it was used
}

// layout says whether the panel, width wide, fits beside a main column
// of at least minMain (inline), and whether it shows.
func (sp *sidePanel) layout(a *app, c *ui.Context, width, minMain float32) (inline, shown bool) {
	inline = a.mainWidth(c)-width >= minMain
	shown = inline
	if sp.set {
		shown = sp.open
	}
	return inline, shown
}

// toggle is the panel's header button (like the Studio's inspector
// toggle): ic on the pressed face while the panel shows.
func (sp *sidePanel) toggle(c *ui.Context, ic icons.Icon, shown bool, showTip, hideTip string) {
	tip := showTip
	if shown {
		tip = hideTip
	}
	b := iconButton(c, ic, tip).Checked(shown)
	if shown {
		b.Background(tok(c.Theme()).Pressed)
	}
	if b.Clicked() {
		sp.open, sp.set = !shown, true
	}
}

// view builds the panel: beside the main column (inline; call it where
// the panel goes in the page's row) or, when shown but not inline, as a
// drawer over the page from the left or the right edge. build fills it.
func (sp *sidePanel) view(c *ui.Context, key, label string, inline, shown, left bool, width float32, build func()) {
	t := c.Theme()
	k := tok(t)
	if !shown {
		return
	}
	if inline {
		if !left {
			ui.Box(c).Width(1).Background(k.Line)
		}
		ui.Scroll(c.Key(key)).Width(width).Shrink(0).Label(label).Children(build)
		if left {
			ui.Box(c).Width(1).Background(k.Line)
		}
		return
	}
	ui.Overlay(c, func() {
		w, h := c.Size()
		width := min(width, w-px(t, 48))
		panel := ui.Scroll(c.Key(key+"-drawer")).Absolute().Top(titleBarHeight).Width(width).
			Height(h-titleBarHeight-px(t, statusBarHeight)-1).Background(k.Bg).Label(label)
		if left {
			panel.Left(0).BorderWidth(0, 1, 0, 0).Shadow(8, 0, 24, 0, ui.RGBA(0, 0, 0, 0.12))
		} else {
			panel.Right(0).BorderWidth(0, 0, 0, 1).Shadow(-8, 0, 24, 0, ui.RGBA(0, 0, 0, 0.12))
		}
		panel.BorderColor(k.Line).Children(build)
		if panel.PressedOutside() || panel.OverlayShortcut(0, ui.KeyEscape) {
			sp.open, sp.set = false, true
		}
	})
}

// roundPlay is the round ink play button of a take or a render (36 px):
// Play, or Pause while playing. label names it.
func roundPlay(c *ui.Context, playing bool, label string) ui.Element {
	t := c.Theme()
	k := tok(t)
	ic := icons.Play
	if playing {
		ic = icons.Pause
	}
	b := ui.ButtonBase(c).Size(px(t, 36), px(t, 36)).Radius(px(t, 18)).Center().Shrink(0).Label(label).Tooltip(label)
	switch {
	case b.Pressed():
		b.Background(k.InkPressed)
	case b.Hovered():
		b.Background(k.InkHover)
	default:
		b.Background(k.Ink)
	}
	b.Children(func() {
		ui.Icon(c, ic.Bold).Size(px(t, 16), px(t, 16)).TextColor(k.OnInk)
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

// statusMark is a 14 px status icon (✓ ✗ ⚠ …) in col.
func statusMark(c *ui.Context, ic icons.Icon, col ui.Color) ui.Element {
	t := c.Theme()
	return ui.Icon(c, ic.Linear).Size(px(t, 14), px(t, 14)).TextColor(col).Shrink(0)
}

// dot is a 6 px status dot.
func dot(c *ui.Context, col ui.Color) ui.Element {
	t := c.Theme()
	return ui.Box(c).Size(px(t, 6), px(t, 6)).Radius(px(t, 3)).Background(col).Shrink(0)
}

// chip is a small grey label (a language, a kind, a count).
func chip(c *ui.Context, s string) ui.Element {
	t := c.Theme()
	k := tok(t)
	return ui.Text(c, s).FontSize(px(t, fsMicro)).TextColor(k.Text2).SingleLine().Shrink(0).
		Padding(px(t, 2), px(t, 6)).Radius(px(t, 6)).Background(k.Hover)
}

// noticeKind is what a notice is about: it picks the icon's colour.
type noticeKind int

const (
	noticeInfo noticeKind = iota
	noticeOK
	noticeWarn
	noticeError
)

// notice is a one-line note on the page (a step's result, a warning):
// a coloured icon, the text (cut with "…", all of it as the tooltip), an
// ⓘ with more when there is more to say, and × when onClose is set.
func notice(c *ui.Context, key string, kind noticeKind, text, more string, onClose func()) {
	t := c.Theme()
	k := tok(t)
	ic, col := icons.InfoCircle, k.Text2
	switch kind {
	case noticeOK:
		ic, col = icons.CheckCircle, k.Success
	case noticeWarn:
		ic, col = icons.DangerTriangle, k.Warning
	case noticeError:
		ic, col = icons.DangerCircle, k.Danger
	}
	ui.Row(c.Key(key)).Gap(px(t, sp2)).AlignItems(ui.Center).Padding(px(t, 4), px(t, 4), px(t, 4), px(t, 10)).
		Radius(px(t, radiusControl)).Background(k.Subtle).MinHeight(px(t, 36)).Children(func() {
		statusMark(c, ic, col)
		ui.Text(c, text).FontSize(px(t, fsCaption)).SingleLine().Ellipsis("…").MinWidth(0).Grow(1).Tooltip(text)
		if more != "" {
			infoTip(c.Key("more"), more)
		}
		if onClose != nil && iconButton(c.Key("close"), icons.Close, i18n.T("Dismiss")).Clicked() {
			onClose()
		}
	})
}

// emptyState is a page's or a panel's empty state: a large icon, a
// title, a line saying what to do, and actions (fn).
func emptyState(c *ui.Context, key string, ic icons.Icon, title, body string, fn func()) ui.Element {
	t := c.Theme()
	k := tok(t)
	col := ui.Column(c.Key(key)).Grow(1).Center().Gap(px(t, sp3)).Padding(px(t, sp6))
	col.Children(func() {
		ui.Icon(c, ic.Linear).Size(px(t, 36), px(t, 36)).TextColor(k.Text3)
		ui.Text(c, title).FontSize(px(t, fsSection)).FontWeight(600).TextAlign(ui.Center)
		if body != "" {
			ui.Text(c, body).TextColor(k.Text2).TextAlign(ui.Center).MaxWidth(px(t, 420))
		}
		if fn != nil {
			ui.Row(c).Gap(px(t, sp2)).Wrap().Justify(ui.Center).Children(fn)
		}
	})
	return col
}

// stepState is a stepper step's state.
type stepState int

const (
	stepTodo stepState = iota
	stepDone
	stepCurrent
)

// stepItem is one step of a stepper: its number shows until it is done.
func stepItem(c *ui.Context, n int, label string, st stepState, disabled bool) ui.Element {
	t := c.Theme()
	k := tok(t)
	b := ghostBase(c).Padding(px(t, 5), px(t, sp2)).Gap(px(t, 6)).Label(label).Disabled(disabled).Shrink(0)
	if st == stepCurrent {
		b.Background(k.Pressed)
	}
	b.Children(func() {
		col := k.Text3
		switch st {
		case stepCurrent:
			col = k.Text
		case stepDone:
			col = k.Text2
		}
		if st == stepDone {
			statusMark(c, icons.CheckCircle, k.AccentInk)
		} else {
			ui.Text(c, itoa(n)).FontSize(px(t, fsMicro)).FontWeight(600).TextColor(col).Width(px(t, 14)).TextAlign(ui.Center)
		}
		txt := ui.Text(c, label).SingleLine().TextColor(col)
		if st == stepCurrent {
			txt.FontWeight(600)
		}
	})
	return b
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
