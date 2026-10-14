package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/store"
)

// projectsPage is /projects.
func (a *app) projectsPage(c *ui.Context) {
	t := c.Theme()
	s := &a.projects
	a.refreshLibrary()
	header(c, i18n.T("Projects"), func() {
		if ui.Button(c.Key("pj-new-story"), i18n.T("New story")).Clicked() {
			a.navigate("/stories")
		}
		if ui.Button(c.Key("pj-new-book"), i18n.T("New audiobook")).Clicked() {
			a.navigate("/audiobook")
		}
	})
	ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c.Key("pj-filters")).Width(px(t, 190)).Shrink(0).Padding(px(t, 12), px(t, 10)).Gap(px(t, 2)).Background(t.Surface).Children(func() {
			for i, cat := range libCategories {
				n := s.counts[cat]
				sel := s.category == i
				row := ui.ButtonBase(c.Key("cat-"+strconv.Itoa(i))).Padding(px(t, 6), px(t, 8)).Radius(6).Label(categoryLabel(cat)).Selected(sel)
				if sel {
					row.Background(t.Accent.Alpha(0.15))
				} else if row.Hovered() {
					row.Background(t.SurfaceHover)
				}
				if row.Clicked() {
					s.category = i
					clear(s.selected)
				}
				row.Children(func() {
					ui.Text(c, categoryLabel(cat)).Grow(1).SingleLine()
					ui.Text(c, strconv.Itoa(n)).FontSize(px(t, 12)).TextColor(t.TextMuted)
				})
			}
		})
		ui.Box(c).Width(1).Background(t.Border)
		ui.Column(c).Grow(1).MinWidth(0).Padding(px(t, 12), px(t, 20)).Gap(px(t, 10)).Children(func() {
			a.libraryToolbar(c)
			a.libraryItems(c)
		})
	})
	a.libraryDialogs(c)
}

// libraryToolbar: search, view, selection.
func (a *app) libraryToolbar(c *ui.Context) {
	t := c.Theme()
	s := &a.projects
	ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
		if ui.TextInput(c.Key("pj-search"), &s.query).Placeholder(i18n.T("Search projects, voices, takes, exports…")).Label(i18n.T("Search the library")).
			Grow(1).MinWidth(px(t, 160)).MaxWidth(px(t, 380)).Changed() {
			clear(s.selected)
		}
		ui.Segmented(c.Key("pj-view"), &s.cards, i18n.T("List"), i18n.T("Cards")).Label(i18n.T("View"))
		ui.Spacer(c)
		n := len(s.selected)
		if n == 0 {
			if ui.Button(c.Key("pj-select"), i18n.T("Select visible")).Disabled(len(s.items) == 0).Clicked() {
				for _, it := range s.items {
					s.selected[libKey(it)] = true
				}
			}
			return
		}
		if ui.Button(c.Key("pj-clear"), i18n.T("Clear")).Clicked() {
			clear(s.selected)
			clear(s.errors)
		}
		if ui.Button(c.Key("pj-delete"), i18n.T("Delete selected (%d)", n)).Clicked() {
			s.deleteOpen = true
		}
	})
}

// libraryItems is the list or the cards.
func (a *app) libraryItems(c *ui.Context) {
	t := c.Theme()
	s := &a.projects
	items := s.items
	if len(items) == 0 {
		ui.Column(c.Key("pj-empty")).Grow(1).Center().Gap(px(t, 8)).Children(func() {
			switch {
			case s.loading && s.loaded == "":
				ui.Text(c, i18n.T("Loading…")).TextColor(t.TextMuted)
			case strings.TrimSpace(s.query) != "":
				ui.Text(c, i18n.T("No matches for “%s”", strings.TrimSpace(s.query))).TextColor(t.TextMuted)
			default:
				ui.Text(c, i18n.T("Nothing here yet")).FontSize(px(t, 15)).Bold()
				ui.Text(c, i18n.T("Write a story, make an audiobook, design a voice or generate a take, and it shows up here.")).TextColor(t.TextMuted)
			}
		})
		return
	}
	if s.cards == 1 {
		ui.GridView(c.Key("pj-grid"), &s.grid, len(items), px(t, 280), px(t, 150), func(i int) {
			it := items[i]
			a.libraryCard(c, it)
		}).Grow(1).Gap(px(t, 10))
	} else {
		s.list.Key = func(i int) any {
			if i < len(items) {
				return libKey(items[i])
			}
			return i
		}
		ui.List(c.Key("pj-list"), &s.list, len(items)).Grow(1).Gap(px(t, 2)).ItemKey(s.list.Key).Rows(func(r ui.ListRow) {
			a.libraryRow(c, items[r.Index])
		})
	}
	if len(items) >= s.limit {
		if ui.Button(c.Key("pj-more"), i18n.T("Show more")).Clicked() {
			s.limit += libPageSize
		}
	}
}

