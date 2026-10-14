package main

// The shared kit of the 0.3 design (research/ui-design-spec.md §Kit):
// buttons by rank, the "⋯" overflow menu, collapsible sections, flat
// cards, the page header, ⓘ tooltips, truncated text and form rows. Pages
// build from these rather than styling MyGo's widgets one by one.
//
// Ranks: one primaryButton per view (the core action, in ink); ui.Button
// for the other actions that need a label (secondary: grey face); ghost
// buttons for quiet actions in toolbars and lists; iconButton, with its
// tooltip, wherever an icon says enough; and the rest in moreMenu.

import (
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
)

// primaryButton is the view's one core action (Generate, Export, Start):
// MyGo's primary button in ink, near-black (near-white in the dark).
//
//	if primaryButton(c.Key("generate"), i18n.T("Generate")).Disabled(!ok).Clicked() { … }
func primaryButton(c *ui.Context, label string) ui.Element {
	t := c.Theme()
	c.SetTheme(inkTheme(t))
	b := ui.PrimaryButton(c, label)
	c.SetTheme(t)
	return b
}

// primaryIconButton is primaryButton with an icon before its label.
func primaryIconButton(c *ui.Context, ic icons.Icon, label string) ui.Element {
	t := c.Theme()
	c.SetTheme(inkTheme(t))
	b := ui.PrimaryButton(c, "")
	b.Children(func() {
		ui.Icon(c, ic.Linear).Size(px(t, 16), px(t, 16))
		ui.Text(c, label).SingleLine()
	})
	c.SetTheme(t)
	return b
}

// secondaryIconButton is a secondary button (ui.Button's look) with an
// icon before its label.
func secondaryIconButton(c *ui.Context, ic icons.Icon, label string) ui.Element {
	t := c.Theme()
	b := ui.Button(c, "")
	b.Children(func() {
		ui.Icon(c, ic.Linear).Size(px(t, 16), px(t, 16))
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
		ui.Icon(c, ic.Linear).Size(px(t, 16), px(t, 16)).TextColor(tok(t).Text2)
		ui.Text(c, label).SingleLine()
	})
	return b
}

// ghostBase is the face of ghost and icon buttons.
func ghostBase(c *ui.Context) ui.Element {
	t := c.Theme()
	k := tok(t)
	b := ui.ButtonBase(c).Radius(px(t, radiusControl)).TextColor(k.Text)
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

// sectionHeading is a heading inside a page or a dialog (15, semibold).
func sectionHeading(c *ui.Context, title string) ui.Element {
	t := c.Theme()
	return ui.Text(c, title).FontSize(px(t, fsSection)).FontWeight(600).SingleLine().Ellipsis("…").MinWidth(0)
}

// caption is a secondary line (12, the secondary grey).
func caption(c *ui.Context, s string) ui.Element {
	t := c.Theme()
	return ui.Text(c, s).FontSize(px(t, fsCaption)).TextColor(tok(t).Text2)
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
	ui.Row(c.Key(key)).Gap(px(t, sp4)).AlignItems(ui.Center).MinHeight(px(t, 32)).Children(func() {
		ui.Row(c).Grow(1).MinWidth(0).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, title).MinWidth(0)
			if info != "" {
				infoTip(c, info)
			}
		})
		ui.Row(c).Gap(px(t, sp2)).AlignItems(ui.Center).Shrink(1).MinWidth(0).Children(control)
	})
}

// pageHeader is a page's title bar, which drags the window: its title,
// then on the right what trailing builds (the page's one primary action,
// icon buttons, moreMenu).
func pageHeader(c *ui.Context, title string, trailing func()) {
	t := c.Theme()
	ui.Row(c).Height(titleBarHeight).Shrink(0).AlignItems(ui.Center).Padding(0, px(t, sp4), 0, px(t, sp6)).Gap(px(t, sp2)).DragWindow().Children(func() {
		ui.Text(c, title).FontSize(px(t, fsTitle)).FontWeight(600).SingleLine().Ellipsis("…").MinWidth(0).Grow(1)
		if trailing != nil {
			trailing()
		}
	})
}
