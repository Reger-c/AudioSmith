package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/store"
	"github.com/hehehai/AudioSmith/internal/telephony"
)

// callsWide is the width from which the Calls page shows three panes.
const callsWide = 1280

// callsPage is /calls: recent calls, the call shown, a new call.
func (a *app) callsPage(c *ui.Context) {
	t := c.Theme()
	header(c, i18n.T("Calls"), func() {
		if ui.Button(c.Key("calls-inbound"), i18n.T("Incoming calls…")).Clicked() {
			a.openInbound()
		}
		if ui.Button(c.Key("calls-setup"), i18n.T("Set up Twilio")).Clicked() {
			a.navigate("/calls/setup")
		}
	})
	wide := false
	if b := a.callPageH.Bounds(c); b.W > 0 {
		wide = b.W/px(t, 1) >= callsWide
	}
	ui.Row(c.Key("calls-body")).Bind(&a.callPageH).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c.Key("calls-history")).Width(px(t, 290)).Shrink(0).Background(t.Surface.Alpha(0.5)).Children(func() {
			a.callHistory(c)
		})
		ui.Box(c).Width(1).Background(t.Border)
		if wide {
			ui.Scroll(c.Key("calls-call")).Grow(1).MinWidth(0).Children(func() {
				ui.Column(c).Padding(px(t, 18)).Gap(px(t, 14)).Children(func() {
					a.callsBanner(c)
					a.callView(c)
				})
			})
			ui.Box(c).Width(1).Background(t.Border)
			ui.Scroll(c.Key("calls-new")).Width(px(t, 400)).Shrink(0).Children(func() {
				ui.Column(c).Padding(px(t, 18)).Gap(px(t, 14)).Children(func() { a.newCallForm(c) })
			})
			return
		}
		ui.Scroll(c.Key("calls-main")).Grow(1).MinWidth(0).Children(func() {
			ui.Column(c).Padding(px(t, 18)).Gap(px(t, 14)).MaxWidth(px(t, 760)).Children(func() {
				a.callsBanner(c)
				ui.Segmented(c.Key("calls-tab"), &a.callTab, i18n.T("New call"), i18n.T("Call")).Label(i18n.T("Show"))
				if a.callTab == 0 {
					a.newCallForm(c)
				} else {
					a.callView(c)
				}
			})
		})
	})
	a.callDialogs(c)
}

// callsBanner lists what is missing before calls can be placed.
func (a *app) callsBanner(c *ui.Context) {
	t := c.Theme()
	checks := a.callChecks()
	ready, n := callsReady(checks)
	if ready {
		return
	}
	ui.Column(c.Key("calls-banner")).Gap(px(t, 6)).Padding(px(t, 12)).Radius(8).Background(t.Warning.Alpha(0.12)).Children(func() {
		ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, i18n.T("Finish setup to place calls")).Bold().Grow(1)
			ui.Text(c, i18n.T("%d of %d ready", n, len(checks))).FontSize(px(t, 12)).TextColor(t.TextMuted)
			if ui.Button(c.Key("calls-recheck"), i18n.T("Check again")).Disabled(a.tw.checking).Clicked() {
				a.checkTwilioAccount()
			}
		})
		for _, ck := range checks {
			if ck.ok {
				continue
			}
			ui.Row(c.Key("ck-" + ck.id)).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "•").TextColor(t.Warning)
				ui.Column(c).Grow(1).MinWidth(0).Children(func() {
					ui.Text(c, ck.title).FontSize(px(t, 13))
					ui.Text(c, ck.detail).FontSize(px(t, 12)).TextColor(t.TextMuted).Wrap()
				})
				if ui.Button(c.Key("fix"), i18n.T("Fix")).Clicked() {
					a.navigate(ck.fix)
				}
			})
		}
	})
}

