package main

// The History page: every take, newest first, with search (the library's
// full-text index), filters (all / starred, by model), star, play, reuse
// and delete, and a details pane with the take's whole recipe and render.
// Its cap (Settings → Studio: keep at most N takes) prunes the oldest
// takes that are not starred.

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// historyView is the History page's view state.
type historyView struct {
	query     string
	filter    int    // 0 all, 1 starred
	model     string // a model's name, or "" (shown as All models)
	selected  string // the take in the details pane
	searchFor string // the query results are for
	results   map[string]bool
	keepTakes float64 // Settings → Studio
}

// searchTakes looks the query up in the library's full-text index in
// the background; the list shows the results once they arrive.
func (a *app) searchTakes() {
	q := strings.TrimSpace(a.history.query)
	if q == "" {
		a.history.results, a.history.searchFor = nil, ""
		return
	}
	svc := a.lib.svc.Takes
	a.background(func() {
		takes, err := svc.List(context.Background(), core.TakeQuery{Search: q})
		a.post(func() {
			if strings.TrimSpace(a.history.query) != q {
				return // typed on meanwhile
			}
			if err != nil {
				a.toast(i18n.T("Could not search: %v", err))
				return
			}
			res := map[string]bool{}
			for _, t := range takes {
				res[t.ID] = true
			}
			a.history.results, a.history.searchFor = res, q
		})
	})
}

// historyTakes are the takes the filters let through, newest first.
func (a *app) historyTakes() []core.Take {
	h := &a.history
	q := strings.TrimSpace(h.query)
	var out []core.Take
	for _, t := range a.takes {
		if h.filter == 1 && !t.Starred {
			continue
		}
		if h.model != "" && modelName(t.Model) != h.model {
			continue
		}
		if q != "" {
			if h.searchFor == q {
				if !h.results[t.ID] {
					continue
				}
			} else if !strings.Contains(strings.ToLower(t.Text), strings.ToLower(q)) {
				continue // until the index answers
			}
		}
		out = append(out, t)
	}
	return out
}

// historyModels are the models of the takes, for the filter.
func (a *app) historyModels() []string {
	var out []string
	for _, t := range a.takes {
		if n := modelName(t.Model); !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

func (a *app) historyPage(c *ui.Context) {
	t := c.Theme()
	h := &a.history
	header(c, i18n.T("History"), func() {
		ui.Text(c, i18n.N(len(a.takes), "%d take", "%d takes", len(a.takes))).FontSize(px(t, 12)).TextColor(t.TextMuted)
		if ui.Button(c.Key("open-folder"), i18n.T("Open folder")).Clicked() {
			mygo.Shell.OpenPath(a.takeStore.AudioDir())
		}
	})
	ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Padding(px(t, 10), px(t, 20), 0).Children(func() {
		if ui.TextInput(c.Key("history-search"), &h.query).Placeholder(i18n.T("Search the scripts")).Label(i18n.T("Search takes")).Grow(1).Changed() {
			a.searchTakes()
		}
		ui.Segmented(c.Key("history-filter"), &h.filter, i18n.T("All"), i18n.T("Starred")).Label(i18n.T("Show"))
		models := append([]string{i18n.T("All models")}, a.historyModels()...)
		choice := h.model
		if choice == "" {
			choice = models[0]
		}
		if ui.Select(c.Key("history-model"), &choice, models).Label(i18n.T("Model")).Width(px(t, 200)).Changed() {
			h.model = choice
			if choice == models[0] {
				h.model = ""
			}
		}
	})
	takes := a.historyTakes()
	sel := a.findTake(h.selected)
	ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
		ui.List(c, &a.historyList, len(takes)).Grow(1).MinWidth(0).Padding(px(t, 10), 0).ItemKey(func(i int) any { return takes[i].ID }).Rows(func(r ui.ListRow) {
			tk := &takes[r.Index]
			ui.Column(c).Padding(px(t, 5), px(t, 20)).Children(func() {
				a.takeRow(c, a.findTake(tk.ID))
			})
		})
		if sel != nil {
			ui.Box(c).Width(1).Background(t.Border)
			ui.Scroll(c.Key("details")).Width(px(t, 360)).Shrink(0).Background(t.Surface).Children(func() {
				a.takeDetails(c, sel)
			})
		}
	})
	if len(takes) == 0 {
		ui.Column(c).Absolute().Left(0).Top(titleBarHeight + px(t, 50)).Right(0).Bottom(0).Center().Gap(px(t, 6)).PassThrough().Children(func() {
			if len(a.takes) == 0 {
				ui.Text(c, i18n.T("No takes yet")).FontSize(px(t, 15)).Bold()
				ui.Text(c, i18n.T("What you generate in the studio shows up here.")).TextColor(t.TextMuted)
			} else {
				ui.Text(c, i18n.T("No take matches")).FontSize(px(t, 15)).Bold()
			}
		})
	}
}

