package main

// The Twilio setup (/calls/setup): four steps — the account (Account SID
// in the settings, auth token in the Keychain), the public tunnel to the
// call gateway (cloudflared or ngrok, a reachability check through it),
// the phone number (listed from Twilio, its voice webhook set to the
// gateway) and the voice and behaviour — with a readiness checklist, a
// phone-quality preview and the gateway's recent requests.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/settings"
	"github.com/hehehai/AudioSmith/internal/telephony"
)

// twilioSetup is the setup page's state.
type twilioSetup struct {
	sid, token string // typed
	replacing  bool   // Replace was clicked: the account fields show again
	account    *telephony.Account
	accountErr string
	checking   bool

	tool      int // 0 cloudflared, 1 ngrok
	publicURL string
	urlErr    string
	tunnel    string // "", "checking", "ok", "fail"
	tunnelMsg string
	port      float64
	all       bool

	numbers    []telephony.Number
	numbersErr string
	listing    bool
	numberSel  string // label
	hooking    bool
	hookMsg    string
	hookErr    bool

	voice      string // label
	model      string // label
	userName   string
	greeting   string
	disclosure string
	inbound    int // 0 greeting, 1 agent
	brief      string
	record     bool

	previewBusy bool
	previewPath string
	previewErr  string
	howOpen     bool
	autoChecked bool // the saved account was checked once
}

// syncTwilioForm fills the setup form from the settings.
func (a *app) syncTwilioForm() {
	c := a.prefs.Calls
	w := &a.tw
	w.sid = c.AccountSID
	w.publicURL = c.PublicURL
	w.port = float64(c.Port())
	w.all = c.GatewayAll
	w.tool = 0
	if c.TunnelTool() == "ngrok" {
		w.tool = 1
	}
	w.userName, w.greeting, w.brief, w.record = c.UserName, c.Greeting, c.InboundBrief, c.Record
	w.disclosure = c.Disclosure
	w.inbound = 0
	if c.InboundMode() == settings.InboundAgent {
		w.inbound = 1
	}
	w.voice, w.model = "", ""
	labels, ids := a.callVoices()
	for i, id := range ids {
		if id == c.Voice {
			w.voice = labels[i]
		}
	}
	ml, mids := callModelOptions()
	for i, id := range mids {
		if id == c.Model {
			w.model = ml[i]
		}
	}
	if w.model == "" {
		w.model = ml[0]
	}
}

// syncTwilioChanged updates the form's fields whose settings changed
// (elsewhere, or by the form's own save), keeping what is being typed in
// the others.
func (a *app) syncTwilioChanged(old, new settings.Calls) {
	w := &a.tw
	if old.AccountSID != new.AccountSID && !w.replacing {
		w.sid = new.AccountSID
	}
	if old.PublicURL != new.PublicURL {
		w.publicURL = new.PublicURL
	}
	if old.Port() != new.Port() {
		w.port = float64(new.Port())
	}
	if old.GatewayAll != new.GatewayAll {
		w.all = new.GatewayAll
	}
	if old.UserName != new.UserName || old.Greeting != new.Greeting || old.InboundBrief != new.InboundBrief || old.Record != new.Record ||
		old.Disclosure != new.Disclosure || old.Inbound != new.Inbound || old.Voice != new.Voice || old.Model != new.Model {
		pub, port, all, sid := w.publicURL, w.port, w.all, w.sid
		a.syncTwilioForm()
		w.publicURL, w.port, w.all, w.sid = pub, port, all, sid
	}
}

// callModelOptions are the setup's model choices: the voice's own, or a
// speech model.
func callModelOptions() (labels, ids []string) {
	labels, ids = []string{i18n.T("The voice's own model")}, []string{""}
	for _, e := range catalog.Builtin().ForTask(catalog.TaskTTS) {
		labels, ids = append(labels, e.DisplayName()), append(ids, e.ID)
	}
	return labels, ids
}

// twilioClient is a client with the saved credentials (any goroutine).
func (a *app) twilioClient(sid, token, proxy string) *telephony.Client {
	return &telephony.Client{AccountSID: sid, AuthToken: token, Base: twilioAPIBase, HTTP: hub.NewHTTPClient(proxy, 20*time.Second)}
}