// callHistory is the recent calls.
func (a *app) callHistory(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Height(px(t, 40)).Padding(0, px(t, 10), 0, px(t, 14)).AlignItems(ui.Center).Children(func() {
		live, max := 0, 1
		if a.callSvc != nil {
			live, max = a.callSvc.Running()
		}
		ui.Text(c, i18n.T("Recent calls")).Bold().Grow(1)
		ui.Text(c, i18n.T("%d of %d lines in use", live, max)).FontSize(px(t, 11)).TextColor(t.TextMuted)
		if ui.Button(c.Key("calls-refresh"), "↻").Label(i18n.T("Refresh")).Tooltip(i18n.T("Refresh")).Clicked() {
			a.loadCalls()
		}
	})
	if len(a.callList) == 0 {
		ui.Column(c).Padding(px(t, 14)).Children(func() {
			ui.Text(c, i18n.T("No calls yet. Your calls will show up here.")).FontSize(px(t, 12)).TextColor(t.TextMuted).Wrap()
		})
		return
	}
	ui.Scroll(c.Key("calls-list")).Grow(1).MinHeight(0).Children(func() {
		ui.Column(c).Padding(px(t, 6)).Gap(px(t, 2)).Children(func() {
			for _, r := range a.callList {
				a.callRow(c, r)
			}
		})
	})
}

// callRow is a call in the list.
func (a *app) callRow(c *ui.Context, r store.CallRecord) {
	t := c.Theme()
	sel := a.callSel == r.ID
	b := ui.ButtonBase(c.Key("call-"+r.ID)).Padding(px(t, 8), px(t, 10)).Radius(6).Label(i18n.T("Open %s", r.Title))
	switch {
	case sel:
		b.Background(t.Accent.Alpha(0.14))
	case b.Hovered():
		b.Background(t.SurfaceHover)
	}
	b.Children(func() {
		ui.Column(c).Gap(px(t, 3)).Grow(1).MinWidth(0).Children(func() {
			ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
				arrow := "↗"
				if r.Direction == services.CallInbound {
					arrow = "↙"
				}
				ui.Text(c, arrow).TextColor(t.TextMuted)
				ui.Text(c, telephony.MaskNumber(r.Number)).FontSize(px(t, 13)).Bold().Grow(1).SingleLine()
				a.callBadge(c, r.Status, r.Outcome)
			})
			title := r.Title
			if title == "" {
				title = i18n.T("No task")
			}
			ui.Text(c, title).FontSize(px(t, 12)).MaxLines(2).Ellipsis("…")
			meta := i18n.DateTime(r.CreatedAt)
			if r.Duration > 0 {
				meta += " · " + callClock(time.Duration(r.Duration*float64(time.Second)))
			}
			ui.Text(c, meta).FontSize(px(t, 11)).TextColor(t.TextMuted)
		})
	})
	if b.Clicked() {
		a.selectCall(r.ID)
	}
}

// callBadge is "Live" or the outcome.
func (a *app) callBadge(c *ui.Context, phase, outcome string) {
	t := c.Theme()
	switch {
	case phase == services.CallDialing || phase == services.CallRinging || phase == services.CallLive:
		ui.Text(c, "● "+i18n.T("Live")).FontSize(px(t, 11)).Bold().TextColor(t.Success)
	case outcome != "":
		col := t.TextMuted
		switch outcome {
		case telephony.OutcomeBooked, telephony.OutcomeDone:
			col = t.Success
		case telephony.OutcomeNeedsYou:
			col = t.Warning
		case telephony.OutcomeFailed:
			col = t.Danger
		}
		ui.Text(c, callOutcomeText(outcome)).FontSize(px(t, 11)).Bold().TextColor(col)
	}
}

// callView shows the selected call: live controls while it runs, the
// summary after.
func (a *app) callView(c *ui.Context) {
	t := c.Theme()
	call := a.callFull[a.callSel]
	if call == nil {
		ui.Text(c, i18n.T("Choose a call, or place a new one.")).TextColor(t.TextMuted)
		return
	}
	ui.Row(c.Key("cv-head")).Gap(px(t, 10)).AlignItems(ui.Center).Children(func() {
		dir := i18n.T("Outgoing")
		if call.Direction == services.CallInbound {
			dir = i18n.T("Incoming")
		}
		ui.Text(c, telephony.MaskNumber(call.Number)).FontSize(px(t, 17)).Bold()
		ui.Badge(c, dir)
		a.callBadge(c, call.Phase, call.Outcome)
		ui.Spacer(c)
		if call.Live() {
			if ui.PrimaryButton(c.Key("cv-hangup"), i18n.T("Hang up")).Background(t.Danger).Clicked() {
				a.hangupCall(call.ID)
			}
		}
	})
	a.callTimeline(c, call)
	if call.Live() {
		a.callLiveControls(c, call)
	}
	if call.Error != "" {
		ui.Text(c.Key("cv-err"), call.Error).TextColor(t.Danger).Wrap()
	}
	a.callDetails(c, call)
	a.callTranscript(c, call)
	if !call.Live() {
		a.callAfter(c, call)
	}
}

