// Package telephonytest fakes Twilio for tests: its REST API (an
// httptest server), its signed webhooks, and the Media Streams client
// that connects to the gateway. No network beyond loopback.
//
// Imports: telephony.
package telephonytest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hehehai/AudioSmith/internal/telephony"
)

// Test credentials.
const (
	AccountSID = "AC" + "0123456789abcdef0123456789abcdef"
	AuthToken  = "test-auth-token"
	NumberSID  = "PN" + "0123456789abcdef0123456789abcdef"
	Number     = "+15550100100"
)

// API is a fake of Twilio's REST API.
type API struct {
	*httptest.Server
	mu sync.Mutex
	// Calls are the calls placed (their form values).
	Calls []url.Values
	// Hangups are the call SIDs ended.
	Hangups []string
	// Webhooks are the numbers' voice URLs set (by number SID).
	Webhooks map[string]string
	// FailCall, when set, answers placing a call with this Twilio error.
	FailCall *telephony.APIError
	// Trial reports the account as a trial account.
	Trial bool
	seq   int
}

// NewAPI starts a fake REST API; Close it when done.
func NewAPI() *API {
	f := &API{Webhooks: map[string]string{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

// Client is a telephony.Client of the fake with the test credentials.
func (f *API) Client() *telephony.Client {
	return &telephony.Client{AccountSID: AccountSID, AuthToken: AuthToken, Base: f.URL, HTTP: f.Server.Client()}
}

var callPath = regexp.MustCompile(`^/2010-04-01/Accounts/([^/]+)/Calls/([^/]+)\.json$`)
var numberPath = regexp.MustCompile(`^/2010-04-01/Accounts/([^/]+)/IncomingPhoneNumbers/([^/]+)\.json$`)

func (f *API) serve(w http.ResponseWriter, r *http.Request) {
	user, pass, ok := r.BasicAuth()
	if !ok || user != AccountSID || pass != AuthToken {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"code":20003,"message":"Authenticate","more_info":"https://www.twilio.com/docs/errors/20003","status":401}`)
		return
	}
	r.ParseForm()
	prefix := "/2010-04-01/Accounts/" + AccountSID
	w.Header().Set("Content-Type", "application/json")
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == prefix+".json" && r.Method == http.MethodGet:
		typ := "Full"
		if f.Trial {
			typ = "Trial"
		}
		json.NewEncoder(w).Encode(map[string]string{"sid": AccountSID, "friendly_name": "Test account", "status": "active", "type": typ})
	case r.URL.Path == prefix+"/IncomingPhoneNumbers.json" && r.Method == http.MethodGet:
		json.NewEncoder(w).Encode(map[string]any{"incoming_phone_numbers": []map[string]any{
			{"sid": NumberSID, "phone_number": Number, "friendly_name": "(555) 010-0100", "voice_url": f.Webhooks[NumberSID],
				"capabilities": map[string]bool{"voice": true, "sms": true}},
			{"sid": "PN99999999999999999999999999999999", "phone_number": "+15550100199", "friendly_name": "SMS only",
				"capabilities": map[string]bool{"voice": false, "sms": true}},
		}})
	case numberPath.MatchString(r.URL.Path) && r.Method == http.MethodPost:
		m := numberPath.FindStringSubmatch(r.URL.Path)
		f.Webhooks[m[2]] = r.PostForm.Get("VoiceUrl")
		json.NewEncoder(w).Encode(map[string]any{"sid": m[2], "phone_number": Number, "voice_url": r.PostForm.Get("VoiceUrl")})
	case r.URL.Path == prefix+"/Calls.json" && r.Method == http.MethodPost:
		if f.FailCall != nil {
			w.WriteHeader(f.FailCall.Status)
			json.NewEncoder(w).Encode(map[string]any{"code": f.FailCall.Code, "message": f.FailCall.Message, "status": f.FailCall.Status})
			return
		}
		f.Calls = append(f.Calls, r.PostForm)
		f.seq++
		sid := fmt.Sprintf("CA%032d", f.seq)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"sid": sid, "status": "queued", "to": r.PostForm.Get("To"), "from": r.PostForm.Get("From")})
	case callPath.MatchString(r.URL.Path) && r.Method == http.MethodPost:
		m := callPath.FindStringSubmatch(r.URL.Path)
		f.Hangups = append(f.Hangups, m[2])
		json.NewEncoder(w).Encode(map[string]string{"sid": m[2], "status": "completed"})
	default:
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"code":20404,"message":"The requested resource was not found","status":404}`)
	}
}

// PlacedCalls returns a copy of the calls placed.
func (f *API) PlacedCalls() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values(nil), f.Calls...)
}

