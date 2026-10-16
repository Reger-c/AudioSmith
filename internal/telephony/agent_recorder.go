package telephony

// The call recording: both sides at 8 kHz.

import (
	"sync"
	"time"

	"github.com/hehehai/AudioSmith/internal/dsp"
)

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
