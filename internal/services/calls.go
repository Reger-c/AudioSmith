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
//
// Files: calls.go (types, the library: get, list, delete, reconcile),
// calls_run.go (live calls: place, hang up, takeover, the agent),
// calls_hooks.go (the gateway's webhooks), calls_media.go (speech).

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
