package main

// The Dub page's transcript editor: a toolbar (language, select all,
// undo/redo, retry failed, paste, glossary, more), the bulk bar, and the
// lines in a virtualised list — time, speaker and voice, the source
// dimmed, the line in the language shown (click to edit; ⌘↩ or leaving
// it keeps it, Esc drops it), badges (Error, Tight, Won't fit, Overflows,
// Trimmed, Verify, Changed), audition, and each line's options.

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/translate"
)

// dubToolbar is the editor's toolbar.
func (a *app) dubToolbar(c *ui.Context) {
	t := c.Theme()
	p := a.dub
	locked := a.dubLocked()
	ui.Row(c.Key("dub-toolbar")).Gap(px(t, 6)).AlignItems(ui.Center).Wrap().Children(func() {
		ui.Badge(c, i18n.N(len(p.Segments), "%d line", "%d lines", len(p.Segments)))
		langs := p.Languages()
		if len(langs) > 0 {
			names := []string{i18n.T("Source")}
			ids := []string{""}
			for _, l := range langs {
				done, total := p.Translated(l)
				names = append(names, fmt.Sprintf("%s %d/%d", dubLangName(l), done, total))
				ids = append(ids, l)
			}
			cur := names[max(slices.Index(ids, a.dubLang), 0)]
			if ui.Select(c.Key("dub-showlang"), &cur, names).Label(i18n.T("Language shown")).Changed() {
				a.dubCommitEdit()
				a.dubLang = ids[slices.Index(names, cur)]
				a.dubPaste.parsed = ""
			}
		}
		all := len(p.Segments) > 0 && len(a.dubSelected()) == len(p.Segments)
		if ui.Checkbox(c.Key("dub-all"), &all, i18n.T("All")).Changed() {
			a.dubSel = map[string]bool{}
			if all {
				for _, s := range p.Segments {
					a.dubSel[s.ID] = true
				}
			}
		}
		if ui.Button(c.Key("dub-undo"), "↶").Label(i18n.T("Undo")).Tooltip(i18n.T("Undo (⌘Z)")).Disabled(locked || !a.dubHist.CanUndo()).Clicked() {
			a.dubUndo()
		}
		if ui.Button(c.Key("dub-redo"), "↷").Label(i18n.T("Redo")).Tooltip(i18n.T("Redo (⇧⌘Z)")).Disabled(locked || !a.dubHist.CanRedo()).Clicked() {
			a.dubRedo()
		}
		if n := a.dubFailedCount(a.dubLang); n > 0 && a.dubLang != "" {
			if ui.Button(c.Key("dub-retry"), i18n.N(n, "Retry %d failed", "Retry %d failed", n)).Disabled(a.dubBusy()).Clicked() {
				a.dubTranslate(false)
			}
		}
		if a.dubLang != "" && ui.Button(c.Key("dub-paste"), i18n.T("Paste translation")).Disabled(locked).Clicked() {
			a.dubPaste.open = !a.dubPaste.open
		}
		if ui.Button(c.Key("dub-gloss"), i18n.T("Glossary (%d)", len(p.Glossary))).Clicked() {
			a.dubGloss.open = !a.dubGloss.open
			a.dubGloss.editing = -1
		}
		ui.MenuButton(c.Key("dub-more"), i18n.T("More"), func(m *ui.Menu) {
			menuDo(m, i18n.T("Clean up fragments"), locked, a.dubCleanup)
			menuDo(m, i18n.T("Mirror source delivery"), locked || p.Media.Audio == "", a.dubMirror)
			menuDo(m, i18n.T("Import subtitles as the transcript…"), locked || a.dubBusy(), a.dubImportSubtitles)
			m.Separator()
			menuDo(m, i18n.T("Restore the source (drop translations)"), locked, a.dubRestore)
			if a.dubLang != "" {
				menuDo(m, i18n.T("Skip failed lines"), locked || a.dubFailedCount(a.dubLang) == 0, a.dubSkipFailed)
			}
		})
		ui.Spacer(c)
		if a.dubLang != "" && p.Track(a.dubLang).File != "" {
			label := i18n.T("Verify")
			if qc := p.Track(a.dubLang).QC; qc != nil && !p.Track(a.dubLang).QCStale() {
				label = i18n.N(qc.Flagged, "Verify · %d to re-listen", "Verify · %d to re-listen", qc.Flagged)
			}
			if ui.Button(c.Key("dub-verify"), label).Tooltip(i18n.T("Listen to the dub again with speech to text, and flag lines that drift")).Disabled(a.dubBusy()).Clicked() {
				a.dubVerify()
			}
		}
	})
	if a.dubNotice != "" {
		ui.Row(c.Key("dub-notice")).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, a.dubNotice).FontSize(px(t, 12)).TextColor(t.TextMuted).Grow(1)
			if ui.Button(c.Key("dub-notice-x"), "×").Label(i18n.T("Dismiss")).Clicked() {
				a.dubNotice = ""
			}
		})
	}
	if n := len(a.dubSelected()); n > 0 {
		a.dubBulkBar(c, n)
	}
}

