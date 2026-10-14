package main

// Phase-B1 additions to the kit (ui_kit.go, ui_kit_b2.go), for Stories,
// Audiobook, Dub, Batch, Transcribe and Projects: the state of a side
// panel with its toggle (on top of ui_kit_b2's sidePanel, plus panels on
// the left), play toggles, one-line notices, and a labelled control with
// a ⓘ.

import (
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
)

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
	b := iconButton(c, ic, tip).Checked(shown)
	if shown {
		b.Background(tok(c.Theme()).Pressed)
	}
	if b.Clicked() {
		sp.open, sp.set = !shown, true
	}
}

// view builds the panel when it shows: ui_kit_b2's sidePanel on the
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

// playToggle is a small play / pause icon button: playTip while
// stopped, pauseTip while playing (the label follows).
func playToggle(c *ui.Context, playing bool, playTip, pauseTip string) ui.Element {
	if playing {
		return iconButton(c, icons.Pause, pauseTip)
	}
	return iconButton(c, icons.Play, playTip)
}

// noticeKind is what a notice is about: it picks the icon and its colour.
type noticeKind int

const (
	noticeInfo noticeKind = iota
	noticeOK
	noticeWarn
	noticeError
)

// notice is a note on the page (a step's result, a warning): ui_kit_b2's
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
