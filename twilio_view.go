package main

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/settings"
	"github.com/hehehai/AudioSmith/internal/telephony"
)

// twilioPage is /calls/setup (and /integrations/twilio): the guided setup.
func (a *app) twilioPage(c *ui.Context) {
	t := c.Theme()
	if !a.tw.autoChecked && a.tw.account == nil && a.prefs.Calls.AccountSID != "" && a.callHasTok {
		a.tw.autoChecked = true // once per run: the account's name and type
		a.checkTwilioAccount()
	}
	checks := a.callChecks()
	ready, _ := callsReady(checks)
	header(c, i18n.T("Twilio setup"), func() {
		status, col := i18n.T("Not set up"), t.TextMuted
		switch {
		case ready && a.callGWAddr != "":
			status, col = i18n.T("Live"), t.Success
		case a.prefs.Calls.Enabled && a.callGWAddr == "":
			status, col = i18n.T("Not listening"), t.Danger
		case ready:
			status, col = i18n.T("Ready"), t.Accent
		}
		ui.Text(c, "● "+status).FontSize(px(t, 12)).Bold().TextColor(col)
		if a.prefs.Calls.Enabled {
			if ui.Button(c.Key("tw-off"), i18n.T("Turn off calls")).Clicked() {
				a.setCallsOn(false)
			}
		} else if ui.PrimaryButton(c.Key("tw-on"), i18n.T("Turn on calls")).Disabled(a.prefs.Calls.AccountSID == "" || !a.callHasTok).Clicked() {
			a.setCallsOn(true)
		}
		if ui.Button(c.Key("tw-calls"), i18n.T("Open Calls")).Clicked() {
			a.navigate("/calls")
		}
	})
	wide := false
	if b := a.twPageH.Bounds(c); b.W > 0 {
		wide = b.W/px(t, 1) >= 1040
	}
	ui.Scroll(c.Key("tw-scroll")).Bind(&a.twPageH).Grow(1).Children(func() {
		body := func() {
			ui.Column(c.Key("tw-steps")).Gap(px(t, 14)).Grow(1).MinWidth(0).MaxWidth(px(t, 700)).Children(func() {
				ui.Text(c, i18n.T("Let an AI agent place and answer phone calls with your Twilio number. Twilio reaches AudioSmith through a tunnel you run; calls are off until you turn them on.")).
					FontSize(px(t, 13)).TextColor(t.TextMuted).Wrap()
				a.twStepAccount(c)
				a.twStepTunnel(c)
				a.twStepNumber(c)
				a.twStepBehaviour(c)
			})
		}
		rail := func() {
			ui.Column(c.Key("tw-rail")).Gap(px(t, 14)).Width(px(t, 330)).Shrink(0).Children(func() {
				a.twChecklist(c, checks)
				a.twTest(c)
				a.twLog(c)
				a.twHow(c)
			})
		}
		if wide {
			ui.Row(c).Padding(px(t, 20)).Gap(px(t, 20)).AlignItems(ui.Start).Children(func() { body(); rail() })
		} else {
			ui.Column(c).Padding(px(t, 20)).Gap(px(t, 20)).Children(func() { body(); rail() })
		}
	})
}

// twCard is a step's frame: its number, title and state.
func twCard(c *ui.Context, key string, n int, title string, done bool, fn func()) {
	t := c.Theme()
	ui.Column(c.Key(key)).Gap(px(t, 10)).Padding(px(t, 16)).Radius(10).Border(1, t.Border).Children(func() {
		ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, itoa(n)+" · "+title).FontSize(px(t, 14)).Bold().Grow(1)
			if done {
				ui.Text(c, "✓ "+i18n.T("Done")).FontSize(px(t, 12)).TextColor(t.Success)
			} else {
				ui.Text(c, i18n.T("To do")).FontSize(px(t, 12)).TextColor(t.TextMuted)
			}
		})
		fn()
	})
}

