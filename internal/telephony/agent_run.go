package telephony

// Run and its state (agentRun): the stream reader, the STT worker, the
// messages that drive a call, cancelling turns and ending the call. The
// replies are in agent_reply.go, the audio out in agent_playback.go.

import (
	"context"
	"strings"
	"time"
)

// turn is a reply (or a spoken line) in progress.
type turn struct {
	gen     int
	cancel  context.CancelFunc
	line    int  // its transcript line, -1 until it speaks
	audio   bool // its first audio was sent
	speaker string
	t0      time.Time // the turn started
	heard   time.Time // the caller's speech it answers ended (zero: none)
	fixed   bool      // a fixed text (no model): not a measured turn
	lat     TurnLatency
}

// Run runs the call until it ends. It closes the stream.
func (a *Agent) Run(ctx context.Context) AgentResult {
	a.init()
	defer close(a.done)
	ctx, cancelAll := context.WithCancel(ctx)
	defer cancelAll()
	start := a.now()
	pb := &playback{s: a.Stream, marks: map[string]bool{}, start: start, now: a.now, end: start}
	var rec *recorder
	if a.Record {
		rec = &recorder{}
		pb.rec = rec
	}
	if a.Mode == "" {
		a.Mode = ModeAgent
	}
	st := &agentRun{a: a, ctx: ctx, pb: pb, start: start, res: AgentResult{}, state: StateListening}
	sttQ := make(chan agentMsg, 32)
	go a.read(rec)
	go a.sttWorker(ctx, sttQ)
	defer close(sttQ)

	// The disclosure (and a greeting) first: uninterruptible.
	first := strings.TrimSpace(a.Disclosure)
	if a.Mode == ModeGreeting {
		first = strings.TrimSpace(strings.TrimSpace(first + " " + strings.TrimSpace(a.Greeting)))
		st.ending = EndAgent
	}
	st.pendingOpen = a.Mode == ModeAgent
	if first != "" {
		st.locked = true
		st.speak(first, SpeakerAgent)
	}
	if a.Mode == ModeGreeting && first == "" {
		st.finish(EndAgent)
		return st.close(rec)
	}
	if a.Mode == ModeAgent && first == "" {
		st.maybeReply()
	}

	var limit, wrap <-chan time.Time
	if a.MaxDuration > 0 {
		lt := time.NewTimer(a.MaxDuration)
		defer lt.Stop()
		limit = lt.C
		if a.MaxDuration > time.Minute {
			wt := time.NewTimer(a.MaxDuration - 30*time.Second)
			defer wt.Stop()
			wrap = wt.C
		}
	}
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	var endBy <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			st.finish(EndUser)
		case m := <-a.controls:
			st.onControl(m)
		case m := <-a.msgs:
			st.onMsg(m, sttQ)
		case <-wrap:
			st.wrapUp = true
			st.event(AgentEvent{Kind: EvNotice, Notice: NoticeWrapUp})
		case <-limit:
			st.limitReached()
			t := time.NewTimer(12 * time.Second)
			defer t.Stop()
			endBy = t.C
		case <-endBy:
			st.finish(EndLimit)
		case <-tick.C:
		}
		if st.closed {
			return st.close(rec)
		}
		st.update()
		if st.closed {
			return st.close(rec)
		}
	}
}

// agentRun is Run's state (its goroutine only).
type agentRun struct {
	a     *Agent
	ctx   context.Context
	pb    *playback
	start time.Time
	res   AgentResult
	state string

	cur         *turn
	locked      bool   // the disclosure is playing: no barge-in
	pendingOpen bool   // the opening turn waits for the disclosure
	pending     bool   // caller text not yet answered
	callerTalks bool   // the caller is speaking
	takeover    bool   // the user took over
	wrapUp      bool   // tell the model to wrap up
	ending      string // End* once the call should end after playback
	failures    int    // consecutive speech failures
	closed      bool
	interrupted bool // the last agent line was cut off
	sttSeq      int
	// lastUtter is when the caller's last transcribed utterance ended,
	// lastSTT how long its transcription took (the next turn's latency).
	lastUtter time.Time
	lastSTT   time.Duration
}

