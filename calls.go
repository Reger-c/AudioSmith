package main

// AI phone calls (M6b, thread #23): the Calls page (/calls: recent calls,
// the live call, a new call) and the Twilio setup (/calls/setup, also
// /integrations/twilio). The domain is internal/telephony (Twilio, the
// gateway, the agent loop) and services.Calls (the library, placing
// calls, the gateway's hooks); this file wires them to the app:
// settings.Calls, the auth token in the Keychain ("twilio.token"), the
// language model (a.llmRouter, skill "call_agent", falling back to the
// default route), voices (designed, or cloned with a verified consent
// recording) and their models, and the gateway's listener.
//
// Files: calls.go (state, wiring, actions), calls_view.go (/calls),
// calls_pages.go (shell and Projects hooks), calls_phone.go (number
// hints), twilio.go (setup actions), twilio_view.go (/calls/setup).

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/llm"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/settings"
	"github.com/hehehai/AudioSmith/internal/store"
	"github.com/hehehai/AudioSmith/internal/telephony"
)

// callSkill routes the call agent's language model (Settings → Writing
// help's default unless a route is set for it).
const callSkill llm.Skill = "call_agent"

// twilioTokenStore keeps the Twilio auth token: the Keychain item
// "twilio.token"; tests replace it.
var twilioTokenStore interface {
	Get() (string, error)
	Set(string) error
	Delete() error
} = hub.Secret("twilio.token", "AudioSmith Twilio auth token")

// twilioAPIBase is Twilio's REST API (tests point it at a fake).
var twilioAPIBase = telephony.DefaultAPIBase

// callMinutes are the choices of a call's maximum length.
var callMinutes = []int{2, 5, 10, 15, 30}

// callsState is the Calls area's state, on the main thread.
type callsState struct {
	callSvc *services.Calls
	callGW  *telephony.Gateway
	// callToken caches the auth token for the gateway's checks
	// (goroutine-safe).
	callToken *callSecret

	callGWAddr string // the address listened on; "" = off
	callGWErr  string
	callHasTok bool // a token is in the Keychain

	callList   []store.CallRecord        // recent calls
	callFull   map[string]*services.Call // calls read or followed, by id
	callLoaded bool
	callSel    string // the call shown
	callTab    int    // narrow windows: 0 new call, 1 the call
	callPageH  ui.Handle
	twPageH    ui.Handle
	callForm   callForm
	callBusy   bool // placing a call
	callErr    string
	callAsk    bool   // the confirmation dialog is open
	callSay    string // "What to say next"
	callTicker bool   // the clock ticks while a call is live

	callInOpen  bool // the incoming-calls dialog
	callInMode  int  // 0 greeting, 1 agent
	callInBrief string

	tw twilioSetup
}

// callForm is the new call form.
type callForm struct {
	number        string
	numberBlurred bool
	brief         string
	voice         string // label
	disclose      bool
	disclosure    string
	edited        bool // the disclosure was edited
	minutes       string
	numberFocus   bool
}

// callSecret is the token, cached.
type callSecret struct {
	mu  sync.Mutex
	tok string
}

func (s *callSecret) get() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tok
}

func (s *callSecret) set(t string) {
	s.mu.Lock()
	s.tok = t
	s.mu.Unlock()
}

// callDisclosureDefault is the outbound disclosure's default template.
func callDisclosureDefault(record bool) string {
	s := i18n.T("Hi, this is an AI assistant calling on behalf of {name}.")
	if record {
		s += " " + i18n.T("This call may be recorded.")
	}
	return s
}

// callInboundDisclosureDefault is the incoming one's.
func callInboundDisclosureDefault(record bool) string {
	s := i18n.T("Hi, you've reached the AI assistant of {name}.")
	if record {
		s += " " + i18n.T("This call may be recorded.")
	}
	return s
}

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
	return sttEntry(firstOf(st.Calls.STTModel, st.Dictation.Model), catalog.DefaultDictation)
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