// twCode is a copyable command or URL.
func (a *app) twCode(c *ui.Context, key, code string) {
	t := c.Theme()
	ui.Row(c.Key(key)).Gap(px(t, 8)).AlignItems(ui.Center).Padding(px(t, 8), px(t, 10)).Radius(6).Background(t.Surface).Border(1, t.Border).Children(func() {
		ui.Text(c, code).Font(monoFont).FontSize(px(t, 12)).Grow(1).MinWidth(0).Wrap().Selectable()
		if ui.Button(c.Key("copy"), i18n.T("Copy")).Clicked() {
			c.Services().WriteClipboard(code)
			a.toast(i18n.T("Copied"))
		}
	})
}

func (a *app) twStepAccount(c *ui.Context) {
	t := c.Theme()
	w := &a.tw
	cfg := a.prefs.Calls
	saved := telephony.ValidAccountSID(cfg.AccountSID) && a.callHasTok
	twCard(c, "tw-account", 1, i18n.T("Twilio account"), saved && w.accountErr == "", func() {
		if saved && !w.replacing {
			ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
				line := i18n.T("Account %s", cfg.AccountSID[:8]+"…")
				if w.account != nil {
					line += " · " + w.account.Name
					if w.account.Type == "Trial" {
						line += " · " + i18n.T("trial")
					}
				}
				ui.Text(c, line).Grow(1).MinWidth(0).SingleLine()
				if w.checking {
					ui.Spinner(c)
				}
				if ui.Button(c.Key("tw-check"), i18n.T("Check")).Disabled(w.checking).Clicked() {
					a.checkTwilioAccount()
				}
				if ui.Button(c.Key("tw-replace"), i18n.T("Replace")).Clicked() {
					w.replacing, w.sid, w.token = true, cfg.AccountSID, ""
				}
				if ui.Button(c.Key("tw-remove"), i18n.T("Remove")).Clicked() {
					a.removeTwilioAccount()
				}
			})
			if w.account != nil && w.account.Type == "Trial" {
				ui.Text(c, i18n.T("Trial accounts can only call numbers verified in the Twilio Console, and callers first hear Twilio's trial message (press a key to continue).")).
					FontSize(px(t, 12)).TextColor(t.Warning).Wrap()
			}
		} else {
			ui.Text(c, i18n.T("Find both on the Twilio Console's home page, under Account Info.")).FontSize(px(t, 12)).TextColor(t.TextMuted).Wrap()
			ui.Field(c, i18n.T("Account SID"), func() {
				ui.TextInput(c.Key("tw-sid"), &w.sid).Placeholder("AC…").Label(i18n.T("Account SID")).Font(monoFont)
			})
			ui.Field(c, i18n.T("Auth token"), func() {
				ui.TextInput(c.Key("tw-token"), &w.token).Password().Label(i18n.T("Auth token"))
			})
			ui.Text(c, i18n.T("The token is kept in your Mac's Keychain. It is never shown again or exported.")).FontSize(px(t, 12)).TextColor(t.TextMuted).Wrap()
			ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
				if ui.PrimaryButton(c.Key("tw-save"), i18n.T("Save and check")).Disabled(w.checking || strings.TrimSpace(w.sid) == "").Clicked() {
					a.saveTwilioAccount()
				}
				if w.replacing && ui.Button(c.Key("tw-cancel"), i18n.T("Cancel")).Clicked() {
					w.replacing, w.accountErr = false, ""
				}
				if w.checking {
					ui.Spinner(c)
				}
			})
		}
		if w.accountErr != "" {
			ui.Text(c.Key("tw-account-err"), w.accountErr).FontSize(px(t, 12)).TextColor(t.Danger).Wrap()
		}
	})
}

