package main

// The status bar along the bottom of the window: the engine (which opens
// the ⌘E quick switch), downloads, the notification centre's bell, the
// logs panel toggle and the version.

import (
	"context"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/i18n"
)

func (a *app) statusBar(c *ui.Context) {
	t := c.Theme()
	eng := a.engineSummary()
	ui.Row(c).Height(px(t, statusBarHeight)).Shrink(0).Padding(0, px(t, 8)).Gap(px(t, 6)).AlignItems(ui.Center).Background(t.Surface).Children(func() {
		// The engine.
		color, label := t.Warning, i18n.T("Engine starting…")
		switch eng.State {
		case engineReady:
			color, label = t.Success, i18n.T("Engine ready")
		case engineFailed:
			color, label = t.Danger, i18n.T("Engine stopped")
		}
		model := i18n.T("No model loaded")
		if len(eng.Loaded) > 0 {
			model = catalog.Builtin().DisplayName(eng.Loaded[0])
		}
		engBtn := statusButton(c, "status-engine", i18n.T("Engine quick switch (⌘E)"), func() {
			ui.Box(c).Size(px(t, 8), px(t, 8)).Radius(px(t, 4)).Background(color)
			ui.Text(c, label).FontSize(px(t, 12))
			ui.Text(c, "· "+model).FontSize(px(t, 12)).TextColor(t.TextMuted).SingleLine().Ellipsis("…").MaxWidth(px(t, 220))
		})
		if engBtn.Clicked() {
			a.switchOpen = !a.switchOpen
		}
		ui.Popover(c, engBtn, &a.switchOpen, func() { a.quickSwitch(c) })
		if eng.Busy {
			ui.Spinner(c).Size(px(t, 12), px(t, 12))
			ui.Text(c, i18n.T("Working…")).FontSize(px(t, 12)).TextColor(t.TextMuted)
		}

		// Downloads.
		for _, e := range catalog.Builtin().Entries {
			m := a.models[e.ID]
			if m == nil || !m.downloading {
				continue
			}
			text := i18n.T("Downloading %s", e.Label)
			if m.progress.TotalBytes > 0 {
				text = i18n.T("Downloading %s · %d%%", e.Label, int(100*m.progress.Bytes/m.progress.TotalBytes))
			}
			if statusButton(c, "status-dl-"+e.ID, i18n.T("Show the models"), func() {
				ui.Text(c, text).FontSize(px(t, 12)).TextColor(t.TextMuted).SingleLine()
			}).Clicked() {
				a.navigate("/models")
			}
		}
		ui.Spacer(c)

		// Notifications.
		unread := 0
		for _, n := range a.notices {
			if !n.Read {
				unread++
			}
		}
		bell := statusButton(c, "status-bell", i18n.T("Notifications"), func() {
			ui.Icon(c, bellIcon).Size(px(t, 14), px(t, 14))
			if unread > 0 {
				ui.Text(c, itoa(unread)).FontSize(px(t, 11)).Bold().TextColor(t.Accent)
			}
		})
		if bell.Clicked() {
			a.noticesOpen = !a.noticesOpen
		}
		ui.Popover(c, bell, &a.noticesOpen, func() { a.noticeCentre(c) })

		logsLabel := i18n.T("Logs")
		if statusButton(c, "status-logs", i18n.T("Show or hide the logs panel (⇧⌘L)"), func() {
			ui.Icon(c, logsIcon).Size(px(t, 14), px(t, 14))
			ui.Text(c, logsLabel).FontSize(px(t, 12))
		}).Clicked() {
			a.logsPanel = !a.logsPanel
		}
		if a.version != "" {
			ui.Text(c, "v"+a.version).FontSize(px(t, 11)).TextColor(t.TextMuted)
		}
	})
}

