package main

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/text"
	"github.com/hehehai/AudioSmith/internal/text/markup"
)

// audiobookPage is /audiobook: the manuscript and its render on the left,
// the book's settings on the right.
func (a *app) audiobookPage(c *ui.Context) {
	t := c.Theme()
	p := a.lfBook
	a.syncManuscript(p)
	a.lfToasts(c)
	locked := a.lfLocked(p)
	header(c, i18n.T("Audiobook"), func() {
		if ui.Button(c.Key("ab-stories"), i18n.T("Stories")).Tooltip(i18n.T("Multi-voice scripts, line by line")).Clicked() {
			a.navigate("/stories")
		}
	})
	ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Padding(px(t, 14), px(t, 20)).Gap(px(t, 10)).Children(func() {
			a.manuscriptToolbar(c, p, locked)
			a.manuscriptView(c, p, locked)
			a.manuscriptStats(c, p)
			a.manuscriptWarnings(c, p)
			a.lfRenderPanel(c, p)
		})
		ui.Box(c).Width(1).Background(t.Border)
		ui.Scroll(c.Key("ab-inspector")).Width(px(t, inspectorWidth+20)).Shrink(0).Background(t.Surface).Children(func() {
			ui.Column(c).Padding(px(t, 16)).Gap(px(t, 6)).Children(func() {
				a.lfSection(c, p, "setup", i18n.T("Setup"), func() { a.lfSetup(c, p, locked) })
				a.lfSection(c, p, "cast", i18n.T("Cast"), func() { a.bookCastCard(c, p, locked) })
				a.lfSection(c, p, "plan", i18n.T("Chapters"), func() { a.chapterPlan(c, p) })
				a.lfSection(c, p, "lexicon", i18n.T("Pronunciation"), func() { a.lexiconCard(c, p, locked) })
				a.lfSection(c, p, "production", i18n.T("Production"), func() { a.lfProduction(c, p, locked) })
				a.lfSection(c, p, "book", i18n.T("Cover & details"), func() { a.lfBookCard(c, p, locked) })
				a.lfSection(c, p, "project", i18n.T("Project"), func() { a.lfProjectCard(c, p, locked) })
			})
		})
	})
	a.lfConfirm(c, p)
	a.syncManuscript(p)
	a.autosaveLf(p)
}

// lfLocked reports whether a page's inputs are locked: its render runs,
// or it imports.
func (a *app) lfLocked(p *lfPage) bool {
	if p.importing {
		return true
	}
	r := a.lfRenders[p.render]
	return r != nil && r.rec.State == core.RenderRunning
}

// lfToasts shows the pending toasts with Undo.
func (a *app) lfToasts(c *ui.Context) {
	for _, u := range a.lfUndo {
		c.ToastAction(u.msg, i18n.T("Undo"), u.undo)
	}
	a.lfUndo = nil
}

// manuscriptToolbar: markup buttons, import, sample, clear.
func (a *app) manuscriptToolbar(c *ui.Context, p *lfPage, locked bool) {
	t := c.Theme()
	ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Wrap().Children(func() {
		if ui.Button(c.Key("mk-pause"), i18n.X("markup", "Pause")).Tooltip(i18n.T("Insert [pause 500ms]")).Disabled(locked).Clicked() {
			a.insertMarkup(p, "[pause 500ms]")
		}
		if ui.Button(c.Key("mk-voice"), i18n.T("Voice")).Tooltip(i18n.T("Switch voice: [voice:NAME] until the next switch or chapter")).Disabled(locked).Clicked() {
			a.insertVoice(p)
		}
		for _, s := range []struct{ key, label, tag string }{
			{"slow", i18n.T("Slow"), markup.Slow}, {"fast", i18n.T("Fast"), markup.Fast},
			{"emphasis", i18n.T("Emphasis"), markup.Emphasis}, {"spell", i18n.T("Spell"), markup.Spell},
		} {
			if ui.Button(c.Key("mk-"+s.key), s.label).Tooltip(i18n.T("Wrap the selection in [%s]…[/%s]", s.tag, s.tag)).Disabled(locked).Clicked() {
				a.wrapMarkup(p, "["+s.tag+"]", "[/"+s.tag+"]")
			}
		}
		ui.MenuButton(c.Key("mk-reactions"), i18n.T("Reactions"), func(m *ui.Menu) {
			for _, tag := range markup.DefaultNonVerbal {
				if m.Item(nonVerbalLabel(tag) + "  [" + tag + "]").Chosen() {
					a.insertMarkup(p, "["+tag+"]")
				}
			}
		}).Disabled(locked)
		ui.Spacer(c)
		if strings.TrimSpace(p.doc.Script) == "" {
			if ui.Button(c.Key("ab-sample"), i18n.T("Load sample")).Tooltip(i18n.T("A short book that shows chapters, voices, pauses and reactions")).Disabled(locked).Clicked() {
				a.loadSample(p)
			}
		}
		if ui.Button(c.Key("ab-import"), i18n.T("Import…")).Tooltip(i18n.T("TXT, Markdown, EPUB or DOCX, up to 64 MB, split into chapters")).Disabled(locked).Clicked() {
			a.importBook(p)
		}
		if ui.Button(c.Key("ab-clear"), i18n.T("Clear")).Disabled(locked || strings.TrimSpace(p.doc.Script) == "").Clicked() {
			p.confirm, p.confirmOpen = "clear", true
		}
	})
}