// saveTwilioAccount saves the Account SID and token, then checks them.
func (a *app) saveTwilioAccount() {
	w := &a.tw
	sid := strings.TrimSpace(w.sid)
	token := strings.TrimSpace(w.token)
	if !telephony.ValidAccountSID(sid) {
		w.accountErr = i18n.T("The Account SID starts with AC followed by 32 letters and digits.")
		return
	}
	if token == "" && !a.callHasTok {
		w.accountErr = i18n.T("Paste the auth token too.")
		return
	}
	w.checking, w.accountErr = true, ""
	a.updateSettings(func(s *settings.Settings) { s.Calls.AccountSID = sid })
	tok, proxy := a.callToken, a.prefs.Models.Proxy
	a.async(func() {
		// Check first: a token Twilio refuses is not kept.
		try := token
		if try == "" {
			try = tok.get()
		}
		acc, err := a.twilioClient(sid, try, proxy).Account(context.Background())
		if token != "" && (err == nil || !telephony.Unauthorized(err)) {
			if serr := twilioTokenStore.Set(token); serr != nil {
				err = serr
			} else {
				tok.set(token)
			}
		}
		a.post(func() {
			w.checking = false
			a.callHasTok = tok.get() != ""
			if err != nil {
				w.accountErr = twilioError(err)
				return
			}
			w.account, w.token, w.replacing = &acc, "", false
			a.toast(i18n.T("Twilio account connected"))
			a.loadTwilioNumbers()
		})
	})
}

// checkTwilioAccount checks the saved credentials again.
func (a *app) checkTwilioAccount() {
	w := &a.tw
	sid := a.prefs.Calls.AccountSID
	if sid == "" || !a.callHasTok {
		return
	}
	w.checking, w.accountErr = true, ""
	tok, proxy := a.callToken.get(), a.prefs.Models.Proxy
	a.async(func() {
		acc, err := a.twilioClient(sid, tok, proxy).Account(context.Background())
		a.post(func() {
			w.checking = false
			if err != nil {
				w.accountErr = twilioError(err)
				return
			}
			w.account = &acc
		})
	})
}

// removeTwilioAccount forgets the credentials and turns calls off.
func (a *app) removeTwilioAccount() {
	w := &a.tw
	tok := a.callToken
	a.updateSettings(func(s *settings.Settings) { s.Calls.AccountSID, s.Calls.Enabled = "", false })
	a.applyCalls()
	w.sid, w.token, w.account, w.accountErr, w.numbers = "", "", nil, "", nil
	a.async(func() {
		err := twilioTokenStore.Delete()
		tok.set("")
		a.post(func() {
			a.callHasTok = false
			if err != nil && !errors.Is(err, hub.ErrNoKeychain) {
				a.toast(i18n.T("Could not remove the token: %v", err))
			}
		})
	})
}

// twilioError explains a Twilio failure.
func twilioError(err error) string {
	var ae *telephony.APIError
	switch {
	case telephony.Unauthorized(err):
		return i18n.T("Twilio did not accept the Account SID or auth token.")
	case errors.As(err, &ae):
		return i18n.T("Twilio: %s", ae.Message)
	case errors.Is(err, hub.ErrNoKeychain):
		return i18n.T("There is no Keychain to keep the token in.")
	}
	return i18n.T("Could not reach Twilio: %v", err)
}

// publicOrigin checks a tunnel address: an https origin without a path.
func publicOrigin(s string) (string, string) {
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	if s == "" {
		return "", ""
	}
	u, err := url.Parse(s)
	switch {
	case err != nil || u.Host == "":
		return "", i18n.T("Paste the tunnel's address, like https://example.trycloudflare.com.")
	case u.Scheme != "https":
		return "", i18n.T("Twilio needs an https address.")
	case u.Path != "" || u.RawQuery != "":
		return "", i18n.T("Paste only the address, without a path.")
	}
	return u.Scheme + "://" + u.Host, ""
}

// saveTunnel saves the public address and the gateway's port.
func (a *app) saveTunnel() {
	w := &a.tw
	origin, msg := publicOrigin(w.publicURL)
	w.urlErr = msg
	if msg != "" {
		return
	}
	w.publicURL = origin
	port := int(w.port)
	tool := "cloudflared"
	if w.tool == 1 {
		tool = "ngrok"
	}
	all := w.all
	a.updateSettings(func(s *settings.Settings) {
		s.Calls.PublicURL, s.Calls.Tunnel, s.Calls.GatewayAll = origin, tool, all
		if port == settings.DefaultGatewayPort {
			port = 0
		}
		s.Calls.GatewayPort = port
	})
	a.applyCalls()
	w.tunnel, w.tunnelMsg = "", ""
}