func (st *agentRun) event(ev AgentEvent) {
	if st.a.Events != nil {
		st.a.Events(ev)
	}
}

func (st *agentRun) at() time.Duration { return st.a.now().Sub(st.start) }

// addLine appends a transcript line and reports it.
func (st *agentRun) addLine(l Line) int {
	st.res.Transcript = append(st.res.Transcript, l)
	i := len(st.res.Transcript) - 1
	st.event(AgentEvent{Kind: EvLine, Index: i, Line: l})
	return i
}

func (st *agentRun) updateLine(i int, fn func(*Line)) {
	if i < 0 || i >= len(st.res.Transcript) {
		return
	}
	fn(&st.res.Transcript[i])
	st.event(AgentEvent{Kind: EvLine, Index: i, Line: st.res.Transcript[i]})
}

// read turns the stream's events into messages.
func (a *Agent) read(rec *recorder) {
	up := NewUpsampler()
	ep := NewEndpointer(a.Endpoint)
	for {
		ev, err := a.Stream.Next()
		if err != nil {
			a.send(agentMsg{kind: mStreamEnd, err: err})
			return
		}
		switch {
		case ev.Audio != nil:
			if rec != nil {
				rec.caller(ev.At, ev.Audio)
			}
			for _, e := range ep.Push(up.Push(ev.Audio)) {
				switch e.Kind {
				case SpeechStarted:
					a.send(agentMsg{kind: mSpeechStart, at: a.now()})
				case SpeechEnded:
					a.send(agentMsg{kind: mUtterance, audio: e.Audio, at: a.now()})
				}
			}
		case ev.Mark != "":
			a.send(agentMsg{kind: mMark, text: ev.Mark})
		case ev.Digit != "":
			a.send(agentMsg{kind: mDigit, text: ev.Digit})
		}
	}
}

// sttWorker transcribes utterances one at a time, in order.
func (a *Agent) sttWorker(ctx context.Context, q <-chan agentMsg) {
	for m := range q {
		if ctx.Err() != nil {
			continue
		}
		t0 := a.now()
		text, err := a.STT(ctx, m.audio)
		a.send(agentMsg{kind: mTranscript, gen: m.gen, text: strings.TrimSpace(text), err: err, at: m.at, dur: a.now().Sub(t0)})
	}
}

func (st *agentRun) onControl(m agentMsg) {
	switch m.kind {
	case mTakeover:
		if st.takeover == m.on {
			return
		}
		st.takeover = m.on
		detail := "off"
		if m.on {
			detail = "on"
			if st.cur != nil && st.cur.speaker == SpeakerAgent && !st.locked {
				st.cancelTurn(false)
			}
		}
		st.event(AgentEvent{Kind: EvNotice, Notice: NoticeTakeover, Detail: detail})
		if !m.on {
			st.maybeReply()
		}
	case mSay:
		text := strings.TrimSpace(m.text)
		if text == "" || st.ending != "" {
			return
		}
		if st.cur != nil {
			st.cancelTurn(true)
		}
		st.speak(text, SpeakerUser)
	case mHangup:
		st.finish(EndUser)
	}
}

