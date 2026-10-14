package main

// The status bar along the bottom of the window: the engine (which opens
// the ⌘E quick switch), the jobs in the queue (a list where each can be
// cancelled by id), downloads, the notification centre's bell, the logs
// panel toggle and the version. The engine part reads the workers'
// status as the supervisor publishes it; the jobs part the scheduler's
// jobs (jobs.Updated, and the list at start).

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
	"github.com/hehehai/AudioSmith/internal/jobs"
)

func (a *app) statusBar(c *ui.Context) {
	t := c.Theme()
	k := tok(t)
	eng := a.engineSummary()
	ui.Row(c).Height(px(t, statusBarHeight)).Shrink(0).Padding(0, px(t, sp2)).Gap(px(t, sp1)).AlignItems(ui.Center).Background(k.Sidebar).TextColor(k.Text2).Children(func() {
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
			ui.Box(c).Size(px(t, 6), px(t, 6)).Radius(px(t, 3)).Background(color)
			ui.Text(c, label).FontSize(px(t, fsCaption)).SingleLine()
			ui.Text(c, "· "+model).FontSize(px(t, fsCaption)).TextColor(k.Text3).SingleLine().Ellipsis("…").MaxWidth(px(t, 200)).MinWidth(0)
		})
		if engBtn.Clicked() {
			a.switchOpen = !a.switchOpen
		}
		ui.Popover(c, engBtn, &a.switchOpen, func() { a.quickSwitch(c) })

		// The queue.
		if active := a.activeJobs(); len(active) > 0 {
			running := 0
			for _, j := range active {
				if j.State == jobs.StateRunning {
					running++
				}
			}
			label := i18n.N(len(active), "%d job", "%d jobs", len(active))
			if running < len(active) {
				label += " · " + i18n.N(len(active)-running, "%d waiting", "%d waiting", len(active)-running)
			}
			jobsBtn := statusButton(c, "status-jobs", i18n.T("Jobs"), func() {
				ui.Spinner(c).Size(px(t, 11), px(t, 11))
				ui.Text(c, label).FontSize(px(t, fsCaption)).SingleLine()
			})
			if jobsBtn.Clicked() {
				a.jobsOpen = !a.jobsOpen
			}
			ui.Popover(c, jobsBtn, &a.jobsOpen, func() { a.jobsPanel(c) })
		} else if eng.Busy {
			ui.Spinner(c).Size(px(t, 11), px(t, 11))
			ui.Text(c, i18n.T("Working…")).FontSize(px(t, fsCaption)).SingleLine()
		}

		// Downloads.
		for _, m := range a.downloads() {
			e := m.entry
			text := i18n.T("Downloading %s", e.Label)
			if m.progress.TotalBytes > 0 {
				text = i18n.T("Downloading %s · %d%%", e.Label, int(100*m.progress.Bytes/m.progress.TotalBytes))
			}
			if statusButton(c, "status-dl-"+modelKey(e), i18n.T("Show the models"), func() {
				ui.Icon(c, icons.DownloadMinimalistic.Linear).Size(px(t, 13), px(t, 13))
				ui.Text(c, text).FontSize(px(t, fsCaption)).SingleLine().Ellipsis("…").MaxWidth(px(t, 240)).MinWidth(0)
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
			ui.Icon(c, bellIcon.Get(unread > 0)).Size(px(t, 14), px(t, 14))
			if unread > 0 {
				ui.Text(c, itoa(unread)).FontSize(px(t, fsMicro)).FontWeight(600).TextColor(k.AccentInk)
			}
		})
		if bell.Clicked() {
			a.noticesOpen = !a.noticesOpen
		}
		ui.Popover(c, bell, &a.noticesOpen, func() { a.noticeCentre(c) })

		if statusButton(c, "status-logs", i18n.T("Show or hide the logs panel (⇧⌘L)"), func() {
			ui.Icon(c, logsIcon.Get(a.logsPanel)).Size(px(t, 14), px(t, 14))
		}).Clicked() {
			a.logsPanel = !a.logsPanel
		}
		if a.version != "" {
			ui.Text(c, "v"+a.version).FontSize(px(t, fsMicro)).TextColor(k.Text3).Margin(0, px(t, sp1))
		}
	})
}

// downloads are the models downloading, catalogue first, in its order.
func (a *app) downloads() []*modelState {
	var out []*modelState
	for _, e := range catalog.Builtin().Entries {
		if m := a.models[e.ID]; m != nil && m.downloading {
			out = append(out, m)
		}
	}
	keys := slices.Sorted(maps.Keys(a.models))
	for _, k := range keys {
		if m := a.models[k]; strings.HasPrefix(k, "repo:") && m.downloading {
			out = append(out, m)
		}
	}
	return out
}

