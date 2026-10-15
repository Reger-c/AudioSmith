package telephony

// The agent loop of one call:
//
//	caller μ-law ─▶ 16 kHz ─▶ endpointer ─┬─ speech started ─▶ barge-in: clear playback, cancel the reply
//	                                      └─ utterance ─▶ STT (one at a time, in order) ─▶ transcript
//	transcript ─▶ LLM (streamed, SAY/OUTCOME/HANGUP lines) ─▶ sentences ─▶ guard ─▶ TTS ─▶ 8 kHz μ-law ─▶ Twilio, a mark per sentence
//
// The disclosure is spoken first and cannot be interrupted. One goroutine
// (Run's) owns the state; the stream reader, the STT worker and each
// reply (LLM + speaker) run on their own and report back by message.
// Every reply turn has a generation: cancelling it (barge-in, take-over,
// "Say…", hang-up) bumps the generation, and audio of an older one is
// dropped before it reaches the stream.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hehehai/AudioSmith/internal/dsp"
)

// Stream is the agent's side of a Media Stream (*MediaStream; fakes in
// tests).
type Stream interface {
	Next() (Event, error)
	SendAudio(mu []byte) error
	SendMark(name string) error
	Clear() error
	Close()
}

// Agent modes.
const (
	ModeAgent    = "agent"    // a conversation with the language model
	ModeGreeting = "greeting" // say the disclosure and the greeting, then hang up
)

// Agent states.
const (
	StateListening = "listening"
	StateThinking  = "thinking"
	StateSpeaking  = "speaking"
)

// Speakers of transcript lines.
const (
	SpeakerAgent  = "agent"  // the AI, in the call's voice
	SpeakerCaller = "caller" // the other person
	SpeakerUser   = "user"   // the user's own words (take-over), spoken in the call's voice
)

// Why a call ended.
const (
	EndCaller = "caller" // the other side hung up (the stream stopped)
	EndAgent  = "agent"  // the agent said goodbye and hung up
	EndUser   = "user"   // Hang up in the app (or the app quit)
	EndLimit  = "limit"  // the maximum length was reached
	EndError  = "error"  // speech failed repeatedly
)

// Notice codes of EvNotice.
const (
	NoticeWithheld = "withheld"   // the guard replaced a sentence
	NoticeWrapUp   = "wrap_up"    // 30 s left: the model was told to wrap up
	NoticeSTT      = "stt_failed" // an utterance could not be transcribed
	NoticeLLM      = "llm_failed"
	NoticeTTS      = "tts_failed"
	NoticeTakeover = "takeover" // Detail "on" / "off"
	NoticeDigit    = "digit"    // the caller pressed a key (Detail)
)

// Line is a transcript line.
type Line struct {
	Speaker     string        `json:"speaker"`
	Text        string        `json:"text"`
	At          time.Duration `json:"at"` // since the call was answered
	Interrupted bool          `json:"interrupted,omitempty"`
}

// TurnLatency measures one turn, from the end of the caller's speech (or
// the start of the turn) to the first audio sent.
type TurnLatency struct {
	STT      time.Duration `json:"stt"`      // transcription
	LLMFirst time.Duration `json:"llmFirst"` // the model's first text
	Sentence time.Duration `json:"sentence"` // the first complete sentence
	TTSFirst time.Duration `json:"ttsFirst"` // the first sentence's audio, after the sentence
	Total    time.Duration `json:"total"`    // speech end → first audio
}

// EventKind is the kind of an agent event.
type EventKind int

const (
	EvState   EventKind = iota + 1 // State changed
	EvLine                         // Line (Index) was added or grew
	EvLatency                      // a turn's Latency
	EvOutcome                      // the model reported Outcome and Note
	EvNotice                       // Notice (and Detail)
)

// AgentEvent is something that happened in the call.
type AgentEvent struct {
	Kind    EventKind
	State   string
	Index   int // of Line in the transcript
	Line    Line
	Latency TurnLatency
	Outcome string
	Note    string
	Notice  string
	Detail  string
}

// AgentResult is how the call went.
type AgentResult struct {
	End        string // End*
	Outcome    string // as the model reported it ("" = unknown)
	Note       string
	Transcript []Line
	Turns      []TurnLatency
	// Recording is the call in stereo at 8 kHz (left the caller, right
	// the agent) when Agent.Record was set.
	Recording *dsp.Buffer
}