// dubBulkBar acts on the lines ticked.
func (a *app) dubBulkBar(c *ui.Context, n int) {
	t := c.Theme()
	locked := a.dubLocked()
	ui.Row(c.Key("dub-bulk")).Gap(px(t, 6)).AlignItems(ui.Center).Padding(px(t, 6), px(t, 8)).Radius(6).Background(t.Surface).Children(func() {
		ui.Text(c, i18n.N(n, "%d selected", "%d selected", n)).Bold()
		ui.MenuButton(c.Key("bulk-voice"), i18n.T("Set voice…"), func(m *ui.Menu) {
			for _, v := range a.dubVoiceChoices() {
				ref := v.ref
				label := v.label
				if v.group != "" {
					label = v.group + " · " + v.label
				}
				menuDo(m, label, locked, func() { a.dubBulkVoice(ref) })
			}
		})
		ui.MenuButton(c.Key("bulk-lang"), i18n.T("Set language…"), func(m *ui.Menu) {
			menuDo(m, i18n.T("The track's (default)"), locked, func() { a.dubBulkLang("") })
			if src := a.dub.SourceLanguage(); src != "" {
				menuDo(m, i18n.T("Keep the source language (%s)", dubLangName(src)), locked, func() { a.dubBulkLang(src) })
			}
			for _, l := range a.dub.Targets {
				menuDo(m, dubLangName(l), locked, func() { a.dubBulkLang(l) })
			}
		})
		if ui.Button(c.Key("bulk-delete"), i18n.T("Delete")).Disabled(locked).Clicked() {
			a.dubBulkDelete()
		}
		ui.Spacer(c)
		if ui.Button(c.Key("bulk-clear"), i18n.T("Clear selection")).Clicked() {
			a.dubSel = map[string]bool{}
		}
	})
}

// dubLines is the list of lines.
func (a *app) dubLines(c *ui.Context) {
	t := c.Theme()
	p := a.dub
	a.dubList2.Key = func(i int) any {
		if a.dub != nil && i < len(a.dub.Segments) {
			return a.dub.Segments[i].ID
		}
		return i
	}
	var after func()
	ui.List(c.Key("dub-lines"), &a.dubList2, len(p.Segments)).Grow(1).MinHeight(px(t, 160)).Gap(px(t, 4)).ItemKey(a.dubList2.Key).Rows(func(r ui.ListRow) {
		if r.Index >= len(a.dub.Segments) {
			return
		}
		if f := a.dubRow(c, r.Index); f != nil {
			after = f
		}
	})
	if after != nil {
		after()
	}
}

// dubBadges are a line's badges in the language shown: label and colour.
func (a *app) dubBadges(s dub.Segment) [][2]string {
	p := a.dub
	l := a.dubLang
	var out [][2]string
	if l == "" {
		return nil
	}
	line := s.Lines[dub.LangFor(s, l)]
	switch {
	case line.Skipped:
		out = append(out, [2]string{i18n.T("Skipped"), "muted"})
	case line.Err != "":
		out = append(out, [2]string{i18n.T("Error"), "danger"})
	}
	if s.Lang != "" {
		out = append(out, [2]string{dubLangName(s.Lang), "muted"})
	}
	t := p.Track(l)
	r, rendered := t.Lines[s.ID]
	switch {
	case rendered && r.Err != "":
		out = append(out, [2]string{i18n.T("Failed"), "danger"})
	case rendered && r.Verdict == dub.VerdictTrimmed:
		out = append(out, [2]string{i18n.T("Trimmed %.1fs", r.Over), "danger"})
	case rendered && (r.Verdict == dub.VerdictOverflow || r.Verdict == dub.VerdictVideo):
		out = append(out, [2]string{i18n.T("Overflows +%.1fs", r.Over), "warning"})
	case rendered && r.Verdict == dub.VerdictTight:
		out = append(out, [2]string{i18n.T("Tight fit"), "warning"})
	case !rendered && line.Fit != nil && line.Fit.Verdict == translate.FitOverflow:
		out = append(out, [2]string{i18n.T("Won't fit +%.1fs", line.Fit.Over), "warning"})
	case !rendered && line.Fit != nil && line.Fit.Verdict == translate.FitTight:
		out = append(out, [2]string{i18n.T("Tight fit"), "warning"})
	}
	if rendered && strings.TrimSpace(p.TextFor(s, l)) != "" && (r.Text != p.TextFor(s, l) || r.Start != s.Start) {
		out = append(out, [2]string{i18n.T("Changed"), "accent"})
	}
	if qc := t.QC; qc != nil && !t.QCStale() {
		for _, q := range qc.Lines {
			if q.ID == s.ID && q.Flagged {
				out = append(out, [2]string{i18n.T("Verify"), "warning"})
			}
		}
	}
	return out
}