// libraryRow is one item in the list.
func (a *app) libraryRow(c *ui.Context, it store.LibraryItem) {
	t := c.Theme()
	s := &a.projects
	key := libKey(it)
	ui.Column(c.Key("it-"+key)).Gap(px(t, 4)).Padding(px(t, 8), px(t, 10)).Radius(8).Background(func() ui.Color {
		if s.selected[key] {
			return t.Accent.Alpha(0.10)
		}
		return ui.Transparent
	}()).Children(func() {
		ui.Row(c).Gap(px(t, 10)).AlignItems(ui.Center).Children(func() {
			on := s.selected[key]
			if ui.Checkbox(c.Key("sel"), &on, "").Label(i18n.T("Select %s", it.Title)).Changed() {
				if on {
					s.selected[key] = true
				} else {
					delete(s.selected, key)
				}
			}
			a.itemAvatar(c, it)
			ui.Column(c).Grow(1).MinWidth(0).Gap(px(t, 2)).Children(func() {
				a.itemTitle(c, it)
				ui.Text(c, a.itemMeta(it)).FontSize(px(t, 11)).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
				if e := s.errors[key]; e != "" {
					ui.Text(c, e).FontSize(px(t, 11)).TextColor(t.Danger)
				}
			})
			a.itemActions(c, it)
		})
		if it.Source == "render" {
			a.renderDetails(c, it)
		}
	})
}

// libraryCard is one item as a card.
func (a *app) libraryCard(c *ui.Context, it store.LibraryItem) {
	t := c.Theme()
	s := &a.projects
	key := libKey(it)
	card := ui.Column(c.Key("card-"+key)).Grow(1).Gap(px(t, 6)).Padding(px(t, 10)).Radius(10).Border(1, t.Border).Background(t.Surface)
	if s.selected[key] {
		card.Border(2, t.Accent)
	}
	card.Children(func() {
		ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
			a.itemAvatar(c, it)
			ui.Column(c).Grow(1).MinWidth(0).Children(func() {
				a.itemTitle(c, it)
			})
			on := s.selected[key]
			if ui.Checkbox(c.Key("sel"), &on, "").Label(i18n.T("Select %s", it.Title)).Changed() {
				if on {
					s.selected[key] = true
				} else {
					delete(s.selected, key)
				}
			}
		})
		ui.Text(c, a.itemMeta(it)).FontSize(px(t, 11)).TextColor(t.TextMuted).MaxLines(2).Ellipsis("…")
		ui.Spacer(c)
		ui.Row(c).Gap(px(t, 6)).Children(func() { a.itemActions(c, it) })
	})
}

// itemAvatar: a voice's portrait, else the kind's icon.
func (a *app) itemAvatar(c *ui.Context, it store.LibraryItem) {
	t := c.Theme()
	if it.Source == "voice" {
		if v := a.findVoice(it.ID); v != nil {
			ui.Avatar(c, v.Name, a.portrait(v))
			return
		}
	}
	ui.Column(c).Width(px(t, 32)).Height(px(t, 32)).Center().Radius(16).Background(t.Accent.Alpha(0.12)).Children(func() {
		ui.Icon(c, itemIcon(it).Linear).TextColor(tok(t).Text2)
	})
}

// itemTitle is the name, an inline editor while renaming.
func (a *app) itemTitle(c *ui.Context, it store.LibraryItem) {
	t := c.Theme()
	s := &a.projects
	key := libKey(it)
	if s.renaming == key {
		ui.Row(c).Gap(px(t, 4)).AlignItems(ui.Center).Children(func() {
			in := ui.TextInput(c.Key("rename"), &s.renameText).Label(i18n.T("Project name")).Grow(1).MinWidth(0).AutoFocus()
			if in.Submitted() || ui.Button(c.Key("rename-ok"), i18n.T("Save name")).Disabled(strings.TrimSpace(s.renameText) == "").Clicked() {
				if strings.TrimSpace(s.renameText) != "" {
					a.renameItem(it, s.renameText)
					s.renaming = ""
				}
			}
			if ui.Button(c.Key("rename-cancel"), i18n.T("Cancel")).Clicked() || c.Shortcut(0, ui.KeyEscape) {
				s.renaming = ""
			}
		})
		return
	}
	title := strings.Join(strings.Fields(it.Title), " ")
	if title == "" {
		title = i18n.T("Untitled")
	}
	if ui.ButtonBase(c.Key("title")).Label(i18n.T("Open %s", title)).Children(func() {
		ui.Text(c, title).Bold().MaxLines(2).Ellipsis("…").Grow(1).MinWidth(0)
	}).Clicked() {
		a.openItem(it)
	}
}