// takeRow is a take in the History list.
func (a *app) takeRow(c *ui.Context, cl *core.Take) {
	if cl == nil {
		return
	}
	t := c.Theme()
	selected := a.history.selected == cl.ID
	card := ui.Column(c.Key("card-"+cl.ID)).Gap(px(t, 8)).Padding(px(t, 12)).Radius(10).Border(1, t.Border).Background(t.Surface)
	if selected {
		card.Border(2, t.Accent)
	}
	card.Children(func() {
		ui.Row(c).Gap(px(t, 10)).AlignItems(ui.Center).Children(func() {
			label := i18n.T("Play")
			if a.pl.Playing(cl.ID) {
				label = i18n.T("Pause")
			}
			if primaryButton(c.Key("play"), label).Width(px(t, 72)).Clicked() {
				a.play(cl)
			}
			a.waveform(c, cl)
		})
		ui.Column(c).Gap(px(t, 2)).Children(func() {
			ui.Text(c, cl.Text).FontSize(px(t, 13)).MaxLines(3).Ellipsis("…")
			ui.Text(c, clipMeta(cl)).FontSize(px(t, 11)).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
		})
		ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Wrap().Children(func() {
			a.starButton(c, cl)
			details := i18n.T("Details")
			if selected {
				details = i18n.T("Hide details")
			}
			if ui.Button(c.Key("details"), details).Clicked() {
				if selected {
					a.history.selected = ""
				} else {
					a.history.selected = cl.ID
				}
			}
			if ui.Button(c.Key("reuse"), i18n.T("Reuse")).Tooltip(i18n.T("Load this take's text and settings into the studio")).Clicked() {
				a.reuse(cl)
			}
			ui.Button(c.Key("delete"), i18n.T("Delete")).OnClick(func() { a.deleteClip(cl) })
		})
	})
}

// recipeRows are a take's recipe as label/value pairs.
func recipeRows(t *core.Take) [][2]string {
	rows := [][2]string{{i18n.T("Model"), modelName(t.Model)}}
	add := func(label, value string) {
		if value != "" {
			rows = append(rows, [2]string{label, value})
		}
	}
	add(i18n.T("Voice"), t.Voice)
	add(i18n.T("Language"), t.Language)
	if t.RefAudio != "" {
		add(i18n.T("Reference clip"), t.RefAudio)
		add(i18n.T("Reference transcript"), t.RefText)
	}
	f := func(v float64, format string) string {
		if v == 0 {
			return ""
		}
		return fmt.Sprintf(format, v)
	}
	add(i18n.T("Speed"), f(t.Speed, "%.2f×"))
	add(i18n.T("Duration"), f(t.Duration, "%.1f s"))
	if t.Steps > 0 {
		add(i18n.T("Refinement steps"), fmt.Sprint(t.Steps))
	}
	add(i18n.T("Temperature"), f(t.Temperature, "%.2f"))
	add(i18n.T("Top P"), f(t.TopP, "%.2f"))
	if t.MaxTokens > 0 {
		add(i18n.T("Max tokens"), fmt.Sprint(t.MaxTokens))
	}
	add(i18n.T("Guidance"), f(t.Guidance, "%.1f"))
	if t.Seed != 0 {
		seed := fmt.Sprint(t.Seed)
		if t.Render != nil && t.Render.SeedPinned {
			seed += " · " + i18n.T("pinned")
		}
		add(i18n.T("Seed"), seed)
	}
	if r := t.Render; r != nil {
		volume := i18n.T("As the model made it")
		if r.Master != "" && r.Master != "raw" {
			volume = i18n.T("Evened out (%s)", r.Master)
		}
		add(i18n.T("Volume"), volume)
		numbers := i18n.T("Read as words")
		if r.NoNormalize {
			numbers = i18n.T("Left as written")
		}
		add(i18n.T("Numbers"), numbers)
	}
	add(i18n.T("File"), fileDetails(t))
	add(i18n.T("Made"), i18n.DateTime(t.CreatedAt))
	add(i18n.T("Length"), renderedIn(t))
	return rows
}