// openCalls wires the calls service and the gateway; app.go calls it.
func (a *app) openCalls(lib *library) {
	a.callFull = map[string]*services.Call{}
	a.callToken = &callSecret{}
	a.callForm = callForm{disclose: true, minutes: minutesLabel(lib.settings.Get().Calls.Minutes())}
	router := a.llmRouter
	tok := a.callToken
	gwOn := func() bool { return lib.settings.Get().Calls.Enabled }
	a.callSvc = services.NewCalls(services.CallsConfig{
		Store: lib.store.Calls(), Bus: lib.bus, Scheduler: lib.sched, Takes: lib.svc.Render, Voices: lib.svc.Voices,
		Options: func() services.CallOptions { return callOptions(lib.settings.Get(), gwOn()) },
		Twilio: func() (*telephony.Client, error) {
			st := lib.settings.Get()
			t := tok.get()
			if st.Calls.AccountSID == "" || t == "" {
				return nil, fmt.Errorf("%s: %w", i18n.T("Connect your Twilio account first."), core.ErrUnsupported)
			}
			return &telephony.Client{AccountSID: st.Calls.AccountSID, AuthToken: t, Base: twilioAPIBase,
				HTTP: hub.NewHTTPClient(st.Models.Proxy, 20*time.Second)}, nil
		},
		Recipe: func(v core.Voice, text string) (core.Take, error) {
			e, err := callModelFor(lib, lib.settings.Get(), v)
			if err != nil {
				return core.Take{}, err
			}
			t := core.Take{Text: text, Model: e.Repo}
			a.fillVoice(&t, &v, e) // reads only the voice store's paths
			return t, nil
		},
		Chat: func(ctx context.Context, msgs []telephony.Message, onDelta func(string)) (string, error) {
			if router == nil || !router.Available(callSkill) {
				return "", errors.New("no language model is set up (Settings → Writing help)")
			}
			return callChat(router)(ctx, msgs, onDelta)
		},
		Signed:   func() { a.post(a.callSigned) },
		CacheDir: filepath.Dir(lib.peaks.Dir), // the app's cache folder
	})
	a.callGW = telephony.NewGateway(func() telephony.GatewayConfig {
		st := lib.settings.Get()
		return telephony.GatewayConfig{AccountSID: st.Calls.AccountSID, AuthToken: tok.get(), PublicURL: st.Calls.Public()}
	}, a.callSvc.Hooks(), nil)
	core.On(lib.bus, func(ev services.CallChanged) { a.post(func() { a.callChanged(ev) }) })
	core.On(lib.bus, func(ev settings.Changed) {
		if ev.Old.Calls != ev.New.Calls {
			a.post(func() {
				a.syncTwilioChanged(ev.Old.Calls, ev.New.Calls)
				a.applyCalls()
			})
		}
	})
	svc := a.callSvc
	a.background(func() {
		t, _ := twilioTokenStore.Get()
		tok.set(t)
		svc.Reconcile(context.Background())
		a.post(func() {
			a.callHasTok = t != ""
			a.applyCalls()
			a.loadCalls()
		})
	})
	a.syncTwilioForm()
}

// closeCalls hangs up running calls and stops the gateway (on quit).
func (a *app) closeCalls() {
	if a.callSvc == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	a.callSvc.Close(ctx)
	a.callGW.Close(ctx)
}

// applyCalls starts, moves or stops the gateway to match the settings.
func (a *app) applyCalls() {
	gw := a.callGW
	if gw == nil {
		return
	}
	cfg := a.prefs.Calls
	if !cfg.Enabled {
		if a.callGWAddr != "" || a.callGWErr != "" {
			a.callGWAddr, a.callGWErr = "", ""
			a.async(func() { gw.Stop(context.Background()) })
		}
		return
	}
	addr := cfg.GatewayAddr()
	if a.callGWAddr == addr && a.callGWErr == "" {
		return
	}
	a.async(func() {
		err := gw.Start(addr)
		a.post(func() {
			if !a.prefs.Calls.Enabled || a.prefs.Calls.GatewayAddr() != addr {
				return
			}
			switch {
			case errors.Is(err, telephony.ErrPortInUse):
				a.callGWAddr, a.callGWErr = "", i18n.T("Port %d is used by another program: choose another one.", cfg.Port())
			case err != nil:
				a.callGWAddr, a.callGWErr = "", i18n.T("The call gateway could not start: %v", err)
			default:
				a.callGWAddr, a.callGWErr = addr, ""
			}
		})
	})
}

// callSigned records that Twilio reached the gateway with a valid
// signature (the setup's number step is done).
func (a *app) callSigned() {
	now := time.Now().Unix()
	if now-a.prefs.Calls.Verified < 60 {
		return
	}
	a.updateSettings(func(s *settings.Settings) { s.Calls.Verified = now })
}

// loadCalls reads the recent calls.
func (a *app) loadCalls() {
	svc := a.callSvc
	a.background(func() {
		list, err := svc.List(context.Background(), 50)
		a.post(func() {
			a.callLoaded = true
			if err != nil {
				a.toast(i18n.T("Could not read the calls: %v", err))
				return
			}
			a.callList = list
			if a.callSel == "" && len(list) > 0 {
				a.selectCall(list[0].ID)
			}
		})
	})
}

// selectCall shows a call, reading it when needed.
func (a *app) selectCall(id string) {
	a.callSel, a.callTab = id, 1
	if _, ok := a.callFull[id]; ok || id == "" {
		return
	}
	svc := a.callSvc
	a.background(func() {
		c, err := svc.Get(context.Background(), id)
		if err != nil {
			return
		}
		a.post(func() {
			if _, ok := a.callFull[id]; !ok {
				a.callFull[id] = &c
			}
		})
	})
}

