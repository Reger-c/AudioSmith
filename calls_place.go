package main

// A new call: the models it can use, the form's fields, the checks
// before placing it, placing it, and the actions on a call (again, hang
// up, take over, say, delete, the inbound settings); split from calls.go.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/llm"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/settings"
	"github.com/hehehai/AudioSmith/internal/telephony"
)

// callOptions maps the settings to the service's options (any goroutine).
func callOptions(st settings.Settings, gatewayOn bool) services.CallOptions {
	c := st.Calls
	disc := c.Disclosure
	if strings.TrimSpace(disc) == "" {
		disc = callDisclosureDefault(c.Record)
	}
	inbound := telephony.ModeGreeting
	if c.InboundMode() == settings.InboundAgent {
		inbound = telephony.ModeAgent
	}
	return services.CallOptions{AccountSID: c.AccountSID, PublicURL: c.Public(), Number: c.Number, UserName: c.UserName,
		Disclosure: disc, InboundDisclosure: callInboundDisclosureDefault(c.Record), Voice: c.Voice, Greeting: c.Greeting,
		Inbound: inbound, InboundBrief: c.InboundBrief, InboundMinutes: c.InboundLimit(), Concurrent: c.Concurrent(),
		Record: c.Record, STTModel: callSTT(st).Repo, GatewayOn: gatewayOn}
}

// callSTT is the model that transcribes callers.
func callSTT(st settings.Settings) catalog.Entry {
	return sttEntry(firstOf(st.Calls.STTModel, st.Dictation.Model), "")
}

// callModelFor is the TTS model a call speaks voice v with: the chosen
// one, the voice's own, the Studio's, then any downloaded model that can
// (any goroutine).
func callModelFor(lib *library, st settings.Settings, v core.Voice) (catalog.Entry, error) {
	c := catalog.Builtin()
	var cands []catalog.Entry
	if e, ok := c.ByID(st.Calls.Model); ok {
		cands = append(cands, e)
	}
	for _, repo := range []string{v.Model, v.CreatedWith} {
		if e, ok := c.ByRepo(repo); ok {
			cands = append(cands, e)
		} else if e, _, ok := replaceRemoved(repo, true); ok {
			cands = append(cands, e)
		}
	}
	if e, ok := c.ByID(st.Studio.Model); ok {
		cands = append(cands, e)
	}
	cands = append(cands, c.ForTask(catalog.TaskTTS)...)
	var first *catalog.Entry
	for i := range cands {
		e := cands[i]
		if ok, _ := voiceUsable(&v, e); !ok {
			continue
		}
		if _, err := lib.modelSnapshot(context.Background(), e.Repo); err == nil {
			return e, nil
		}
		if first == nil {
			first = &cands[i]
		}
	}
	if first != nil {
		return *first, nil
	}
	return catalog.Entry{}, fmt.Errorf("%s: %w", i18n.T("No speech model can speak this voice."), core.ErrUnsupported)
}

// callChat runs the call agent's language model through the router.
func callChat(router *llm.Router) func(ctx context.Context, msgs []telephony.Message, onDelta func(string)) (string, error) {
	return func(ctx context.Context, msgs []telephony.Message, onDelta func(string)) (string, error) {
		req := llm.Request{Temperature: llm.F64(0.4), MaxTokens: 400, Timeout: 30 * time.Second}
		for _, m := range msgs {
			req.Messages = append(req.Messages, llm.Message{Role: llm.Role(m.Role), Content: m.Content})
		}
		req.OnDelta = onDelta
		resp, err := router.Chat(ctx, callSkill, req)
		return resp.Text, err
	}
}

func minutesLabel(n int) string { return i18n.N(n, "%d min", "%d min", n) }

// formMinutes is the form's maximum length.
func (a *app) formMinutes() int {
	for _, n := range callMinutes {
		if minutesLabel(n) == a.callForm.minutes {
			return n
		}
	}
	return a.prefs.Calls.Minutes()
}