// Agent is one call's conversation. Fill the fields, then Run.
type Agent struct {
	Stream Stream
	// STT transcribes an utterance (16 kHz mono).
	STT func(ctx context.Context, pcm []float32) (string, error)
	// LLM answers the conversation, calling onDelta with streamed text.
	LLM func(ctx context.Context, msgs []Message, onDelta func(string)) (string, error)
	// TTS speaks text, handing audio (mono, at rate) to out as it is made.
	TTS func(ctx context.Context, text string, out func(pcm []float32, rate int) error) error

	Mode       string // ModeAgent (default) or ModeGreeting
	Brief      Brief
	Disclosure string // spoken first, uninterruptibly ("" = none)
	Greeting   string // ModeGreeting: said after the disclosure
	// ClosingLine is said when the time limit is reached; "" = a default
	// English line.
	ClosingLine string
	MaxDuration time.Duration // 0 = no limit
	Record      bool
	Endpoint    EndpointOptions
	// Events reports what happens, on Run's goroutine; it must not block.
	Events func(AgentEvent)
	Logf   func(string, ...any)

	now      func() time.Time
	msgs     chan agentMsg
	done     chan struct{}
	controls chan agentMsg
	once     sync.Once
}

// DefaultClosingLine ends a call at its limit.
const DefaultClosingLine = "I'm sorry, I have to end the call now. Thank you, goodbye."

type msgKind int

const (
	mSpeechStart msgKind = iota + 1
	mUtterance
	mMark
	mDigit
	mStreamEnd
	mTranscript
	mSpoken
	mFirstAudio
	mFirstText
	mTurnDone
	mWithheld
	mTakeover
	mSay
	mHangup
)

type agentMsg struct {
	kind  msgKind
	gen   int
	text  string
	audio []float32
	on    bool
	at    time.Time
	dur   time.Duration
	err   error
	reply Reply
	lat   TurnLatency
}

func (a *Agent) init() {
	a.once.Do(func() {
		if a.now == nil {
			a.now = time.Now
		}
		a.msgs = make(chan agentMsg, 256)
		a.controls = make(chan agentMsg, 16)
		a.done = make(chan struct{})
		if a.Logf == nil {
			a.Logf = func(string, ...any) {}
		}
	})
}

// send hands a message to Run's goroutine, unless Run has returned.
func (a *Agent) send(m agentMsg) {
	select {
	case a.msgs <- m:
	case <-a.done:
	}
}

func (a *Agent) control(m agentMsg) {
	a.init()
	select {
	case a.controls <- m:
	case <-a.done:
	}
}

// Takeover pauses (on) or resumes the language model: while taken over,
// the caller is still transcribed and Say speaks the user's lines.
func (a *Agent) Takeover(on bool) { a.control(agentMsg{kind: mTakeover, on: on}) }

// Say speaks text in the call's voice now, interrupting the agent.
func (a *Agent) Say(text string) { a.control(agentMsg{kind: mSay, text: text}) }

// Hangup ends the call now.
func (a *Agent) Hangup() { a.control(agentMsg{kind: mHangup}) }

// playback tracks what was sent for playing: the marks not yet echoed,
// when the audio should end, and the generation allowed to send.
type playback struct {
	mu    sync.Mutex
	s     Stream
	gen   int
	marks map[string]bool
	end   time.Time
	start time.Time
	now   func() time.Time
	rec   *recorder
	gain  float32
	seq   int
}

// send plays μ-law audio of generation gen; false when gen is stale.
func (p *playback) send(gen int, mu []byte) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if gen != p.gen {
		return false
	}
	now := p.now()
	at := now
	if p.end.After(at) {
		at = p.end
	}
	p.end = at.Add(time.Duration(len(mu)) * time.Second / PhoneRate)
	if p.rec != nil {
		p.rec.agent(at.Sub(p.start), mu)
	}
	return p.s.SendAudio(mu) == nil
}

// mark asks for the end of what gen sent to be reported.
func (p *playback) mark(gen int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if gen != p.gen {
		return
	}
	p.seq++
	name := fmt.Sprintf("g%d-%d", gen, p.seq)
	p.marks[name] = true
	p.s.SendMark(name)
}

// echoed records a mark Twilio reached.
func (p *playback) echoed(name string) {
	p.mu.Lock()
	delete(p.marks, name)
	p.mu.Unlock()
}

