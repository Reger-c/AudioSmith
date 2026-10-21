package main

// The activity cards at the sidebar's foot (the ActNow family's window
// spec §2): what goes on, instead of a status bar. In order: a stopped
// engine (red, Restart), dictation and calls under way (Stop, Hang up),
// the jobs by kind — each story or book render, dub and workflow run its
// own card, the Studio's takes, exports and transcriptions one card a
// kind, "2/5" when there are several — and the model downloads. A kind's
// finished card stays 3 s. actnowkit/activity is the logic (jobs by kind,
// finished cards, the stack's geometry); this file words and draws them.
//
// One card shows in full, two more peek out under it; the pointer over
// the stack (or a click on its line) opens it into a list of four, "View
// all" the Tasks page (shell_tasks.go). A click on a card goes where it
// is managed; its button (on hover; always on a stopped engine) stops it.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/actnowkit/activity"
)

// activityState is the cards' view state.
type activityState struct {
	actTracker activity.Tracker
	actCards   []actCard // this frame's (buildActivity)
	actTick    bool      // a card counts time or goes soon: build again
	actPinned  bool      // the stack's line was clicked: open while the pointer is away
}

// actCard is a card at the sidebar's foot.
type actCard struct {
	key    string
	icon   icons.Icon
	title  string
	detail string  // on the right: "42%", "2/5", "1:05", "Waiting"
	frac   float64 // the bar: 0–1, -1 going on (no fraction), 2 none
	danger bool    // needs the user: red, its button always shows
	done   bool    // finished: a check, no bar
	open   func()  // where it is managed
	action *actAction
}

// actAction is a card's button.
type actAction struct {
	icon icons.Icon
	tip  string
	run  func()
}

// activityKind is the card a job belongs to: "" none (a call's, the
// dictation's, a download's, which have cards of their own; the voice
// pages' previews). Each render, dub and workflow run is one card, by its
// owner; the rest one card a kind.
func (a *app) activityKind(j jobs.Job) string {
	o := j.Owner
	switch {
	case strings.HasPrefix(o, services.CallOwner), strings.HasPrefix(o, "dictation:"), strings.HasPrefix(o, "model:"),
		strings.HasPrefix(o, "preview:"), strings.HasPrefix(o, "archetype:"), strings.HasPrefix(o, "family:"):
		return ""
	case a.dict != nil && a.dict.trID != "" && o == services.TranscriptOwner+a.dict.trID:
		return ""
	case strings.HasPrefix(o, services.OwnerLongform), strings.HasPrefix(o, services.DubOwner), strings.HasPrefix(o, services.WorkflowOwner):
		return o
	case strings.HasPrefix(o, services.DubExportOwner), j.Kind == services.KindEncode:
		return "export"
	case strings.HasPrefix(o, services.TranscriptOwner):
		return "transcribe"
	case strings.HasPrefix(o, voiceWorkOwner):
		return "voice"
	case j.Kind == services.KindRender, strings.HasPrefix(o, "take:"):
		return "speech"
	}
	return j.Kind
}

// activityTitle names a kind's card when it has several jobs ("" none:
// the first job's title), done its finished card.
func (a *app) activityTitle(kind string, done bool) string {
	id := kind[strings.IndexByte(kind, ':')+1:]
	switch {
	case kind == "speech":
		if done {
			return i18n.T("Speech generated")
		}
		return i18n.T("Generating speech")
	case kind == "export":
		if done {
			return i18n.T("Exported")
		}
		return i18n.T("Exporting")
	case kind == "transcribe":
		if done {
			return i18n.T("Transcribed")
		}
		return i18n.T("Transcribing")
	case kind == "voice":
		if done {
			return i18n.T("Voice work done")
		}
		return i18n.T("Working on a voice")
	case strings.HasPrefix(kind, services.OwnerLongform):
		name := ""
		if r := a.lfRenders[id]; r != nil {
			name = r.rec.Title
		}
		if name == "" {
			name = i18n.T("Render")
		}
		if done {
			return i18n.T("%s is ready", name)
		}
		return name
	case strings.HasPrefix(kind, services.DubOwner):
		name := i18n.T("Dub")
		for _, r := range a.dubList {
			if r.ID == id && r.Title != "" {
				name = r.Title
			}
		}
		if done {
			return i18n.T("%s is dubbed", name)
		}
		return name
	case strings.HasPrefix(kind, services.WorkflowOwner):
		name := i18n.T("Workflow")
		for _, r := range a.wfList {
			if r.ID == id && r.Name != "" {
				name = r.Name
			}
		}
		if done {
			return i18n.T("%s finished", name)
		}
		return name
	}
	return ""
}

