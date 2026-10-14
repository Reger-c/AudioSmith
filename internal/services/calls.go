package services

// AI phone calls (M6b): the call library (store.Calls), placing calls
// through Twilio, and the gateway's side (telephony.Hooks): answering
// voice webhooks (calls AudioSmith placed, and incoming calls), following
// call status, and running each call's agent over its Media Stream with
// speech from the job queue (calls_media.go) and the app's language
// model.
//
// A call's live state lives in memory while it runs; every change is
// published as CallChanged (with a copy of the call), and saved by one
// writer goroutine per call, so saves never block the agent and never
// reorder.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/store"
	"github.com/hehehai/AudioSmith/internal/telephony"
)

// CallOwner prefixes the owner of a call's jobs ("call:<id>/<n>").
const CallOwner = "call:"

// Call directions.
const (
	CallOutbound = "outbound"
	CallInbound  = "inbound"
)

// Call phases.
const (
	CallDialing = "dialing"
	CallRinging = "ringing"
	CallLive    = "live"
	CallEnded   = "ended"
	CallFailed  = "failed" // never connected: Twilio refused, busy, no answer…
)

// CallEvent is a step of a call's timeline.
type CallEvent struct {
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"` // CallEvent* or a Twilio status
	Detail string    `json:"detail,omitempty"`
}

// Timeline kinds besides Twilio's statuses.
const (
	CallEventPlaced    = "placed"
	CallEventAnswered  = "answered"
	CallEventStream    = "stream"     // the media stream connected
	CallEventTakeover  = "takeover"   // Detail "on" / "off"
	CallEventWithheld  = "withheld"   // the guard replaced a sentence
	CallEventWrapUp    = "wrap_up"    // 30 s left
	CallEventError     = "error"      // Detail: what failed
	CallEventEnded     = "ended"      // Detail: telephony.End*
	CallEventRejected  = "rejected"   // the gateway refused a request (Detail: the reason)
	CallEventKey       = "key"        // the caller pressed a key
	CallEventNoStream  = "no_stream"  // answered, but the media stream never connected
	CallEventSummarize = "summarized" // the summary is ready
)

// Call is a phone call: its setup, its live state and how it went.
type Call struct {
	ID        string    `json:"id"`
	Direction string    `json:"direction"`
	Number    string    `json:"number"`         // the other party (E.164)
	From      string    `json:"from,omitempty"` // the Twilio number
	SID       string    `json:"sid,omitempty"`  // Twilio's Call SID
	Phase     string    `json:"phase"`
	Status    string    `json:"status,omitempty"` // Twilio's last status
	Created   time.Time `json:"created"`
	Answered  time.Time `json:"answered,omitzero"`
	Ended     time.Time `json:"ended,omitzero"`
	Duration  float64   `json:"duration,omitempty"` // seconds since answered

	Mode       string `json:"mode"` // telephony.ModeAgent / ModeGreeting
	Brief      string `json:"brief,omitempty"`
	VoiceID    string `json:"voiceId,omitempty"`
	VoiceName  string `json:"voiceName,omitempty"`
	Model      string `json:"model,omitempty"` // the TTS model's repo
	Disclosure string `json:"disclosure,omitempty"`
	Greeting   string `json:"greeting,omitempty"`
	MaxMinutes int    `json:"maxMinutes"`
	Record     bool   `json:"record,omitempty"` // recording was on and disclosed

	State    string `json:"state,omitempty"` // the agent's state while live
	Takeover bool   `json:"takeover,omitempty"`

	Outcome string `json:"outcome,omitempty"`
	Note    string `json:"note,omitempty"`    // the agent's note with its outcome
	Summary string `json:"summary,omitempty"` // written after the call
	End     string `json:"end,omitempty"`     // telephony.End*
	Error   string `json:"error,omitempty"`

	Transcript []telephony.Line        `json:"transcript,omitempty"`
	Timeline   []CallEvent             `json:"timeline,omitempty"`
	Turns      []telephony.TurnLatency `json:"turns,omitempty"`
	Recording  string                  `json:"recording,omitempty"` // file name in the calls folder
}

// Live reports whether the call is still going (or about to).
func (c Call) Live() bool {
	return c.Phase == CallDialing || c.Phase == CallRinging || c.Phase == CallLive
}

// Title is the call's line in lists: its brief's first line.
func (c Call) Title() string {
	b := strings.TrimSpace(c.Brief)
	if i := strings.IndexByte(b, '\n'); i >= 0 {
		b = b[:i]
	}
	if r := []rune(b); len(r) > 120 {
		b = string(r[:119]) + "…"
	}
	return b
}

