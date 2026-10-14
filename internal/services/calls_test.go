package services

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/store"
	"github.com/hehehai/AudioSmith/internal/telephony"
	tt "github.com/hehehai/AudioSmith/internal/telephony/telephonytest"
)

const callPublic = "https://calls.example.trycloudflare.com"

// callVoices is a voice library with the three kinds that matter.
type callVoices struct {
	core.VoiceService
	m map[string]core.Voice
}

func (v callVoices) Get(_ context.Context, id string) (core.Voice, error) {
	x, ok := v.m[id]
	if !ok {
		return x, core.ErrNotFound
	}
	return x, nil
}

// callEnv is the calls service over the render env, a fake Twilio and a
// running gateway.
type callEnv struct {
	*renderEnv
	calls   *Calls
	api     *tt.API
	gw      *telephony.Gateway
	base    string
	mu      sync.Mutex
	opts    CallOptions
	heard   []string
	replies []string
	chats   [][]telephony.Message
	changed []CallChanged
	signed  int
}

func newCallEnv(t *testing.T) *callEnv {
	t.Helper()
	e := &callEnv{renderEnv: newRenderEnv(t, kokoroInfo), api: tt.NewAPI()}
	t.Cleanup(e.api.Close)
	e.opts = CallOptions{AccountSID: tt.AccountSID, PublicURL: callPublic, Number: tt.Number, UserName: "Sam",
		Disclosure: "AI for {name}. Recorded.", InboundDisclosure: "AI line of {name}.", Voice: "designed",
		Greeting: "Please call back.", Concurrent: 1, STTModel: "m/asr", GatewayOn: true}
	e.sched.Register(proto.KindFor(proto.RoleListen, proto.MethodSTTTranscribe), jobs.ExecutorFunc(e.stt))
	noop := jobs.ExecutorFunc(func(context.Context, jobs.Job, func(jobs.Progress), func(json.RawMessage)) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	e.sched.Register(proto.KindFor(proto.RoleListen, proto.MethodModelsLoad), noop)
	e.sched.Register(proto.MethodModelsLoad, noop)
	voices := callVoices{m: map[string]core.Voice{
		"designed": {ID: "designed", Name: "Nova", Kind: core.VoiceDesigned},
		"mine":     {ID: "mine", Name: "Me", Kind: core.VoiceCloned, Consent: core.VoiceConsent{State: core.ConsentVerified}},
		"theirs":   {ID: "theirs", Name: "Someone", Kind: core.VoiceCloned},
		"builtin":  {ID: "builtin", Name: "Heart", Kind: core.VoiceBuiltin},
	}}
	e.calls = NewCalls(CallsConfig{
		Store: e.st.Calls(), Bus: e.bus, Scheduler: e.sched, Takes: e.svc.Render, Voices: voices,
		Options: func() CallOptions { e.mu.Lock(); defer e.mu.Unlock(); return e.opts },
		Twilio:  func() (*telephony.Client, error) { return e.api.Client(), nil },
		Recipe: func(v core.Voice, text string) (core.Take, error) {
			return core.Take{Text: text, Model: "m/kokoro", Voice: "af_heart", Speed: 4}, nil
		},
		Chat:          e.chat,
		Signed:        func() { e.mu.Lock(); e.signed++; e.mu.Unlock() },
		CacheDir:      t.TempDir(),
		Logf:          t.Logf,
		NoStreamAfter: 300 * time.Millisecond,
	})
	core.On(e.bus, func(ev CallChanged) { e.mu.Lock(); e.changed = append(e.changed, ev); e.mu.Unlock() })
	e.gw = telephony.NewGateway(func() telephony.GatewayConfig {
		return telephony.GatewayConfig{AccountSID: tt.AccountSID, AuthToken: tt.AuthToken, PublicURL: callPublic}
	}, e.calls.Hooks(), t.Logf)
	if err := e.gw.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	e.base = "http://" + e.gw.Addr()
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		e.calls.Close(c)
		e.gw.Close(c)
	})
	return e
}

