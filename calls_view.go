package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/store"
	"github.com/hehehai/AudioSmith/internal/telephony"
)

// callsHistoryWidth is the recent calls' column.
const callsHistoryWidth = 280

// callsPage is /calls: recent calls, the call shown, a new call. The
// recent calls are a drawer when the window is narrow; the new call is a
// third column when it is wide, else a tab beside the call.
func (a *app) callsPage(c *ui.Context) {
	t := c.Theme()
	k := tok(t)
	pw := a.pageWidth(c)
	histInline := pw-px(t, callsHistoryWidth) >= px(t, 480)
	wide := histInline && pw-px(t, callsHistoryWidth+380) >= px(t, 480)
	pageHeader(c, i18n.T("Calls"), func() {
		if !histInline {
			tip := i18n.T("Show recent calls")
			if a.callHistOn {
				tip = i18n.T("Hide recent calls")
			}
			if panelToggle(c.Key("calls-hist-toggle"), icons.PanelLeft, a.callHistOn, tip).Clicked() {
				a.callHistOn = !a.callHistOn
			}
		}
		if labelOrIcon(c.Key("calls-inbound"), icons.PhoneCallingRounded, i18n.T("Incoming calls…"), pw < px(t, 640)).Clicked() {
			a.openInbound()
		}
		if iconButton(c.Key("calls-setup"), icons.Settings, i18n.T("Set up Twilio")).Clicked() {
			a.navigate("/calls/setup")
		}
	})
	main := func() {
		if wide {
			ui.Scroll(c.Key("calls-call")).Grow(1).MinWidth(0).Children(func() {
				ui.Column(c).Padding(px(t, sp1), px(t, sp6), px(t, sp6)).Gap(px(t, sp3)).Children(func() {
					a.callsBanner(c)
					a.callView(c)
				})
			})
			ui.Box(c).Width(1).Background(k.Line)
			ui.Scroll(c.Key("calls-new")).Width(px(t, 380)).Shrink(0).Children(func() {
				ui.Column(c).Padding(px(t, sp1), px(t, sp5), px(t, sp6)).Gap(px(t, sp3)).Children(func() { a.newCallForm(c) })
			})
			return
		}
		ui.Scroll(c.Key("calls-main")).Grow(1).MinWidth(0).Children(func() {
			ui.Column(c).Padding(px(t, sp1), px(t, sp6), px(t, sp6)).Gap(px(t, sp3)).MaxWidth(px(t, 760)).Children(func() {
				a.callsBanner(c)
				ui.Row(c).Children(func() {
					ui.Segmented(c.Key("calls-tab"), &a.callTab, i18n.T("New call"), i18n.T("Call")).Label(i18n.T("Show"))
				})
				if a.callTab == 0 {
					a.newCallForm(c)
				} else {
					a.callView(c)
				}
			})
		})
	}
	ui.Row(c.Key("calls-body")).Bind(&a.callPageH).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
		if histInline {
			ui.Column(c.Key("calls-history")).Width(px(t, callsHistoryWidth)).Shrink(0).Background(k.Subtle).Children(func() {
				a.callHistory(c)
			})
			ui.Box(c).Width(1).Background(k.Line)
		}
		main()
	})
	if !histInline && a.callHistOn {
		ui.Overlay(c, func() {
			w, h := c.Size()
			d := ui.Column(c.Key("calls-history-drawer")).Absolute().Left(w-pw).Top(titleBarHeight).Width(px(t, callsHistoryWidth)).
				Height(h-titleBarHeight-px(t, statusBarHeight)-1).Background(k.Bg).BorderWidth(0, 1, 0, 0).BorderColor(k.Line).
				Shadow(8, 0, 24, 0, ui.RGBA(0, 0, 0, 0.12)).Label(i18n.T("Recent calls"))
			d.Children(func() { a.callHistory(c) })
			if d.PressedOutside() || d.OverlayShortcut(0, ui.KeyEscape) {
				a.callHistOn = false
			}
		})
	}
	a.callDialogs(c)
}