// callTimeline is Dialing → Ringing → In call → Ended.
func (a *app) callTimeline(c *ui.Context, call *services.Call) {
	t := c.Theme()
	steps := []string{services.CallDialing, services.CallRinging, services.CallLive, services.CallEnded}
	cur := 0
	switch call.Phase {
	case services.CallRinging:
		cur = 1
	case services.CallLive:
		cur = 2
	case services.CallEnded, services.CallFailed:
		cur = 3
	}
	ui.Row(c.Key("cv-steps")).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
		for i, s := range steps {
			if i > 0 {
				ui.Box(c).Width(px(t, 24)).Height(1).Background(t.Border)
			}
			mark, col := "○", t.TextMuted
			switch {
			case i < cur:
				mark, col = "✓", t.Success
			case i == cur && i == 3 && call.Phase == services.CallFailed:
				mark, col = "✕", t.Danger
			case i == cur && i == 3:
				mark, col = "✓", t.Success
			case i == cur:
				mark, col = "●", t.Accent
			}
			label := callPhaseText(s)
			if i == 3 && call.Phase == services.CallFailed {
				label = callPhaseText(services.CallFailed)
			}
			ui.Text(c, mark+" "+label).FontSize(px(t, 12)).TextColor(col)
		}
	})
}

// callLiveControls: the agent's state, the clock, take-over and Say.
func (a *app) callLiveControls(c *ui.Context, call *services.Call) {
	t := c.Theme()
	ui.Column(c.Key("cv-live")).Gap(px(t, 10)).Padding(px(t, 12)).Radius(8).Background(t.Surface).Children(func() {
		ui.Row(c).Gap(px(t, 10)).AlignItems(ui.Center).Children(func() {
			if call.Phase == services.CallLive && call.State != "" {
				col := t.Accent
				if call.State == telephony.StateSpeaking {
					col = t.Success
				}
				ui.Text(c, "● "+callStateText(call.State)).Bold().TextColor(col)
			}
			ui.Spacer(c)
			if !call.Answered.IsZero() {
				ui.Text(c, callClock(time.Since(call.Answered))+" / "+callClock(time.Duration(call.MaxMinutes)*time.Minute)).
					Font(monoFont).FontSize(px(t, 12)).TextColor(t.TextMuted)
			}
		})
		if call.Phase != services.CallLive {
			return
		}
		on := call.Takeover
		settingRow(c, "cv-takeover", i18n.T("Take over"), i18n.T("Pause the agent and type what to say; the caller is still transcribed."), func() {
			if ui.Switch(c.Key("cv-takeover-switch"), &on).Label(i18n.T("Take over")).Changed() {
				a.takeoverCall(call.ID, on)
			}
		})
		if call.Takeover {
			ui.Row(c.Key("cv-say")).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
				in := ui.TextInput(c.Key("cv-say-text"), &a.callSay).Placeholder(i18n.T("What to say next")).Label(i18n.T("What to say next")).Grow(1).AutoFocus()
				if in.Submitted() || ui.PrimaryButton(c.Key("cv-say-go"), i18n.T("Say")).Disabled(strings.TrimSpace(a.callSay) == "").Clicked() {
					a.sayInCall(call.ID)
				}
			})
		}
	})
}

