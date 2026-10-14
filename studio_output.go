package main

// A take's output panel (the Studio's latest take, the History's details):
// waveform with click-to-seek and a handle to drag the file out, file
// details, the render's warnings, Check audio (findings that seek when
// clicked), Save as… in a bit depth, Show in Finder.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
)

// takeBits is the bit depth of a take's file.
func takeBits(t *core.Take) int {
	if t.Render != nil && t.Render.Bits != 0 {
		return t.Render.Bits
	}
	return 16
}

// bitsLabel names a bit depth.
func bitsLabel(bits int) string {
	if bits == 32 {
		return i18n.T("32-bit float")
	}
	return i18n.T("%d-bit", bits)
}

// humanBytes formats a file size: "840 KB", "12.4 MB".
func humanBytes(n int64) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.1f GB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.1f MB", float64(n)/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%.0f KB", float64(n)/1e3)
	}
	return fmt.Sprintf("%d B", n)
}

// fileDetails is "1.2 MB · 24 kHz · 16-bit · mono".
func fileDetails(t *core.Take) string {
	var parts []string
	if t.Render != nil && t.Render.Bytes > 0 {
		parts = append(parts, humanBytes(t.Render.Bytes))
	}
	if t.SampleRate > 0 {
		parts = append(parts, i18n.T("%s kHz", strings.TrimSuffix(fmt.Sprintf("%.1f", float64(t.SampleRate)/1000), ".0")))
	}
	parts = append(parts, bitsLabel(takeBits(t)))
	ch := 1
	if t.Render != nil && t.Render.Channels > 0 {
		ch = t.Render.Channels
	}
	if ch == 1 {
		parts = append(parts, i18n.T("mono"))
	} else {
		parts = append(parts, i18n.T("stereo"))
	}
	return strings.Join(parts, " · ")
}

// renderedIn is "5.1 s · rendered in 1.2 s".
func renderedIn(t *core.Take) string {
	s := i18n.T("%.1f s", t.AudioSeconds)
	if t.GenSeconds > 0 {
		s += " · " + i18n.T("rendered in %.1f s", t.GenSeconds)
	}
	return s
}

// seekTake plays a take from seconds in.
func (a *app) seekTake(t *core.Take, seconds float64) {
	if a.pl.Current() != t.ID || a.pl.Streaming(t.ID) {
		a.pl.Stop()
		a.play(t)
	}
	if t.AudioSeconds > 0 {
		a.pl.Seek(t.ID, float32(min(max(seconds/t.AudioSeconds, 0), 1)))
	}
}

// checkTake runs Check audio over a take in the background; the take
// comes back through TakeUpdated.
func (a *app) checkTake(t *core.Take) {
	id := t.ID
	a.checkOpen[id] = true
	if a.checking[id] {
		return
	}
	a.checking[id] = true
	svc := a.lib.svc.Render
	a.background(func() {
		_, err := svc.Check(context.Background(), id)
		a.post(func() {
			delete(a.checking, id)
			if err != nil {
				a.toast(i18n.T("Could not check the audio: %v", err))
			}
		})
	})
}

// saveAs asks where to save a copy of the take in bits (16, 24, 32 float).
func (a *app) saveAs(t *core.Take, bits int) {
	id, name := t.ID, exportName(t)
	svc := a.lib.svc.Render
	go func() {
		dst, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
			Parent:      a.win,
			Title:       i18n.T("Save audio as"),
			DefaultPath: name,
			Filters:     []mygo.FileFilter{{Name: i18n.T("WAV audio"), Extensions: []string{"wav"}}},
		})
		if err != nil || dst == "" {
			return
		}
		if filepath.Ext(dst) == "" {
			dst += ".wav"
		}
		err = svc.ExportAs(context.Background(), id, dst, bits)
		if err == nil {
			a.recordTakeExport(id, dst) // M3: the Projects page's Exports (projects.go, thread #18)
		}
		a.post(func() {
			if err != nil {
				a.toast(i18n.T("Export failed: %v", err))
			} else {
				a.toast(i18n.T("Saved %s", filepath.Base(dst)))
			}
		})
	}()
}

// saveAsMenu is the Save as… button with its bit depths.
func (a *app) saveAsMenu(c *ui.Context, t *core.Take) {
	tk := *t
	ui.MenuButton(c.Key("save-as"), i18n.T("Save as…"), func(m *ui.Menu) {
		for _, bits := range bitDepths {
			if m.Item(i18n.T("WAV, %s", bitsLabel(bits))).Chosen() {
				a.saveAs(&tk, bits)
			}
		}
		a.ffmpegSaveItems(m, &tk) // M7a: MP3, FLAC, Opus with ffmpeg (ffmpeg_formats.go)
	})
}

// dragHandle lets the take's file be dragged to Finder or another app.
func (a *app) dragHandle(c *ui.Context, t *core.Take) {
	th := c.Theme()
	path := a.takeStore.AudioPath(*t)
	h := ui.ButtonBase(c.Key("drag-out")).Padding(px(th, 4), px(th, 6)).Radius(5).Label(i18n.T("Drag the file")).
		Tooltip(i18n.T("Drag to Finder or another app"))
	h.DragDataFrom(func() transfer.Data {
		d, _ := transfer.FileData(path)
		return d
	})
	if h.Hovered() {
		h.Background(th.SurfaceHover)
	}
	h.Children(func() { ui.Text(c, "⠿").TextColor(th.TextMuted) })
}