// callsBanner lists what is missing before calls can be placed.
func (a *app) callsBanner(c *ui.Context) {
	t := c.Theme()
	k := tok(t)
	checks := a.callChecks()
	ready, n := callsReady(checks)
	if ready {
		return
	}
	fillCard(c.Key("calls-banner")).Gap(px(t, sp2)).Padding(px(t, sp3), px(t, sp4)).Children(func() {
		ui.Row(c).Gap(px(t, sp2)).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icons.DangerTriangle.Linear).Size(px(t, 16), px(t, 16)).TextColor(t.Warning)
			ui.Text(c, i18n.T("Finish setup to place calls")).FontWeight(600).Grow(1).MinWidth(0)
			caption(c, i18n.T("%d of %d ready", n, len(checks))).SingleLine()
			if iconButton(c.Key("calls-recheck"), icons.Refresh, i18n.T("Check again")).Disabled(a.tw.checking).Clicked() {
				a.checkTwilioAccount()
			}
		})
		for _, ck := range checks {
			if ck.ok {
				continue
			}
			ui.Row(c.Key("ck-" + ck.id)).Gap(px(t, sp2)).AlignItems(ui.Center).Children(func() {
				statusDot(c, t.Warning)
				ui.Column(c).Grow(1).MinWidth(0).Children(func() {
					ui.Text(c, ck.title)
					ui.Text(c, ck.detail).FontSize(px(t, fsCaption)).TextColor(k.Text2).Wrap()
				})
				if ghostButton(c.Key("fix"), i18n.T("Fix")).Clicked() {
					a.navigate(ck.fix)
				}
			})
		}
	})
}

