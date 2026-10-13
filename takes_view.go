package main

import (
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/audio"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
)

// clipCard shows a take: its waveform, what made it, and what to do with it.
func (a *app) clipCard(c *ui.Context, cl *core.Take, latest bool) {
	t := c.Theme()
	ui.Column(c.Key("card-"+cl.ID)).Gap(8).Padding(12).Radius(10).Border(1, t.Border).Background(t.Surface).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			label := i18n.T("Play")
			if a.pl.Playing(cl.ID) {
				label = i18n.T("Pause")
			}
			if ui.PrimaryButton(c.Key("play"), label).Width(72).Clicked() {
				a.play(cl)
			}
			a.waveform(c, cl)
		})
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			title := cl.Text
			if latest {
				title = i18n.T("Latest take")
			}
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				if latest {
					ui.Text(c, title).FontSize(13).SingleLine().Ellipsis("…")
				} else {
					ui.Text(c, title).FontSize(13).MaxLines(3).Ellipsis("…")
				}
				ui.Text(c, clipMeta(cl)).FontSize(11).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
			})
			if !latest && ui.Button(c.Key("reuse"), i18n.T("Reuse")).Tooltip(i18n.T("Load this take's text and settings into the studio")).Clicked() {
				a.reuse(cl)
			}
			if ui.Button(c.Key("reveal"), i18n.T("Reveal")).Tooltip(i18n.T("Show in Finder")).Clicked() {
				mygo.Shell.ShowItemInFolder(a.takeStore.AudioPath(*cl))
			}
			if ui.Button(c.Key("export"), i18n.T("Export…")).Clicked() {
				a.export(cl)
			}
			if !latest {
				ui.Button(c.Key("delete"), i18n.T("Delete")).OnClick(func() { a.deleteClip(cl) })
			}
		})
	})
}

func (a *app) waveform(c *ui.Context, cl *core.Take) {
	t := c.Theme()
	peaks, ok := a.peaks[cl.ID]
	if !ok {
		// Decodes the whole file in the build pass, as the MVP did: thread A
		// moves this to a cached background job.
		p, err := audio.Peaks(a.takeStore.AudioPath(*cl), 160)
		if err != nil || p == nil {
			p = []float32{}
		}
		a.peaks[cl.ID] = p
		peaks = p
	}
	w := ui.ButtonBase(c.Key("wave")).Grow(1).MinWidth(0).Height(44).Label(i18n.T("Waveform")).Tooltip(i18n.T("Click to seek"))
	if w.Clicked() {
		if x, _, ok := w.PointerPosition(); ok {
			if a.pl.Current() != cl.ID {
				a.play(cl)
			}
			a.pl.Seek(cl.ID, x/max(w.Bounds().W, 1))
		}
	}
	played, muted := t.Accent, t.TextMuted.Alpha(0.45)
	pl := &a.pl
	id := cl.ID
	w.Draw(func(p *ui.Painter, r ui.Rect) {
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
	parts = append(parts, cl.CreatedAt.Format("Jan 2 15:04"))
	return strings.Join(parts, " · ")
}

func (a *app) historyPage(c *ui.Context) {
	t := c.Theme()
	header(c, i18n.T("History"), func() {
		ui.Text(c, i18n.T("%d takes", len(a.takes))).FontSize(12).TextColor(t.TextMuted)
		if ui.Button(c.Key("open-folder"), i18n.T("Open folder")).Clicked() {
			mygo.Shell.OpenPath(a.takeStore.AudioDir())
		}
	})
	takes := a.takes
	ui.List(c, &a.historyList, len(takes)).Grow(1).Padding(10, 0).ItemKey(func(i int) any { return takes[i].ID }).Rows(func(r ui.ListRow) {
		cl := &takes[r.Index]
		ui.Column(c).Padding(6, 20).Children(func() {
			a.clipCard(c, cl, false)
		})
	})
	if len(takes) == 0 {
		ui.Column(c).Absolute().Left(0).Top(titleBarHeight).Right(0).Bottom(0).Center().Gap(6).PassThrough().Children(func() {
			ui.Text(c, i18n.T("No takes yet")).FontSize(15).Bold()
			ui.Text(c, i18n.T("What you generate in the studio shows up here.")).TextColor(t.TextMuted)
		})
	}
}