// dubRow is one line; it returns a change to make after the list.
func (a *app) dubRow(c *ui.Context, i int) (after func()) {
	t := c.Theme()
	p := a.dub
	s := p.Segments[i]
	id := s.ID
	locked := a.dubLocked()
	cur := a.dubCur == id
	overlap := i+1 < len(p.Segments) && s.End > p.Segments[i+1].Start+0.01
	card := ui.Column(c.Key("row-"+id)).Gap(px(t, 4)).Padding(px(t, 6), px(t, 10)).Radius(6).Border(1, t.Border)
	if cur {
		card.Border(2, t.Accent)
	}
	if card.Clicked() && !cur {
		a.dubCur = id
		a.dubTL.sel = id
	}
	card.Children(func() {
		ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
			sel := a.dubSel[id]
			if ui.Checkbox(c.Key("sel"), &sel, "").Label(i18n.T("Select line %d", i+1)).Changed() {
				if sel {
					a.dubSel[id] = true
				} else {
					delete(a.dubSel, id)
				}
			}
			if ui.Button(c.Key("time"), tlClock(s.Start)).Label(i18n.T("Play from %s", tlClock(s.Start))).Clicked() {
				after = func() { a.dubPlayRange(id) }
			}
			who := p.SpeakerName(s.Speaker) + " · " + a.dubVoiceLabel(p.VoiceOf(s))
			if ui.Button(c.Key("who"), who).Tooltip(i18n.T("Line options")).Clicked() {
				if a.dubOpen == id {
					a.dubOpen = ""
				} else {
					a.dubOpen = id
				}
			}
			if overlap {
				ui.Text(c, i18n.T("Overlaps the next line")).FontSize(px(t, 11)).TextColor(t.Danger)
			}
			for _, b := range a.dubBadges(s) {
				badge := ui.Badge(c, b[0])
				switch b[1] {
				case "danger":
					badge.TextColor(t.Danger)
				case "warning":
					badge.TextColor(t.Warning)
				case "accent":
					badge.TextColor(t.Accent)
				}
			}
			ui.Spacer(c)
			if a.dubLang != "" {
				busy := a.dubAudition.busy && a.dubAudition.seg == id
				label := "🎧"
				if busy {
					label = "…"
				}
				if ui.Button(c.Key("hear"), label).Label(i18n.T("Hear this line dubbed")).Tooltip(i18n.T("Hear this line dubbed")).Disabled(busy || strings.TrimSpace(p.TextFor(s, a.dubLang)) == "").Clicked() {
					after = func() { a.dubAuditionLine(id) }
				}
			}
			ui.MenuButton(c.Key("menu"), "⋯", func(m *ui.Menu) {
				menuDo(m, i18n.T("Split (⌘D)"), locked, func() { after = func() { a.dubSplit(id, -1) } })
				menuDo(m, i18n.T("Merge with previous (⇧⌘M)"), locked || i == 0, func() { after = func() { a.dubMerge(id, false) } })
				menuDo(m, i18n.T("Merge with next (⌘M)"), locked || i+1 >= len(p.Segments), func() { after = func() { a.dubMerge(id, true) } })
				menuDo(m, i18n.T("Insert a line below"), locked, func() { after = func() { a.dubInsert(id) } })
				m.Separator()
				menuDo(m, i18n.T("Delete"), locked, func() { after = func() { a.dubDelete(id) } })
			}).Label(i18n.T("Line actions"))
		})
		// The source, dimmed, when a translation shows.
		translated := a.dubLang != "" && strings.TrimSpace(p.TextFor(s, a.dubLang)) != ""
		if translated && !(a.dubEdit == id && a.dubEditL == "") {
			src := ui.Text(c, s.Text).FontSize(px(t, 12)).TextColor(t.TextMuted)
			if !locked && src.Clicked() {
				after = func() { a.dubBeginEdit(id, "") }
			}
		}
		a.dubLineText(c, s, locked, &after)
		line := s.Lines[dub.LangFor(s, a.dubLang)]
		if a.dubLang != "" && line.Shorter != "" {
			ui.Row(c.Key("shorter")).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
				ui.Text(c, i18n.T("Shorter: %s", line.Shorter)).FontSize(px(t, 12)).TextColor(t.TextMuted).Grow(1).MinWidth(0)
				if ui.Button(c.Key("use-shorter"), i18n.T("Use shorter rewrite")).Disabled(locked).Clicked() {
					after = func() { a.dubUseShorter(id) }
				}
			})
		}
		if a.dubLang != "" && line.Err != "" {
			ui.Text(c, i18n.T("The translation failed (%s).", line.Err)).FontSize(px(t, 11)).TextColor(t.Danger)
		}
		if qc := p.Track(a.dubLang).QC; qc != nil && a.dubLang != "" && !p.Track(a.dubLang).QCStale() {
			for _, q := range qc.Lines {
				if q.ID == id && q.Flagged {
					ui.Text(c, i18n.T("Heard: “%s”", q.Heard)).FontSize(px(t, 11)).TextColor(t.Warning)
				}
			}
		}
		if a.dubOpen == id {
			a.dubLineOptions(c, s, locked, &after)
		}
	})
	return after
}