func (st *agentRun) onMsg(m agentMsg, sttQ chan<- agentMsg) {
	switch m.kind {
	case mStreamEnd:
		if st.ending != "" {
			st.finish(st.ending) // it was ending anyway
			return
		}
		st.finish(EndCaller)
	case mSpeechStart:
		st.callerTalks = true
		if st.locked || st.ending != "" {
			return
		}
		if st.cur != nil || st.pb.busy() {
			// Barge-in.
			st.cancelTurn(true)
		}
	case mUtterance:
		st.callerTalks = false
		st.sttSeq++
		select {
		case sttQ <- agentMsg{audio: m.audio, gen: st.sttSeq, at: m.at}:
		default:
			st.a.Logf("calls: dropped an utterance (transcription is behind)")
		}
	case mTranscript:
		if m.err != nil {
			if st.ctx.Err() == nil {
				st.a.Logf("calls: transcription failed: %v", m.err)
				st.event(AgentEvent{Kind: EvNotice, Notice: NoticeSTT, Detail: m.err.Error()})
			}
			return
		}
		if m.text == "" {
			return
		}
		st.addLine(Line{Speaker: SpeakerCaller, Text: m.text, At: m.at.Sub(st.start)})
		st.pending = true
		st.lastUtter, st.lastSTT = m.at, m.dur
		st.maybeReply()
	case mMark:
		st.pb.echoed(m.text)
	case mDigit:
		st.event(AgentEvent{Kind: EvNotice, Notice: NoticeDigit, Detail: m.text})
	case mFirstText:
		if c := st.cur; c != nil && c.gen == m.gen {
			c.lat.LLMFirst = m.at.Sub(c.t0)
		}
	case mSpoken:
		c := st.cur
		if c == nil || c.gen != m.gen {
			return
		}
		if c.line < 0 {
			c.line = st.addLine(Line{Speaker: c.speaker, Text: m.text, At: st.at()})
		} else {
			st.updateLine(c.line, func(l *Line) { l.Text = strings.TrimSpace(l.Text + " " + m.text) })
		}
	case mFirstAudio:
		c := st.cur
		if c == nil || c.gen != m.gen || c.audio {
			return
		}
		c.audio = true
		if !c.fixed && c.lat.Total == 0 {
			c.lat.TTSFirst = m.dur
			c.lat.Sentence = m.at.Add(-m.dur).Sub(c.t0)
			c.lat.Total = m.at.Sub(c.t0)
			if !c.heard.IsZero() {
				c.lat.Total = m.at.Sub(c.heard)
			}
			st.res.Turns = append(st.res.Turns, c.lat)
			st.event(AgentEvent{Kind: EvLatency, Latency: c.lat})
			st.a.Logf("calls: turn latency: stt %v, llm first %v, sentence %v, tts first audio %v, total %v",
				c.lat.STT.Round(time.Millisecond), c.lat.LLMFirst.Round(time.Millisecond), c.lat.Sentence.Round(time.Millisecond),
				c.lat.TTSFirst.Round(time.Millisecond), c.lat.Total.Round(time.Millisecond))
		}
	case mWithheld:
		st.event(AgentEvent{Kind: EvNotice, Notice: NoticeWithheld})
	case mTurnDone:
		st.turnDone(m)
	}
}

func (st *agentRun) turnDone(m agentMsg) {
	c := st.cur
	if c == nil || c.gen != m.gen {
		return
	}
	st.cur = nil
	wasLocked := st.locked
	st.locked = false
	if c.speaker == SpeakerAgent && m.err == nil {
		r := m.reply
		if r.Outcome != "" && ValidOutcome(r.Outcome) {
			st.res.Outcome, st.res.Note = r.Outcome, r.Note
			st.event(AgentEvent{Kind: EvOutcome, Outcome: r.Outcome, Note: r.Note})
		}
		if r.Hangup && st.ending == "" {
			if ok, why := AllowHangup(st.hangupCheck(r)); ok {
				st.ending = EndAgent
			} else {
				st.a.Logf("calls: ignored the model's HANGUP (%s)", why)
			}
		}
		if !wasLocked {
			st.failures = 0
		}
	}
	if m.err != nil && st.ctx.Err() == nil {
		notice := NoticeTTS
		if m.on { // the language model failed
			notice = NoticeLLM
		}
		st.a.Logf("calls: %s: %v", notice, m.err)
		st.event(AgentEvent{Kind: EvNotice, Notice: notice, Detail: m.err.Error()})
		st.failures++
		if st.failures >= 2 {
			st.res.Outcome = OutcomeFailed
			st.finish(EndError)
			return
		}
	}
	if st.pendingOpen {
		st.pendingOpen = false
		st.openTurn()
		return
	}
	st.maybeReply()
}

