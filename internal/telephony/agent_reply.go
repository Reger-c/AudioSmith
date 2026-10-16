package telephony

// The agent's turns: when to answer, the conversation for the language
// model, a reply (LLM + speaker) and a fixed line spoken as a turn.

import (
	"context"
	"strings"
	"time"
)

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