// dubLineText is the line's text in the language shown, or its editor.
func (a *app) dubLineText(c *ui.Context, s dub.Segment, locked bool, after *func()) {
	t := c.Theme()
	id := s.ID
	if a.dubEdit == id {
		box := ui.TextArea(c.Key("edit"), &a.dubBuf).Lines(1, 6).AutoFocus().Label(i18n.T("Line text"))
		if box.Shortcut(ui.Cmd, ui.KeyEnter) {
			*after = a.dubCommitEdit
		}
		if box.Shortcut(0, ui.KeyEscape) {
			*after = a.dubCancelEdit
		}
		if box.Shortcut(ui.Cmd, ui.KeyD) {
			*after = func() { a.dubSplit(id, -1) }
		}
		ui.Row(c).Gap(px(t, 6)).Children(func() {
			if ui.Button(c.Key("edit-ok"), i18n.T("Done")).Clicked() {
				*after = a.dubCommitEdit
			}
			if ui.Button(c.Key("edit-cancel"), i18n.T("Cancel")).Clicked() {
				*after = a.dubCancelEdit
			}
		})
		return
	}
	text := s.Text
	missing := false
	if a.dubLang != "" {
		if tr := a.dub.TextFor(s, a.dubLang); strings.TrimSpace(tr) != "" {
			text = tr
		} else {
			missing = true // the source shows until it is translated
		}
	}
	var label ui.Element
	if strings.TrimSpace(text) == "" {
		label = ui.Text(c, i18n.T("(no text: click to write it)")).TextColor(t.TextMuted)
	} else {
		label = ui.Text(c, text)
	}
	label.Grow(1)
	if missing && strings.TrimSpace(text) != "" {
		ui.Text(c, i18n.T("Not translated yet: click to write the translation")).FontSize(px(t, 11)).TextColor(t.TextMuted)
	}
	if !locked && label.Clicked() {
		l := a.dubLang
		*after = func() { a.dubBeginEdit(id, l) }
	}
}

