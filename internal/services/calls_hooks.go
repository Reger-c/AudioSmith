package services

// AI calls, the gateway's side (telephony.Hooks): signed and rejected
// webhooks, answering voice webhooks (calls AudioSmith placed, and
// incoming calls), following call status, and handing each call's Media
// Stream to run (calls_run.go); split from calls.go.

import (
	"context"
	"strings"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/telephony"
)

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