func (c *Call) clone() *Call {
	d := *c
	d.Transcript = append([]telephony.Line(nil), c.Transcript...)
	d.Timeline = append([]CallEvent(nil), c.Timeline...)
	d.Turns = append([]telephony.TurnLatency(nil), c.Turns...)
	return &d
}

// CallChanged is published after a call changes (Call is a copy) or is
// deleted. Topic "call.changed".
type CallChanged struct {
	ID      string
	Call    *Call
	Deleted bool
}

func (CallChanged) Topic() string { return "call.changed" }

// CallRequest places a call.
type CallRequest struct {
	To      string // E.164 (normalised by the caller or here)
	Brief   string
	VoiceID string
	// Disclosure is said first; nil = the default template, "" = none.
	Disclosure *string
	MaxMinutes int // 2–30
}

// CallOptions are the settings calls follow (settings.Calls, mapped by
// the app), read when needed.
type CallOptions struct {
	AccountSID string
	PublicURL  string
	Number     string // the Twilio number calls come from
	UserName   string
	// Disclosure is the outbound disclosure's template ("{name}" is the
	// user's or the voice's name); InboundDisclosure the incoming one's.
	Disclosure        string
	InboundDisclosure string
	// Voice speaks incoming calls.
	Voice          string
	Greeting       string
	Inbound        string // telephony.ModeGreeting / ModeAgent
	InboundBrief   string
	InboundMinutes int
	Concurrent     int
	Record         bool
	STTModel       string
	// GatewayOn: the gateway listens (calls can be placed).
	GatewayOn bool
}

// CallsConfig wires the calls service.
type CallsConfig struct {
	Store     store.Calls
	Bus       *core.Bus
	Scheduler jobs.Scheduler
	// Takes renders speech (svc.Render).
	Takes  *Takes
	Voices core.VoiceService
	// Options are the settings.
	Options func() CallOptions
	// Twilio returns the REST client with the account's credentials (the
	// auth token from the Keychain), or an error.
	Twilio func() (*telephony.Client, error)
	// Recipe is the take recipe that speaks text in a voice (the app
	// chooses the model).
	Recipe func(v core.Voice, text string) (core.Take, error)
	// Chat runs the language model (streaming with onDelta); nil or
	// failing: calls cannot run in agent mode.
	Chat func(ctx context.Context, msgs []telephony.Message, onDelta func(string)) (string, error)
	// Signed is told when a webhook passed the signature check.
	Signed func()
	// CacheDir holds temporary audio ("" = the system's temp dir).
	CacheDir string
	Now      func() time.Time
	Logf     func(string, ...any)
	// NoStreamAfter is how long an answered call may wait for its media
	// stream before it is hung up; 0 = 20 s.
	NoStreamAfter time.Duration
	// Endpoint tunes the agents' endpointer (tests).
	Endpoint telephony.EndpointOptions
}

// GatewayEntry is a line of the setup page's call log.
type GatewayEntry struct {
	At      time.Time
	CallSID string
	Event   string // a Twilio status, "stream", or a telephony.Reject* reason
}

// Calls is the calls service. Goroutine-safe.
type Calls struct {
	cfg   CallsConfig
	watch *callWatcher
	mu    sync.Mutex
	live  map[string]*liveCall
	log   []GatewayEntry
	seq   int
	wg    sync.WaitGroup
}

// liveCall is a call in progress.
type liveCall struct {
	call   *Call
	agent  *telephony.Agent
	recipe func(text string) (core.Take, error)
	dirty  chan struct{}
	done   chan struct{} // the writer stopped
	timer  *time.Timer   // the no-stream timer
}

// ErrCallVoice: the voice may not place calls (only designed voices and
// clones with a verified consent recording may).
var ErrCallVoice = errors.New("only designed voices and voices with a verified consent recording can make calls")

// ErrCallsBusy: as many calls as allowed are running.
var ErrCallsBusy = errors.New("another call is running")

// NewCalls makes the calls service.
func NewCalls(cfg CallsConfig) *Calls {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logf == nil {
		cfg.Logf = log.Printf
	}
	if cfg.CacheDir == "" {
		cfg.CacheDir = filepath.Join(os.TempDir(), "AudioSmith")
	}
	if cfg.NoStreamAfter <= 0 {
		cfg.NoStreamAfter = 20 * time.Second
	}
	if cfg.Options == nil {
		cfg.Options = func() CallOptions { return CallOptions{} }
	}
	s := &Calls{cfg: cfg, watch: &callWatcher{waits: map[string]*callWait{}}, live: map[string]*liveCall{}}
	if cfg.Bus != nil {
		core.On(cfg.Bus, s.watch.publish)
	}
	return s
}

// VoiceAllowed reports whether v may place calls.
func VoiceAllowed(v core.Voice) bool {
	return v.Kind == core.VoiceDesigned || v.Kind == core.VoiceCloned && v.Verified()
}