// formVoice is the form's voice id ("" = none can call).
func (a *app) formVoice() string {
	labels, ids := a.callVoices()
	for i, l := range labels {
		if l == a.callForm.voice {
			return ids[i]
		}
	}
	if len(ids) > 0 {
		a.callForm.voice = labels[0]
		return ids[0]
	}
	return ""
}

// formDisclosure is what the call will say first, and the request's
// value (nil = the default).
func (a *app) formDisclosure() (string, *string) {
	f := &a.callForm
	if !f.disclose {
		empty := ""
		return "", &empty
	}
	if f.edited {
		s := strings.TrimSpace(f.disclosure)
		return s, &s
	}
	name := ""
	if v := a.findVoice(a.formVoice()); v != nil {
		name = v.Name
	}
	return services.Disclosure(callOptions(a.prefs, true).Disclosure, a.prefs.Calls.UserName, name), nil
}

// callReady are the checks before a call can be placed.
type callCheck struct {
	id, title, detail string
	ok                bool
	fix               string // where Fix goes
}

// callChecks is the readiness checklist.
func (a *app) callChecks() []callCheck {
	c := a.prefs.Calls
	var out []callCheck
	acc := callCheck{id: "account", title: i18n.T("Twilio account connected"), fix: "/calls/setup"}
	switch {
	case !telephony.ValidAccountSID(c.AccountSID) || !a.callHasTok:
		acc.detail = i18n.T("Add your Account SID and auth token.")
	case a.tw.accountErr != "":
		acc.detail = a.tw.accountErr
	default:
		acc.ok = true
		acc.detail = c.AccountSID[:6] + "…"
	}
	out = append(out, acc)
	link := callCheck{id: "tunnel", title: i18n.T("Public link for Twilio"), fix: "/calls/setup"}
	switch {
	case c.Public() == "":
		link.detail = i18n.T("Run a tunnel and paste its https address.")
	case !c.Enabled:
		link.detail = i18n.T("Calls are turned off.")
	case a.callGWAddr == "":
		link.detail = firstOf(a.callGWErr, i18n.T("The call gateway is starting…"))
	default:
		link.ok = true
		link.detail = c.Public()
	}
	out = append(out, link)
	num := callCheck{id: "number", title: i18n.T("Phone number to call from"), fix: "/calls/setup"}
	if c.Number == "" {
		num.detail = i18n.T("Choose one of your Twilio numbers.")
	} else {
		num.ok, num.detail = true, c.Number
		if c.Verified == 0 {
			num.detail += " · " + i18n.T("waiting for Twilio's first signed request")
		}
	}
	out = append(out, num)
	lm := callCheck{id: "llm", title: i18n.T("Language model for the conversation"), fix: "/settings/writing"}
	if a.llmRouter != nil && a.llmRouter.Available(callSkill) {
		lm.ok = true
		if rs := a.llmRouter.Routes(callSkill); len(rs) > 0 {
			lm.detail = a.routeName(rs[0])
		}
	} else {
		lm.detail = i18n.T("Set up a language model in Settings → Writing help.")
	}
	out = append(out, lm)
	stt := callCheck{id: "asr", title: i18n.T("Speech recognition"), fix: "/models"}
	e := callSTT(a.prefs)
	if ready, known := a.modelOnDisk(e); ready || !known {
		stt.ok, stt.detail = true, e.DisplayName()
	} else {
		stt.detail = i18n.T("%s is not downloaded yet.", modelRef(e))
	}
	out = append(out, stt)
	vc := callCheck{id: "voice", title: i18n.T("A voice that may place calls"), fix: "/voices"}
	if labels, _ := a.callVoices(); len(labels) > 0 {
		vc.ok, vc.detail = true, i18n.N(len(labels), "%d voice", "%d voices", len(labels))
	} else {
		vc.detail = i18n.T("Design a voice, or record the consent statement for your own cloned voice.")
	}
	out = append(out, vc)
	return out
}

// callsReady reports whether every check passes, and how many do.
func callsReady(checks []callCheck) (bool, int) {
	n := 0
	for _, c := range checks {
		if c.ok {
			n++
		}
	}
	return n == len(checks), n
}