// itemActions are an item's buttons, by kind.
func (a *app) itemActions(c *ui.Context, it store.LibraryItem) {
	s := &a.projects
	key := libKey(it)
	switch it.Source {
	case "dub", "workflow", "call": // M4 (thread #19), M6 (thread #21), M6b (thread #23)
		if ui.Button(c.Key("open"), i18n.T("Open")).Clicked() {
			a.openItem(it)
		}
	case "project":
		if ui.Button(c.Key("open"), i18n.T("Open")).Clicked() {
			a.openItem(it)
		}
		if ui.Button(c.Key("rename"), "✎").Label(i18n.T("Rename")).Tooltip(i18n.T("Rename")).Clicked() {
			s.renaming, s.renameText = key, it.Title
		}
	case "render":
		if r := a.renderFor(it.ID); r != nil && r.body.Output != nil {
			id := "book:" + r.rec.ID
			label := "▶"
			if a.pl.Playing(id) {
				label = "❚❚"
			}
			book := r.body.Output.Book
			if ui.Button(c.Key("play"), label).Label(i18n.T("Play")).Tooltip(i18n.T("Play")).Clicked() {
				if err := a.pl.Toggle(id, book); err != nil {
					a.toast(i18n.T("Can't play: %v", err))
				}
			}
			if ui.Button(c.Key("reveal"), i18n.T("Show in Finder")).Clicked() {
				mygo.Shell.ShowItemInFolder(book)
			}
		}
	case "voice":
		if ui.Button(c.Key("use"), i18n.T("Use voice")).Clicked() {
			if v := a.findVoice(it.ID); v != nil {
				a.useVoiceInStudio(v)
			}
		}
	case "take":
		if tk := a.findTake(it.ID); tk != nil {
			label := "▶"
			if a.pl.Playing(tk.ID) {
				label = "❚❚"
			}
			if ui.Button(c.Key("play"), label).Label(i18n.T("Play")).Tooltip(i18n.T("Play")).Clicked() {
				a.play(tk)
			}
			if ui.Button(c.Key("reuse"), i18n.T("Reuse script")).Clicked() {
				a.reuse(tk)
			}
		}
	case "export":
		path := it.Detail
		if ui.Button(c.Key("reveal"), i18n.T("Show in Finder")).Clicked() {
			mygo.Shell.ShowItemInFolder(path)
		}
	}
	if ui.Button(c.Key("delete"), "✕").Label(i18n.T("Delete")).Tooltip(i18n.T("Delete")).Clicked() {
		clear(s.selected)
		s.selected[key] = true
		s.deleteOpen = true
	}
}

// renderDetails is "How it was made".
func (a *app) renderDetails(c *ui.Context, it store.LibraryItem) {
	t := c.Theme()
	s := &a.projects
	key := libKey(it)
	open := s.details[key]
	ui.Collapsible(c.Key("how-"+key), i18n.T("How it was made"), &open, func() {
		r := a.renderFor(it.ID)
		if r == nil {
			ui.Text(c, i18n.T("Loading…")).FontSize(px(t, 12)).TextColor(t.TextMuted)
			return
		}
		sum := r.body.Summary
		if sum.Model == "" {
			ui.Text(c, i18n.T("Rendered before details were recorded.")).FontSize(px(t, 12)).TextColor(t.TextMuted)
			return
		}
		row := func(k, v string) {
			if v == "" {
				return
			}
			ui.Row(c.Key("how-" + k)).Gap(px(t, 8)).Children(func() {
				ui.Text(c, k).FontSize(px(t, 12)).TextColor(t.TextMuted).Width(px(t, 110))
				ui.Text(c, v).FontSize(px(t, 12)).Grow(1).MinWidth(0).Selectable()
			})
		}
		lang := sum.Lang
		if lang == "" {
			lang = i18n.T("Auto-detect")
		} else {
			lang = languageName(lang)
		}
		format := strings.ToUpper(sum.Format)
		if format == "" {
			format = "WAV"
		}
		row(i18n.T("Voices"), a.voiceNames(sum.Voices))
		row(i18n.T("Speed"), speedRange(sum.MinSpeed, sum.MaxSpeed))
		row(i18n.T("Engine"), modelName(sum.Model))
		row(i18n.T("Language"), lang)
		row(i18n.T("Format"), format)
		size := i18n.N(sum.Chapters, "%d chapter", "%d chapters", sum.Chapters) + " · " + i18n.N(sum.Lines, "%d line", "%d lines", sum.Lines)
		if sum.Words > 0 {
			size += " · " + i18n.N(sum.Words, "%d word", "%d words", sum.Words)
		}
		if sum.Chars > 0 {
			size += " · " + i18n.N(sum.Chars, "%d character", "%d characters", sum.Chars)
		}
		row(i18n.T("Size"), size)
		row(i18n.T("Settings"), productionText(sum))
		if out := r.body.Output; out != nil && out.Loudness.Target != "" {
			row(i18n.T("Loudness"), fmt.Sprintf("%s · %.1f LUFS · %.1f dBTP", strings.ToUpper(out.Loudness.Target), out.Loudness.Integrated, out.Loudness.TruePeak))
		}
	})
	s.details[key] = open
}