// manuscriptView is the editor.
func (a *app) manuscriptView(c *ui.Context, p *lfPage, locked bool) {
	t := c.Theme()
	st := p.ed
	st.Font = ui.Font{Family: "system-ui", Size: px(t, 15)}
	st.Placeholder = i18n.T("# Chapter One\n\nOnce upon a time… [pause 500ms] the end.\n\n# Chapter Two\n\n[voice:Mara] Hello!")
	st.Label = i18n.T("Manuscript")
	st.ReadOnly = locked
	box := ui.Column(c.Key("ms-box")).Grow(1).MinHeight(px(t, 160)).Radius(8).Border(1, t.Border).Background(editor.DefaultTheme(t).Background).
		Padding(px(t, 6), px(t, 4))
	if files := box.DroppedFiles(); len(files) > 0 && !locked {
		a.importBookFrom(p, files[0])
	}
	box.Children(func() {
		if p.importing {
			ui.Row(c).Gap(px(t, 6)).Padding(px(t, 6)).Children(func() {
				ui.Spinner(c)
				ui.Text(c, i18n.T("Importing…")).TextColor(t.TextMuted)
			})
		}
		editor.View(c.Key("ms"), st).Grow(1).MinHeight(0)
	})
}

// manuscriptStats: chapters · words · estimated runtime.
func (a *app) manuscriptStats(c *ui.Context, p *lfPage) {
	t := c.Theme()
	st := p.stats
	parts := []string{i18n.N(max(st.Chapters, 1), "%d chapter", "%d chapters", max(st.Chapters, 1))}
	if st.Words > 0 {
		parts = append(parts, i18n.N(st.Words, "%d word", "%d words", st.Words))
	}
	if st.Chars > 0 {
		parts = append(parts, i18n.N(st.Chars, "%d character", "%d characters", st.Chars))
	}
	parts = append(parts, i18n.T("%s est. runtime", clockRuntime(st.Seconds)))
	ui.Text(c.Key("ab-stats"), strings.Join(parts, " · ")).FontSize(px(t, 12)).TextColor(t.TextMuted)
}

// clockRuntime is M:SS, or H:MM from an hour.
func clockRuntime(seconds float64) string {
	s := int(seconds + 0.5)
	if s >= 3600 {
		return strconv.Itoa(s/3600) + ":" + twoDigits(s/60%60)
	}
	return strconv.Itoa(s/60) + ":" + twoDigits(s%60)
}

func twoDigits(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// manuscriptWarnings is "Before you create": unknown voices, empty
// chapters, tags read aloud. Dismissed until the next edit.
func (a *app) manuscriptWarnings(c *ui.Context, p *lfPage) {
	t := c.Theme()
	var warn []services.Problem
	for _, pr := range p.problems {
		switch pr.Code {
		case services.ProblemUnknownVoice, services.ProblemEmptyChapter, services.ProblemUnknownTag, services.ProblemMissingVoice:
			warn = append(warn, pr)
		}
	}
	if len(warn) == 0 || p.dismissed == p.fp || a.lfLocked(p) {
		return
	}
	ui.Column(c.Key("ab-warn")).Gap(px(t, 4)).Padding(px(t, 10)).Radius(8).Background(t.Warning.Alpha(0.12)).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, i18n.T("Before you create")).Bold().Grow(1)
			if ui.Button(c.Key("ab-warn-x"), "✕").Label(i18n.T("Dismiss")).Clicked() {
				p.dismissed = p.fp
			}
		})
		for i, pr := range warn {
			if i == 8 {
				ui.Text(c, i18n.N(len(warn)-8, "and %d more", "and %d more", len(warn)-8)).FontSize(px(t, 12)).TextColor(t.TextMuted)
				break
			}
			msg := problemText(pr)
			if pr.Span.End > pr.Span.Start {
				start := pr.Span.Start
				if ui.ButtonBase(c.Key("w-"+strconv.Itoa(i))).Label(msg).Children(func() {
					ui.Text(c, "• "+msg).FontSize(px(t, 12)).Grow(1).MinWidth(0)
				}).Clicked() {
					p.ed.Select(start, pr.Span.End)
				}
				continue
			}
			ui.Text(c, "• "+msg).FontSize(px(t, 12))
		}
	})
}