func (a *app) twStepTunnel(c *ui.Context) {
	t := c.Theme()
	w := &a.tw
	cfg := a.prefs.Calls
	twCard(c, "tw-tunnel", 2, i18n.T("Public tunnel"), cfg.Public() != "" && w.tunnel == "ok", func() {
		ui.Text(c, i18n.T("Twilio needs a public https address. A tunnel gives your Mac one and forwards it to AudioSmith's call gateway — a separate port that serves only Twilio, never the Local API.")).
			FontSize(px(t, 12)).TextColor(t.TextMuted).Wrap()
		ui.Segmented(c.Key("tw-tool"), &w.tool, "cloudflared", "ngrok").Label(i18n.T("Tunnel"))
		install, run := tunnelCommands(w.tool, cfg.Port())
		ui.Text(c, i18n.T("Install it (Homebrew):")).FontSize(px(t, 12))
		a.twCode(c, "tw-install", install)
		ui.Text(c, i18n.T("Run it, and keep it running while calls are on:")).FontSize(px(t, 12))
		a.twCode(c, "tw-run", run)
		if w.tool == 1 {
			ui.Text(c, i18n.T("ngrok asks you to sign up and add its authtoken once.")).FontSize(px(t, 12)).TextColor(t.TextMuted).Wrap()
		} else {
			ui.Text(c, i18n.T("cloudflared prints an address ending in trycloudflare.com; it changes each time the tunnel starts.")).FontSize(px(t, 12)).TextColor(t.TextMuted).Wrap()
		}
		ui.Field(c, i18n.T("Public tunnel URL"), func() {
			ui.TextInput(c.Key("tw-url"), &w.publicURL).Placeholder("https://example.trycloudflare.com").Label(i18n.T("Public tunnel URL")).Font(monoFont)
		})
		ui.Row(c).Gap(px(t, 12)).AlignItems(ui.End).Children(func() {
			ui.Field(c, i18n.T("Gateway port"), func() {
				ui.NumberInput(c.Key("tw-port"), &w.port, 1024, 65535, 1).Label(i18n.T("Gateway port"))
			})
			ui.Column(c).Grow(1).Children(func() {
				ui.Checkbox(c.Key("tw-all"), &w.all, i18n.T("Listen on every interface (a tunnel on another machine)"))
			})
		})
		ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
			if ui.PrimaryButton(c.Key("tw-url-save"), i18n.T("Save")).Clicked() {
				a.saveTunnel()
			}
			if ui.Button(c.Key("tw-url-check"), i18n.T("Check")).Disabled(w.tunnel == "checking").Clicked() {
				a.checkTunnel()
			}
			switch {
			case w.tunnel == "checking":
				ui.Spinner(c)
			case !cfg.Enabled:
				ui.Text(c, i18n.T("The gateway starts when you turn on calls.")).FontSize(px(t, 12)).TextColor(t.TextMuted)
			case a.callGWAddr != "":
				ui.Text(c, i18n.T("Listening on %s", a.callGWAddr)).FontSize(px(t, 12)).TextColor(t.TextMuted)
			case a.callGWErr != "":
				ui.Text(c, a.callGWErr).FontSize(px(t, 12)).TextColor(t.Danger).Wrap()
			}
		})
		if w.urlErr != "" {
			ui.Text(c.Key("tw-url-err"), w.urlErr).FontSize(px(t, 12)).TextColor(t.Danger)
		}
		if w.tunnelMsg != "" {
			col := t.Danger
			if w.tunnel == "ok" {
				col = t.Success
			}
			ui.Text(c.Key("tw-tunnel-msg"), w.tunnelMsg).FontSize(px(t, 12)).TextColor(col).Wrap()
		}
		ui.Text(c, i18n.T("Anyone who learns the address can send requests to the gateway; it answers only requests signed with your auth token, and streams only with single-use tokens.")).
			FontSize(px(t, 11)).TextColor(t.TextMuted).Wrap()
	})
}