// jobsChanged keeps the snapshot of unfinished jobs (every owner) the
// status bar lists; it runs on the main thread.
func (a *app) jobsChanged(j jobs.Job) {
	if j.State.Terminal() || j.State == jobs.StateInterrupted {
		delete(a.jobList, j.ID)
		return
	}
	a.jobList[j.ID] = j
}

// activeJobs are the unfinished jobs: running first, then by age.
func (a *app) activeJobs() []jobs.Job {
	list := slices.Collect(maps.Values(a.jobList))
	slices.SortFunc(list, func(x, y jobs.Job) int {
		if rx, ry := x.State == jobs.StateRunning, y.State == jobs.StateRunning; rx != ry {
			if rx {
				return -1
			}
			return 1
		}
		if c := x.Created.Compare(y.Created); c != 0 {
			return c
		}
		return strings.Compare(x.ID, y.ID)
	})
	return list
}

// jobsPanel lists the unfinished jobs, each with its progress and Cancel.
func (a *app) jobsPanel(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Width(px(t, 360)).Gap(px(t, sp2)).Padding(px(t, sp1)).Children(func() {
		sectionHeading(c, i18n.T("Jobs"))
		active := a.activeJobs()
		if len(active) == 0 {
			ui.Text(c, i18n.T("Nothing is running.")).TextColor(t.TextMuted)
			return
		}
		for _, j := range active {
			ui.Row(c.Key("job-" + j.ID)).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(px(t, 4)).Children(func() {
					title := j.Title
					if title == "" {
						title = j.Kind
					}
					ui.Text(c, title).FontSize(px(t, 13)).SingleLine().Ellipsis("…")
					state := i18n.T("Waiting")
					if j.State == jobs.StateRunning {
						state = stageText(j.Progress.Stage)
						if j.Progress.Fraction >= 0 {
							state += fmt.Sprintf(" · %d%%", int(100*j.Progress.Fraction))
						}
					}
					if j.State == jobs.StateRunning {
						ui.Progress(c, j.Progress.Fraction).Label(title)
					}
					ui.Text(c, state).FontSize(px(t, 11)).TextColor(t.TextMuted)
				})
				if iconButton(c.Key("cancel"), icons.CloseCircle, i18n.T("Cancel")).Clicked() {
					a.cancelJobByID(j)
				}
			})
		}
	})
}

// cancelJobByID cancels any job by its id, as the Studio's Cancel does for
// its own.
func (a *app) cancelJobByID(j jobs.Job) {
	if a.job != nil && a.job.id == j.ID {
		a.cancel()
		return
	}
	for _, m := range a.models {
		if m.jobID == j.ID && m.downloading {
			a.cancelDownload(m.entry)
			return
		}
	}
	a.cancelJob(j.ID)
}

// statusButton is a flat button of the status bar.
func statusButton(c *ui.Context, key, label string, content func()) ui.Element {
	t := c.Theme()
	b := ui.ButtonBase(c.Key(key)).Padding(px(t, 2), px(t, 6)).Radius(px(t, 6)).Label(label).Tooltip(label).MinWidth(0).Shrink(1)
	if b.Hovered() {
		b.Background(tok(t).Hover)
	}
	return b.Children(func() {
		ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).MinWidth(0).Children(content)
	})
}

