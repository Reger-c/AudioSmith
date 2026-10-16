package main

// The voice picker the creation pages share (thread #60): Studio's
// inspector, the Stories and Audiobook casts, the dub's cast and line
// options. A trigger showing the chosen voice opens a list of the voices
// in groups; a row with a preview has a ▶ that plays it (■ stops it).
// One preview plays at a time, in the app's player; closing the list
// stops it. From the keyboard, as a select: with the focus on the
// trigger the arrows open the list and move in it, Enter and Space pick,
// Escape closes, and P plays the highlighted row's preview; Tab also
// reaches each row and its ▶.

import (
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
)

// voiceOption is one row of a voice picker.
type voiceOption struct {
	value   string // what picking it chooses (unique in the picker)
	label   string
	sub     string // a second line: "Recommended", the voice's kind
	group   string // its section's title; a group's rows follow one another
	voice   *core.Voice
	preview voicePreview
	// action rows are commands (Clone a clip…), after a divider: picking
	// one never changes the choice; on shows it checked.
	action, on bool
	warn       bool // a missing or unusable voice
}

// voicePickerOpts tune a voice picker.
type voicePickerOpts struct {
	label  string      // what it picks, for accessibility: "Voice of A"
	text   string      // the trigger's text when no row is chosen ("" = None)
	voice  *core.Voice // … and its portrait
	locked bool
	width  float32 // the list's least width in DIPs (0: 280)
}

// voicePickerState is the pickers' view state, by picker key.
type voicePickerState struct {
	open      map[string]bool
	highlight map[string]int // the row the arrows are on; -1 until they move
	// previewKey is the picker whose ▶ started the preview playing, and
	// previewID its player id: the picker stops it as it closes.
	previewKey, previewID string
}

// voicePickerOpen opens or closes picker key (tests and snapshots open
// it this way too).
func (a *app) voicePickerOpen(key string, open bool) {
	vp := &a.voicePickers
	if vp.open == nil {
		vp.open, vp.highlight = map[string]bool{}, map[string]int{}
	}
	vp.open[key] = open
	vp.highlight[key] = -1
}

// voicePicker picks one of opts; cur is the chosen value. It returns the
// value of the row picked this frame and true (an action's too: the
// caller acts), or "" and false. key must be unique in its scope.
func (a *app) voicePicker(c *ui.Context, key, cur string, opts []voiceOption, o voicePickerOpts) (string, bool) {
	t := c.Theme()
	k := tok(t)
	vp := &a.voicePickers
	if vp.open == nil {
		a.voicePickerOpen(key, false)
	}
	chosen := -1
	for i, op := range opts {
		if op.value == cur && !op.action {
			chosen = i
		}
	}
	label, voice, warn := o.text, o.voice, false
	if chosen >= 0 {
		label, voice, warn = opts[chosen].label, opts[chosen].voice, opts[chosen].warn
	}
	if label == "" {
		label = i18n.T("None")
	}
	open := vp.open[key] && !o.locked
	// The trigger looks as ui.Select's does.
	b := ui.ButtonBase(c.Key(key)).Padding(t.Space(1.5), t.Space(3.5)).Gap(t.Space(1.5)).Radius(t.Radius).Background(t.Surface).
		TextColor(t.Text).Border(1, t.Border).AlignItems(ui.Center).MinWidth(t.Space(35)).Label(o.label).Tooltip(label).
		Disabled(o.locked).Role(ui.RolePopUpButton).Expanded(open)
	switch {
	case o.locked:
	case b.Pressed():
		b.Background(t.SurfacePressed)
	case b.Hovered():
		b.Background(t.SurfaceHover)
	}
	b.Children(func() {
		if voice != nil {
			ui.Avatar(c, voice.Name, a.portrait(voice)).Size(px(t, 18), px(t, 18))
		}
		txt := ui.Text(c, label).SingleLine().Ellipsis("…").MinWidth(0).Grow(1)
		if warn {
			txt.TextColor(t.Warning)
		}
		ui.Box(c).Size(t.Space(2.5), t.Space(2.5)).Shrink(0).Draw(func(p *ui.Painter, r ui.Rect) {
			var path ui.Path
			path.MoveTo(r.X+r.W*0.1, r.Y+r.H*0.3).LineTo(r.X+r.W*0.5, r.Y+r.H*0.7).LineTo(r.X+r.W*0.9, r.Y+r.H*0.3)
			p.StrokePath(&path, 1.5, t.TextMuted)
		})
	})
	picked, ok := "", false
	pick := func(i int) {
		open, picked, ok = false, opts[i].value, opts[i].action || opts[i].value != cur
	}
	// The keyboard, while the trigger has the focus.
	hl := vp.highlight[key]
	move := func(i int) {
		if len(opts) > 0 {
			hl = max(0, min(i, len(opts)-1))
		}
	}
	keyed := false
	switch {
	case o.locked:
	case !open && (b.Shortcut(0, ui.KeyDown) || b.Shortcut(0, ui.KeyUp)):
		open, hl = true, max(chosen, 0)
	case open && b.Shortcut(0, ui.KeyDown):
		move(max(hl, chosen) + 1)
	case open && b.Shortcut(0, ui.KeyUp):
		move(min(hl, max(chosen, 0)) - 1)
		if hl < 0 && chosen < 0 {
			move(0)
		}
	case open && b.Shortcut(0, ui.KeyHome):
		move(0)
	case open && b.Shortcut(0, ui.KeyEnd):
		move(len(opts) - 1)
	case open && hl >= 0 && hl < len(opts) && (b.Shortcut(0, ui.KeyEnter) || b.Shortcut(0, ui.KeySpace)):
		pick(hl)
		keyed = true
	case open && hl >= 0 && hl < len(opts) && b.Shortcut(0, ui.KeyP):
		if p := opts[hl].preview; p.ok() {
			a.toggleVoicePreview(key, p)
		}
	}
	if b.Clicked() && !keyed {
		open = !open
		hl = -1
	}
	var played *voicePreview // the row whose ▶ was pressed
	ui.PopoverBase(c, b, &open, func(panel ui.Element) {
		panel.Margin(px(t, 4), 0, 0, 0).Padding(px(t, 4)).Radius(px(t, radiusControl+2)).Background(t.Background).Border(1, t.Border).
			Shadow(0, 6, 20, 0, ui.RGBA(0, 0, 0, 0.18)).MinWidth(b.Bounds().W)
		_, h := c.Size()
		width := o.width
		if width == 0 {
			width = 280
		}
		r := b.Bounds()
		// The room below the trigger (less the panel's margin, padding and
		// some air), or above it when that is too little: the panel then
		// opens above.
		maxH := min(h*0.6, h-(r.Y+r.H)-px(t, 32))
		if maxH < px(t, 160) {
			maxH = max(px(t, 160), min(h*0.6, r.Y-px(t, 48)))
		}
		ui.Scroll(c.Key(key + "-scroll")).MaxHeight(maxH).Children(func() {
			ui.Column(c.Key(key + "-list")).MinWidth(px(t, width)).Gap(px(t, 1)).Children(func() {
				group, divided := "", false
				for i := range opts {
					op := &opts[i]
					if op.action && !divided {
						divided = true
						ui.Divider(c)
					}
					if !op.action && op.group != group {
						group = op.group
						if group != "" {
							ui.Text(c, group).FontSize(px(t, fsMicro)).FontWeight(600).TextColor(k.Text3).Padding(px(t, 6), px(t, sp2), px(t, 2))
						}
					}
					row, play := a.voiceRow(c, key, op, i == chosen || op.action && op.on, i == hl)
					if i == hl {
						b.ActiveDescendant(row)
					}
					if row.Clicked() {
						pick(i)
					}
					if play {
						pv := op.preview
						played = &pv
					}
				}
			})
		})
	})
	if played != nil {
		a.toggleVoicePreview(key, *played)
	}
	if !open {
		hl = -1
		a.stopPickerPreview(key)
	}
	vp.open[key], vp.highlight[key] = open, hl
	return picked, ok
}