// takeDetails is the History's details pane.
func (a *app) takeDetails(c *ui.Context, tk *core.Take) {
	t := c.Theme()
	ui.Column(c.Key("details-" + tk.ID)).Padding(px(t, 16)).Gap(px(t, 12)).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, i18n.T("Take details")).FontSize(px(t, 14)).Bold().Grow(1)
			if iconButton(c.Key("close-details"), icons.Close, i18n.T("Close")).Clicked() {
				a.history.selected = ""
			}
		})
		a.outputPanel(c, tk, false)
		ui.Row(c).Gap(px(t, 8)).Children(func() {
			if primaryButton(c.Key("details-reuse"), i18n.T("Reuse")).Tooltip(i18n.T("Load the whole recipe into the studio, seed included")).Clicked() {
				a.reuse(tk)
			}
		})
		ui.Text(c, i18n.T("Script")).Bold()
		ui.Text(c, tk.Text).FontSize(px(t, 13)).Selectable()
		ui.Text(c, i18n.T("Recipe")).Bold()
		ui.Column(c).Gap(px(t, 4)).Children(func() {
			for _, row := range recipeRows(tk) {
				ui.Row(c.Key("recipe-" + row[0])).Gap(px(t, 8)).Children(func() {
					ui.Text(c, row[0]).FontSize(px(t, 12)).TextColor(t.TextMuted).Width(px(t, 120)).Shrink(0)
					ui.Text(c, row[1]).FontSize(px(t, 12)).Selectable().Grow(1).MinWidth(0)
				})
			}
		})
		if tk.Render != nil && len(tk.Render.Chunks) > 1 {
			ui.Text(c, i18n.N(len(tk.Render.Chunks), "%d chunk", "%d chunks", len(tk.Render.Chunks))).Bold()
			ui.Column(c).Gap(px(t, 2)).Children(func() {
				for i, ch := range tk.Render.Chunks {
					row := ui.ButtonBase(c.Key(fmt.Sprintf("chunk-%d", i))).Padding(px(t, 3), px(t, 6)).Radius(5).Label(ch.Text).Tooltip(i18n.T("Play from here"))
					if row.Hovered() {
						row.Background(t.SurfaceHover)
					}
					if row.Clicked() {
						a.seekTake(tk, ch.Start)
					}
					row.Children(func() {
						ui.Text(c, fmt.Sprintf("%d", i+1)).FontSize(px(t, 11)).TextColor(t.TextMuted).Width(px(t, 22))
						ui.Text(c, noticeTime(ch.Start)).FontSize(px(t, 11)).TextColor(t.TextMuted).Width(px(t, 48))
						ui.Text(c, ch.Text).FontSize(px(t, 12)).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
						if ch.Cached {
							ui.Text(c, i18n.T("cached")).FontSize(px(t, 11)).TextColor(t.TextMuted)
						}
					})
				}
			})
		}
	})
}