// callDetails are the call's fields.
func (a *app) callDetails(c *ui.Context, call *services.Call) {
	t := c.Theme()
	row := func(k, label, value string) {
		if value == "" {
			return
		}
		ui.Row(c.Key("cvd-" + k)).Gap(px(t, 10)).Children(func() {
			ui.Text(c, label).FontSize(px(t, 12)).TextColor(t.TextMuted).Width(px(t, 90)).Shrink(0)
			ui.Text(c, value).FontSize(px(t, 12)).Wrap().Grow(1).MinWidth(0).Selectable()
		})
	}
	ui.Column(c.Key("cv-details")).Gap(px(t, 4)).Children(func() {
		voice := call.VoiceName
		if call.Model != "" {
			voice += " · " + modelName(call.Model)
		}
		row("voice", i18n.T("Voice"), voice)
		row("when", i18n.T("When"), i18n.DateTime(call.Created))
		if call.Duration > 0 {
			row("dur", i18n.T("Duration"), callClock(time.Duration(call.Duration*float64(time.Second))))
		}
		task := call.Brief
		if call.Mode == telephony.ModeGreeting {
			task = i18n.T("Greeting only")
		}
		row("brief", i18n.T("Task"), task)
		row("disc", i18n.T("Said first"), call.Disclosure)
	})
}

// callTranscript shows the conversation as bubbles.
func (a *app) callTranscript(c *ui.Context, call *services.Call) {
	t := c.Theme()
	ui.Text(c, i18n.T("Transcript")).Bold()
	if len(call.Transcript) == 0 {
		msg := i18n.T("No transcript for this call.")
		if call.Live() {
			msg = i18n.T("The transcript appears here once the call is answered.")
		}
		ui.Text(c, msg).FontSize(px(t, 12)).TextColor(t.TextMuted)
		return
	}
	ui.Column(c.Key("cv-transcript")).Gap(px(t, 8)).Children(func() {
		for i, l := range call.Transcript {
			if l.Text == "" {
				continue
			}
			mine := l.Speaker != telephony.SpeakerCaller
			who := i18n.T("Them")
			bg := t.Surface
			switch l.Speaker {
			case telephony.SpeakerAgent:
				who, bg = "AudioSmith", t.Accent.Alpha(0.14)
			case telephony.SpeakerUser:
				who, bg = i18n.T("You"), t.Warning.Alpha(0.14)
			}
			ui.Row(c.Key(i)).Children(func() {
				if mine {
					ui.Spacer(c)
				}
				ui.Column(c).Gap(px(t, 2)).Padding(px(t, 8), px(t, 10)).Radius(10).Background(bg).MaxWidth(px(t, 460)).Children(func() {
					head := who + " · " + callClock(l.At)
					ui.Text(c, head).FontSize(px(t, 10)).TextColor(t.TextMuted)
					text := l.Text
					if l.Interrupted {
						text += " —"
					}
					tx := ui.Text(c, text).FontSize(px(t, 13)).Wrap().Selectable()
					if l.Interrupted {
						tx.TextColor(t.TextMuted)
					}
				})
				if !mine {
					ui.Spacer(c)
				}
			})
		}
	})
}

// callAfter is the summary and the actions of an ended call.
func (a *app) callAfter(c *ui.Context, call *services.Call) {
	t := c.Theme()
	ui.Column(c.Key("cv-after")).Gap(px(t, 8)).Padding(px(t, 12)).Radius(8).Background(t.Surface).Children(func() {
		ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, i18n.T("Summary")).Bold().Grow(1)
			a.callBadge(c, call.Phase, call.Outcome)
		})
		if call.Summary != "" {
			ui.Text(c, call.Summary).Wrap().Selectable()
		} else {
			ui.Text(c, i18n.T("The summary appears here when it is ready.")).FontSize(px(t, 12)).TextColor(t.TextMuted)
		}
		if call.Note != "" && call.Note != call.Summary {
			ui.Text(c, i18n.T("Agent's note: %s", call.Note)).FontSize(px(t, 12)).TextColor(t.TextMuted).Wrap()
		}
		if s := callLatencyText(call.Turns); s != "" {
			ui.Text(c, s).FontSize(px(t, 11)).TextColor(t.TextMuted).Wrap()
		}
		ui.Row(c).Gap(px(t, 8)).Children(func() {
			if call.Direction == services.CallOutbound {
				if ui.Button(c.Key("cv-again"), i18n.T("Call again")).Clicked() {
					a.callAgain(call)
				}
			}
			if ui.Button(c.Key("cv-copy"), i18n.T("Copy summary")).Disabled(call.Summary == "").Clicked() {
				c.Services().WriteClipboard(call.Summary)
				a.toast(i18n.T("Copied"))
			}
			if p := a.callSvc.RecordingPath(*call); p != "" {
				label := i18n.T("Play recording")
				if a.pl.Playing("call-rec:" + call.ID) {
					label = i18n.T("Stop")
				}
				if ui.Button(c.Key("cv-rec"), label).Clicked() {
					if err := a.pl.Toggle("call-rec:"+call.ID, p); err != nil {
						a.toast(i18n.T("Could not play: %v", err))
					}
				}
			}
			ui.Spacer(c)
			if ui.Button(c.Key("cv-delete"), i18n.T("Delete")).Clicked() {
				a.deleteCall(call.ID)
			}
		})
	})
}