// CallVoice reads a voice that may place calls.
func (s *Calls) CallVoice(ctx context.Context, id string) (core.Voice, error) {
	if id == "" || s.cfg.Voices == nil {
		return core.Voice{}, fmt.Errorf("no voice chosen: %w", ErrCallVoice)
	}
	v, err := s.cfg.Voices.Get(ctx, id)
	if err != nil {
		return v, err
	}
	if !VoiceAllowed(v) {
		return v, ErrCallVoice
	}
	return v, nil
}

// Disclosure fills a disclosure template: "{name}" is the user's name,
// else the voice's.
func Disclosure(template, user, voice string) string {
	name := strings.TrimSpace(user)
	if name == "" {
		name = strings.TrimSpace(voice)
	}
	if name == "" {
		name = i18n.T("my user")
	}
	return strings.TrimSpace(strings.ReplaceAll(template, "{name}", name))
}

// MentionsRecording reports whether a disclosure tells the other party
// the call is recorded (recording needs that).
func MentionsRecording(disclosure string) bool {
	d := strings.ToLower(disclosure)
	for _, w := range []string{"record", "录音", "錄音", "录制", "錄製", "enregistr", "grabad", "aufgezeichnet", "aufnahme", "録音"} {
		if strings.Contains(d, w) {
			return true
		}
	}
	return false
}

func (s *Calls) publish(c *Call) {
	if s.cfg.Bus != nil {
		s.cfg.Bus.Publish(CallChanged{ID: c.ID, Call: c.clone()})
	}
}

func record(c *Call) (store.CallRecord, error) {
	doc, err := json.Marshal(c)
	if err != nil {
		return store.CallRecord{}, err
	}
	return store.CallRecord{ID: c.ID, CreatedAt: c.Created, UpdatedAt: time.Now(), Direction: c.Direction, Number: c.Number,
		Title: c.Title(), Status: c.Phase, Outcome: c.Outcome, Summary: c.Summary, ProviderSID: c.SID, Duration: c.Duration, Doc: doc}, nil
}

func (s *Calls) save(ctx context.Context, c *Call) error {
	if s.cfg.Store == nil {
		return nil
	}
	r, err := record(c)
	if err != nil {
		return err
	}
	r.UpdatedAt = s.cfg.Now()
	return s.cfg.Store.Put(ctx, r)
}

// startLive registers a call in progress with its writer.
func (s *Calls) startLive(c *Call) *liveCall {
	lc := &liveCall{call: c, dirty: make(chan struct{}, 1), done: make(chan struct{})}
	s.live[c.ID] = lc
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(lc.done)
		for range lc.dirty {
			s.mu.Lock()
			snap := lc.call.clone()
			s.mu.Unlock()
			if err := s.save(context.Background(), snap); err != nil {
				s.cfg.Logf("calls: saving call %s: %v", snap.ID, err)
			}
		}
	}()
	return lc
}

// changed marks a live call for saving (s.mu held or not).
func (lc *liveCall) changed() {
	select {
	case lc.dirty <- struct{}{}:
	default:
	}
}

// update changes a live call under the lock, then saves and publishes it.
func (s *Calls) update(id string, fn func(c *Call)) (*Call, bool) {
	s.mu.Lock()
	lc := s.live[id]
	if lc == nil {
		s.mu.Unlock()
		return nil, false
	}
	fn(lc.call)
	lc.changed()
	snap := lc.call.clone()
	s.mu.Unlock()
	s.publish(snap)
	return snap, true
}

// running counts the calls in progress.
func (s *Calls) running() int {
	n := 0
	for _, lc := range s.live {
		if lc.call.Live() {
			n++
		}
	}
	return n
}

func (s *Calls) event(c *Call, kind, detail string) {
	c.Timeline = append(c.Timeline, CallEvent{At: s.cfg.Now(), Kind: kind, Detail: detail})
}