// historySettings is the History's part of Settings → Studio.
func (a *app) historySettings(c *ui.Context) {
	t := c.Theme()
	a.history.keepTakes = float64(a.prefs.Studio.KeepTakes)
	settingRow(c, "keep-takes", i18n.T("Keep at most"), i18n.T("Takes in the history; beyond it the oldest ones that are not starred are deleted. 0 keeps every take."), func() {
		if ui.NumberInput(c.Key("keep-takes-input"), &a.history.keepTakes, 0, 100000, 10).Width(px(t, 120)).Label(i18n.T("Keep at most")).Changed() {
			n := int(a.history.keepTakes)
			a.updateSettings(func(s *settings.Settings) { s.Studio.KeepTakes = n })
		}
	})
}

func (a *app) waveform(c *ui.Context, cl *core.Take) {
	t := c.Theme()
	peaks, ready := a.peaks[cl.ID]
	if !ready {
		// Computed (or read from the disk cache) in the background; a
		// placeholder is drawn until then. Guarded: requested once.
		a.requestPeaks(cl)
		peaks, ready = a.peaks[cl.ID]
	}
	w := ui.ButtonBase(c.Key("wave")).Grow(1).MinWidth(0).Height(px(t, 44)).Label(i18n.T("Waveform")).Tooltip(i18n.T("Click to seek"))
	if w.Clicked() {
		if x, _, ok := w.PointerPosition(); ok {
			if a.pl.Current() != cl.ID || a.pl.Streaming(cl.ID) {
				a.pl.Stop()
				a.play(cl)
			}
			a.pl.Seek(cl.ID, x/max(w.Bounds().W, 1))
		}
	}
	played, muted := t.Accent, t.TextMuted.Alpha(0.45)
	pl := &a.pl
	id := cl.ID
	w.Draw(func(p *ui.Painter, r ui.Rect) {
		if !ready {
			// Placeholder: faint stubs where the bars will be.
			step := r.W / peakCount
			bar := max(step*0.6, 1)
			for i := range peakCount {
				p.Fill(ui.Rect{X: r.X + float32(i)*step, Y: r.Y + r.H/2 - 1, W: bar, H: 2}, muted.Alpha(0.5), bar/2)
			}
			return
		}
		n := len(peaks)
		if n == 0 {
			p.Line(r.X, r.Y+r.H/2, r.X+r.W, r.Y+r.H/2, 1, muted)
			return
		}
		progress := pl.Progress(id)
		step := r.W / float32(n)
		bar := max(step*0.6, 1)
		for i, v := range peaks {
			h := max(v*r.H, 2)
			x := r.X + float32(i)*step
			color := muted
			if float32(i)/float32(n) < progress {
				color = played
			}
			p.Fill(ui.Rect{X: x, Y: r.Y + (r.H-h)/2, W: bar, H: h}, color, bar/2)
		}
		if pl.Playing(id) {
			p.AnimationFrame()
		}
	})
}

func clipMeta(cl *core.Take) string {
	parts := []string{catalog.Builtin().DisplayName(cl.Model)}
	if cl.Voice != "" {
		parts = append(parts, cl.Voice)
	}
	if cl.Language != "" {
		parts = append(parts, cl.Language)
	}
	parts = append(parts, i18n.T("%.1f s", cl.AudioSeconds))
	if rtf := cl.RTF(); rtf > 0 {
		parts = append(parts, i18n.T("%.1f× realtime", rtf))
	}
	parts = append(parts, i18n.DateTime(cl.CreatedAt))
	return strings.Join(parts, " · ")
}

// clipCard shows a take with its output panel (Play, waveform, details,
// Check audio, Save as…): the seam other pages use for a take they made
// (the Design page's preview). latest titles it "Latest take".
func (a *app) clipCard(c *ui.Context, cl *core.Take, latest bool) {
	a.outputPanel(c, cl, latest)
}