// statusButton is a flat button of the status bar.
func statusButton(c *ui.Context, key, label string, content func()) ui.Element {
	t := c.Theme()
	b := ui.ButtonBase(c.Key(key)).Padding(px(t, 2), px(t, 6)).Radius(4).Label(label).Tooltip(label)
	if b.Hovered() {
		b.Background(t.SurfaceHover)
	}
	return b.Children(func() {
		ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Children(content)
	})
}

// quickSwitch is the ⌘E panel: the engine's state and models, and what
// to do with them.
func (a *app) quickSwitch(c *ui.Context) {
	t := c.Theme()
	eng := a.engineSummary()
	ui.Column(c).Width(px(t, 320)).Gap(px(t, 10)).Padding(px(t, 4)).Children(func() {
		ui.Text(c, i18n.T("Speech engine")).Bold()
		state := map[engineState]string{engineStarting: i18n.T("Starting"), engineReady: i18n.T("Ready"), engineFailed: i18n.T("Stopped")}[eng.State]
		info := state
		if eng.Version != "" {
			info += " · " + i18n.T("version %s", eng.Version)
		}
		if eng.PID != 0 {
			info += " · PID " + itoa(eng.PID)
		}
		ui.Text(c, info).FontSize(px(t, 12)).TextColor(t.TextMuted)
		if eng.Err != "" {
			ui.Text(c, eng.Err).FontSize(px(t, 12)).TextColor(t.Danger).MaxLines(3).Ellipsis("…")
		}
		ui.Divider(c)
		ui.Text(c, i18n.T("In memory")).FontSize(px(t, 12)).Bold().TextColor(t.TextMuted)
		if len(eng.Loaded) == 0 {
			ui.Text(c, i18n.T("No model loaded")).FontSize(px(t, 12))
		}
		for _, repo := range eng.Loaded {
			ui.Row(c.Key("loaded-" + repo)).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
				ui.Text(c, catalog.Builtin().DisplayName(repo)).Grow(1).SingleLine().Ellipsis("…")
				if ui.Button(c.Key("unload"), i18n.T("Unload")).Disabled(eng.Busy).Clicked() {
					a.unload()
				}
			})
		}
		ui.Divider(c)
		ui.Text(c, i18n.T("Speech model")).FontSize(px(t, 12)).Bold().TextColor(t.TextMuted)
		for _, e := range catalog.Builtin().ForTask(catalog.TaskTTS) {
			m := a.models[e.ID]
			chosen := a.preset().Repo == e.Repo
			row := ui.ButtonBase(c.Key("switch-"+e.ID)).Padding(px(t, 5), px(t, 8)).Radius(6).Label(e.Label)
			if chosen {
				row.Background(t.Accent.Alpha(0.15))
			} else if row.Hovered() {
				row.Background(t.SurfaceHover)
			}
			row.Children(func() {
				ui.Row(c).Grow(1).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
					ui.Text(c, e.Label).Grow(1).SingleLine()
					if m != nil && m.known && m.local.State == hub.Downloaded {
						ui.Text(c, i18n.T("Downloaded")).FontSize(px(t, 11)).TextColor(t.TextMuted)
					}
				})
			})
			if row.Clicked() {
				a.chooseModel(e)
			}
		}
		ui.Row(c).Gap(px(t, 8)).Children(func() {
			if ui.Button(c.Key("switch-preload"), i18n.T("Preload")).Disabled(eng.Busy || eng.State != engineReady).Clicked() {
				a.preload()
			}
			if ui.Button(c.Key("switch-restart"), i18n.T("Restart engine")).Clicked() {
				a.restartEngine()
			}
			ui.Spacer(c)
			if ui.Button(c.Key("switch-engine"), i18n.T("Details")).Clicked() {
				a.switchOpen = false
				a.navigate("/engine")
			}
		})
	})
}

// chooseModel makes e the Studio's model.
//
// It sets the Studio form's model as its Model select does; proposed as a
// seam of studio.go (thread A) in the handoff.
func (a *app) chooseModel(e catalog.Entry) {
	if a.modelLabel == e.Label {
		return
	}
	a.modelLabel = e.Label
	a.modelChanged()
}