// HungUp returns a copy of the call SIDs ended.
func (f *API) HungUp() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.Hangups...)
}

// Webhook posts a signed webhook to the gateway as Twilio would: the
// signature is computed for publicURL+path (what Twilio requested),
// the request goes to gatewayURL+path. token "" signs with AuthToken.
func Webhook(gatewayURL, publicURL, pathAndQuery, token string, params url.Values) (*http.Response, string, error) {
	if token == "" {
		token = AuthToken
	}
	if params.Get("AccountSid") == "" {
		params.Set("AccountSid", AccountSID)
	}
	sig := telephony.Signature(token, strings.TrimRight(publicURL, "/")+pathAndQuery, params)
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(gatewayURL, "/")+pathAndQuery, strings.NewReader(params.Encode()))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Twilio-Signature", sig)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(body), nil
}

var tokenRE = regexp.MustCompile(`<Parameter name="token" value="([^"]+)"/>`)

// StreamToken is the stream token in TwiML ("" when there is none).
func StreamToken(twiml string) string {
	m := tokenRE.FindStringSubmatch(twiml)
	if m == nil {
		return ""
	}
	return m[1]
}

// Media is a fake Twilio Media Streams client connected to the gateway.
// It plays the caller: Send audio, and it collects what the gateway plays
// back, echoing each mark at once (as if played instantly).
type Media struct {
	ws        *telephony.WSConn
	mu        sync.Mutex
	played    []byte
	marks     []string
	clears    int
	closed    bool
	done      chan struct{}
	ts        time.Duration
	streamSID string
	// Hold, when set, stops echoing marks (playback "takes forever").
	hold bool
}

// Dial connects to the gateway's stream URL (ws://…/twilio/stream) and
// sends connected and start with token.
func Dial(ctx context.Context, streamURL, token, callSID string) (*Media, error) {
	ws, err := telephony.DialWebSocket(ctx, streamURL, nil)
	if err != nil {
		return nil, err
	}
	m := &Media{ws: ws, done: make(chan struct{}), streamSID: "MZ" + strings.Repeat("0", 31) + "1"}
	send := func(v any) error {
		data, _ := json.Marshal(v)
		return ws.WriteText(data)
	}
	if err := send(map[string]any{"event": "connected", "protocol": "Call", "version": "1.0.0"}); err != nil {
		return nil, err
	}
	err = send(map[string]any{"event": "start", "sequenceNumber": "1", "streamSid": m.streamSID, "start": map[string]any{
		"accountSid": AccountSID, "streamSid": m.streamSID, "callSid": callSID, "tracks": []string{"inbound"},
		"customParameters": map[string]string{"token": token},
		"mediaFormat":      map[string]any{"encoding": "audio/x-mulaw", "sampleRate": 8000, "channels": 1}}})
	if err != nil {
		return nil, err
	}
	go m.read()
	return m, nil
}