// callChanged follows a call's changes.
func (a *app) callChanged(ev services.CallChanged) {
	if ev.Deleted {
		delete(a.callFull, ev.ID)
		for i, r := range a.callList {
			if r.ID == ev.ID {
				a.callList = append(a.callList[:i:i], a.callList[i+1:]...)
				break
			}
		}
		if a.callSel == ev.ID {
			a.callSel = ""
		}
		return
	}
	c := ev.Call
	old := a.callFull[c.ID]
	a.callFull[c.ID] = c
	rec := store.CallRecord{ID: c.ID, CreatedAt: c.Created, UpdatedAt: time.Now(), Direction: c.Direction, Number: c.Number,
		Title: c.Title(), Status: c.Phase, Outcome: c.Outcome, Summary: c.Summary, ProviderSID: c.SID, Duration: c.Duration}
	found := false
	for i := range a.callList {
		if a.callList[i].ID == c.ID {
			a.callList[i], found = rec, true
		}
	}
	if !found {
		a.callList = append([]store.CallRecord{rec}, a.callList...)
		if c.Direction == services.CallInbound && c.Live() {
			a.notify(core.SeverityInfo, i18n.T("Incoming call"), telephony.MaskNumber(c.Number), "/calls")
			if a.callSel == "" || !a.callIsLive(a.callSel) {
				a.callSel = c.ID
			}
		}
	}
	if old != nil && old.Live() && !c.Live() {
		a.notify(core.SeveritySuccess, i18n.T("Call ended"), callOutcomeText(c.Outcome)+" · "+telephony.MaskNumber(c.Number), "/calls")
	}
	if old == nil || old.Phase != c.Phase || old.Outcome != c.Outcome || old.Summary != c.Summary {
		a.projects.dirty = true // the Projects page lists calls
	}
	if c.Live() {
		a.callTick()
	}
}

// callIsLive reports whether a call (by id) is running.
func (a *app) callIsLive(id string) bool {
	c := a.callFull[id]
	return c != nil && c.Live()
}

// callTick redraws every second while a call is live (its clock).
func (a *app) callTick() {
	if a.callTicker || a.win == nil {
		return
	}
	a.callTicker = true
	go func() {
		for {
			time.Sleep(time.Second)
			done := make(chan bool, 1)
			a.post(func() {
				live := false
				for _, c := range a.callFull {
					live = live || c.Live()
				}
				if !live {
					a.callTicker = false
				}
				done <- live
			})
			if !<-done {
				return
			}
		}
	}()
}

// callVoices are the voices that may make calls: labels and ids.
func (a *app) callVoices() (labels, ids []string) {
	for _, v := range a.voices {
		if services.VoiceAllowed(v) {
			tag := i18n.T("Designed")
			if v.Kind == core.VoiceCloned {
				tag = i18n.T("Your voice")
			}
			labels, ids = append(labels, v.Name+" · "+tag), append(ids, v.ID)
		}
	}
	return labels, ids
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
		stt.ok, stt.detail = true, e.Label
	} else {
		stt.detail = i18n.T("%s is not downloaded yet.", e.Label)
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

// callErrorText explains a failure to place or control a call.
func callErrorText(err error) string {
	var ae *telephony.APIError
	var lic *core.LicenceRequiredError
	switch {
	case errors.Is(err, services.ErrCallVoice):
		return i18n.T("Only designed voices and voices with a verified consent recording can make calls.")
	case errors.Is(err, services.ErrCallsBusy):
		return i18n.T("Another call is running. Wait for it to end.")
	case telephony.Unauthorized(err):
		return i18n.T("Twilio did not accept the Account SID or auth token.")
	case errors.As(err, &ae) && ae.Code == 21219:
		return i18n.T("Trial accounts can only call numbers verified in the Twilio Console.")
	case errors.As(err, &ae):
		return i18n.T("Twilio: %s", ae.Message)
	case errors.Is(err, core.ErrNotDownloaded):
		return i18n.T("The voice's speech model is not downloaded yet.")
	case errors.As(err, &lic):
		return i18n.T("The voice's speech model needs its licence accepted on the Models page.")
	case errors.Is(err, core.ErrUnsupported):
		return i18n.T("Finish the setup steps above to place calls.")
	}
	return err.Error()
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

// callOutcomeText names an outcome.
func callOutcomeText(o string) string {
	switch o {
	case telephony.OutcomeBooked:
		return i18n.T("Booked")
	case telephony.OutcomeDone:
		return i18n.T("Done")
	case telephony.OutcomeNotDone:
		return i18n.T("Not done")
	case telephony.OutcomeNeedsYou:
		return i18n.T("Needs you")
	case telephony.OutcomeFailed:
		return i18n.T("Failed")
	}
	return i18n.T("No outcome")
}

// callPhaseText names a phase.
func callPhaseText(p string) string {
	switch p {
	case services.CallDialing:
		return i18n.T("Dialing")
	case services.CallRinging:
		return i18n.T("Ringing")
	case services.CallLive:
		return i18n.T("In call")
	case services.CallFailed:
		return i18n.T("Not connected")
	}
	return i18n.T("Ended")
}

// callStateText names the agent's state.
func callStateText(s string) string {
	switch s {
	case telephony.StateThinking:
		return i18n.T("Thinking")
	case telephony.StateSpeaking:
		return i18n.T("Speaking")
	}
	return i18n.T("Listening")
}

// callClock is m:ss.
func callClock(d time.Duration) string {
	s := int(d.Seconds())
	if s < 0 {
		s = 0
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