func (a *app) twStepNumber(c *ui.Context) {
	t := c.Theme()
	w := &a.tw
	cfg := a.prefs.Calls
	twCard(c, "tw-number", 3, i18n.T("Phone number"), cfg.Number != "" && cfg.Verified != 0, func() {
		if cfg.Public() == "" {
			ui.Text(c, i18n.T("Set the public tunnel first.")).FontSize(px(t, 12)).TextColor(t.TextMuted)
			return
		}
		if cfg.Number != "" {
			ui.Text(c, i18n.T("Calls come from %s.", cfg.Number)).Bold()
		}
		ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
			labels := make([]string, len(w.numbers))
			for i, n := range w.numbers {
				labels[i] = numberLabel(n)
			}
			if len(labels) > 0 {
				if w.numberSel == "" {
					w.numberSel = labels[0]
					for i, n := range w.numbers {
						if n.Number == cfg.Number {
							w.numberSel = labels[i]
						}
					}
				}
				ui.Select(c.Key("tw-numbers"), &w.numberSel, labels).Label(i18n.T("Your Twilio number")).Width(px(t, 300))
				if ui.PrimaryButton(c.Key("tw-use"), i18n.T("Use this number")).Disabled(w.hooking).Clicked() {
					a.useTwilioNumber()
				}
			}
			if ui.Button(c.Key("tw-list"), i18n.T("Load my numbers")).Disabled(w.listing || !a.callHasTok).Clicked() {
				a.loadTwilioNumbers()
			}
			if w.listing || w.hooking {
				ui.Spinner(c)
			}
		})
		if w.numbersErr != "" {
			ui.Text(c.Key("tw-numbers-err"), w.numbersErr).FontSize(px(t, 12)).TextColor(t.Danger).Wrap()
		}
		if w.hookMsg != "" {
			col := t.Success
			if w.hookErr {
				col = t.Danger
			}
			ui.Text(c.Key("tw-hook-msg"), w.hookMsg).FontSize(px(t, 12)).TextColor(col).Wrap()
		}
		ui.Text(c, i18n.T("Choosing a number sets its voice webhook for you. To set it by hand: Twilio Console → Phone Numbers → Active numbers → the number → Voice Configuration → \"A call comes in\": Webhook, HTTP POST, this URL:")).
			FontSize(px(t, 12)).TextColor(t.TextMuted).Wrap()
		a.twCode(c, "tw-voice-url", telephony.VoiceURL(cfg.Public()))
		if cfg.Verified != 0 {
			ui.Text(c, i18n.T("Twilio reached AudioSmith with a signed request on %s.", i18n.DateTime(time.Unix(cfg.Verified, 0)))).FontSize(px(t, 12)).TextColor(t.Success).Wrap()
		} else {
			ui.Text(c, i18n.T("This step is done once a request signed by Twilio arrives: call your number, or place a call.")).FontSize(px(t, 12)).TextColor(t.TextMuted).Wrap()
		}
	})
}

