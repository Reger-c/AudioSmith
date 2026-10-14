package main

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
	"github.com/hehehai/AudioSmith/internal/services"
)

// storiesPage is /stories: the review buffer, the lines and the render on
// the left; setup, cast and the rest on the right.
func (a *app) storiesPage(c *ui.Context) {
	t := c.Theme()
	p := a.lfStories
	a.lfToasts(c)
	locked := a.lfLocked(p)
	header(c, i18n.T("Stories"), func() {
		if ui.Button(c.Key("st-audiobook"), i18n.T("Audiobook")).Tooltip(i18n.T("One manuscript, chapters and voices in markup")).Clicked() {
			a.navigate("/audiobook")
		}
	})
	ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Padding(px(t, 14), px(t, 20)).Gap(px(t, 10)).Children(func() {
			a.storyToolbar(c, p, locked)
			a.reviewBuffer(c, p, locked)
			a.storyLines(c, p, locked)
			a.lfRenderPanel(c, p)
		})
		ui.Box(c).Width(1).Background(t.Border)
		ui.Scroll(c.Key("st-inspector")).Width(px(t, inspectorWidth+20)).Shrink(0).Background(t.Surface).Children(func() {
			ui.Column(c).Padding(px(t, 16)).Gap(px(t, 6)).Children(func() {
				a.lfSection(c, p, "setup", i18n.T("Setup"), func() { a.lfSetup(c, p, locked) })
				a.lfSection(c, p, "cast", i18n.T("Cast"), func() { a.storyCast(c, p, locked) })
				a.lfSection(c, p, "production", i18n.T("Production"), func() { a.lfProduction(c, p, locked) })
				a.lfSection(c, p, "book", i18n.T("Cover & details"), func() { a.lfBookCard(c, p, locked) })
				a.lfSection(c, p, "project", i18n.T("Project"), func() { a.lfProjectCard(c, p, locked) })
			})
		})
	})
	a.lfConfirm(c, p)
	a.autosaveLf(p)
}

// storyToolbar: import, add line, add chapter, sample, clear, and stats.
func (a *app) storyToolbar(c *ui.Context, p *lfPage, locked bool) {
	t := c.Theme()
	ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Wrap().Children(func() {
		if ui.Button(c.Key("st-import"), i18n.T("Import…")).Tooltip(i18n.T("TXT, Markdown, SRT, VTT, EPUB or DOCX, into the review buffer")).Disabled(locked).Clicked() {
			a.importStory(p)
		}
		if ui.Button(c.Key("st-add"), i18n.T("Add line")).Disabled(locked).Clicked() {
			a.addLine(p, false)
		}
		if ui.Button(c.Key("st-chapter"), i18n.T("Add chapter")).Disabled(locked).Clicked() {
			a.addLine(p, true)
		}
		if ui.Button(c.Key("st-clear"), i18n.T("Clear")).Disabled(locked || p.empty()).
			Tooltip(i18n.T("Remove every line and chapter from the script (the cast stays)")).Clicked() {
			p.confirm, p.confirmOpen = "clear", true
		}
		ui.Spacer(c)
		st := p.stats
		if st.Words+st.Chars > 0 {
			ui.Text(c, i18n.N(len(p.doc.Lines), "%d line", "%d lines", len(p.doc.Lines))+" · "+
				i18n.T("%s est. runtime", clockRuntime(st.Seconds))).FontSize(px(t, 12)).TextColor(t.TextMuted)
		}
	})
}