// setCallsOn turns the gateway on or off.
func (a *app) setCallsOn(on bool) {
	a.updateSettings(func(s *settings.Settings) { s.Calls.Enabled = on })
	a.applyCalls()
}

// twilioHTTP fetches a URL for the reachability check (tests replace it).
var twilioHTTP = func(proxy string) *http.Client { return hub.NewHTTPClient(proxy, 10*time.Second) }

// checkTunnel asks the gateway, through the public address, to answer a
// nonce: the tunnel reaches it.
func (a *app) checkTunnel() {
	w := &a.tw
	pub := a.prefs.Calls.Public()
	gw := a.callGW
	switch {
	case pub == "":
		w.tunnel, w.tunnelMsg = "fail", i18n.T("Save the tunnel's address first.")
		return
	case !a.prefs.Calls.Enabled || a.callGWAddr == "":
		w.tunnel, w.tunnelMsg = "fail", i18n.T("Turn on calls first: the gateway is not listening.")
		return
	}
	w.tunnel, w.tunnelMsg = "checking", ""
	client := twilioHTTP(a.prefs.Models.Proxy)
	addr := a.callGWAddr
	a.async(func() {
		nonce := gw.ExpectPing()
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, pub+telephony.PathPing+nonce, nil)
		resp, err := client.Do(req)
		status := 0
		if err == nil {
			status = resp.StatusCode
			resp.Body.Close()
		}
		ok := err == nil && status == http.StatusNoContent && gw.Pinged(nonce)
		a.post(func() {
			if ok {
				w.tunnel, w.tunnelMsg = "ok", i18n.T("The tunnel reaches the gateway on %s.", addr)
				return
			}
			w.tunnel = "fail"
			switch {
			case err != nil:
				w.tunnelMsg = i18n.T("Could not reach %s: %v", pub, err)
			default:
				w.tunnelMsg = i18n.T("%s answered %d: is the tunnel pointing at %s?", pub, status, addr)
			}
		})
	})
}

// tunnelCommands are the copyable install and run commands.
func tunnelCommands(tool int, port int) (install, run string) {
	target := "http://127.0.0.1:" + itoa(port)
	if tool == 1 {
		return "brew install ngrok", "ngrok http " + target
	}
	return "brew install cloudflared", "cloudflared tunnel --url " + target
}

// loadTwilioNumbers lists the account's numbers.
func (a *app) loadTwilioNumbers() {
	w := &a.tw
	sid := a.prefs.Calls.AccountSID
	if sid == "" || !a.callHasTok || w.listing {
		return
	}
	w.listing, w.numbersErr = true, ""
	tok, proxy := a.callToken.get(), a.prefs.Models.Proxy
	a.async(func() {
		nums, err := a.twilioClient(sid, tok, proxy).Numbers(context.Background())
		a.post(func() {
			w.listing = false
			if err != nil {
				w.numbersErr = twilioError(err)
				return
			}
			w.numbers = nums
			if len(nums) == 0 {
				w.numbersErr = i18n.T("This account has no phone numbers yet: buy one in the Twilio Console.")
			}
		})
	})
}

// numberLabel shows a number in the picker.
func numberLabel(n telephony.Number) string {
	l := n.Number
	if n.Name != "" && n.Name != n.Number {
		l += " · " + n.Name
	}
	if !n.Capabilities.Voice {
		l += " · " + i18n.T("no voice")
	}
	return l
}