func (a *app) twStepBehaviour(c *ui.Context) {
	t := c.Theme()
	w := &a.tw
	cfg := a.prefs.Calls
	voiceOK := cfg.Voice != "" && a.findVoice(cfg.Voice) != nil
	twCard(c, "tw-behaviour", 4, i18n.T("Voice and behaviour"), voiceOK, func() {
		labels, _ := a.callVoices()
		ui.Field(c, i18n.T("Voice"), func() {
			if len(labels) == 0 {
				ui.Text(c, i18n.T("No voice can place calls yet.")).TextColor(t.Warning)
				return
			}
			if w.voice == "" {
				w.voice = labels[0]
			}
			ui.Select(c.Key("tw-voice"), &w.voice, labels).Label(i18n.T("Voice")).Width(px(t, 300))
		})
		ui.Text(c, i18n.T("Calls speak only in designed voices, or in your own cloned voice once you record its consent statement.")).FontSize(px(t, 12)).TextColor(t.TextMuted).Wrap()
		ml, _ := callModelOptions()
		ui.Field(c, i18n.T("Speech model"), func() {
			ui.Select(c.Key("tw-model"), &w.model, ml).Label(i18n.T("Speech model")).Width(px(t, 300))
		})
		ui.Field(c, i18n.T("Your name"), func() {
			ui.TextInput(c.Key("tw-name"), &w.userName).Placeholder(i18n.T("Sam")).Label(i18n.T("Your name")).Width(px(t, 240))
		})
		ui.Field(c, i18n.T("AI disclosure"), func() {
			ui.TextArea(c.Key("tw-disclosure"), &w.disclosure).Height(px(t, 56)).Placeholder(callDisclosureDefault(w.record)).Label(i18n.T("AI disclosure"))
		})
		ui.Text(c, i18n.T("Said first on every outgoing call. {name} becomes your name (or the voice's). Leave it empty for the default.")).FontSize(px(t, 12)).TextColor(t.TextMuted).Wrap()
		ui.Field(c, i18n.T("When someone calls"), func() {
			ui.Segmented(c.Key("tw-inbound"), &w.inbound, i18n.T("Play greeting"), i18n.T("AI agent")).Label(i18n.T("When someone calls"))
		})
		if w.inbound == 0 {
			ui.Field(c, i18n.T("Greeting"), func() {
				ui.TextArea(c.Key("tw-greeting"), &w.greeting).Height(px(t, 64)).Placeholder(i18n.T("Sam can't take your call right now. Please call back later.")).Label(i18n.T("Greeting"))
			})
			n := utf8.RuneCountInString(w.greeting)
			col := t.TextMuted
			if n > settings.MaxGreeting {
				col = t.Danger
			}
			ui.Text(c, i18n.T("%d of %d characters", n, settings.MaxGreeting)).FontSize(px(t, 11)).TextColor(col)
		} else {
			ui.Field(c, i18n.T("What the agent should do"), func() {
				ui.TextArea(c.Key("tw-brief"), &w.brief).Height(px(t, 64)).Placeholder(i18n.T("Take a message: ask who is calling and why, and when to call back.")).Label(i18n.T("What the agent should do"))
			})
		}
		settingRow(c, "tw-record", i18n.T("Record calls"), i18n.T("Keeps a stereo recording of each call (the caller left, the agent right), only when the disclosure says the call may be recorded. Recording laws differ by place."), func() {
			ui.Switch(c.Key("tw-record-switch"), &w.record).Label(i18n.T("Record calls"))
		})
		if w.record && strings.TrimSpace(w.disclosure) != "" && !callMentionsRecording(w.disclosure) {
			ui.Text(c.Key("tw-record-warn"), i18n.T("Your disclosure does not say the call is recorded, so calls won't be.")).FontSize(px(t, 12)).TextColor(t.Warning).Wrap()
		}
		ui.Text(c, i18n.T("You are responsible for following the laws on automated calls, AI disclosure and recording where you and the people you call are.")).
			FontSize(px(t, 11)).TextColor(t.TextMuted).Wrap()
		ui.Row(c).Children(func() {
			if ui.PrimaryButton(c.Key("tw-behaviour-save"), i18n.T("Save")).Clicked() {
				a.saveTwilioBehaviour()
			}
		})
	})
}

// twChecklist is the rail's readiness list.
func (a *app) twChecklist(c *ui.Context, checks []callCheck) {
	t := c.Theme()
	ui.Column(c.Key("tw-checklist")).Gap(px(t, 6)).Padding(px(t, 14)).Radius(10).Background(t.Surface).Children(func() {
		_, n := callsReady(checks)
		ui.Row(c).Children(func() {
			ui.Text(c, i18n.T("Readiness")).Bold().Grow(1)
			ui.Text(c, i18n.T("%d of %d ready", n, len(checks))).FontSize(px(t, 12)).TextColor(t.TextMuted)
		})
		for _, ck := range checks {
			b := ui.ButtonBase(c.Key("twck-" + ck.id)).Padding(px(t, 4)).Radius(6).Label(ck.title)
			if b.Hovered() {
				b.Background(t.SurfaceHover)
			}
			b.Children(func() {
				ui.Row(c).Gap(px(t, 8)).Grow(1).MinWidth(0).Children(func() {
					mark, col := "○", t.TextMuted
					if ck.ok {
						mark, col = "✓", t.Success
					}
					ui.Text(c, mark).TextColor(col)
					ui.Column(c).Grow(1).MinWidth(0).Children(func() {
						ui.Text(c, ck.title).FontSize(px(t, 12))
						ui.Text(c, ck.detail).FontSize(px(t, 11)).TextColor(t.TextMuted).MaxLines(2).Ellipsis("…")
					})
				})
			})
			if b.Clicked() && !ck.ok && ck.fix != "/calls/setup" {
				a.navigate(ck.fix)
			}
		}
	})
}