func (m *Media) read() {
	defer close(m.done)
	for {
		_, data, err := m.ws.ReadMessage()
		if err != nil {
			m.mu.Lock()
			m.closed = true
			m.mu.Unlock()
			return
		}
		var msg telephony.StreamMessage
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		m.mu.Lock()
		switch msg.Event {
		case "media":
			if msg.Media != nil {
				b, _ := base64.StdEncoding.DecodeString(msg.Media.Payload)
				m.played = append(m.played, b...)
			}
		case "mark":
			if msg.Mark != nil {
				m.marks = append(m.marks, msg.Mark.Name)
				if !m.hold {
					echo, _ := json.Marshal(map[string]any{"event": "mark", "streamSid": m.streamSID, "mark": map[string]string{"name": msg.Mark.Name}})
					go m.ws.WriteText(echo)
				}
			}
		case "clear":
			m.clears++
		}
		m.mu.Unlock()
	}
}

// SetHold stops (true) or resumes echoing marks.
func (m *Media) SetHold(v bool) {
	m.mu.Lock()
	m.hold = v
	m.mu.Unlock()
}

// Send sends caller audio (8 kHz float) as 20 ms μ-law frames, as fast
// as the socket takes them.
func (m *Media) Send(pcm []float32) error {
	mu := telephony.PCMToMulaw(nil, pcm)
	for len(mu) > 0 {
		n := min(len(mu), telephony.FrameBytes)
		frame := mu[:n]
		mu = mu[n:]
		data, _ := json.Marshal(map[string]any{"event": "media", "streamSid": m.streamSID, "media": map[string]string{
			"track": "inbound", "timestamp": fmt.Sprint(m.ts.Milliseconds()), "payload": base64.StdEncoding.EncodeToString(frame)}})
		m.ts += time.Duration(n) * time.Second / telephony.PhoneRate
		if err := m.ws.WriteText(data); err != nil {
			return err
		}
	}
	return nil
}

// Speak sends seconds of speech-like sound, then the silence that ends an
// utterance.
func (m *Media) Speak(seconds float64) error {
	if err := m.Send(Voice(seconds)); err != nil {
		return err
	}
	return m.Send(Silence(0.9))
}

// Digit sends a key press.
func (m *Media) Digit(d string) error {
	data, _ := json.Marshal(map[string]any{"event": "dtmf", "streamSid": m.streamSID, "dtmf": map[string]string{"track": "inbound_track", "digit": d}})
	return m.ws.WriteText(data)
}

// Stop ends the stream as Twilio does when the caller hangs up.
func (m *Media) Stop() {
	data, _ := json.Marshal(map[string]any{"event": "stop", "streamSid": m.streamSID, "stop": map[string]string{"accountSid": AccountSID}})
	m.ws.WriteText(data)
	m.ws.Close()
}

// Played is the μ-law audio played to the caller so far.
func (m *Media) Played() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]byte(nil), m.played...)
}

// Marks are the marks received; Clears how many clears.
func (m *Media) Marks() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.marks...)
}

func (m *Media) Clears() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.clears
}

// Closed reports whether the gateway closed the stream (the call ended).
func (m *Media) Closed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

// Done is closed when the gateway closes the stream.
func (m *Media) Done() <-chan struct{} { return m.done }

// Voice is seconds of a loud harmonic sound at 8 kHz that the endpointer
// takes for speech.
func Voice(seconds float64) []float32 {
	n := int(seconds * telephony.PhoneRate)
	out := make([]float32, n)
	for i := range out {
		t := float64(i) / telephony.PhoneRate
		v := 0.25*math.Sin(2*math.Pi*180*t) + 0.12*math.Sin(2*math.Pi*360*t) + 0.06*math.Sin(2*math.Pi*720*t)
		out[i] = float32(v * (0.6 + 0.4*math.Sin(2*math.Pi*4*t)))
	}
	return out
}

// Silence is seconds of near silence at 8 kHz (a little noise).
func Silence(seconds float64) []float32 {
	n := int(seconds * telephony.PhoneRate)
	out := make([]float32, n)
	x := uint32(1)
	for i := range out {
		x = x*1664525 + 1013904223
		out[i] = (float32(x>>16)/65536 - 0.5) * 0.0005
	}
	return out
}