func (e *callEnv) stt(ctx context.Context, job jobs.Job, _ func(jobs.Progress), _ func(json.RawMessage)) (json.RawMessage, error) {
	var p proto.TranscribeParams
	json.Unmarshal(job.Params, &p)
	if _, err := os.Stat(p.Audio); err != nil || p.Model != "m/asr" {
		return nil, &jobs.Error{Code: proto.CodeBadRequest, Message: "bad params"}
	}
	e.mu.Lock()
	text := ""
	if len(e.heard) > 0 {
		text, e.heard = e.heard[0], e.heard[1:]
	}
	e.mu.Unlock()
	return json.Marshal(proto.TranscribeResult{Text: text})
}

func (e *callEnv) chat(ctx context.Context, msgs []telephony.Message, onDelta func(string)) (string, error) {
	e.mu.Lock()
	e.chats = append(e.chats, msgs)
	r := "SAY: Okay."
	if len(e.replies) > 0 {
		r, e.replies = e.replies[0], e.replies[1:]
	}
	e.mu.Unlock()
	if onDelta != nil {
		for i := 0; i < len(r); i += 5 {
			onDelta(r[i:min(i+5, len(r))])
		}
	}
	return r, nil
}

func (e *callEnv) webhook(t *testing.T, path string, params url.Values) string {
	t.Helper()
	resp, body, err := tt.Webhook(e.base, callPublic, path, "", params)
	if err != nil || resp.StatusCode >= 300 {
		t.Fatalf("%s: %v %v %s", path, err, resp.StatusCode, body)
	}
	return body
}

