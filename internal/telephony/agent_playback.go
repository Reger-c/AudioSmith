package telephony

// Playback: what was sent to the stream for playing (marks, generations,
// the echo level) and speaking sentences into it.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

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