// reviewBuffer is the text pasted or imported, before it becomes lines.
func (a *app) reviewBuffer(c *ui.Context, p *lfPage, locked bool) {
	t := c.Theme()
	open := p.open["review"] || strings.TrimSpace(p.doc.ImportText) != "" || len(p.doc.Lines) == 0
	ui.Collapsible(c.Key("st-review"), i18n.T("Paste or import text"), &open, func() {
		ui.Column(c).Gap(px(t, 6)).Children(func() {
			box := ui.TextArea(c.Key("st-buffer"), &p.doc.ImportText).Placeholder(i18n.T("Paste a story. Auto-cast finds who speaks — [Name] tags, NAME: lines, or quoted dialogue — and casts a voice for each; Split makes one line per sentence, paragraph or chapter.")).
				Label(i18n.T("Text to review")).Lines(3, 12).MinHeight(0).Disabled(locked)
			if files := box.DroppedFiles(); len(files) > 0 && !locked {
				a.importStoryFrom(p, files[0])
			}
			ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Wrap().Children(func() {
				empty := strings.TrimSpace(p.doc.ImportText) == ""
				if primaryButton(c.Key("st-autocast"), i18n.T("Auto-cast")).Tooltip(i18n.T("Find who speaks and give each a voice")).
					Disabled(locked || empty).Clicked() {
					a.autoCastStory(p)
				}
				if ui.Button(c.Key("st-split"), i18n.T("Split into lines")).Disabled(locked || empty).Clicked() {
					a.splitStory(p)
				}
				labels := []string{i18n.T("Sentences"), i18n.T("Paragraphs"), i18n.T("Chapters")}
				i := slices.Index(splitModes, p.doc.SplitBy)
				if i < 0 {
					i = 1
				}
				cur := labels[i]
				if ui.Select(c.Key("st-splitby"), &cur, labels).Label(i18n.T("Split by")).Disabled(locked).Changed() {
					p.doc.SplitBy = splitModes[slices.Index(labels, cur)]
				}
				if p.doc.SplitBy == "sentences" {
					mc := float64(p.doc.MaxChars)
					if mc == 0 {
						mc = 500
					}
					if ui.NumberInput(c.Key("st-maxchars"), &mc, 40, 2000, 10).Label(i18n.T("Max characters per line")).Width(px(t, 110)).Disabled(locked).Changed() {
						p.doc.MaxChars = int(mc)
					}
				}
				if len(p.doc.Lines) == 0 && empty && ui.Button(c.Key("st-sample"), i18n.T("Load sample")).Disabled(locked).Clicked() {
					a.loadSampleStory(p)
				}
			})
			ui.Text(c, i18n.T("Every line is a take of its own. For one narrator, paragraphs or whole chapters flow better than sentences.")).FontSize(px(t, 11)).TextColor(t.TextMuted)
		})
	})
	p.open["review"] = open
}

// storyLines is the line list.
func (a *app) storyLines(c *ui.Context, p *lfPage, locked bool) {
	t := c.Theme()
	lines := p.doc.Lines
	if len(lines) == 0 {
		ui.Column(c.Key("st-empty")).Grow(1).Center().Gap(px(t, 8)).Padding(px(t, 24)).Radius(10).Border(1, t.Border).Children(func() {
			ui.Text(c, i18n.T("New story")).FontSize(px(t, 15)).Bold()
			ui.Text(c, i18n.T("Start your story with dialogue and narration lines, and give each character a voice in the Cast.")).
				TextColor(t.TextMuted).TextAlign(ui.Center).MaxWidth(px(t, 420))
			ui.Row(c).Gap(px(t, 8)).Children(func() {
				if ui.Button(c.Key("st-empty-sample"), i18n.T("Load sample")).Disabled(locked).Clicked() {
					a.loadSampleStory(p)
				}
				if primaryButton(c.Key("st-empty-add"), i18n.T("Add first line")).Disabled(locked).Clicked() {
					a.addLine(p, false)
				}
			})
		})
		return
	}
	var after func() // a change to the lines, made once they are built
	p.lines.Key = func(i int) any {
		if i < len(p.doc.Lines) {
			return p.doc.Lines[i].ID
		}
		return i
	}
	ui.List(c.Key("st-lines"), &p.lines, len(lines)).Grow(1).MinHeight(px(t, 160)).Gap(px(t, 6)).ItemKey(p.lines.Key).Rows(func(r ui.ListRow) {
		i := r.Index
		if i >= len(p.doc.Lines) {
			return
		}
		l := &p.doc.Lines[i]
		if f := a.storyLine(c, p, i, l, locked); f != nil {
			after = f
		}
	})
	if after != nil {
		after()
	}
}