// cancel stops gen's audio from being sent; clear also drops what is
// queued at Twilio (barge-in). It returns the new generation.
func (p *playback) cancel(clear bool) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.gen++
	if clear && (len(p.marks) > 0 || p.end.After(p.now())) {
		p.s.Clear()
		p.marks = map[string]bool{}
		now := p.now()
		if p.rec != nil {
			p.rec.cut(now.Sub(p.start))
		}
		p.end = now
	}
	return p.gen
}

// current is the generation allowed to send.
func (p *playback) current() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.gen
}

// busy reports whether audio is still playing: marks not echoed, until
// shortly after the estimated end (in case an echo is lost).
func (p *playback) busy() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	return len(p.marks) > 0 && now.Before(p.end.Add(1500*time.Millisecond)) || now.Before(p.end)
}

// level fixes the gain of the call's voice from its first audio.
func (p *playback) level(pcm []float32) float32 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gain == 0 && len(pcm) > 0 {
		p.gain = Level(pcm)
	}
	if p.gain == 0 {
		return 1
	}
	return p.gain
}

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

// maybeReply answers the caller when nothing is in the way.
func (st *agentRun) maybeReply() {
	if st.a.Mode != ModeAgent || st.cur != nil || st.ending != "" || st.takeover || st.callerTalks || st.locked {
		return
	}
	if st.pendingOpen {
		st.pendingOpen = false
		st.openTurn()
		return
	}
	if !st.pending {
		return
	}
	st.pending = false
	st.reply(st.lastUtter, st.lastSTT)
}

// openTurn is the agent's first turn: it opens the conversation.
func (st *agentRun) openTurn() {
	if st.a.Mode != ModeAgent || st.takeover || st.ending != "" {
		return
	}
	if st.pending {
		st.pending = false
		st.reply(st.lastUtter, st.lastSTT)
		return
	}
	st.reply(time.Time{}, 0)
}

// history is the conversation for the language model.
func (st *agentRun) history() []Message {
	a := st.a
	msgs := []Message{{Role: RoleSystem, Content: SystemPrompt(a.Brief)}, {Role: RoleUser, Content: OpeningNote(a.Brief)}}
	add := func(r Role, text string) {
		if n := len(msgs); n > 0 && msgs[n-1].Role == r {
			msgs[n-1].Content += "\n" + text
			return
		}
		msgs = append(msgs, Message{Role: r, Content: text})
	}
	cut := false
	for _, l := range st.res.Transcript {
		switch l.Speaker {
		case SpeakerCaller:
			text := CallerLine(l.Text)
			if cut {
				text = "[They interrupted you.] " + text
				cut = false
			}
			add(RoleUser, text)
		case SpeakerAgent:
			if l.Text == strings.TrimSpace(a.Disclosure) && len(msgs) == 2 {
				continue // the disclosure is in the system prompt
			}
			add(RoleAssistant, "SAY: "+l.Text)
			cut = l.Interrupted
		case SpeakerUser:
			add(RoleUser, UserLine(l.Text))
		}
	}
	if st.wrapUp {
		add(RoleUser, WrapUpNote)
	}
	if msgs[len(msgs)-1].Role == RoleAssistant {
		add(RoleUser, "[Continue.]")
	}
	return msgs
}

// reply starts a model turn; t0 is the end of the caller's speech (zero
// for the opening turn), stt its transcription time.
func (st *agentRun) reply(t0 time.Time, stt time.Duration) {
	gen := st.pb.current()
	ctx, cancel := context.WithCancel(st.ctx)
	start := st.a.now()
	c := &turn{gen: gen, cancel: cancel, line: -1, speaker: SpeakerAgent, t0: start, heard: t0}
	c.lat.STT = stt
	st.cur = c
	msgs := st.history()
	brief := st.a.Brief.Task
	a := st.a
	go func() {
		sentences := make(chan string, 32)
		spoke := make(chan error, 1)
		go func() { spoke <- a.speakAll(ctx, st.pb, gen, sentences) }()
		var p ReplyParser
		first := true
		push := func(list []string) {
			for _, s := range list {
				g, changed := Guard(s, brief)
				if changed {
					a.send(agentMsg{kind: mWithheld, gen: gen})
					a.Logf("calls: withheld a long number from the reply")
				}
				select {
				case sentences <- g:
				case <-ctx.Done():
					return
				}
			}
		}
		_, err := a.LLM(ctx, msgs, func(d string) {
			if first && strings.TrimSpace(d) != "" {
				first = false
				a.send(agentMsg{kind: mFirstText, gen: gen, at: a.now()})
			}
			push(p.Feed(d))
		})
		rest, r := p.Finish()
		if err == nil {
			push(rest)
		}
		close(sentences)
		serr := <-spoke
		if err != nil {
			a.send(agentMsg{kind: mTurnDone, gen: gen, err: err, on: true})
			return
		}
		a.send(agentMsg{kind: mTurnDone, gen: gen, err: serr, reply: r})
	}()
}