// bookCastCard maps every [voice:NAME] of the manuscript to a voice.
func (a *app) bookCastCard(c *ui.Context, p *lfPage, locked bool) {
	t := c.Theme()
	names := bookCast(p)
	if len(names) == 0 {
		ui.Text(c, i18n.T("Add [voice:NAME] tags to the manuscript to cast more voices; the narrator reads the rest.")).FontSize(px(t, 12)).TextColor(t.TextMuted)
		return
	}
	if p.doc.VoiceCast == nil {
		p.doc.VoiceCast = map[string]string{}
	}
	e := entryFor(p.doc.Model)
	for _, n := range names {
		ref := p.doc.VoiceCast[n]
		ui.Field(c, n, func() {
			a.lfVoiceSelect(c, "cast-"+n, &ref, e, i18n.T("Narrator voice"), locked)
		})
		if ref != p.doc.VoiceCast[n] {
			p.doc.VoiceCast[n] = ref
		}
	}
}

// chapterPlan lists the chapters as they will render (title · characters)
// with an audition each.
func (a *app) chapterPlan(c *ui.Context, p *lfPage) {
	t := c.Theme()
	if len(p.spec.Chapters) == 0 {
		ui.Text(c, i18n.T("Write or import a manuscript to see its chapters.")).FontSize(px(t, 12)).TextColor(t.TextMuted)
		return
	}
	ui.Text(c, i18n.T("Listen to one chapter with every setting before making the whole book; it warms the cache.")).FontSize(px(t, 11)).TextColor(t.TextMuted)
	ui.Scroll(c.Key("plan")).MaxHeight(px(t, 260)).Children(func() {
		ui.Column(c).Gap(px(t, 2)).Children(func() {
			for i, ch := range p.spec.Chapters {
				title := ch.Title
				if title == "" {
					title = i18n.T("Chapter %d", i+1)
				}
				chars := 0
				for _, l := range ch.Lines {
					for _, s := range l.Spans {
						chars += text.RuneLen(s.Text)
					}
				}
				what := "chapter:" + strconv.Itoa(i)
				ui.Row(c.Key("plan-"+strconv.Itoa(i))).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
					ui.Column(c).Grow(1).MinWidth(0).Children(func() {
						ui.Text(c, title).SingleLine().Ellipsis("…")
						ui.Text(c, i18n.N(chars, "%d character", "%d characters", chars)).FontSize(px(t, 11)).TextColor(t.TextMuted)
					})
					a.auditionButton(c, p, what, i18n.T("Preview chapter: %s", title))
				})
			}
		})
	})
}

// auditionButton renders, plays or stops an audition.
func (a *app) auditionButton(c *ui.Context, p *lfPage, what, label string) {
	st, r := a.auditionState(p, what)
	switch st {
	case "running":
		ui.Spinner(c).Label(i18n.T("Rendering the preview…"))
	case "done":
		play := "▶"
		if a.pl.Playing("audition:" + r.rec.ID) {
			play = "❚❚"
		}
		if ui.Button(c.Key("aud-"+what), play).Label(label).Tooltip(label).Clicked() {
			a.playAudition(r)
		}
	default:
		if ui.Button(c.Key("aud-"+what), "▶").Label(label).Tooltip(label).Disabled(a.engState != engineReady).Clicked() {
			a.startLf(p, what)
		}
	}
}

// lexiconCard is the book's pronunciations: word → say it as.
func (a *app) lexiconCard(c *ui.Context, p *lfPage, locked bool) {
	t := c.Theme()
	ui.Text(c, i18n.T("Words of this book and how to say them, on top of Settings → Pronunciation.")).FontSize(px(t, 11)).TextColor(t.TextMuted)
	dups := services.DuplicateWords(p.doc.Lexicon)
	remove := -1
	for i := range p.doc.Lexicon {
		e := &p.doc.Lexicon[i]
		ui.Row(c.Key("lex-"+strconv.Itoa(i))).Gap(px(t, 4)).AlignItems(ui.Center).Children(func() {
			ui.TextInput(c.Key("w"), &e.Word).Placeholder(i18n.T("Word")).Label(i18n.T("Word")).Grow(1).MinWidth(0).Disabled(locked)
			ui.Text(c, "→").TextColor(t.TextMuted)
			ui.TextInput(c.Key("s"), &e.Say).Placeholder(i18n.T("Say it as…")).Label(i18n.T("Say it as…")).Grow(1).MinWidth(0).Disabled(locked)
			if ui.Button(c.Key("x"), "✕").Label(i18n.T("Remove")).Disabled(locked).Clicked() {
				remove = i
			}
		})
	}
	if remove >= 0 {
		p.doc.Lexicon = append(p.doc.Lexicon[:remove:remove], p.doc.Lexicon[remove+1:]...)
	}
	if len(dups) > 0 {
		ui.Text(c, i18n.T("Keep one pronunciation per word: %s", strings.Join(dups, ", "))).FontSize(px(t, 12)).TextColor(t.Danger)
	}
	if ui.Button(c.Key("lex-add"), i18n.T("Add word")).Disabled(locked).Clicked() {
		p.doc.Lexicon = append(p.doc.Lexicon, services.LexiconEntry{})
	}
}