// callLatencyText sums up the turns' latency (for tuning).
func callLatencyText(turns []telephony.TurnLatency) string {
	if len(turns) == 0 {
		return ""
	}
	var total, stt, llm, tts time.Duration
	for _, x := range turns {
		total += x.Total
		stt += x.STT
		llm += x.LLMFirst
		tts += x.TTSFirst
	}
	n := time.Duration(len(turns))
	ms := func(d time.Duration) string { return fmt.Sprintf("%.1f s", (d / n).Seconds()) }
	return i18n.T("Average response %s (listening %s, thinking %s, voice %s) over %d turns", ms(total), ms(stt), ms(llm), ms(tts), len(turns))
}

// newCallForm is the new call.
func (a *app) newCallForm(c *ui.Context) {
	t := c.Theme()
	f := &a.callForm
	ui.Text(c, i18n.T("New call")).FontSize(px(t, 15)).Bold()
	ui.Field(c, i18n.T("Phone number"), func() {
		ui.TextInput(c.Key("nc-number"), &f.number).Placeholder("+1 555 010 0199").Label(i18n.T("Phone number"))
	})
	num := telephony.NormalizeNumber(f.number)
	hint, bad := i18n.T("Include the country code, like +44 for the UK or +86 for China."), false
	switch telephony.CheckNumber(num) {
	case telephony.NumberOK:
		if r := callRegionName(telephony.Region(num)); r != "" {
			hint = i18n.T("Calling %s", r)
		} else {
			hint = ""
		}
	case telephony.NumberNoPlus:
		if len(f.number) > 3 {
			hint, bad = i18n.T("Start with + and the country code, like +1 or +44."), true
		}
	case telephony.NumberBadShape:
		if len(f.number) > 3 {
			hint, bad = i18n.T("That doesn't look like a full international number."), true
		}
	}
	if hint != "" {
		col := t.TextMuted
		if bad {
			col = t.Danger
		}
		ui.Text(c.Key("nc-number-hint"), hint).FontSize(px(t, 12)).TextColor(col)
	}
	ui.Field(c, i18n.T("What should it do?"), func() {
		ui.TextArea(c.Key("nc-brief"), &f.brief).Height(px(t, 96)).Placeholder(i18n.T("Book a table for two this Friday at 7 pm under the name Sam. If 7 is taken, anything between 6 and 8 is fine.")).
			Label(i18n.T("What should it do?"))
	})
	ui.Row(c.Key("nc-chips")).Gap(px(t, 6)).Wrap().Children(func() {
		for i, ex := range callExamples() {
			if ui.Button(c.Key(i), ex.title).Clicked() {
				f.brief = ex.brief
			}
		}
	})
	labels, _ := a.callVoices()
	ui.Field(c, i18n.T("Voice"), func() {
		if len(labels) == 0 {
			ui.Column(c.Key("nc-novoice")).Gap(px(t, 4)).Children(func() {
				ui.Text(c, i18n.T("No voice can place calls yet.")).TextColor(t.Warning)
				ui.Text(c, i18n.T("Calls speak only in designed voices, or in your own cloned voice once you record its consent statement.")).FontSize(px(t, 12)).TextColor(t.TextMuted).Wrap()
				ui.Row(c).Gap(px(t, 8)).Children(func() {
					if ui.Button(c.Key("nc-verify"), i18n.T("Verify a voice")).Clicked() {
						a.navigate("/voices")
					}
					if ui.Button(c.Key("nc-design"), i18n.T("Design a voice")).Clicked() {
						a.navigate("/design")
					}
				})
			})
			return
		}
		a.formVoice()
		ui.Select(c.Key("nc-voice"), &f.voice, labels).Label(i18n.T("Voice")).Width(px(t, 300))
	})
	ui.Text(c, i18n.T("What it will say first")).Bold()
	settingRow(c, "nc-disclose", i18n.T("AI disclosure"), i18n.T("Plays in full before the conversation starts; it can't be interrupted."), func() {
		ui.Switch(c.Key("nc-disclose-switch"), &f.disclose).Label(i18n.T("AI disclosure"))
	})
	if f.disclose {
		if !f.edited {
			f.disclosure, _ = a.formDisclosure()
		}
		if ui.TextArea(c.Key("nc-disclosure"), &f.disclosure).Height(px(t, 56)).Label(i18n.T("What it will say first")).Changed() {
			f.edited = true
		}
	} else {
		ui.Text(c.Key("nc-nodisclose"), i18n.T("Without a disclosure the other person may not know they are talking to an AI. In many places telling them is required.")).
			FontSize(px(t, 12)).TextColor(t.Warning).Wrap()
	}
	mins := make([]string, len(callMinutes))
	for i, n := range callMinutes {
		mins[i] = minutesLabel(n)
	}
	ui.Field(c, i18n.T("Maximum length"), func() {
		ui.Select(c.Key("nc-minutes"), &f.minutes, mins).Label(i18n.T("Maximum length")).Width(px(t, 140))
	})
	// The plan.
	ui.Column(c.Key("nc-plan")).Gap(px(t, 6)).Padding(px(t, 12)).Radius(8).Border(1, t.Border).Children(func() {
		ui.Text(c, i18n.T("Plan preview")).Bold()
		if b := strings.TrimSpace(f.brief); b != "" {
			ui.Text(c, b).FontSize(px(t, 12)).Wrap()
		} else {
			ui.Text(c, i18n.T("Describe the task above and the plan appears here.")).FontSize(px(t, 12)).TextColor(t.TextMuted)
		}
		var rules []string
		if f.disclose {
			rules = append(rules, i18n.T("Opens with the disclosure."))
		}
		rules = append(rules, i18n.T("Says it is an AI if asked, and never claims to be you."),
			i18n.T("Won't share payment details, passwords or ID numbers."),
			i18n.T("Asks you when it is unsure; you can take over at any time."),
			i18n.T("Ends politely, and hangs up after %d min at most.", a.formMinutes()))
		for _, r := range rules {
			ui.Text(c, "✓ "+r).FontSize(px(t, 12)).TextColor(t.TextMuted).Wrap()
		}
	})
	ready, _ := callsReady(a.callChecks())
	problem := a.callFormProblem()
	label := i18n.T("Call")
	if telephony.ValidNumber(num) {
		label = i18n.T("Call %s", num)
	}
	if a.callBusy {
		label = i18n.T("Starting call…")
	}
	ui.Row(c.Key("nc-go")).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
		if ui.PrimaryButton(c.Key("nc-call"), label).Disabled(!ready || problem != "" || a.callBusy).Clicked() {
			a.callAsk = true
		}
		if a.callBusy {
			ui.Spinner(c)
		}
	})
	switch {
	case !ready:
		ui.Text(c, i18n.T("Finish the setup steps above to place calls.")).FontSize(px(t, 12)).TextColor(t.TextMuted)
	case problem != "":
		ui.Text(c, problem).FontSize(px(t, 12)).TextColor(t.TextMuted)
	}
	if a.callErr != "" {
		ui.Text(c.Key("nc-err"), a.callErr).FontSize(px(t, 12)).TextColor(t.Danger).Wrap()
	}
}