// speak says a fixed text (the disclosure, a greeting, the user's line,
// the closing line) as a turn of its own.
func (st *agentRun) speak(text, speaker string) {
	gen := st.pb.current()
	ctx, cancel := context.WithCancel(st.ctx)
	st.cur = &turn{gen: gen, cancel: cancel, line: -1, speaker: speaker, t0: st.a.now(), fixed: true}
	a := st.a
	go func() {
		sentences := make(chan string, 1)
		sentences <- text
		close(sentences)
		err := a.speakAll(ctx, st.pb, gen, sentences)
		a.send(agentMsg{kind: mTurnDone, gen: gen, err: err})
	}()
}

// speakAll speaks sentences in order until they end or ctx does.
func (a *Agent) speakAll(ctx context.Context, pb *playback, gen int, sentences <-chan string) error {
	var firstErr error
	for s := range sentences {
		if ctx.Err() != nil || firstErr != nil {
			continue // drain
		}
		t0 := a.now()
		var enc *PhoneEncoder
		sent := false
		err := a.TTS(ctx, s, func(pcm []float32, rate int) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if enc == nil {
				enc = NewPhoneEncoder(rate, pb.level(pcm))
			}
			mu := enc.Push(pcm)
			if len(mu) == 0 {
				return nil
			}
			if !pb.send(gen, mu) {
				return context.Canceled
			}
			if !sent {
				sent = true
				now := a.now()
				a.send(agentMsg{kind: mFirstAudio, gen: gen, at: now, dur: now.Sub(t0)})
				a.send(agentMsg{kind: mSpoken, gen: gen, text: s})
			}
			return nil
		})
		if enc != nil && ctx.Err() == nil {
			if mu := enc.Flush(); len(mu) > 0 {
				pb.send(gen, mu)
			}
			pb.mark(gen)
		}
		if err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
			firstErr = err
		}
		if err == nil && !sent && ctx.Err() == nil {
			a.send(agentMsg{kind: mSpoken, gen: gen, text: s}) // no audio, but it was "said"
		}
	}
	return firstErr
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

// recorder keeps both sides of the call at 8 kHz (Agent.Record).
type recorder struct {
	mu    sync.Mutex
	left  []float32 // the caller
	right []float32 // the agent
}

func (r *recorder) put(track *[]float32, at time.Duration, mu []byte) {
	pos := int(at * PhoneRate / time.Second)
	if pos < 0 {
		pos = 0
	}
	need := pos + len(mu)
	if need > 3*3600*PhoneRate {
		return // over three hours: stop recording
	}
	if len(*track) < need {
		*track = append(*track, make([]float32, need-len(*track))...)
	}
	for i, u := range mu {
		(*track)[pos+i] = float32(mulawDecode[u]) / 32768
	}
}

func (r *recorder) caller(at time.Duration, mu []byte) {
	r.mu.Lock()
	r.put(&r.left, at, mu)
	r.mu.Unlock()
}

func (r *recorder) agent(at time.Duration, mu []byte) {
	r.mu.Lock()
	r.put(&r.right, at, mu)
	r.mu.Unlock()
}

// cut drops the agent's audio after at (it was cleared at Twilio).
func (r *recorder) cut(at time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pos := int(at * PhoneRate / time.Second)
	if pos >= 0 && pos < len(r.right) {
		r.right = r.right[:pos]
	}
}

func (r *recorder) stereo() dsp.Buffer {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := max(len(r.left), len(r.right))
	b := dsp.New(PhoneRate, 2, n)
	for i := range n {
		if i < len(r.left) {
			b.Samples[2*i] = r.left[i]
		}
		if i < len(r.right) {
			b.Samples[2*i+1] = r.right[i]
		}
	}
	return b
}