// jobEnded tells the tracker a job it showed ended (jobsChanged).
func (a *app) jobEnded(j jobs.Job) {
	kind := a.activityKind(j)
	if kind == "" {
		return
	}
	title := a.activityTitle(kind, true)
	if title == "" {
		title = i18n.T("%s: done", orDash(j.Title))
	}
	a.actTracker.Ended(kind, title, j.State == jobs.StateSucceeded)
}

// buildActivity makes this frame's cards.
func (a *app) buildActivity() {
	a.actCards, a.actTick = a.actCards[:0], false
	add := func(c actCard) { a.actCards = append(a.actCards, c) }
	if a.engState == engineFailed {
		add(actCard{key: "engine", icon: icons.DangerTriangle, title: i18n.T("Engine stopped"), frac: 2, danger: true,
			open:   func() { a.navigate("/engine") },
			action: &actAction{icons.Restart, i18n.T("Restart engine"), a.restartEngine}})
	}
	if s := a.dict; s != nil && s.phase.live() {
		d := ""
		if !s.started.IsZero() {
			d = clock(time.Since(s.started).Seconds())
			a.actTick = true
		}
		add(actCard{key: "dictation", icon: micIcon, title: i18n.T("Dictating"), detail: d, frac: -1,
			open:   func() { a.navigate("/settings/dictation") },
			action: &actAction{icons.Stop, i18n.T("Stop dictation"), a.dictToggle}})
	}
	for _, c := range a.callFull {
		if c == nil || !c.Live() {
			continue
		}
		id, d := c.ID, ""
		if !c.Answered.IsZero() {
			d = clock(time.Since(c.Answered).Seconds())
			a.actTick = true
		}
		add(actCard{key: "call-" + id, icon: callsIcon, title: c.Title(), detail: d, frac: -1,
			open:   func() { a.navigate("/calls") },
			action: &actAction{icons.EndCallRounded, i18n.T("Hang up"), func() { a.hangupCall(id) }}})
	}
	var active []activity.Job
	for _, j := range a.activeJobs() {
		kind := a.activityKind(j)
		if kind == "" {
			continue
		}
		title := j.Title
		if title == "" {
			title = j.Kind
		}
		active = append(active, activity.Job{ID: j.ID, Kind: kind, Title: title, Running: j.State == jobs.StateRunning, Fraction: j.Progress.Fraction})
	}
	cards := a.actTracker.Cards(time.Now(), active, func(kind string) string { return a.activityTitle(kind, false) })
	a.actTick = a.actTick || activity.Showing(cards)
	for _, c := range cards {
		add(a.jobCard(c))
	}
	for _, m := range a.downloads() {
		e := m.entry
		d, f := "", -1.0
		if m.progress.TotalBytes > 0 {
			f = float64(m.progress.Bytes) / float64(m.progress.TotalBytes)
			d = fmt.Sprintf("%d%%", int(100*f))
		}
		add(actCard{key: "dl-" + modelKey(e), icon: icons.DownloadMinimalistic, title: i18n.T("Downloading %s", modelRef(e)), detail: d, frac: f,
			open:   func() { a.navigate("/settings/models") },
			action: &actAction{icons.CloseCircle, i18n.T("Cancel the download"), func() { a.cancelDownload(e) }}})
	}
}

// jobCard words a card of jobs.
func (a *app) jobCard(c activity.Card) actCard {
	kind := c.Kind
	ic := icons.Restart
	switch {
	case kind == "speech":
		ic = studioIcon
	case kind == "export":
		ic = icons.Export
	case kind == "transcribe":
		ic = transcribeIcon
	case kind == "voice":
		ic = voicesIcon
	case strings.HasPrefix(kind, services.OwnerLongform):
		ic = storiesIcon
		if r := a.lfRenders[kind[len(services.OwnerLongform):]]; r != nil && r.rec.Kind == core.ProjectAudiobook {
			ic = audiobookIcon
		}
	case strings.HasPrefix(kind, services.DubOwner):
		ic = dubIcon
	case strings.HasPrefix(kind, services.WorkflowOwner):
		ic = workflowsIcon
	}
	out := actCard{key: c.Key, icon: ic, title: c.Title, frac: c.Frac}
	switch {
	case c.Finished:
		out.done, out.frac, out.icon = true, 2, icons.CheckCircle
		if c.Done > 1 {
			out.detail = i18n.N(c.Done, "%d item", "%d items", c.Done)
		}
	case c.Total > 1:
		out.detail = itoa(c.Done) + "/" + itoa(c.Total)
	case c.Waiting:
		out.detail, out.frac = i18n.T("Waiting"), 0
	case c.Frac >= 0:
		out.detail = fmt.Sprintf("%d%%", int(100*c.Frac))
	}
	ids := c.Jobs
	out.open = func() { a.openActivity(kind) }
	if !c.Finished {
		tipText := i18n.T("Cancel")
		if strings.HasPrefix(kind, services.OwnerLongform) || strings.HasPrefix(kind, services.DubOwner) || strings.HasPrefix(kind, services.WorkflowOwner) {
			tipText = i18n.T("Stop")
		}
		out.action = &actAction{icons.CloseCircle, tipText, func() { a.stopActivity(kind, ids) }}
	}
	return out
}