// hangupCheck is the state a HANGUP request is judged on.
func (st *agentRun) hangupCheck(r Reply) HangupCheck {
	c := HangupCheck{Say: strings.Join(r.Say, " "), Outcome: st.res.Outcome, WrapUp: st.wrapUp}
	lastAgent := ""
	for _, l := range st.res.Transcript {
		switch l.Speaker {
		case SpeakerCaller:
			c.CallerTurns++
			c.LastCaller = l.Text
		case SpeakerAgent:
			lastAgent = l.Text
		}
	}
	if strings.TrimSpace(c.Say) == "" {
		c.Say = lastAgent // a bare HANGUP after an earlier goodbye
	}
	return c
}

// cancelTurn stops the turn in progress; clear also drops its queued
// audio at Twilio (barge-in).
func (st *agentRun) cancelTurn(clear bool) {
	c := st.cur
	st.pb.cancel(clear)
	if c == nil {
		if clear {
			st.markInterrupted()
		}
		return
	}
	c.cancel()
	st.cur = nil
	if c.line >= 0 {
		st.updateLine(c.line, func(l *Line) { l.Interrupted = true })
		st.interrupted = true
	} else if clear {
		st.markInterrupted()
	}
	if c.speaker == SpeakerAgent && c.line < 0 {
		// Nothing was said: the caller's text is still to answer.
		st.pending = st.pending || st.hadCaller()
	}
}

// markInterrupted flags the last agent line when its audio was cut.
func (st *agentRun) markInterrupted() {
	for i := len(st.res.Transcript) - 1; i >= 0; i-- {
		l := st.res.Transcript[i]
		if l.Speaker == SpeakerCaller {
			return
		}
		if l.Speaker == SpeakerAgent && !l.Interrupted {
			st.updateLine(i, func(l *Line) { l.Interrupted = true })
			st.interrupted = true
			return
		}
	}
}

func (st *agentRun) hadCaller() bool {
	for i := len(st.res.Transcript) - 1; i >= 0; i-- {
		switch st.res.Transcript[i].Speaker {
		case SpeakerCaller:
			return true
		case SpeakerAgent:
			return false
		}
	}
	return false
}

// limitReached ends the call politely at its limit.
func (st *agentRun) limitReached() {
	if st.ending != "" && st.cur != nil {
		return
	}
	if st.cur != nil {
		st.cancelTurn(true)
	}
	st.ending = EndLimit
	line := st.a.ClosingLine
	if line == "" {
		line = DefaultClosingLine
	}
	st.locked = true
	st.speak(line, SpeakerAgent)
}

// update recomputes the agent's state and ends the call after its last
// words played.
func (st *agentRun) update() {
	state := StateListening
	switch {
	case st.cur != nil && (st.cur.audio || st.pb.busy()):
		state = StateSpeaking
	case st.cur != nil:
		state = StateThinking
	case st.pb.busy():
		state = StateSpeaking
	}
	if state != st.state {
		st.state = state
		st.event(AgentEvent{Kind: EvState, State: state})
	}
	if st.ending != "" && st.cur == nil && !st.pb.busy() {
		st.finish(st.ending)
	}
}

// finish ends the call.
func (st *agentRun) finish(end string) {
	if st.closed {
		return
	}
	st.closed = true
	st.res.End = end
	if st.cur != nil {
		st.cur.cancel()
		st.cur = nil
	}
}

func (st *agentRun) close(rec *recorder) AgentResult {
	st.a.Stream.Close()
	if rec != nil {
		b := rec.stereo()
		st.res.Recording = &b
	}
	if st.state != StateListening {
		st.event(AgentEvent{Kind: EvState, State: StateListening})
	}
	return st.res
}