// productionText lists a render's production settings.
func productionText(s services.RenderSummary) string {
	p := s.Prod
	var parts []string
	if p.Steps > 0 {
		parts = append(parts, i18n.T("steps %d", p.Steps))
	}
	if p.Guidance > 0 {
		parts = append(parts, i18n.T("guidance %.1f", p.Guidance))
	}
	if p.Seed != 0 {
		parts = append(parts, i18n.T("seed %d", p.Seed))
	}
	parts = append(parts, i18n.T("line gap %d ms", p.LineGapMs), i18n.T("paragraph gap %d ms", p.ParagraphGapMs))
	if p.VaryRepeats {
		parts = append(parts, i18n.T("vary repeated lines"))
	}
	if p.KeepEdges {
		parts = append(parts, i18n.T("engine silence kept"))
	} else {
		parts = append(parts, i18n.T("engine silence trimmed"))
	}
	if s.Stems {
		parts = append(parts, i18n.T("stems"))
	}
	return strings.Join(parts, " · ")
}

// libraryDialogs: delete the selection.
func (a *app) libraryDialogs(c *ui.Context) {
	t := c.Theme()
	s := &a.projects
	if s.deleteOpen {
		var items []store.LibraryItem
		for _, it := range s.items {
			if s.selected[libKey(it)] {
				items = append(items, it)
			}
		}
		if len(items) == 0 {
			s.deleteOpen = false
		}
		ui.Modal(c, &s.deleteOpen, func() {
			ui.Column(c.Key("pj-del")).Width(px(t, 440)).Gap(px(t, 10)).Padding(px(t, 18)).Children(func() {
				ui.Text(c, i18n.N(len(items), "Delete %d item?", "Delete %d items?", len(items))).FontSize(px(t, 15)).Bold()
				ui.Scroll(c).MaxHeight(px(t, 200)).Children(func() {
					ui.Column(c).Gap(px(t, 2)).Children(func() {
						for _, it := range items {
							line := "• " + strings.Join(strings.Fields(it.Title), " ")
							if e := s.errors[libKey(it)]; e != "" {
								line += " — " + e
							}
							ui.Text(c, line).FontSize(px(t, 12)).SingleLine().Ellipsis("…")
						}
					})
				})
				ui.Text(c, i18n.T("This cannot be undone. Projects and export records are removed; deleting voices, takes or renders removes their audio too. Exported files are kept.")).
					FontSize(px(t, 12)).TextColor(t.TextMuted)
				ui.Row(c).Gap(px(t, 8)).Children(func() {
					ui.Spacer(c)
					if ui.Button(c.Key("del-cancel"), i18n.T("Cancel")).AutoFocus().Clicked() {
						s.deleteOpen = false
					}
					label := i18n.N(len(items), "Delete %d item", "Delete %d items", len(items))
					if len(s.errors) > 0 {
						label = i18n.T("Retry")
					}
					if primaryButton(c.Key("del-ok"), label).Background(t.Danger).Clicked() {
						a.deleteItems(items)
					}
				})
			})
		})
	}
}

// projectKindOf is the kind of a project item.
func projectKindOf(it store.LibraryItem) core.ProjectKind { return core.ProjectKind(it.Category) }