// Place places an outbound call.
func (s *Calls) Place(ctx context.Context, req CallRequest) (Call, error) {
	o := s.cfg.Options()
	to := telephony.NormalizeNumber(req.To)
	if !telephony.ValidNumber(to) {
		return Call{}, fmt.Errorf("%q is not a phone number in international format: %w", req.To, core.ErrInvalid)
	}
	brief := strings.TrimSpace(req.Brief)
	if brief == "" {
		return Call{}, fmt.Errorf("the call has no task: %w", core.ErrInvalid)
	}
	if o.Number == "" || o.PublicURL == "" || !o.GatewayOn {
		return Call{}, fmt.Errorf("calls are not set up (number, public link, gateway): %w", core.ErrUnsupported)
	}
	if s.cfg.Chat == nil {
		return Call{}, fmt.Errorf("no language model for the conversation: %w", core.ErrUnsupported)
	}
	v, err := s.CallVoice(ctx, req.VoiceID)
	if err != nil {
		return Call{}, err
	}
	recipe, err := s.cfg.Recipe(v, "Hello.")
	if err != nil {
		return Call{}, err
	}
	if _, err := s.cfg.Takes.check(ctx, recipe); err != nil {
		return Call{}, err
	}
	client, err := s.cfg.Twilio()
	if err != nil {
		return Call{}, err
	}
	minutes := min(max(req.MaxMinutes, 2), 30)
	if req.MaxMinutes == 0 {
		minutes = 5
	}
	disclosure := Disclosure(o.Disclosure, o.UserName, v.Name)
	if req.Disclosure != nil {
		disclosure = strings.TrimSpace(*req.Disclosure)
	}
	now := s.cfg.Now()
	c := &Call{ID: core.NewID(now), Direction: CallOutbound, Number: to, From: o.Number, Phase: CallDialing, Created: now,
		Mode: telephony.ModeAgent, Brief: brief, VoiceID: v.ID, VoiceName: v.Name, Model: recipe.Model, Disclosure: disclosure,
		MaxMinutes: minutes, Record: o.Record && MentionsRecording(disclosure)}
	s.event(c, CallEventPlaced, "")
	s.mu.Lock()
	if s.running() >= max(o.Concurrent, 1) {
		s.mu.Unlock()
		return Call{}, ErrCallsBusy
	}
	lc := s.startLive(c)
	lc.recipe = func(text string) (core.Take, error) { return s.cfg.Recipe(v, text) }
	s.mu.Unlock()
	if err := s.save(ctx, c); err != nil {
		s.dropLive(c.ID)
		return Call{}, err
	}
	s.publish(c.clone())
	s.preload(o.STTModel, recipe.Model)
	q := "?call=" + c.ID
	info, err := client.Call(ctx, telephony.CallRequest{To: to, From: o.Number, URL: telephony.VoiceURL(o.PublicURL) + q,
		StatusURL: telephony.StatusURL(o.PublicURL) + q, TimeLimit: time.Duration(minutes)*time.Minute + time.Minute})
	if err != nil {
		snap, _ := s.update(c.ID, func(c *Call) {
			c.Phase, c.Error, c.Outcome, c.Ended = CallFailed, err.Error(), telephony.OutcomeFailed, s.cfg.Now()
			s.event(c, CallEventError, err.Error())
		})
		s.dropLive(c.ID)
		if snap != nil {
			return *snap, err
		}
		return Call{}, err
	}
	snap, _ := s.update(c.ID, func(c *Call) {
		c.SID, c.Status = info.SID, info.Status
	})
	return *snap, nil
}

// dropLive stops a call's writer (after a last save) and forgets it.
func (s *Calls) dropLive(id string) {
	s.mu.Lock()
	lc := s.live[id]
	delete(s.live, id)
	s.mu.Unlock()
	if lc == nil {
		return
	}
	if lc.timer != nil {
		lc.timer.Stop()
	}
	close(lc.dirty)
	<-lc.done
	s.mu.Lock()
	snap := lc.call.clone()
	s.mu.Unlock()
	if err := s.save(context.Background(), snap); err != nil {
		s.cfg.Logf("calls: saving call %s: %v", id, err)
	}
}

// Get returns a call: the live state while it runs.
func (s *Calls) Get(ctx context.Context, id string) (Call, error) {
	s.mu.Lock()
	if lc := s.live[id]; lc != nil {
		c := *lc.call.clone()
		s.mu.Unlock()
		return c, nil
	}
	s.mu.Unlock()
	if s.cfg.Store == nil {
		return Call{}, core.ErrNotFound
	}
	r, err := s.cfg.Store.Get(ctx, id)
	if err != nil {
		return Call{}, err
	}
	var c Call
	if err := json.Unmarshal(r.Doc, &c); err != nil {
		return Call{}, err
	}
	return c, nil
}

// List returns recent calls (records, no transcripts), newest first.
func (s *Calls) List(ctx context.Context, limit int) ([]store.CallRecord, error) {
	if s.cfg.Store == nil {
		return nil, nil
	}
	return s.cfg.Store.List(ctx, store.CallQuery{Limit: limit})
}

// RecordingPath is the absolute path of a call's recording ("" = none).
func (s *Calls) RecordingPath(c Call) string {
	if c.Recording == "" || s.cfg.Store == nil {
		return ""
	}
	return filepath.Join(s.cfg.Store.Dir(), c.Recording)
}