// noticeList shows notices; those with a place in the audio seek there.
func (a *app) noticeList(c *ui.Context, t *core.Take, key string, notices []core.TakeNotice) {
	th := c.Theme()
	ui.Column(c.Key(key)).Gap(px(th, 2)).Children(func() {
		for i, n := range notices {
			color := th.Warning
			if n.Severity == "error" {
				color = th.Danger
			} else if n.Severity == "info" {
				color = th.TextMuted
			}
			row := ui.ButtonBase(c.Key(fmt.Sprintf("%s-%d", key, i))).Padding(px(th, 3), px(th, 6)).Radius(5).Label(noticeText(n))
			seek := n.Start > 0 || n.End > 0
			if seek {
				if row.Hovered() {
					row.Background(th.SurfaceHover)
				}
				if row.Clicked() {
					a.seekTake(t, n.Start)
				}
				row.Tooltip(i18n.T("Play from here"))
			}
			row.Children(func() {
				ui.Text(c, "●").FontSize(px(th, 9)).TextColor(color)
				if seek {
					ui.Text(c, noticeTime(n.Start)).FontSize(px(th, 12)).TextColor(th.TextMuted).Width(px(th, 52))
				}
				ui.Text(c, noticeText(n)).FontSize(px(th, 12)).Grow(1).MinWidth(0)
			})
		}
	})
}

// checkResults shows what Check audio found.
func (a *app) checkResults(c *ui.Context, t *core.Take) {
	th := c.Theme()
	switch {
	case a.checking[t.ID]:
		ui.Text(c, i18n.T("Checking…")).FontSize(px(th, 12)).TextColor(th.TextMuted)
	case t.Render == nil || !t.Render.QCDone:
	case len(t.Render.Issues) == 0:
		ui.Text(c, i18n.T("✓ No problems found: no clipping, silences or sudden jumps in loudness.")).FontSize(px(th, 12)).TextColor(th.Success)
	default:
		ui.Text(c, i18n.N(len(t.Render.Issues), "Check audio found %d thing to look at:", "Check audio found %d things to look at:", len(t.Render.Issues))).
			FontSize(px(th, 12)).Bold()
		a.noticeList(c, t, "qc", t.Render.Issues)
	}
}

// outputPanel is a take's panel: the Studio's latest take, or the
// History's selected one (detailed: the recipe follows).
func (a *app) outputPanel(c *ui.Context, t *core.Take, latest bool) {
	th := c.Theme()
	ui.Column(c.Key("out-"+t.ID)).Gap(px(th, 8)).Padding(px(th, 12)).Radius(10).Border(1, th.Border).Background(th.Surface).Children(func() {
		ui.Row(c).Gap(px(th, 10)).AlignItems(ui.Center).Children(func() {
			label := i18n.T("Play")
			if a.pl.Playing(t.ID) {
				label = i18n.T("Pause")
			}
			if ui.PrimaryButton(c.Key("play"), label).Width(px(th, 72)).Clicked() {
				a.play(t)
			}
			a.waveform(c, t)
			a.dragHandle(c, t)
		})
		ui.Column(c).Gap(px(th, 2)).Children(func() {
			title := t.Text
			if latest {
				title = i18n.T("Latest take")
			}
			ui.Text(c, title).FontSize(px(th, 13)).Bold().SingleLine().Ellipsis("…")
			ui.Text(c, renderedIn(t)+" · "+fileDetails(t)).FontSize(px(th, 11)).TextColor(th.TextMuted).SingleLine().Ellipsis("…")
			if latest {
				ui.Text(c, clipMeta(t)).FontSize(px(th, 11)).TextColor(th.TextMuted).SingleLine().Ellipsis("…")
			}
		})
		ui.Row(c).Gap(px(th, 8)).AlignItems(ui.Center).Wrap().Children(func() {
			a.starButton(c, t)
			check := ui.Button(c.Key("check"), i18n.T("Check audio")).Tooltip(i18n.T("Look for clipping, silences and jumps in loudness"))
			if check.Clicked() {
				if t.Render != nil && t.Render.QCDone {
					a.checkOpen[t.ID] = !a.checkOpen[t.ID]
				} else {
					a.checkTake(t)
				}
			}
			a.saveAsMenu(c, t)
			if ui.Button(c.Key("reveal"), i18n.T("Show in Finder")).Clicked() {
				mygo.Shell.ShowItemInFolder(a.takeStore.AudioPath(*t))
			}
		})
		if t.Render != nil && len(t.Render.Warnings) > 0 {
			a.noticeList(c, t, "warn", t.Render.Warnings)
		}
		if a.checkOpen[t.ID] {
			a.checkResults(c, t)
		}
	})
}

// starButton stars or unstars a take; starred takes are never pruned.
func (a *app) starButton(c *ui.Context, t *core.Take) {
	label, tip := "☆", i18n.T("Star: keep this take")
	if t.Starred {
		label, tip = "★", i18n.T("Unstar")
	}
	b := ui.Button(c.Key("star"), label).Tooltip(tip).Label(tip)
	if b.Clicked() {
		a.setStarred(t, !t.Starred)
	}
}

// setStarred stars a take in the background (TakeUpdated follows).
func (a *app) setStarred(t *core.Take, on bool) {
	id := t.ID
	t.Starred = on // at once in the snapshot; the event confirms it
	svc := a.lib.svc.Takes
	a.background(func() {
		if err := svc.SetStarred(context.Background(), id, on); err != nil {
			a.post(func() { a.toast(i18n.T("Could not star the take: %v", err)) })
		}
	})
}

// modelName names a take's model.
func modelName(repo string) string { return catalog.Builtin().DisplayName(repo) }