// voiceRow is a picker's row: the voice (a button that picks it), its ▶
// and a check when chosen. It returns the button and whether ▶ was
// pressed.
func (a *app) voiceRow(c *ui.Context, key string, op *voiceOption, chosen, highlighted bool) (ui.Element, bool) {
	t := c.Theme()
	k := tok(t)
	var btn ui.Element
	play := false
	row := ui.Row(c.Key(key + "-row-" + op.value)).Radius(px(t, radiusControl)).Gap(px(t, 4)).AlignItems(ui.Center).PaddingEnd(px(t, 6))
	switch {
	case highlighted || row.Hovered():
		row.Background(k.Hover)
	case chosen:
		row.Background(k.Pressed)
	}
	row.Children(func() {
		btn = ui.ButtonBase(c.Key("pick")).Padding(px(t, 4), px(t, sp2)).Gap(px(t, 6)).AlignItems(ui.Center).Grow(1).MinWidth(0).
			MinHeight(px(t, 30)).Label(op.label).Checked(chosen)
		btn.Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Children(func() {
				l := ui.Text(c, op.label).SingleLine().Ellipsis("…").MinWidth(0)
				if op.warn {
					l.TextColor(t.Warning)
				}
				if op.sub != "" {
					ui.Text(c, op.sub).FontSize(px(t, fsMicro)).TextColor(k.Text2).SingleLine().Ellipsis("…").MinWidth(0)
				}
			})
		})
		if op.preview.ok() {
			playing := a.pl.Playing(op.preview.id)
			ic := icons.Play
			if playing {
				ic = icons.Stop
			}
			pb := iconButton(c.Key("play"), ic, previewTip(op.label, playing)).Size(px(t, 24), px(t, 24)).Shrink(0)
			if playing {
				pb.Background(k.AccentSoft)
			}
			play = pb.Clicked()
		}
		if chosen {
			iconMark(c, icons.CheckCircle, k.AccentInk, i18n.T("Chosen"))
		}
	})
	return btn, play
}

// toggleVoicePreview plays p (from picker key), or stops it if it plays.
func (a *app) toggleVoicePreview(key string, p voicePreview) {
	vp := &a.voicePickers
	if a.pl.Playing(p.id) {
		a.pl.Stop()
		vp.previewKey, vp.previewID = "", ""
		return
	}
	if a.dubPlayer.p != nil {
		a.dubStopPlayer()
	}
	limit := previewLimit
	if p.data != nil {
		limit = 0
	}
	if err := a.pl.PlayPreview(p.id, p.data, p.path, limit); err != nil {
		a.toast(i18n.T("Cannot play: %v", err))
		return
	}
	vp.previewKey, vp.previewID = key, p.id
}

// stopPickerPreview stops the preview picker key started, if it plays.
func (a *app) stopPickerPreview(key string) {
	vp := &a.voicePickers
	if vp.previewKey != key {
		return
	}
	if a.pl.Current() == vp.previewID {
		a.pl.Stop()
	}
	vp.previewKey, vp.previewID = "", ""
}