// callExample is an example task.
type callExample struct{ title, brief string }

func callExamples() []callExample {
	return []callExample{
		{i18n.T("Book a restaurant"), i18n.T("Book a table for two this Friday at 7 pm under the name Sam. If 7 is taken, anything between 6 and 8 is fine. Ask whether they have outdoor seating.")},
		{i18n.T("Reschedule an appointment"), i18n.T("I have an appointment on Tuesday at 10 am under the name Sam. Ask to move it to Thursday morning or Friday afternoon, and confirm the new time.")},
		{i18n.T("Ask opening hours"), i18n.T("Ask what their opening hours are this weekend and whether they are open on the public holiday on Monday.")},
	}
}

// callDialogs are the confirmation and the incoming-calls settings.
func (a *app) callDialogs(c *ui.Context) {
	t := c.Theme()
	if a.callAsk {
		f := &a.callForm
		num := telephony.NormalizeNumber(f.number)
		voice := strings.SplitN(f.voice, " · ", 2)[0]
		ui.Modal(c.Key("calls-confirm"), &a.callAsk, func() {
			ui.Column(c.Key("cc")).Width(px(t, 440)).Gap(px(t, 10)).Padding(px(t, 18)).Children(func() {
				ui.Text(c, i18n.T("Call %s now?", num)).FontSize(px(t, 15)).Bold()
				ui.Text(c, i18n.T("AudioSmith will call as %s and follow this task for up to %d min:", voice, a.formMinutes())).FontSize(px(t, 13)).Wrap()
				ui.Text(c, strings.TrimSpace(f.brief)).FontSize(px(t, 12)).TextColor(t.TextMuted).Wrap().MaxLines(6).Ellipsis("…")
				ui.Row(c).Gap(px(t, 8)).Children(func() {
					ui.Spacer(c)
					if ui.Button(c.Key("cc-cancel"), i18n.T("Cancel")).AutoFocus().Clicked() {
						a.callAsk = false
					}
					if ui.PrimaryButton(c.Key("cc-call"), i18n.T("Call")).Clicked() {
						a.placeCall()
					}
				})
			})
		})
	}
	if a.callInOpen {
		ui.Modal(c.Key("calls-inbound-modal"), &a.callInOpen, func() {
			ui.Column(c.Key("ci")).Width(px(t, 460)).Gap(px(t, 10)).Padding(px(t, 18)).Children(func() {
				ui.Text(c, i18n.T("Incoming calls")).FontSize(px(t, 15)).Bold()
				ui.Text(c, i18n.T("Choose what happens when someone calls your Twilio number.")).FontSize(px(t, 12)).TextColor(t.TextMuted).Wrap()
				ui.Segmented(c.Key("ci-mode"), &a.callInMode, i18n.T("Greeting"), i18n.T("AI agent")).Label(i18n.T("When someone calls"))
				if a.callInMode == 0 {
					ui.Text(c, i18n.T("Play your greeting, then hang up.")).FontSize(px(t, 12)).Wrap()
				} else {
					ui.Text(c, i18n.T("Talk with the caller in your voice, following the brief below.")).FontSize(px(t, 12)).Wrap()
					ui.Field(c, i18n.T("What the agent should do"), func() {
						ui.TextArea(c.Key("ci-brief"), &a.callInBrief).Height(px(t, 80)).
							Placeholder(i18n.T("Take a message: ask who is calling and why, and when to call back.")).Label(i18n.T("What the agent should do"))
					})
				}
				ui.Text(c, i18n.T("The voice, greeting and disclosure are set in the Twilio setup.")).FontSize(px(t, 12)).TextColor(t.TextMuted).Wrap()
				ui.Row(c).Gap(px(t, 8)).Children(func() {
					if ui.Button(c.Key("ci-setup"), i18n.T("Open the setup")).Clicked() {
						a.callInOpen = false
						a.navigate("/calls/setup")
					}
					ui.Spacer(c)
					if ui.Button(c.Key("ci-cancel"), i18n.T("Cancel")).Clicked() {
						a.callInOpen = false
					}
					if ui.PrimaryButton(c.Key("ci-save"), i18n.T("Save")).Clicked() {
						a.saveInbound()
					}
				})
			})
		})
	}
}