// dubLineOptions are a line's voice, volume, speed, direction, timing,
// language and speaker.
func (a *app) dubLineOptions(c *ui.Context, s dub.Segment, locked bool, after *func()) {
	t := c.Theme()
	p := a.dub
	id := s.ID
	ui.Column(c.Key("opts")).Gap(px(t, 6)).Padding(px(t, 8)).Radius(6).Background(t.Surface).Children(func() {
		ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Wrap().Children(func() {
			choices := a.dubVoiceChoices()
			labels := []string{i18n.T("The speaker's (%s)", a.dubVoiceLabel(p.Cast[s.Speaker]))}
			refs := []string{""}
			for _, v := range choices[1:] {
				labels = append(labels, v.label)
				refs = append(refs, v.ref)
			}
			cur := labels[max(slices.Index(refs, s.Voice), 0)]
			ui.Field(c, i18n.T("Voice"), func() {
				if ui.Select(c.Key("voice"), &cur, labels).Label(i18n.T("Voice")).Disabled(locked).Changed() {
					ref := refs[slices.Index(labels, cur)]
					a.dubSetLine(id, "voice", func(s *dub.Segment) { s.Voice = ref })
				}
			})
			speakers := p.SpeakerLabels()
			names := make([]string, len(speakers))
			for i, sp := range speakers {
				names[i] = p.SpeakerName(sp)
			}
			if len(names) > 0 {
				who := p.SpeakerName(s.Speaker)
				ui.Field(c, i18n.T("Speaker"), func() {
					if ui.Select(c.Key("speaker"), &who, names).Label(i18n.T("Speaker")).Disabled(locked).Changed() {
						sp := speakers[slices.Index(names, who)]
						a.dubSetLine(id, "speaker", func(s *dub.Segment) { s.Speaker, s.Parts = sp, nil })
					}
				})
			}
		})
		ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Wrap().Children(func() {
			vol := s.Volume
			ui.Field(c, i18n.T("Volume"), func() {
				if ui.StepSlider(c.Key("volume"), &vol, 0, 2, 0.1).Label(i18n.T("Volume")).Width(px(t, 140)).Disabled(locked).Changed() {
					a.dubSetLine(id, "volume", func(s *dub.Segment) { s.Volume = dubRound(vol) })
				}
			})
			ui.Text(c, strconv.Itoa(int(vol*100+0.5))+"%").FontSize(px(t, 11)).TextColor(t.TextMuted)
			speed := s.Speed
			if speed == 0 {
				speed = 1
			}
			ui.Field(c, i18n.T("Speed"), func() {
				if ui.StepSlider(c.Key("speed"), &speed, 0.5, 2, 0.05).Label(i18n.T("Speed")).Width(px(t, 140)).Disabled(locked).Changed() {
					v := dubRound(speed)
					if v == 1 {
						v = 0
					}
					a.dubSetLine(id, "speed", func(s *dub.Segment) { s.Speed = v })
				}
			})
			ui.Text(c, fmt.Sprintf("%.2f×", speed)).FontSize(px(t, 11)).TextColor(t.TextMuted)
		})
		dir := s.Direction
		ui.Field(c, i18n.T("Direction"), func() {
			in := ui.TextInput(c.Key("direction"), &dir).Placeholder(i18n.T("e.g. whispered, excited, slow")).Label(i18n.T("Direction")).Disabled(locked)
			if in.Changed() {
				d := strings.TrimSpace(dir)
				a.dubSetLine(id, "direction", func(s *dub.Segment) { s.Direction = d })
			}
		})
		if !dubCaps(p.Settings.Model).Instruct && s.Direction != "" {
			ui.Text(c, i18n.T("This voice model does not take directions; Prosody Mirror's notes still guide the translation.")).FontSize(px(t, 11)).TextColor(t.TextMuted)
		}
		ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
			start, end := s.Start, s.End
			ui.Field(c, i18n.T("Start"), func() {
				if ui.NumberInput(c.Key("start"), &start, 0, max(p.Source.Duration, s.End), 0.01).Label(i18n.T("Start")).Width(px(t, 100)).Disabled(locked).Changed() {
					*after = func() { a.dubSetTiming(id, start, s.End, true) }
				}
			})
			ui.Field(c, i18n.T("End"), func() {
				if ui.NumberInput(c.Key("end"), &end, 0, max(p.Source.Duration, s.End+10), 0.01).Label(i18n.T("End")).Width(px(t, 100)).Disabled(locked).Changed() {
					*after = func() { a.dubSetTiming(id, s.Start, end, true) }
				}
			})
			lockT := s.Locked
			if ui.Checkbox(c.Key("lock"), &lockT, i18n.T("Lock timing")).Disabled(locked).Changed() {
				a.dubSetLine(id, "lock", func(s *dub.Segment) { s.Locked = lockT })
			}
		})
	})
}

// dubShortcuts are the editor's keys.
func (a *app) dubShortcuts(c *ui.Context) {
	p := a.dub
	if p == nil || len(p.Segments) == 0 {
		return
	}
	if a.dubEdit == "" {
		if c.Shortcut(ui.Cmd, ui.KeyZ) {
			a.dubUndo()
		}
		if c.Shortcut(ui.Cmd|ui.Shift, ui.KeyZ) {
			a.dubRedo()
		}
	}
	if a.dubCur == "" {
		return
	}
	if c.Shortcut(ui.Cmd, ui.KeyD) && a.dubEdit == "" {
		a.dubSplit(a.dubCur, -1)
	}
	if c.Shortcut(ui.Cmd, ui.KeyM) {
		a.dubMerge(a.dubCur, true)
	}
	if c.Shortcut(ui.Cmd|ui.Shift, ui.KeyM) {
		a.dubMerge(a.dubCur, false)
	}
}

// menuDo adds an item that runs fn when chosen.
func menuDo(m *ui.Menu, label string, disabled bool, fn func()) {
	if m.Item(label).Disabled(disabled).Chosen() {
		fn()
	}
}