// callHistory is the recent calls.
func (a *app) callHistory(c *ui.Context) {
	t := c.Theme()
	k := tok(t)
	ui.Row(c).Height(px(t, 40)).Padding(0, px(t, sp2), 0, px(t, sp4)).Gap(px(t, sp2)).AlignItems(ui.Center).Children(func() {
		live, max := 0, 1
		if a.callSvc != nil {
			live, max = a.callSvc.Running()
		}
		ui.Text(c, i18n.T("Recent calls")).FontSize(px(t, fsCaption)).FontWeight(600).TextColor(k.Text2).Grow(1).SingleLine()
		ui.Text(c, i18n.T("%d of %d lines in use", live, max)).FontSize(px(t, fsMicro)).TextColor(k.Text3).SingleLine().Ellipsis("…").MinWidth(0)
		if iconButton(c.Key("calls-refresh"), icons.Refresh, i18n.T("Refresh")).Clicked() {
			a.loadCalls()
		}
	})
	if len(a.callList) == 0 {
		ui.Column(c).Padding(px(t, sp4)).Children(func() {
			caption(c, i18n.T("No calls yet. Your calls will show up here.")).Wrap()
		})
		return
	}
	ui.Scroll(c.Key("calls-list")).Grow(1).MinHeight(0).Children(func() {
		ui.Column(c).Padding(px(t, sp1), px(t, sp2)).Gap(px(t, 2)).Children(func() {
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
	k := tok(t)
	b := ui.ButtonBase(c.Key("call-"+r.ID)).Padding(px(t, sp2), px(t, 10)).Radius(px(t, radiusControl)).Label(i18n.T("Open %s", r.Title))
	switch {
	case sel:
		b.Background(k.Pressed)
	case b.Hovered():
		b.Background(k.Hover)
	}
	b.Children(func() {
		ui.Column(c).Gap(px(t, 2)).Grow(1).MinWidth(0).Children(func() {
			ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Center).Children(func() {
				ic, tip := icons.ArrowRightUp, i18n.T("Outgoing")
				if r.Direction == services.CallInbound {
					ic, tip = icons.ArrowLeftDown, i18n.T("Incoming")
				}
				iconMark(c, ic, k.Text2, tip)
				ui.Text(c, telephony.MaskNumber(r.Number)).FontWeight(600).Grow(1).SingleLine().Ellipsis("…").MinWidth(0)
				a.callBadge(c, r.Status, r.Outcome)
			})
			title := r.Title
			if title == "" {
				title = i18n.T("No task")
			}
			ui.Text(c, title).FontSize(px(t, fsCaption)).MaxLines(2).Ellipsis("…")
			meta := i18n.DateTime(r.CreatedAt)
			if r.Duration > 0 {
				meta += " · " + callClock(time.Duration(r.Duration*float64(time.Second)))
			}
			ui.Text(c, meta).FontSize(px(t, fsMicro)).TextColor(k.Text3)
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
		ui.Row(c).Gap(px(t, 4)).AlignItems(ui.Center).Shrink(0).Children(func() {
			statusDot(c, t.Success)
			ui.Text(c, i18n.T("Live")).FontSize(px(t, fsMicro)).FontWeight(600).TextColor(t.Success)
		})
	case outcome != "":
		col := tok(t).Text2
		switch outcome {
		case telephony.OutcomeBooked, telephony.OutcomeDone:
			col = t.Success
		case telephony.OutcomeNeedsYou:
			col = t.Warning
		case telephony.OutcomeFailed:
			col = t.Danger
		}
		ui.Text(c, callOutcomeText(outcome)).FontSize(px(t, fsMicro)).FontWeight(600).TextColor(col).SingleLine().Shrink(0)
	}
}

// callView shows the selected call: live controls while it runs, the
// summary after.
func (a *app) callView(c *ui.Context) {
	t := c.Theme()
	call := a.callFull[a.callSel]
	if call == nil {
		emptyState(c, icons.PhoneCallingRounded, i18n.T("Choose a call, or place a new one."), "", nil)
		return
	}
	ui.Row(c.Key("cv-head")).Gap(px(t, sp2)).AlignItems(ui.Center).Children(func() {
		dir := i18n.T("Outgoing")
		if call.Direction == services.CallInbound {
			dir = i18n.T("Incoming")
		}
		ui.Text(c, telephony.MaskNumber(call.Number)).FontSize(px(t, fsSection)).FontWeight(600).SingleLine().Ellipsis("…").MinWidth(0)
		tagChip(c, dir)
		a.callBadge(c, call.Phase, call.Outcome)
		ui.Spacer(c)
		if call.Live() {
			if recordButton(c.Key("cv-hangup"), i18n.T("Hang up")).Clicked() {
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
	k := tok(t)
	ui.Row(c.Key("cv-steps")).Gap(px(t, 6)).AlignItems(ui.Center).Wrap().Children(func() {
		for i, s := range steps {
			if i > 0 {
				ui.Box(c).Width(px(t, 20)).Height(1).Background(k.Line)
			}
			label := callPhaseText(s)
			if i == 3 && call.Phase == services.CallFailed {
				label = callPhaseText(services.CallFailed)
			}
			ui.Row(c).Gap(px(t, 4)).AlignItems(ui.Center).Children(func() {
				col := k.Text3
				switch {
				case i < cur, i == cur && i == 3 && call.Phase != services.CallFailed:
					col = k.Text2
					ui.Icon(c, icons.CheckCircle.Linear).Size(px(t, 13), px(t, 13)).TextColor(t.Success)
				case i == cur && i == 3:
					col = t.Danger
					ui.Icon(c, icons.CloseCircle.Linear).Size(px(t, 13), px(t, 13)).TextColor(t.Danger)
				case i == cur:
					col = k.Text
					statusDot(c, k.Accent)
				default:
					statusDot(c, k.G[5])
				}
				tx := ui.Text(c, label).FontSize(px(t, fsCaption)).TextColor(col).SingleLine()
				if i == cur {
					tx.FontWeight(600)
				}
			})
		}
	})
}

// callLiveControls: the agent's state, the clock, take-over and Say.
func (a *app) callLiveControls(c *ui.Context, call *services.Call) {
	t := c.Theme()
	fillCard(c.Key("cv-live")).Gap(px(t, sp2)).Padding(px(t, sp3), px(t, sp4)).Children(func() {
		ui.Row(c).Gap(px(t, sp2)).AlignItems(ui.Center).Children(func() {
			if call.Phase == services.CallLive && call.State != "" {
				col := tok(t).Accent
				if call.State == telephony.StateSpeaking {
					col = t.Success
				}
				statusDot(c, col)
				ui.Text(c, callStateText(call.State)).FontWeight(600)
			}
			ui.Spacer(c)
			if !call.Answered.IsZero() {
				ui.Text(c, callClock(time.Since(call.Answered))+" / "+callClock(time.Duration(call.MaxMinutes)*time.Minute)).
					Font(monoFont).FontSize(px(t, fsCaption)).TextColor(tok(t).Text2)
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
				if in.Submitted() || ui.Button(c.Key("cv-say-go"), i18n.T("Say")).Disabled(strings.TrimSpace(a.callSay) == "").Clicked() {
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
		ui.Row(c.Key("cvd-" + k)).Gap(px(t, sp3)).Children(func() {
			ui.Text(c, label).FontSize(px(t, fsCaption)).TextColor(tok(t).Text2).Width(px(t, 90)).Shrink(0)
			ui.Text(c, value).FontSize(px(t, fsCaption)).Wrap().Grow(1).MinWidth(0).Selectable()
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
	k := tok(t)
	sectionTitle(c, i18n.T("Transcript"))
	if len(call.Transcript) == 0 {
		msg := i18n.T("No transcript for this call.")
		if call.Live() {
			msg = i18n.T("The transcript appears here once the call is answered.")
		}
		caption(c, msg)
		return
	}
	ui.Column(c.Key("cv-transcript")).Gap(px(t, 8)).Children(func() {
		for i, l := range call.Transcript {
			if l.Text == "" {
				continue
			}
			mine := l.Speaker != telephony.SpeakerCaller
			who := i18n.T("Them")
			bg := k.Subtle
			switch l.Speaker {
			case telephony.SpeakerAgent:
				who, bg = "AudioSmith", k.AccentSoft
			case telephony.SpeakerUser:
				who, bg = i18n.T("You"), k.Pressed
			}
			ui.Row(c.Key(i)).Children(func() {
				if mine {
					ui.Spacer(c)
				}
				ui.Column(c).Gap(px(t, 2)).Padding(px(t, sp2), px(t, sp3)).Radius(px(t, radiusCard)).Background(bg).MaxWidth(px(t, 460)).Shrink(1).MinWidth(0).Children(func() {
					head := who + " · " + callClock(l.At)
					ui.Text(c, head).FontSize(px(t, fsMicro)).TextColor(k.Text2)
					text := l.Text
					if l.Interrupted {
						text += " —"
					}
					tx := ui.Text(c, text).Wrap().Selectable()
					if l.Interrupted {
						tx.TextColor(k.Text2)
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
	k := tok(t)
	card(c.Key("cv-after")).Gap(px(t, sp2)).Children(func() {
		ui.Row(c).Gap(px(t, sp2)).AlignItems(ui.Center).Children(func() {
			sectionHeading(c, i18n.T("Summary")).Grow(1)
			a.callBadge(c, call.Phase, call.Outcome)
		})
		if call.Summary != "" {
			ui.Text(c, call.Summary).Wrap().Selectable()
		} else {
			caption(c, i18n.T("The summary appears here when it is ready."))
		}
		if call.Note != "" && call.Note != call.Summary {
			caption(c, i18n.T("Agent's note: %s", call.Note)).Wrap()
		}
		if s := callLatencyText(call.Turns); s != "" {
			ui.Text(c, s).FontSize(px(t, fsMicro)).TextColor(k.Text3).Wrap()
		}
		ui.Row(c).Gap(px(t, sp1)).AlignItems(ui.Center).Children(func() {
			if call.Direction == services.CallOutbound {
				if secondaryIconButton(c.Key("cv-again"), icons.PhoneCallingRounded, i18n.T("Call again")).Label(i18n.T("Call again")).Clicked() {
					a.callAgain(call)
				}
			}
			if iconButton(c.Key("cv-copy"), icons.Copy, i18n.T("Copy summary")).Disabled(call.Summary == "").Clicked() {
				c.Services().WriteClipboard(call.Summary)
				a.toast(i18n.T("Copied"))
			}
			if p := a.callSvc.RecordingPath(*call); p != "" {
				label, ic := i18n.T("Play recording"), icons.Play
				if a.pl.Playing("call-rec:" + call.ID) {
					label, ic = i18n.T("Stop"), icons.Stop
				}
				if iconButton(c.Key("cv-rec"), ic, label).Clicked() {
					if err := a.pl.Toggle("call-rec:"+call.ID, p); err != nil {
						a.toast(i18n.T("Could not play: %v", err))
					}
				}
			}
			ui.Spacer(c)
			if iconButton(c.Key("cv-delete"), icons.TrashBinMinimalistic, i18n.T("Delete")).Clicked() {
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
	k := tok(t)
	f := &a.callForm
	sectionHeading(c, i18n.T("New call"))
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
		col := k.Text2
		if bad {
			col = t.Danger
		}
		ui.Text(c.Key("nc-number-hint"), hint).FontSize(px(t, fsCaption)).TextColor(col)
	}
	ui.Field(c, i18n.T("What should it do?"), func() {
		ui.TextArea(c.Key("nc-brief"), &f.brief).Height(px(t, 96)).Placeholder(i18n.T("Book a table for two this Friday at 7 pm under the name Sam. If 7 is taken, anything between 6 and 8 is fine.")).
			Label(i18n.T("What should it do?"))
	})
	ui.Row(c.Key("nc-chips")).Gap(px(t, sp1)).Wrap().Children(func() {
		for i, ex := range callExamples() {
			b := chip(c.Key(i), ex.title, "", f.brief == ex.brief)
			if f.brief != ex.brief && !b.Hovered() {
				b.Background(k.Hover)
			}
			if b.Clicked() {
				f.brief = ex.brief
			}
		}
	})
	labels, _ := a.callVoices()
	ui.Field(c, i18n.T("Voice"), func() {
		if len(labels) == 0 {
			ui.Column(c.Key("nc-novoice")).Gap(px(t, 4)).Children(func() {
				statusMark(c, icons.DangerTriangle, t.Warning, i18n.T("No voice can place calls yet."))
				caption(c, i18n.T("Calls speak only in designed voices, or in your own cloned voice once you record its consent statement.")).Wrap()
				ui.Row(c).Gap(px(t, sp2)).Children(func() {
					if ghostButton(c.Key("nc-verify"), i18n.T("Verify a voice")).Clicked() {
						a.navigate("/voices")
					}
					if ghostButton(c.Key("nc-design"), i18n.T("Design a voice")).Clicked() {
						a.navigate("/design")
					}
				})
			})
			return
		}
		a.formVoice()
		ui.Select(c.Key("nc-voice"), &f.voice, labels).Label(i18n.T("Voice")).Width(px(t, 300)).Shrink(1).MinWidth(0)
	})
	sectionTitle(c, i18n.T("What it will say first"))
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
		ui.Column(c.Key("nc-nodisclose")).Children(func() {
			banner(c, icons.DangerTriangle, t.Warning, i18n.T("Without a disclosure the other person may not know they are talking to an AI. In many places telling them is required."), nil)
		})
	}
	mins := make([]string, len(callMinutes))
	for i, n := range callMinutes {
		mins[i] = minutesLabel(n)
	}
	ui.Field(c, i18n.T("Maximum length"), func() {
		ui.Select(c.Key("nc-minutes"), &f.minutes, mins).Label(i18n.T("Maximum length")).Width(px(t, 140))
	})
	// The plan.
	fillCard(c.Key("nc-plan")).Gap(px(t, 6)).Padding(px(t, sp3), px(t, sp4)).Children(func() {
		ui.Text(c, i18n.T("Plan preview")).FontWeight(600)
		if b := strings.TrimSpace(f.brief); b != "" {
			ui.Text(c, b).FontSize(px(t, fsCaption)).Wrap()
		} else {
			caption(c, i18n.T("Describe the task above and the plan appears here."))
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
			ui.Row(c).Gap(px(t, 6)).AlignItems(ui.Start).Children(func() {
				ui.Icon(c, icons.Check.Linear).Size(px(t, 13), px(t, 13)).TextColor(k.AccentInk).Shrink(0).Margin(px(t, 2), 0, 0)
				ui.Text(c, r).FontSize(px(t, fsCaption)).TextColor(k.Text2).Wrap().Grow(1).MinWidth(0)
			})
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
		if primaryButton(c.Key("nc-call"), label).Disabled(!ready || problem != "" || a.callBusy).Clicked() {
			a.callAsk = true
		}
		if a.callBusy {
			ui.Spinner(c)
		}
	})
	switch {
	case !ready:
		caption(c, i18n.T("Finish the setup steps above to place calls."))
	case problem != "":
		caption(c, problem)
	}
	if a.callErr != "" {
		ui.Text(c.Key("nc-err"), a.callErr).FontSize(px(t, fsCaption)).TextColor(t.Danger).Wrap()
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
				sectionHeading(c, i18n.T("Call %s now?", num))
				ui.Text(c, i18n.T("AudioSmith will call as %s and follow this task for up to %d min:", voice, a.formMinutes())).Wrap()
				fillCard(c).Padding(px(t, sp3)).Children(func() {
					ui.Text(c, strings.TrimSpace(f.brief)).FontSize(px(t, fsCaption)).TextColor(tok(t).Text2).Wrap().MaxLines(6).Ellipsis("…")
				})
				ui.Row(c).Gap(px(t, 8)).Children(func() {
					ui.Spacer(c)
					if ui.Button(c.Key("cc-cancel"), i18n.T("Cancel")).AutoFocus().Clicked() {
						a.callAsk = false
					}
					if primaryButton(c.Key("cc-call"), i18n.T("Call")).Clicked() {
						a.placeCall()
					}
				})
			})
		})
	}
	if a.callInOpen {
		ui.Modal(c.Key("calls-inbound-modal"), &a.callInOpen, func() {
			ui.Column(c.Key("ci")).Width(px(t, 460)).Gap(px(t, 10)).Padding(px(t, 18)).Children(func() {
				sectionHeading(c, i18n.T("Incoming calls"))
				caption(c, i18n.T("Choose what happens when someone calls your Twilio number.")).Wrap()
				segmentedFit(c.Key("ci-mode"), &a.callInMode, i18n.T("Greeting"), i18n.T("AI agent")).Label(i18n.T("When someone calls"))
				if a.callInMode == 0 {
					ui.Text(c, i18n.T("Play your greeting, then hang up.")).FontSize(px(t, 12)).Wrap()
				} else {
					ui.Text(c, i18n.T("Talk with the caller in your voice, following the brief below.")).FontSize(px(t, 12)).Wrap()
					ui.Field(c, i18n.T("What the agent should do"), func() {
						ui.TextArea(c.Key("ci-brief"), &a.callInBrief).Height(px(t, 80)).
							Placeholder(i18n.T("Take a message: ask who is calling and why, and when to call back.")).Label(i18n.T("What the agent should do"))
					})
				}
				caption(c, i18n.T("The voice, greeting and disclosure are set in the Twilio setup.")).Wrap()
				ui.Row(c).Gap(px(t, sp2)).Children(func() {
					if ghostButton(c.Key("ci-setup"), i18n.T("Open the setup")).Clicked() {
						a.callInOpen = false
						a.navigate("/calls/setup")
					}
					ui.Spacer(c)
					if ui.Button(c.Key("ci-cancel"), i18n.T("Cancel")).Clicked() {
						a.callInOpen = false
					}
					if primaryButton(c.Key("ci-save"), i18n.T("Save")).Clicked() {
						a.saveInbound()
					}
				})
			})
		})
	}
}