// openActivity goes where a kind's work is managed.
func (a *app) openActivity(kind string) {
	id := kind[strings.IndexByte(kind, ':')+1:]
	switch {
	case kind == "speech":
		if a.job != nil {
			a.navigate("/studio")
		} else {
			a.navigate("/history")
		}
	case kind == "transcribe":
		a.navigate("/transcripts")
	case kind == "voice":
		a.navigate("/voices")
	case strings.HasPrefix(kind, services.OwnerLongform):
		if r := a.lfRenders[id]; r != nil {
			a.navigate("/" + lfRoute(r.rec.Kind))
			return
		}
		a.navigate("/tasks")
	case strings.HasPrefix(kind, services.DubOwner):
		a.openDub(id)
	case strings.HasPrefix(kind, services.WorkflowOwner):
		a.navigate("/workflows")
	default:
		a.navigate("/tasks")
	}
}

// stopActivity stops a card's work: a render, dub or workflow run through
// its service (what was made is kept), other jobs one by one.
func (a *app) stopActivity(kind string, ids []string) {
	id := kind[strings.IndexByte(kind, ':')+1:]
	ctx := context.Background()
	switch {
	case strings.HasPrefix(kind, services.OwnerLongform) && a.lib != nil && a.lib.svc != nil:
		svc := a.lib.svc.Longform
		a.background(func() { svc.Stop(ctx, id) })
	case strings.HasPrefix(kind, services.DubOwner) && a.dubSvc != nil:
		svc := a.dubSvc
		a.background(func() { svc.Cancel(ctx, id) })
	case strings.HasPrefix(kind, services.WorkflowOwner) && a.wfSvc != nil:
		svc := a.wfSvc
		a.background(func() { svc.Cancel(ctx, id) })
	default:
		for _, jid := range ids {
			if j, ok := a.jobList[jid]; ok {
				a.cancelJobByID(j)
			}
		}
	}
}

// activityStack draws the cards: closed, the first in full and two
// peeking out under it; open (the pointer over it, or its line clicked),
// a list of four and "View all".
func (a *app) activityStack(c *ui.Context) {
	cards := a.actCards
	n := len(cards)
	if a.actTick {
		c.After(time.Second)
	}
	if n == 0 {
		a.actPinned = false
		return
	}
	t := c.Theme()
	k := tok(t)
	wrap := ui.Column(c.Key("activity")).Shrink(0).Gap(px(t, sp1)).Role(ui.RoleList).Label(i18n.T("In progress"))
	open := n > 1 && (a.actPinned || wrap.Hovered())
	wrap.Children(func() {
		h := activity.Height(n, open)
		ui.Box(c.Key("activity-cards")).Height(px(t, h)).Shrink(0).Children(func() {
			poses := activity.Poses(n, open)
			anim := ui.Animation{Duration: 220 * time.Millisecond, Ease: ui.EaseInOut}
			for i := len(poses) - 1; i >= 0; i-- {
				p := poses[i]
				card := a.activityCard(c, cards[i], px(t, p.Top))
				fade := float32(0)
				if p.Fade {
					fade = 1
				}
				card.TransformOrigin(0.5, 1).AnimatePose(ui.Pose{Y: px(t, p.Y), Scale: p.Scale, Fade: fade}, anim)
			}
		})
		if n < 2 {
			return
		}
		ui.Row(c.Key("activity-line")).Gap(px(t, 6)).AlignItems(ui.Center).Padding(0, px(t, 4)).Children(func() {
			line := ui.ButtonBase(c.Key("activity-more")).Gap(px(t, 6)).AlignItems(ui.Center).Justify(ui.Start).Padding(px(t, 2), px(t, 2)).Radius(px(t, 6)).
				Label(i18n.N(n, "%d in progress", "%d in progress", n)).Grow(1).MinWidth(0)
			line.Children(func() {
				statusDot(c, k.Accent)
				ui.Text(c, i18n.N(n, "%d in progress", "%d in progress", n)).FontSize(px(t, fsMicro)).TextColor(k.Text2).SingleLine().Ellipsis("…").MinWidth(0)
			})
			if line.Clicked() {
				a.actPinned = !a.actPinned
			}
			if open {
				all := ghostBase(c.Key("activity-all")).Padding(px(t, 2), px(t, 6)).Label(i18n.T("View all")).Shrink(0)
				all.Children(func() {
					ui.Text(c, i18n.T("View all")+" ›").FontSize(px(t, fsMicro)).TextColor(k.Text2).SingleLine()
				})
				if all.Clicked() {
					a.actPinned = false
					a.navigate("/tasks")
				}
			}
		})
	})
}