// storyLine is one line's card; it returns a change to make after the list.
func (a *app) storyLine(c *ui.Context, p *lfPage, i int, l *services.StoryLine, locked bool) (after func()) {
	t := c.Theme()
	sel := p.sel == i
	card := ui.Column(c.Key("line-"+l.ID)).Gap(px(t, 6)).Padding(px(t, 8), px(t, 10)).Radius(8).Border(1, t.Border).Background(t.Surface)
	if sel {
		card.Border(2, t.Accent)
	}
	card.Children(func() {
		ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
			if l.Chapter {
				ui.Text(c, i18n.T("Chapter")).Bold().TextColor(t.Accent)
				ui.TextInput(c.Key("title"), &l.Text).Label(i18n.T("Chapter title")).Grow(1).MinWidth(0).Disabled(locked)
			} else {
				ui.Text(c, strconv.Itoa(a.lineNumber(p, i))).FontSize(px(t, 11)).TextColor(t.TextMuted).Width(px(t, 22))
				a.characterSelect(c, p, l, locked)
				if l.Voice != "" {
					ui.Badge(c, i18n.T("voice · %s", a.lfVoiceLabel(l.Voice)))
				}
				if l.Speed > 0 {
					ui.Badge(c, strconv.FormatFloat(l.Speed, 'f', 2, 64)+"×")
				}
				if l.PauseAfter > 0 {
					ui.Badge(c, i18n.T("then %d ms", l.PauseAfter))
				}
				ui.Spacer(c)
				a.auditionButton(c, p, "line:"+l.ID, i18n.T("Preview this line"))
			}
			if l.Chapter {
				ui.Spacer(c)
			}
			if iconButton(c.Key("more"), icons.MenuDots, i18n.T("Line options")).Tooltip(i18n.T("Line options")).Disabled(l.Chapter).Clicked() {
				if sel {
					p.sel = -1
				} else {
					p.sel = i
				}
			}
			if iconButton(c.Key("up"), icons.AltArrowUp, i18n.T("Move up")).Disabled(locked || i == 0).Clicked() {
				after = func() { a.moveLine(p, i, -1) }
			}
			if iconButton(c.Key("down"), icons.AltArrowDown, i18n.T("Move down")).Disabled(locked || i == len(p.doc.Lines)-1).Clicked() {
				after = func() { a.moveLine(p, i, 1) }
			}
			if iconButton(c.Key("del"), icons.Close, i18n.T("Remove line")).Tooltip(i18n.T("Remove line")).Disabled(locked).Clicked() {
				after = func() { a.deleteLine(p, i) }
			}
		})
		if l.Chapter {
			return
		}
		ui.TextArea(c.Key("text"), &l.Text).Placeholder(i18n.T("Dialogue or narration… [pause 500ms] for a silent break")).
			Label(i18n.T("Line %d", a.lineNumber(p, i))).Lines(1, 10).MinHeight(0).Disabled(locked)
		if sel {
			a.lineOptions(c, p, l, locked)
		}
	})
	return after
}

// lineNumber is a line's number among the spoken lines (1-based).
func (a *app) lineNumber(p *lfPage, i int) int {
	n := 0
	for j := 0; j <= i && j < len(p.doc.Lines); j++ {
		if !p.doc.Lines[j].Chapter {
			n++
		}
	}
	return n
}

// characterSelect picks a line's character.
func (a *app) characterSelect(c *ui.Context, p *lfPage, l *services.StoryLine, locked bool) {
	labels, ids := []string{i18n.T("Narrator")}, []string{""}
	for _, m := range p.doc.Cast {
		name := m.Name
		if strings.TrimSpace(name) == "" {
			name = i18n.T("Character")
		}
		labels, ids = append(labels, name), append(ids, m.ID)
	}
	cur := labels[0]
	if i := slices.Index(ids, l.Character); i >= 0 {
		cur = labels[i]
	}
	if ui.Select(c.Key("who"), &cur, labels).Label(i18n.T("Character")).Disabled(locked).Changed() {
		if i := slices.Index(labels, cur); i >= 0 {
			l.Character = ids[i]
		}
	}
}