// quickSwitch is the ⌘E panel: the engine's state and models, and what
// to do with them.
func (a *app) quickSwitch(c *ui.Context) {
	t := c.Theme()
	eng := a.engineSummary()
	k := tok(t)
	ui.Column(c).Width(px(t, 320)).Gap(px(t, sp2)).Padding(px(t, sp1)).Children(func() {
		sectionHeading(c, i18n.T("Speech engine"))
		state := map[engineState]string{engineStarting: i18n.T("Starting"), engineReady: i18n.T("Ready"), engineFailed: i18n.T("Stopped")}[eng.State]
		info := state
		if eng.Version != "" {
			info += " · " + i18n.T("version %s", eng.Version)
		}
		if eng.PID != 0 {
			info += " · PID " + itoa(eng.PID)
		}
		caption(c, info)
		if eng.Err != "" {
			ui.Text(c, eng.Err).FontSize(px(t, 12)).TextColor(t.Danger).MaxLines(3).Ellipsis("…")
		}
		if mem := a.gpuMemory(); mem != "" {
			caption(c, mem)
		}
		ui.Divider(c)
		sectionTitle(c, i18n.T("In memory"))
		if len(eng.Loaded) == 0 {
			ui.Text(c, i18n.T("No model loaded")).FontSize(px(t, 12))
		}
		for _, repo := range eng.Loaded {
			ui.Row(c.Key("loaded-" + repo)).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
				ui.Text(c, catalog.Builtin().DisplayName(repo)).Grow(1).SingleLine().Ellipsis("…")
				if ghostButton(c.Key("unload"), i18n.T("Unload")).Disabled(eng.Busy).Clicked() {
					a.unload()
				}
			})
		}
		ui.Divider(c)
		sectionTitle(c, i18n.T("Speech model"))
		_, winH := c.Size()
		ui.Scroll(c.Key("switch-models")).MaxHeight(max(winH*0.45, px(t, 120))).Children(func() {
			for _, e := range catalog.Builtin().ForTask(catalog.TaskTTS) {
				m := a.models[modelKey(e)]
				chosen := a.preset().Repo == e.Repo
				row := ui.ButtonBase(c.Key("switch-"+e.ID)).Padding(px(t, 5), px(t, sp2)).Radius(px(t, radiusControl)).Label(e.Label).Checked(chosen)
				if chosen {
					row.Background(k.Pressed)
				} else if row.Hovered() {
					row.Background(k.Hover)
				}
				row.Children(func() {
					ui.Row(c).Grow(1).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
						ui.Text(c, e.Label).Grow(1).SingleLine().Ellipsis("…").MinWidth(0)
						if m != nil && m.known && m.local.State == hub.Downloaded {
							ui.Text(c, i18n.T("Downloaded")).FontSize(px(t, fsMicro)).TextColor(k.Text3)
						}
						if chosen {
							ui.Icon(c, icons.Check.Linear).Size(px(t, 14), px(t, 14)).TextColor(k.AccentInk)
						}
					})
				})
				if row.Clicked() {
					a.selectModel(e)
				}
			}
		})
		ui.Divider(c)
		ui.Row(c).Gap(px(t, sp1)).AlignItems(ui.Center).Children(func() {
			if ui.Button(c.Key("switch-preload"), i18n.T("Preload")).Disabled(eng.Busy || eng.State != engineReady).Clicked() {
				a.preload()
			}
			if iconButton(c.Key("switch-restart"), icons.Restart, i18n.T("Restart engine")).Clicked() {
				a.restartEngine()
			}
			ui.Spacer(c)
			moreMenu(c.Key("switch-more"), "", func(m *ui.Menu) {
				if m.Item(i18n.T("Clear cache")).Disabled(eng.State != engineReady).Chosen() {
					a.flushEngine()
				}
				if m.Item(i18n.T("Details")).Chosen() {
					a.switchOpen = false
					a.navigate("/engine")
				}
			})
		})
	})
}

// gpuMemory describes the gpu worker's memory, from its last stats.
func (a *app) gpuMemory() string {
	for _, w := range a.workers {
		if w.Role == proto.RoleGPU && w.Stats != nil && w.State == engine.WorkerReady {
			return i18n.T("Memory: %s in use · %s cached", catalog.HumanSize(w.Stats.Memory.Active), catalog.HumanSize(w.Stats.Memory.Cache))
		}
	}
	return ""
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
	ui.Column(c).Width(px(t, 360)).Gap(px(t, sp2)).Padding(px(t, sp1)).Children(func() {
		ui.Row(c).Gap(px(t, sp1)).AlignItems(ui.Center).Children(func() {
			sectionHeading(c, i18n.T("Notifications")).Grow(1)
			if ghostButton(c.Key("notices-read"), i18n.T("Mark all read")).Disabled(len(a.notices) == 0).Clicked() {
				for i := range a.notices {
					a.notices[i].Read = true
				}
				notes := a.notes
				a.async(func() { notes.MarkRead(context.Background(), "") })
			}
			if ghostButton(c.Key("notices-clear"), i18n.T("Clear")).Disabled(len(a.notices) == 0).Clicked() {
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
	k := tok(t)
	row := ui.Row(c.Key("notice-" + n.ID)).Gap(px(t, sp2)).Padding(px(t, sp2)).Radius(px(t, radiusControl)).AlignItems(ui.Start)
	if !n.Read {
		row.Background(k.Subtle)
	}
	row.Children(func() {
		ui.Box(c).Size(px(t, 6), px(t, 6)).Radius(px(t, 3)).Background(color).Margin(px(t, 6), 0, 0, 0)
		ui.Column(c).Grow(1).MinWidth(0).Gap(px(t, 2)).Children(func() {
			ui.Text(c, n.Title).FontSize(px(t, fsBody)).FontWeight(600).MaxLines(2).Ellipsis("…")
			if n.Body != "" {
				ui.Text(c, n.Body).FontSize(px(t, fsCaption)).TextColor(k.Text2).MaxLines(4).Ellipsis("…").Selectable()
			}
			ui.Text(c, timeAgo(n.Time)).FontSize(px(t, fsMicro)).TextColor(k.Text3)
		})
		if n.Link != "" {
			if ghostButton(c.Key("open"), i18n.T("Open")).Clicked() {
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
	return i18n.DateTime(t)
}