// Delete removes a call that has ended, and its recording.
func (s *Calls) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	_, live := s.live[id]
	s.mu.Unlock()
	if live {
		return fmt.Errorf("call %s is still running: %w", id, core.ErrInvalid)
	}
	c, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.cfg.Store.Delete(ctx, id); err != nil {
		return err
	}
	if p := s.RecordingPath(c); p != "" {
		os.Remove(p)
	}
	if s.cfg.Bus != nil {
		s.cfg.Bus.Publish(CallChanged{ID: id, Deleted: true})
	}
	return nil
}

// Reconcile closes calls a previous run left open (the app quit during
// them).
func (s *Calls) Reconcile(ctx context.Context) error {
	recs, err := s.List(ctx, 200)
	if err != nil {
		return err
	}
	for _, r := range recs {
		if r.Status != CallDialing && r.Status != CallRinging && r.Status != CallLive {
			continue
		}
		s.mu.Lock()
		_, live := s.live[r.ID]
		s.mu.Unlock()
		if live {
			continue
		}
		c, err := s.Get(ctx, r.ID)
		if err != nil {
			continue
		}
		c.Phase, c.State = CallEnded, ""
		if c.Ended.IsZero() {
			c.Ended = s.cfg.Now()
		}
		if c.Outcome == "" {
			c.Outcome = telephony.OutcomeFailed
		}
		c.Error = i18n.T("AudioSmith closed during the call.")
		s.save(ctx, &c)
	}
	return nil
}

// Hangup ends a call.
func (s *Calls) Hangup(ctx context.Context, id string) error {
	s.mu.Lock()
	lc := s.live[id]
	var agent *telephony.Agent
	var sid string
	if lc != nil {
		agent, sid = lc.agent, lc.call.SID
	}
	s.mu.Unlock()
	if lc == nil {
		return fmt.Errorf("call %s is not running: %w", id, core.ErrNotFound)
	}
	if agent != nil {
		agent.Hangup()
	}
	if sid == "" {
		return nil
	}
	client, err := s.cfg.Twilio()
	if err != nil {
		return err
	}
	return client.Hangup(ctx, sid)
}

// Takeover pauses (on) or resumes the agent of a live call.
func (s *Calls) Takeover(id string, on bool) error {
	s.mu.Lock()
	lc := s.live[id]
	s.mu.Unlock()
	if lc == nil || lc.agent == nil {
		return fmt.Errorf("call %s is not connected: %w", id, core.ErrNotFound)
	}
	lc.agent.Takeover(on)
	return nil
}

// Say speaks the user's line in the call now.
func (s *Calls) Say(id, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("nothing to say: %w", core.ErrInvalid)
	}
	s.mu.Lock()
	lc := s.live[id]
	s.mu.Unlock()
	if lc == nil || lc.agent == nil {
		return fmt.Errorf("call %s is not connected: %w", id, core.ErrNotFound)
	}
	lc.agent.Say(text)
	return nil
}

// GatewayLog is the recent gateway events, newest first.
func (s *Calls) GatewayLog() []GatewayEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]GatewayEntry, len(s.log))
	for i, e := range s.log {
		out[len(s.log)-1-i] = e
	}
	return out
}

func (s *Calls) logEvent(sid, ev string) {
	s.mu.Lock()
	s.log = append(s.log, GatewayEntry{At: s.cfg.Now(), CallSID: sid, Event: ev})
	if len(s.log) > 50 {
		s.log = s.log[len(s.log)-50:]
	}
	s.mu.Unlock()
}

// Running reports how many calls are in progress, and the limit.
func (s *Calls) Running() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running(), max(s.cfg.Options().Concurrent, 1)
}