// useTwilioNumber saves the chosen number and points its incoming calls
// at the gateway.
func (a *app) useTwilioNumber() {
	w := &a.tw
	var n *telephony.Number
	for i := range w.numbers {
		if numberLabel(w.numbers[i]) == w.numberSel {
			n = &w.numbers[i]
		}
	}
	if n == nil {
		return
	}
	if !n.Capabilities.Voice {
		w.hookMsg, w.hookErr = i18n.T("This number cannot take voice calls."), true
		return
	}
	pub := a.prefs.Calls.Public()
	num := *n
	a.updateSettings(func(s *settings.Settings) { s.Calls.Number, s.Calls.NumberSID = num.Number, num.SID })
	if pub == "" {
		w.hookMsg, w.hookErr = i18n.T("Saved. Set the tunnel's address to connect incoming calls."), false
		return
	}
	w.hooking, w.hookMsg = true, ""
	sid, tok, proxy := a.prefs.Calls.AccountSID, a.callToken.get(), a.prefs.Models.Proxy
	a.async(func() {
		_, err := a.twilioClient(sid, tok, proxy).SetVoiceWebhook(context.Background(), num.SID, telephony.VoiceURL(pub), telephony.StatusURL(pub))
		a.post(func() {
			w.hooking = false
			if err != nil {
				w.hookMsg, w.hookErr = twilioError(err), true
				return
			}
			w.hookMsg, w.hookErr = i18n.T("Incoming calls to %s now reach AudioSmith.", num.Number), false
		})
	})
}

// saveTwilioBehaviour saves step 4.
func (a *app) saveTwilioBehaviour() {
	w := &a.tw
	voice := ""
	labels, ids := a.callVoices()
	for i, l := range labels {
		if l == w.voice {
			voice = ids[i]
		}
	}
	model := ""
	ml, mids := callModelOptions()
	for i, l := range ml {
		if l == w.model {
			model = mids[i]
		}
	}
	greeting := strings.TrimSpace(w.greeting)
	if r := []rune(greeting); len(r) > settings.MaxGreeting {
		greeting = string(r[:settings.MaxGreeting])
	}
	inbound := settings.InboundGreeting
	if w.inbound == 1 {
		inbound = settings.InboundAgent
	}
	name, disc, brief, rec := strings.TrimSpace(w.userName), strings.TrimSpace(w.disclosure), strings.TrimSpace(w.brief), w.record
	a.updateSettings(func(s *settings.Settings) {
		c := &s.Calls
		c.Voice, c.Model, c.Greeting, c.Inbound, c.UserName, c.Disclosure, c.InboundBrief, c.Record = voice, model, greeting, inbound, name, disc, brief, rec
	})
	a.toast(i18n.T("Saved"))
}

// previewPhone renders the greeting (or a sample line) as a phone carries it.
func (a *app) previewPhone() {
	w := &a.tw
	voice := ""
	labels, ids := a.callVoices()
	for i, l := range labels {
		if l == w.voice {
			voice = ids[i]
		}
	}
	text := strings.TrimSpace(w.greeting)
	if text == "" {
		text = i18n.T("Hi, this is how I sound on the phone.")
	}
	w.previewBusy, w.previewErr = true, ""
	svc := a.callSvc
	a.background(func() {
		path, err := svc.PhonePreview(context.Background(), voice, text)
		a.post(func() {
			w.previewBusy = false
			if err != nil {
				w.previewErr = callErrorText(err)
				return
			}
			w.previewPath = path
			if err := a.pl.Toggle("phone-preview", path); err != nil {
				a.toast(i18n.T("Could not play: %v", err))
			}
		})
	})
}

// gatewayEventText names a gateway log event.
func gatewayEventText(ev string) string {
	switch ev {
	case telephony.RejectSignature:
		return i18n.T("Rejected: invalid signature")
	case telephony.RejectToken:
		return i18n.T("Rejected: invalid stream token")
	case telephony.RejectAccount:
		return i18n.T("Rejected: another account")
	case telephony.RejectLimit:
		return i18n.T("Rejected: too many requests")
	case "no_voice":
		return i18n.T("Rejected: no voice may answer")
	case "incoming":
		return i18n.T("Incoming call")
	case "answered":
		return i18n.T("Answered")
	case "stream":
		return i18n.T("Audio connected")
	case "ringing":
		return i18n.T("Ringing")
	case "in-progress":
		return i18n.T("In progress")
	case "completed":
		return i18n.T("Completed")
	case "busy":
		return i18n.T("Busy")
	case "no-answer":
		return i18n.T("No answer")
	case "canceled":
		return i18n.T("Cancelled")
	case "failed":
		return i18n.T("Failed")
	case "queued", "initiated":
		return i18n.T("Dialing")
	}
	return ev
}