func (e *callEnv) dial(t *testing.T, twiml, sid string) *tt.Media {
	t.Helper()
	tok := tt.StreamToken(twiml)
	if tok == "" {
		t.Fatalf("no stream in %s", twiml)
	}
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m, err := tt.Dial(c, "ws://"+e.gw.Addr()+telephony.PathStream, tok, sid)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (e *callEnv) waitCall(t *testing.T, id string, cond func(Call) bool) Call {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		c, err := e.calls.Get(ctx, id)
		if err == nil && cond(c) {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("call %s: timed out (%+v, %v)", id, c, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func timelineKinds(c Call) string {
	var k []string
	for _, ev := range c.Timeline {
		k = append(k, ev.Kind)
	}
	return strings.Join(k, ",")
}

func TestCallsOutbound(t *testing.T) {
	e := newCallEnv(t)
	e.opts.Record = true
	e.heard = []string{"Luigi's, how can I help?"}
	e.replies = []string{
		"SAY: Hi, I'd like a table for two tonight.",
		"SAY: Seven works. Thank you!\nOUTCOME: booked - 7 pm, two people\nHANGUP",
		"OUTCOME: booked\nSUMMARY: Table for two at 7 pm tonight.",
	}
	c, err := e.calls.Place(ctx, CallRequest{To: "+44 20 7946 0958", Brief: "Book a table for two tonight.\nAny time after 6.", VoiceID: "designed", MaxMinutes: 10})
	if err != nil {
		t.Fatal(err)
	}
	if c.Phase != CallDialing || c.Number != "+442079460958" || c.SID == "" || c.Disclosure != "AI for Sam. Recorded." || !c.Record || c.MaxMinutes != 10 {
		t.Fatalf("placed %+v", c)
	}
	placed := e.api.PlacedCalls()[0]
	if placed.Get("Url") != callPublic+"/twilio/voice?call="+c.ID || placed.Get("From") != tt.Number || placed.Get("TimeLimit") != "660" {
		t.Errorf("twilio got %v", placed)
	}
	if r, _ := e.calls.List(ctx, 0); len(r) != 1 || r[0].Title != "Book a table for two tonight." || r[0].Status != CallDialing {
		t.Errorf("list %+v", r)
	}
	e.webhook(t, "/twilio/status?call="+c.ID, url.Values{"CallSid": {c.SID}, "CallStatus": {"ringing"}})
	if got, _ := e.calls.Get(ctx, c.ID); got.Phase != CallRinging {
		t.Errorf("ringing: %s", got.Phase)
	}
	e.webhook(t, "/twilio/status?call="+c.ID, url.Values{"CallSid": {c.SID}, "CallStatus": {"in-progress"}})
	twiml := e.webhook(t, "/twilio/voice?call="+c.ID, url.Values{"CallSid": {c.SID}, "Direction": {"outbound-api"}})
	m := e.dial(t, twiml, c.SID)
	// The disclosure, then the opening line; then the caller answers.
	e.waitCall(t, c.ID, func(c Call) bool { return len(c.Transcript) >= 2 && c.State == telephony.StateListening })
	if err := m.Speak(0.7); err != nil {
		t.Fatal(err)
	}
	<-m.Done()
	got := e.waitCall(t, c.ID, func(c Call) bool { return c.Phase == CallEnded && c.Summary != "" })
	e.waitCall(t, c.ID, func(c Call) bool { return true })
	var lines []string
	for _, l := range got.Transcript {
		lines = append(lines, l.Speaker+": "+l.Text)
	}
	want := "agent: AI for Sam. Recorded.|agent: Hi, I'd like a table for two tonight.|caller: Luigi's, how can I help?|agent: Seven works. Thank you!"
	if strings.Join(lines, "|") != want {
		t.Errorf("transcript %q", lines)
	}
	if got.Outcome != telephony.OutcomeBooked || got.Note != "7 pm, two people" || got.Summary != "Table for two at 7 pm tonight." || got.End != telephony.EndAgent {
		t.Errorf("outcome %q %q %q %q", got.Outcome, got.Note, got.Summary, got.End)
	}
	if k := timelineKinds(got); !strings.HasPrefix(k, "placed,ringing,in-progress,answered,stream,ended") {
		t.Errorf("timeline %s", k)
	}
	if len(got.Turns) != 2 || got.Duration <= 0 {
		t.Errorf("turns %+v duration %v", got.Turns, got.Duration)
	}
	// The recording, disclosed.
	b, _, err := wav.ReadFile(e.calls.RecordingPath(got))
	if err != nil || b.Channels != 2 || b.Rate != 8000 {
		t.Errorf("recording %v %+v", err, b.Rate)
	}
	// Twilio was told to hang up after the goodbye.
	if h := e.api.HungUp(); len(h) != 1 || h[0] != c.SID {
		t.Errorf("hangups %v", h)
	}
	// The model got the brief, and the summary prompt the transcript.
	if len(e.chats) != 3 || !strings.Contains(e.chats[0][0].Content, "Any time after 6.") || !strings.Contains(e.chats[2][1].Content, "Them: Luigi's, how can I help?") {
		t.Errorf("chats %d", len(e.chats))
	}
	// Saved, listed in the library, events published.
	rec, err := e.st.Calls().Get(ctx, c.ID)
	if err != nil || rec.Outcome != telephony.OutcomeBooked || rec.Status != CallEnded {
		t.Errorf("record %+v %v", rec, err)
	}
	items, _ := e.st.Library().Search(ctx, store.LibraryQuery{Category: store.LibCall})
	if len(items) != 1 || items[0].Detail != "booked|ended" {
		t.Errorf("library %+v", items)
	}
	if len(e.changed) < 5 || e.signed < 3 {
		t.Errorf("events %d signed %d", len(e.changed), e.signed)
	}
	// It can be deleted now, with its recording.
	if err := e.calls.Delete(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.calls.RecordingPath(got)); !os.IsNotExist(err) {
		t.Error("recording kept")
	}
}

func TestCallsRefusals(t *testing.T) {
	e := newCallEnv(t)
	ok := CallRequest{To: "+15550100111", Brief: "Ask opening hours", VoiceID: "designed"}
	for name, tc := range map[string]struct {
		req  CallRequest
		want error
	}{
		"number":  {CallRequest{To: "555 0100", Brief: "x", VoiceID: "designed"}, core.ErrInvalid},
		"brief":   {CallRequest{To: ok.To, Brief: "  ", VoiceID: "designed"}, core.ErrInvalid},
		"theirs":  {CallRequest{To: ok.To, Brief: "x", VoiceID: "theirs"}, ErrCallVoice},
		"builtin": {CallRequest{To: ok.To, Brief: "x", VoiceID: "builtin"}, ErrCallVoice},
		"none":    {CallRequest{To: ok.To, Brief: "x"}, ErrCallVoice},
	} {
		if _, err := e.calls.Place(ctx, tc.req); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	e.opts.GatewayOn = false
	if _, err := e.calls.Place(ctx, ok); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("gateway off: %v", err)
	}
	e.opts.GatewayOn = true
	// The verified clone may call; with no disclosure, nothing is recorded.
	none := ""
	e.opts.Record = true
	c, err := e.calls.Place(ctx, CallRequest{To: ok.To, Brief: "x", VoiceID: "mine", Disclosure: &none})
	if err != nil || c.Record || c.Disclosure != "" {
		t.Fatalf("mine: %+v %v", c, err)
	}
	// One call at a time.
	if _, err := e.calls.Place(ctx, ok); !errors.Is(err, ErrCallsBusy) {
		t.Errorf("busy: %v", err)
	}
	// It never connects: the line is busy.
	e.webhook(t, "/twilio/status?call="+c.ID, url.Values{"CallSid": {c.SID}, "CallStatus": {"busy"}})
	got := e.waitCall(t, c.ID, func(c Call) bool { return !c.Live() })
	if got.Phase != CallFailed || got.Outcome != telephony.OutcomeNotDone || got.Summary != "The line was busy." {
		t.Errorf("busy line: %+v", got)
	}
	// Twilio refuses to place it (a trial account calling an unverified number).
	e.api.FailCall = &telephony.APIError{Status: 400, Code: 21219, Message: "unverified number"}
	c, err = e.calls.Place(ctx, ok)
	if err == nil || c.Phase != CallFailed || !strings.Contains(c.Error, "unverified") {
		t.Errorf("twilio error: %+v %v", c, err)
	}
	if n, _ := e.calls.Running(); n != 0 {
		t.Errorf("running %d", n)
	}
}

func TestCallsNoStream(t *testing.T) {
	e := newCallEnv(t)
	c, err := e.calls.Place(ctx, CallRequest{To: "+15550100111", Brief: "x", VoiceID: "designed"})
	if err != nil {
		t.Fatal(err)
	}
	e.webhook(t, "/twilio/status?call="+c.ID, url.Values{"CallSid": {c.SID}, "CallStatus": {"in-progress"}})
	got := e.waitCall(t, c.ID, func(c Call) bool { return !c.Live() })
	if got.Phase != CallEnded || got.Error == "" || !strings.Contains(timelineKinds(got), CallEventNoStream) {
		t.Errorf("%+v", got)
	}
	time.Sleep(50 * time.Millisecond)
	if h := e.api.HungUp(); len(h) != 1 {
		t.Errorf("hangups %v", h)
	}
}

func TestCallsInbound(t *testing.T) {
	e := newCallEnv(t)
	// Greeting mode.
	twiml := e.webhook(t, "/twilio/voice", url.Values{"CallSid": {"CAin1"}, "From": {"+15550100111"}, "To": {tt.Number}, "Direction": {"inbound"}})
	m := e.dial(t, twiml, "CAin1")
	<-m.Done()
	list, _ := e.calls.List(ctx, 0)
	if len(list) != 1 {
		t.Fatalf("list %+v", list)
	}
	got := e.waitCall(t, list[0].ID, func(c Call) bool { return c.Phase == CallEnded && c.Summary != "" })
	if got.Direction != CallInbound || got.Number != "+15550100111" || got.Mode != telephony.ModeGreeting || got.Outcome != telephony.OutcomeDone ||
		len(got.Transcript) != 1 || got.Transcript[0].Text != "AI line of Sam. Please call back." || len(e.chats) != 0 {
		t.Errorf("greeting %+v", got)
	}
	// Agent mode: the agent greets and takes a message.
	e.opts.Inbound, e.opts.InboundBrief = telephony.ModeAgent, "Take a message for Sam."
	e.heard = []string{"Tell Sam the parcel is here."}
	e.replies = []string{"SAY: Hello, Sam's assistant here. How can I help?", "SAY: I'll tell Sam. Goodbye!\nOUTCOME: done - parcel arrived\nHANGUP", "OUTCOME: done\nSUMMARY: The parcel is here."}
	twiml = e.webhook(t, "/twilio/voice", url.Values{"CallSid": {"CAin2"}, "From": {"+15550100122"}, "To": {tt.Number}, "Direction": {"inbound"}})
	m = e.dial(t, twiml, "CAin2")
	list, _ = e.calls.List(ctx, 0)
	id := list[0].ID
	e.waitCall(t, id, func(c Call) bool { return len(c.Transcript) >= 2 && c.State == telephony.StateListening })
	m.Speak(0.7)
	<-m.Done()
	got = e.waitCall(t, id, func(c Call) bool { return c.Phase == CallEnded && c.Summary != "" })
	if got.Mode != telephony.ModeAgent || got.Outcome != telephony.OutcomeDone || got.Summary != "The parcel is here." ||
		!strings.Contains(e.chats[0][0].Content, "answering calls") || !strings.Contains(e.chats[0][0].Content, "Take a message for Sam.") {
		t.Errorf("agent %+v", got)
	}
	// No voice that may answer: refused.
	e.opts.Voice = "theirs"
	body := e.webhook(t, "/twilio/voice", url.Values{"CallSid": {"CAin3"}, "From": {"+15550100133"}, "To": {tt.Number}, "Direction": {"inbound"}})
	if !strings.Contains(body, "<Reject") {
		t.Errorf("no voice: %s", body)
	}
	log := e.calls.GatewayLog()
	if len(log) == 0 || log[0].Event != "no_voice" {
		t.Errorf("log %+v", log)
	}
}

func TestCallsTakeoverAndReconcile(t *testing.T) {
	e := newCallEnv(t)
	e.heard = []string{"Hello?"}
	e.replies = []string{"SAY: Hi, it's about your appointment."}
	c, err := e.calls.Place(ctx, CallRequest{To: "+15550100111", Brief: "Reschedule", VoiceID: "designed"})
	if err != nil {
		t.Fatal(err)
	}
	twiml := e.webhook(t, "/twilio/voice?call="+c.ID, url.Values{"CallSid": {c.SID}, "Direction": {"outbound-api"}})
	m := e.dial(t, twiml, c.SID)
	e.waitCall(t, c.ID, func(c Call) bool { return len(c.Transcript) >= 2 && c.State == telephony.StateListening })
	if err := e.calls.Takeover(c.ID, true); err != nil {
		t.Fatal(err)
	}
	e.waitCall(t, c.ID, func(c Call) bool { return c.Takeover })
	m.Speak(0.7)
	e.waitCall(t, c.ID, func(c Call) bool { return len(c.Transcript) >= 3 })
	if err := e.calls.Say(c.ID, "Sam here, sorry."); err != nil {
		t.Fatal(err)
	}
	e.waitCall(t, c.ID, func(c Call) bool {
		return len(c.Transcript) >= 4 && c.Transcript[3].Speaker == telephony.SpeakerUser
	})
	if err := e.calls.Hangup(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	<-m.Done()
	got := e.waitCall(t, c.ID, func(c Call) bool { return c.Phase == CallEnded && c.Summary != "" })
	if got.End != telephony.EndUser || !strings.Contains(timelineKinds(got), "takeover") {
		t.Errorf("%+v", got)
	}
	if err := e.calls.Say(c.ID, "x"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("say after: %v", err)
	}

	// A call left live by a crash is closed at the next start.
	doc, _ := json.Marshal(Call{ID: "old", Direction: CallOutbound, Phase: CallLive, Created: time.Now()})
	e.st.Calls().Put(ctx, store.CallRecord{ID: "old", CreatedAt: time.Now(), UpdatedAt: time.Now(), Status: CallLive, Doc: doc})
	if err := e.calls.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	old, _ := e.calls.Get(ctx, "old")
	if old.Phase != CallEnded || old.Outcome != telephony.OutcomeFailed || old.Error == "" {
		t.Errorf("reconciled %+v", old)
	}
}

func TestCallsPhonePreview(t *testing.T) {
	e := newCallEnv(t)
	path, err := e.calls.PhonePreview(ctx, "designed", "Hello, this is how I sound on the phone.")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := wav.ReadFile(path)
	if err != nil || b.Rate != 8000 || b.Channels != 1 || b.Seconds() < 0.3 {
		t.Errorf("%v %d %d %.2f", err, b.Rate, b.Channels, b.Seconds())
	}
	if _, err := e.calls.PhonePreview(ctx, "theirs", "x"); !errors.Is(err, ErrCallVoice) {
		t.Errorf("theirs: %v", err)
	}
	if !MentionsRecording("This call may be recorded.") || !MentionsRecording("本次通话将被录音。") || MentionsRecording("Hi, I'm an AI.") {
		t.Error("mentions recording")
	}
	if Disclosure("Calling for {name}.", "", "Nova") != "Calling for Nova." {
		t.Error("disclosure")
	}
}