// Close ends the calls in progress and waits for them.
func (s *Calls) Close(ctx context.Context) {
	s.mu.Lock()
	var agents []*telephony.Agent
	for _, lc := range s.live {
		if lc.agent != nil {
			agents = append(agents, lc.agent)
		}
	}
	s.mu.Unlock()
	for _, a := range agents {
		a.Hangup()
	}
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// Hooks is the gateway's side of the service.
func (s *Calls) Hooks() telephony.Hooks { return callHooks{s} }

type callHooks struct{ s *Calls }

func (h callHooks) Signed() {
	if h.s.cfg.Signed != nil {
		h.s.cfg.Signed()
	}
}

func (h callHooks) Rejected(reason, sid string) {
	h.s.logEvent(sid, reason)
	if sid == "" {
		return
	}
	s := h.s
	s.mu.Lock()
	var id string
	for _, lc := range s.live {
		if lc.call.SID == sid {
			id = lc.call.ID
		}
	}
	s.mu.Unlock()
	if id != "" {
		s.update(id, func(c *Call) { s.event(c, CallEventRejected, reason) })
	}
}

// Voice answers a voice webhook.
func (h callHooks) Voice(ctx context.Context, r telephony.VoiceRequest) telephony.VoiceAnswer {
	s := h.s
	if r.CallID != "" {
		ok := false
		s.update(r.CallID, func(c *Call) {
			if c.SID == "" || c.SID == r.CallSID {
				ok = true
				c.SID = r.CallSID
			}
		})
		if !ok {
			s.cfg.Logf("calls: voice webhook for unknown call %s (%s)", r.CallID, r.CallSID)
			return telephony.VoiceAnswer{}
		}
		s.logEvent(r.CallSID, "answered")
		return telephony.VoiceAnswer{CallID: r.CallID}
	}
	return s.incoming(ctx, r)
}

// incoming answers a call to the Twilio number.
func (s *Calls) incoming(ctx context.Context, r telephony.VoiceRequest) telephony.VoiceAnswer {
	o := s.cfg.Options()
	v, err := s.CallVoice(ctx, o.Voice)
	if err != nil {
		s.cfg.Logf("calls: an incoming call was refused: no voice may answer calls (%v)", err)
		s.logEvent(r.CallSID, "no_voice")
		return telephony.VoiceAnswer{Reject: "rejected"}
	}
	mode := telephony.ModeGreeting
	brief := ""
	if o.Inbound == telephony.ModeAgent && s.cfg.Chat != nil {
		mode, brief = telephony.ModeAgent, strings.TrimSpace(o.InboundBrief)
	}
	disclosure := Disclosure(o.InboundDisclosure, o.UserName, v.Name)
	minutes := min(max(o.InboundMinutes, 2), 30)
	if o.InboundMinutes == 0 {
		minutes = 5
	}
	now := s.cfg.Now()
	c := &Call{ID: core.NewID(now), Direction: CallInbound, Number: r.From, From: r.To, SID: r.CallSID, Phase: CallLive, Status: "ringing",
		Created: now, Answered: now, Mode: mode, Brief: brief, VoiceID: v.ID, VoiceName: v.Name, Disclosure: disclosure,
		Greeting: strings.TrimSpace(o.Greeting), MaxMinutes: minutes, Record: o.Record && MentionsRecording(disclosure)}
	if mode == telephony.ModeGreeting {
		c.Brief = i18n.T("Incoming call (greeting)")
	}
	recipe, err := s.cfg.Recipe(v, "Hello.")
	if err == nil {
		c.Model = recipe.Model
		_, err = s.cfg.Takes.check(ctx, recipe)
	}
	if err != nil {
		s.cfg.Logf("calls: an incoming call was refused: the voice cannot speak (%v)", err)
		s.logEvent(r.CallSID, "no_voice")
		return telephony.VoiceAnswer{Reject: "rejected"}
	}
	s.event(c, CallEventAnswered, "")
	s.mu.Lock()
	if s.running() >= max(o.Concurrent, 1) {
		s.mu.Unlock()
		s.logEvent(r.CallSID, telephony.RejectBusy)
		return telephony.VoiceAnswer{Reject: "busy"}
	}
	lc := s.startLive(c)
	lc.recipe = func(text string) (core.Take, error) { return s.cfg.Recipe(v, text) }
	s.mu.Unlock()
	lc.changed()
	s.publish(c.clone())
	s.preload(o.STTModel, c.Model)
	s.logEvent(r.CallSID, "incoming")
	s.armNoStream(c.ID)
	return telephony.VoiceAnswer{CallID: c.ID}
}

// armNoStream hangs up an answered call whose media stream never comes
// (the tunnel passes webhooks but not WebSockets, say).
func (s *Calls) armNoStream(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lc := s.live[id]
	if lc == nil || lc.timer != nil {
		return
	}
	lc.timer = time.AfterFunc(s.cfg.NoStreamAfter, func() {
		s.mu.Lock()
		lc := s.live[id]
		waiting := lc != nil && lc.agent == nil
		sid := ""
		if lc != nil {
			sid = lc.call.SID
		}
		s.mu.Unlock()
		if !waiting {
			return
		}
		s.update(id, func(c *Call) {
			c.Phase, c.Ended, c.Outcome = CallEnded, s.cfg.Now(), telephony.OutcomeFailed
			c.Error = i18n.T("The call was answered, but its audio never reached AudioSmith. Check that the tunnel passes WebSockets.")
			s.event(c, CallEventNoStream, "")
		})
		if client, err := s.cfg.Twilio(); err == nil && sid != "" {
			client.Hangup(context.Background(), sid)
		}
		s.dropLive(id)
	})
}

// Status follows a call's progress.
func (h callHooks) Status(ctx context.Context, ev telephony.StatusEvent) {
	s := h.s
	s.logEvent(ev.CallSID, ev.Status)
	id := ev.CallID
	s.mu.Lock()
	if id == "" {
		for _, lc := range s.live {
			if lc.call.SID == ev.CallSID && ev.CallSID != "" {
				id = lc.call.ID
			}
		}
	}
	lc := s.live[id]
	streaming := lc != nil && lc.agent != nil
	s.mu.Unlock()
	if lc == nil {
		return // a call that already ended (or not ours)
	}
	end := false
	s.update(id, func(c *Call) {
		if ev.CallSID != "" && c.SID == "" {
			c.SID = ev.CallSID
		}
		if c.Status == ev.Status {
			return
		}
		c.Status = ev.Status
		s.event(c, ev.Status, "")
		switch ev.Status {
		case "queued", "initiated":
			c.Phase = CallDialing
		case "ringing":
			if c.Phase == CallDialing {
				c.Phase = CallRinging
			}
		case "in-progress", "answered":
			if c.Answered.IsZero() {
				c.Answered = s.cfg.Now()
				s.event(c, CallEventAnswered, "")
			}
			c.Phase = CallLive
		case "completed":
			if ev.Duration > 0 {
				c.Duration = ev.Duration.Seconds()
			}
			if !streaming {
				c.Phase, c.Ended = CallEnded, s.cfg.Now()
				if c.Answered.IsZero() {
					c.Phase = CallFailed
					if c.Outcome == "" {
						c.Outcome = telephony.OutcomeNotDone
					}
				}
				end = true
			}
		case "busy", "no-answer", "canceled", "failed":
			if !streaming {
				c.Phase, c.Ended = CallFailed, s.cfg.Now()
				if c.Outcome == "" {
					c.Outcome = telephony.OutcomeNotDone
					if ev.Status == "failed" {
						c.Outcome = telephony.OutcomeFailed
					}
				}
				c.Summary = statusSummary(ev.Status)
				end = true
			}
		}
	})
	if ev.Status == "in-progress" || ev.Status == "answered" {
		s.armNoStream(id)
	}
	if end {
		s.dropLive(id)
	}
}

// statusSummary says why a call never connected.
func statusSummary(status string) string {
	switch status {
	case "busy":
		return i18n.T("The line was busy.")
	case "no-answer":
		return i18n.T("Nobody answered.")
	case "canceled":
		return i18n.T("The call was cancelled before it was answered.")
	}
	return i18n.T("Twilio could not connect the call.")
}

// Stream runs a call's agent over its media stream.
func (h callHooks) Stream(ctx context.Context, callID string, ms *telephony.MediaStream) {
	h.s.run(ctx, callID, ms)
}

// run is Stream with any telephony.Stream (tests).
func (s *Calls) run(ctx context.Context, callID string, stream telephony.Stream) {
	o := s.cfg.Options()
	s.mu.Lock()
	lc := s.live[callID]
	if lc == nil || lc.agent != nil {
		s.mu.Unlock()
		stream.Close()
		return
	}
	c := lc.call
	recipe := lc.recipe
	brief := telephony.Brief{Task: c.Brief, Inbound: c.Direction == CallInbound, User: o.UserName, Disclosure: c.Disclosure,
		Number: c.Number, MaxMinutes: c.MaxMinutes}
	a := &telephony.Agent{
		Stream:      stream,
		Mode:        c.Mode,
		Brief:       brief,
		Disclosure:  c.Disclosure,
		Greeting:    c.Greeting,
		MaxDuration: time.Duration(c.MaxMinutes) * time.Minute,
		Record:      c.Record,
		Endpoint:    s.cfg.Endpoint,
		Logf:        s.cfg.Logf,
		STT: func(ctx context.Context, pcm []float32) (string, error) {
			return s.transcribe(ctx, callID, o.STTModel, "", pcm)
		},
		LLM: func(ctx context.Context, msgs []telephony.Message, onDelta func(string)) (string, error) {
			if s.cfg.Chat == nil {
				return "", errors.New("no language model")
			}
			return s.cfg.Chat(ctx, msgs, onDelta)
		},
		TTS: func(ctx context.Context, text string, out func([]float32, int) error) error {
			t, err := recipe(text)
			if err != nil {
				return err
			}
			return s.speak(ctx, callID, t, out)
		},
	}
	a.Events = func(ev telephony.AgentEvent) { s.agentEvent(callID, ev) }
	lc.agent = a
	if lc.timer != nil {
		lc.timer.Stop()
	}
	s.mu.Unlock()
	s.update(callID, func(c *Call) {
		c.Phase, c.State = CallLive, telephony.StateListening
		if c.Answered.IsZero() {
			c.Answered = s.cfg.Now()
		}
		s.event(c, CallEventStream, "")
	})
	s.logEvent(c.SID, "stream")

	res := a.Run(ctx)

	// The call is over: the result, the recording, the summary.
	var recName string
	if res.Recording != nil && s.cfg.Store != nil {
		dir := s.cfg.Store.Dir()
		if err := os.MkdirAll(dir, 0o755); err == nil {
			recName = callID + ".wav"
			if err := wav.WriteFile(filepath.Join(dir, recName), *res.Recording, wav.PCM16, wav.Options{}); err != nil {
				s.cfg.Logf("calls: saving the recording of %s: %v", callID, err)
				recName = ""
			}
		}
	}
	snap, _ := s.update(callID, func(c *Call) {
		c.Phase, c.State, c.End, c.Ended = CallEnded, "", res.End, s.cfg.Now()
		if !c.Answered.IsZero() {
			c.Duration = c.Ended.Sub(c.Answered).Seconds()
		}
		if res.Outcome != "" {
			c.Outcome, c.Note = res.Outcome, res.Note
		}
		c.Transcript, c.Turns, c.Recording = res.Transcript, res.Turns, recName
		s.event(c, CallEventEnded, res.End)
	})
	if res.End != telephony.EndCaller && snap != nil && snap.SID != "" {
		if client, err := s.cfg.Twilio(); err == nil {
			hctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			client.Hangup(hctx, snap.SID)
			cancel()
		}
	}
	if snap != nil {
		s.summarize(callID, *snap, o)
	}
	s.dropLive(callID)
}

// agentEvent folds an agent event into the live call.
func (s *Calls) agentEvent(id string, ev telephony.AgentEvent) {
	s.update(id, func(c *Call) {
		switch ev.Kind {
		case telephony.EvState:
			c.State = ev.State
		case telephony.EvLine:
			for len(c.Transcript) <= ev.Index {
				c.Transcript = append(c.Transcript, telephony.Line{})
			}
			c.Transcript[ev.Index] = ev.Line
		case telephony.EvLatency:
			c.Turns = append(c.Turns, ev.Latency)
		case telephony.EvOutcome:
			c.Outcome, c.Note = ev.Outcome, ev.Note
		case telephony.EvNotice:
			switch ev.Notice {
			case telephony.NoticeTakeover:
				c.Takeover = ev.Detail == "on"
				s.event(c, CallEventTakeover, ev.Detail)
			case telephony.NoticeWithheld:
				s.event(c, CallEventWithheld, "")
			case telephony.NoticeWrapUp:
				s.event(c, CallEventWrapUp, "")
			case telephony.NoticeDigit:
				s.event(c, CallEventKey, ev.Detail)
			default:
				s.event(c, CallEventError, ev.Notice+": "+ev.Detail)
			}
		}
	})
}

// summarize writes the call's summary (and its outcome, when the agent
// gave none) with the language model.
func (s *Calls) summarize(id string, c Call, o CallOptions) {
	var lines []string
	for _, l := range c.Transcript {
		who := "Them"
		switch l.Speaker {
		case telephony.SpeakerAgent:
			who = "Agent"
		case telephony.SpeakerUser:
			who = "User (taking over)"
		}
		lines = append(lines, who+": "+l.Text)
	}
	hasCaller := false
	for _, l := range c.Transcript {
		hasCaller = hasCaller || l.Speaker == telephony.SpeakerCaller
	}
	outcome, summary := "", ""
	if c.Mode == telephony.ModeAgent && hasCaller && s.cfg.Chat != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		text, err := s.cfg.Chat(ctx, telephony.SummaryPrompt(telephony.Brief{Task: c.Brief, Inbound: c.Direction == CallInbound, User: o.UserName},
			strings.Join(lines, "\n")), nil)
		cancel()
		if err != nil {
			s.cfg.Logf("calls: summary of %s: %v", id, err)
		} else {
			outcome, summary = telephony.ParseSummary(text)
		}
	}
	if summary == "" {
		switch {
		case c.Mode == telephony.ModeGreeting:
			summary = i18n.T("The greeting was played.")
		case !hasCaller && c.End == telephony.EndCaller:
			summary = i18n.T("They hung up before saying anything.")
		case !hasCaller:
			summary = i18n.T("Nobody spoke on the call.")
		}
	}
	s.update(id, func(c *Call) {
		if summary != "" {
			c.Summary = summary
		}
		if c.Outcome == "" {
			switch {
			case outcome != "":
				c.Outcome = outcome
			case c.Mode == telephony.ModeGreeting:
				c.Outcome = telephony.OutcomeDone
			case c.End == telephony.EndError:
				c.Outcome = telephony.OutcomeFailed
			default:
				c.Outcome = telephony.OutcomeNotDone
			}
		}
		s.event(c, CallEventSummarize, "")
	})
}