// twTest is the phone-quality preview.
func (a *app) twTest(c *ui.Context) {
	t := c.Theme()
	w := &a.tw
	ui.Column(c.Key("tw-test")).Gap(px(t, 8)).Padding(px(t, 14)).Radius(10).Background(t.Surface).Children(func() {
		ui.Text(c, i18n.T("Test")).Bold()
		ui.Text(c, i18n.T("Hear the greeting (or a sample line) in the chosen voice as a phone carries it: 8 kHz μ-law. Nothing is sent to Twilio.")).FontSize(px(t, 12)).TextColor(t.TextMuted).Wrap()
		labels, _ := a.callVoices()
		ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
			label := i18n.T("Play phone-quality preview")
			if a.pl.Playing("phone-preview") {
				label = i18n.T("Stop")
			}
			if ui.Button(c.Key("tw-preview"), label).Disabled(len(labels) == 0 || w.previewBusy).Clicked() {
				if a.pl.Playing("phone-preview") {
					a.pl.Toggle("phone-preview", w.previewPath)
				} else {
					a.previewPhone()
				}
			}
			if w.previewBusy {
				ui.Spinner(c)
			}
		})
		if w.previewErr != "" {
			ui.Text(c.Key("tw-preview-err"), w.previewErr).FontSize(px(t, 12)).TextColor(t.Danger).Wrap()
		}
	})
}

// twLog is the gateway's recent requests.
func (a *app) twLog(c *ui.Context) {
	t := c.Theme()
	ui.Column(c.Key("tw-log")).Gap(px(t, 4)).Padding(px(t, 14)).Radius(10).Background(t.Surface).Children(func() {
		live, max := a.callSvc.Running()
		ui.Row(c).Children(func() {
			ui.Text(c, i18n.T("Recent requests")).Bold().Grow(1)
			ui.Text(c, i18n.T("%d of %d lines in use", live, max)).FontSize(px(t, 11)).TextColor(t.TextMuted)
		})
		log := a.callSvc.GatewayLog()
		if len(log) == 0 {
			ui.Text(c, i18n.T("Nothing yet.")).FontSize(px(t, 12)).TextColor(t.TextMuted)
			return
		}
		for i, e := range log {
			if i >= 12 {
				break
			}
			sid := e.CallSID
			if len(sid) > 12 {
				sid = sid[:6] + "…" + sid[len(sid)-4:]
			}
			col := t.Text
			if strings.HasPrefix(e.Event, "invalid") || e.Event == telephony.RejectAccount || e.Event == telephony.RejectLimit || e.Event == "no_voice" {
				col = t.Danger
			}
			ui.Text(c.Key(i), e.At.Format("15:04:05")+" · "+firstOf(sid, "—")+" · "+gatewayEventText(e.Event)).FontSize(px(t, 11)).TextColor(col).SingleLine()
		}
	})
}

// twHow explains the call path and its safeguards.
func (a *app) twHow(c *ui.Context) {
	t := c.Theme()
	ui.Collapsible(c.Key("tw-how"), i18n.T("How it works"), &a.tw.howOpen, func() {
		ui.Column(c).Gap(px(t, 4)).Children(func() {
			ui.Text(c, i18n.T("Twilio → your tunnel → call gateway (%s) → AudioSmith", a.prefs.Calls.GatewayAddr())).FontSize(px(t, 12)).Font(monoFont).Wrap()
			for _, s := range []string{
				i18n.T("Every webhook must carry Twilio's signature for your auth token."),
				i18n.T("Audio streams need a single-use token that expires after a minute."),
				i18n.T("The gateway is its own port: it serves only Twilio, never the Local API."),
				i18n.T("The auth token stays in the Keychain."),
				i18n.T("Speech recognition, the voice and (with a local model) the conversation run on this Mac."),
			} {
				ui.Text(c, "• "+s).FontSize(px(t, 12)).TextColor(t.TextMuted).Wrap()
			}
		})
	})
}