// lineOptions: voice override, speed, pause after, tones.
func (a *app) lineOptions(c *ui.Context, p *lfPage, l *services.StoryLine, locked bool) {
	t := c.Theme()
	e := entryFor(p.doc.Model)
	ui.Column(c.Key("opts")).Gap(px(t, 6)).Padding(px(t, 8)).Radius(6).Background(t.Background).Children(func() {
		ui.Field(c, i18n.T("Voice of this line"), func() {
			a.lfVoiceSelect(c, "line-voice", &l.Voice, e, i18n.T("The character's voice"), locked)
		})
		ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
			sp := l.Speed
			if sp == 0 {
				sp = p.doc.Speed
				if sp <= 0 {
					sp = 1
				}
			}
			ui.Text(c, i18n.T("Speed %.2f×", sp)).FontSize(px(t, 12)).Width(px(t, 90))
			if snapped(ui.Slider(c.Key("line-speed"), &sp, 0.5, 2).Label(i18n.T("Speed")).Grow(1).Disabled(locked), &sp, 0.05).Changed() {
				l.Speed = sp
			}
			if l.Speed > 0 && ui.Button(c.Key("speed-reset"), i18n.T("Reset")).Disabled(locked).Clicked() {
				l.Speed = 0
			}
		})
		ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, i18n.T("Pause after (ms)")).FontSize(px(t, 12)).Grow(1)
			ms := float64(l.PauseAfter)
			if ui.NumberInput(c.Key("pause-after"), &ms, 0, 10000, 50).Label(i18n.T("Pause after (ms)")).Width(px(t, 110)).Disabled(locked).Changed() {
				l.PauseAfter = int(ms)
			}
		})
		ui.Row(c).Gap(px(t, 4)).Wrap().AlignItems(ui.Center).Children(func() {
			ui.Text(c, i18n.T("Tone")).FontSize(px(t, 12)).TextColor(t.TextMuted)
			for _, tag := range toneTags {
				if ui.Button(c.Key("tone-"+tag), nonVerbalLabel(tag)).Tooltip("[" + tag + "]").Disabled(locked).Clicked() {
					appendTag(l, "["+tag+"]")
				}
			}
			if ui.Button(c.Key("ins-pause"), i18n.T("Insert pause")).Disabled(locked).Clicked() {
				appendTag(l, "[pause 500ms]")
			}
		})
		ui.Text(c, i18n.T("Switch voice inside a line with [voice:NAME] … [voice:default]; NAME is a character or a voice.")).FontSize(px(t, 11)).TextColor(t.TextMuted)
	})
}

// storyCast is the cast panel.
func (a *app) storyCast(c *ui.Context, p *lfPage, locked bool) {
	t := c.Theme()
	e := entryFor(p.doc.Model)
	if len(p.doc.Cast) == 0 {
		ui.Text(c, i18n.T("No characters yet: Auto-cast finds them, or add one.")).FontSize(px(t, 12)).TextColor(t.TextMuted)
	}
	remove := ""
	for i := range p.doc.Cast {
		m := &p.doc.Cast[i]
		ui.Column(c.Key("cast-"+m.ID)).Gap(px(t, 4)).Padding(px(t, 6)).Radius(6).Border(1, t.Border).Children(func() {
			ui.Row(c).Gap(px(t, 4)).AlignItems(ui.Center).Children(func() {
				ui.TextInput(c.Key("name"), &m.Name).Placeholder(i18n.T("Character name")).Label(i18n.T("Character name")).Grow(1).MinWidth(0).Disabled(locked)
				n := 0
				for _, l := range p.doc.Lines {
					if l.Character == m.ID {
						n++
					}
				}
				ui.Text(c, i18n.N(n, "%d line", "%d lines", n)).FontSize(px(t, 11)).TextColor(t.TextMuted)
				if iconButton(c.Key("x"), icons.Close, i18n.T("Remove character")).Tooltip(i18n.T("Remove character: its lines go to the narrator")).Disabled(locked).Clicked() {
					remove = m.ID
				}
			})
			a.lfVoiceSelect(c, "voice", &m.Voice, e, i18n.T("Default voice"), locked)
		})
	}
	if remove != "" {
		a.removeCast(p, remove)
	}
	ui.Row(c).Gap(px(t, 6)).Children(func() {
		if ui.Button(c.Key("cast-add"), i18n.T("Add character")).Disabled(locked).Clicked() {
			p.doc.Cast = append(p.doc.Cast, services.CastMember{ID: core.NewID(time.Now()), Name: i18n.T("Character")})
		}
		if ui.Button(c.Key("cast-voices"), i18n.T("Voices…")).Tooltip(i18n.T("Clone, design or pick voices")).Clicked() {
			a.navigate("/voices")
		}
	})
}