// activityCard is one card, top DIPs from the stack's top when open.
func (a *app) activityCard(c *ui.Context, card actCard, top float32) ui.Element {
	t := c.Theme()
	k := tok(t)
	label := card.title
	if card.detail != "" {
		label += " · " + card.detail
	}
	border := k.Line
	if card.danger {
		border = t.Danger
	}
	box := ui.Column(c.Key(card.key)).Absolute().Left(0).Right(0).Top(top).Height(px(t, activity.CardHeight)).
		Radius(px(t, radiusNotice)).Background(k.Bg).Border(1, border).Shadow(0, 1, 4, 0, ui.RGBA(0, 0, 0, 0.06))
	box.Children(func() {
		b := ui.ButtonBase(c.Key("open")).Absolute().Left(0).Right(0).Top(0).Bottom(0).Radius(px(t, radiusNotice)).Label(label).Tooltip(label)
		if b.Hovered() {
			b.Background(k.Subtle)
		}
		showAction := card.action != nil && (card.danger || box.Hovered())
		b.Children(func() {
			ui.Column(c).Grow(1).Justify(ui.Center).Gap(px(t, 5)).Padding(0, px(t, 10)).Children(func() {
				ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
					col := k.Text2
					switch {
					case card.danger:
						col = t.Danger
					case card.done:
						col = t.Success
					}
					ui.Icon(c, card.icon.Linear).Size(px(t, 14), px(t, 14)).TextColor(col).Shrink(0)
					ui.Text(c, card.title).FontSize(px(t, 12)).FontWeight(600).SingleLine().Ellipsis("…").MinWidth(0).Grow(1)
					if showAction {
						ui.Box(c).Width(px(t, 22)).Shrink(0)
					} else if card.detail != "" {
						ui.Text(c, card.detail).FontSize(px(t, 11)).TextColor(k.Text3).SingleLine().Shrink(0)
					}
				})
				switch {
				case card.frac >= 0 && card.frac <= 1:
					ui.Box(c).Height(px(t, 2)).Radius(px(t, 1)).Background(k.G[2]).Children(func() {
						ui.Box(c).WidthPercent(float32(card.frac) * 100).Height(px(t, 2)).Radius(px(t, 1)).Background(k.Accent)
					})
				case card.frac < 0:
					ui.Progress(c, -1).Height(px(t, 2)).Label(card.title)
				}
			})
		})
		if b.Clicked() && card.open != nil {
			a.actPinned = false
			card.open()
		}
		if showAction {
			act := card.action
			ui.Box(c).Absolute().Right(px(t, 6)).Top(0).Bottom(0).Justify(ui.Center).Children(func() {
				ib := ghostBase(c.Key("action")).Size(px(t, 22), px(t, 22)).Center().Label(act.tip).Tooltip(act.tip)
				ib.Children(func() {
					col := k.Text2
					if card.danger {
						col = t.Danger
					}
					ui.Icon(c, act.icon.Linear).Size(px(t, 14), px(t, 14)).TextColor(col)
				})
				if ib.Clicked() {
					act.run()
				}
			})
		}
	})
	return box
}

// activityRail is the cards on the icon rail: one icon with their count
// (red while one needs the user); it opens the Tasks page.
func (a *app) activityRail(c *ui.Context) {
	n := len(a.actCards)
	if a.actTick {
		c.After(time.Second)
	}
	if n == 0 {
		return
	}
	label := i18n.N(n, "%d in progress", "%d in progress", n)
	ic := tasksIcon
	for _, cd := range a.actCards {
		if cd.danger {
			ic = icons.DangerTriangle
			label = cd.title + " · " + label
		}
	}
	a.railButton(c, "rail-activity", ic, label, label, section(a.router.Path()) == "/tasks", itoa(n), func() { a.navigate("/tasks") })
}
