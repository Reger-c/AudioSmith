package services

// AI calls while they run: live state (startLive, update), placing a call,
// hanging up, takeover and say, the gateway log, closing, and each call's
// agent over its Media Stream with its summary; split from calls.go.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/telephony"
)

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