// callFormProblem says what stops the form ("" = nothing).
func (a *app) callFormProblem() string {
	f := &a.callForm
	switch {
	case telephony.CheckNumber(telephony.NormalizeNumber(f.number)) != telephony.NumberOK:
		return i18n.T("Add a valid number, a task and a voice to continue.")
	case strings.TrimSpace(f.brief) == "":
		return i18n.T("Add a valid number, a task and a voice to continue.")
	case a.formVoice() == "":
		return i18n.T("Add a valid number, a task and a voice to continue.")
	}
	return ""
}

// placeCall places the form's call (after the confirmation).
func (a *app) placeCall() {
	f := &a.callForm
	_, disc := a.formDisclosure()
	req := services.CallRequest{To: telephony.NormalizeNumber(f.number), Brief: strings.TrimSpace(f.brief), VoiceID: a.formVoice(),
		Disclosure: disc, MaxMinutes: a.formMinutes()}
	a.callAsk, a.callBusy, a.callErr = false, true, ""
	svc := a.callSvc
	a.background(func() {
		c, err := svc.Place(context.Background(), req)
		a.post(func() {
			a.callBusy = false
			if err != nil {
				a.callErr = callErrorText(err)
				a.toast(i18n.T("Couldn't start the call: %s", a.callErr))
				if c.ID != "" {
					a.callFull[c.ID] = &c
					a.callSel, a.callTab = c.ID, 1
				}
				return
			}
			a.callFull[c.ID] = &c
			a.callSel, a.callTab = c.ID, 1
			f.number, f.brief, f.numberBlurred = "", "", false
			a.callTick()
		})
	})
}

// callAgain fills the form from a call (the number only when it is known
// in full).
func (a *app) callAgain(c *services.Call) {
	f := &a.callForm
	f.brief, f.number, f.numberBlurred = c.Brief, c.Number, false
	labels, ids := a.callVoices()
	for i, id := range ids {
		if id == c.VoiceID {
			f.voice = labels[i]
		}
	}
	if c.MaxMinutes > 0 {
		f.minutes = minutesLabel(c.MaxMinutes)
	}
	a.callTab = 0
}

// hangupCall ends a live call.
func (a *app) hangupCall(id string) {
	svc := a.callSvc
	a.background(func() {
		err := svc.Hangup(context.Background(), id)
		if err != nil && !errors.Is(err, core.ErrNotFound) {
			a.post(func() { a.toast(callErrorText(err)) })
		}
	})
}

// takeoverCall pauses or resumes the agent.
func (a *app) takeoverCall(id string, on bool) {
	if err := a.callSvc.Takeover(id, on); err != nil {
		a.toast(callErrorText(err))
	}
}

// sayInCall speaks the user's line.
func (a *app) sayInCall(id string) {
	text := strings.TrimSpace(a.callSay)
	if text == "" {
		return
	}
	if err := a.callSvc.Say(id, text); err != nil {
		a.toast(callErrorText(err))
		return
	}
	a.callSay = ""
}

// deleteCall removes an ended call.
func (a *app) deleteCall(id string) {
	svc := a.callSvc
	a.background(func() {
		err := svc.Delete(context.Background(), id)
		a.post(func() {
			if err != nil {
				a.toast(i18n.T("Could not delete the call: %v", err))
			}
		})
	})
}

// saveInbound saves the incoming-calls dialog.
func (a *app) saveInbound() {
	mode := settings.InboundGreeting
	if a.callInMode == 1 {
		mode = settings.InboundAgent
	}
	brief := strings.TrimSpace(a.callInBrief)
	a.updateSettings(func(s *settings.Settings) { s.Calls.Inbound, s.Calls.InboundBrief = mode, brief })
	a.callInOpen = false
	a.toast(i18n.T("Saved"))
}

// openInbound opens the incoming-calls dialog.
func (a *app) openInbound() {
	a.callInMode = 0
	if a.prefs.Calls.InboundMode() == settings.InboundAgent {
		a.callInMode = 1
	}
	a.callInBrief = a.prefs.Calls.InboundBrief
	a.callInOpen = true
}