// noticePosted adds a notification to the centre's snapshot and shows it
// as a toast.
func (a *app) noticePosted(n core.Notification) {
	for i := range a.notices {
		if a.notices[i].ID == n.ID {
			a.notices = append(a.notices[:i], a.notices[i+1:]...)
			break
		}
	}
	a.notices = append([]core.Notification{n}, a.notices...)
	msg := n.Title
	if n.Body != "" {
		msg += ": " + n.Body
	}
	a.toast(msg)
}

// notify posts a notification (on any goroutine).
func (a *app) notify(sev core.Severity, title, body, link string) {
	a.notes.Post(core.Notification{Severity: sev, Title: title, Body: body, Link: link})
}

// noticeCentre lists the notifications, newest first.
func (a *app) noticeCentre(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Width(px(t, 360)).Gap(px(t, 8)).Padding(px(t, 4)).Children(func() {
		ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, i18n.T("Notifications")).Bold().Grow(1)
			if ui.Button(c.Key("notices-read"), i18n.T("Mark all read")).Disabled(len(a.notices) == 0).Clicked() {
				for i := range a.notices {
					a.notices[i].Read = true
				}
				notes := a.notes
				a.async(func() { notes.MarkRead(context.Background(), "") })
			}
			if ui.Button(c.Key("notices-clear"), i18n.T("Clear")).Disabled(len(a.notices) == 0).Clicked() {
				a.notices = nil
				notes := a.notes
				a.async(func() { notes.Clear(context.Background()) })
			}
		})
		if len(a.notices) == 0 {
			ui.Text(c, i18n.T("Nothing new.")).TextColor(t.TextMuted).Padding(px(t, 8), 0)
			return
		}
		ui.Scroll(c).MaxHeight(px(t, 360)).Children(func() {
			ui.Column(c).Gap(px(t, 6)).Children(func() {
				for _, n := range a.notices {
					a.noticeRow(c, n)
				}
			})
		})
	})
}

func (a *app) noticeRow(c *ui.Context, n core.Notification) {
	t := c.Theme()
	color := t.TextMuted
	switch n.Severity {
	case core.SeverityError:
		color = t.Danger
	case core.SeverityWarning:
		color = t.Warning
	case core.SeveritySuccess:
		color = t.Success
	}
	bg := t.Surface
	if !n.Read {
		bg = t.Accent.Alpha(0.08)
	}
	ui.Row(c.Key("notice-" + n.ID)).Gap(px(t, 8)).Padding(px(t, 8)).Radius(8).Background(bg).AlignItems(ui.Start).Children(func() {
		ui.Box(c).Size(px(t, 8), px(t, 8)).Radius(px(t, 4)).Background(color).Margin(px(t, 5), 0, 0, 0)
		ui.Column(c).Grow(1).MinWidth(0).Gap(px(t, 2)).Children(func() {
			ui.Text(c, n.Title).FontSize(px(t, 13)).Bold().MaxLines(2).Ellipsis("…")
			if n.Body != "" {
				ui.Text(c, n.Body).FontSize(px(t, 12)).MaxLines(4).Ellipsis("…").Selectable()
			}
			ui.Text(c, timeAgo(n.Time)).FontSize(px(t, 11)).TextColor(t.TextMuted)
		})
		if n.Link != "" {
			if ui.Button(c.Key("open"), i18n.T("Open")).Clicked() {
				a.noticesOpen = false
				a.openURL(n.Link)
			}
		}
	})
}

// timeAgo says how long ago t was, briefly.
func timeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return i18n.T("just now")
	case d < time.Hour:
		return i18n.N(int(d.Minutes()), "%d minute ago", "%d minutes ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return i18n.N(int(d.Hours()), "%d hour ago", "%d hours ago", int(d.Hours()))
	}
	return strings.TrimSpace(t.Format("Jan 2 15:04"))
}
