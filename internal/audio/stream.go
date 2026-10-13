package audio

import (
	"encoding/binary"
	"io"
	"sync"
)

// Stream is audio that grows while it plays: the Studio's preview of a
// take still rendering. The render's chunks are appended as they arrive
// (Append, AppendPCM16) and Finish marks the end. Readers (NewReader) are
// mygo/audio Decoders over it, each with its own position: Read waits for
// audio that has not arrived yet (the player plays silence meanwhile) and
// ends after Finish. Samples are kept as 16-bit, up to a cap; audio past
// the cap is dropped (the take's file has it all).
//
// Every method may be called from any goroutine.
type Stream struct {
	mu       sync.Mutex
	cond     sync.Cond
	rate     int
	channels int
	samples  []int16
	max      int // samples kept at most
	done     bool
	closed   bool
}

// NewStream returns an empty stream of rate/channels audio keeping at
// most maxSeconds of it (0: 30 minutes).
func NewStream(rate, channels int, maxSeconds float64) *Stream {
	if channels < 1 {
		channels = 1
	}
	if maxSeconds <= 0 {
		maxSeconds = 30 * 60
	}
	s := &Stream{rate: rate, channels: channels, max: int(maxSeconds*float64(rate)) * channels}
	s.cond.L = &s.mu
	return s
}

// Append adds interleaved float samples (whole frames).
func (s *Stream) Append(samples []float32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done || s.closed {
		return
	}
	room := s.max - len(s.samples)
	if room <= 0 {
		return
	}
	if len(samples) > room {
		samples = samples[:room-room%s.channels]
	}
	for _, v := range samples {
		v = min(max(v, -1), 1)
		s.samples = append(s.samples, int16(v*32767))
	}
	s.cond.Broadcast()
}

// AppendPCM16 adds little-endian 16-bit PCM (the protocol's "pcm16le").
func (s *Stream) AppendPCM16(data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done || s.closed {
		return
	}
	n := len(data) / 2
	n -= n % s.channels
	n = min(n, max(s.max-len(s.samples), 0))
	for i := range n {
		s.samples = append(s.samples, int16(binary.LittleEndian.Uint16(data[2*i:])))
	}
	s.cond.Broadcast()
}

// Finish marks the end: readers return io.EOF once they read everything.
func (s *Stream) Finish() {
	s.mu.Lock()
	s.done = true
	s.cond.Broadcast()
	s.mu.Unlock()
}

// Done reports whether Finish was called.
func (s *Stream) Done() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done
}

// Seconds is how much audio the stream has.
func (s *Stream) Seconds() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rate == 0 {
		return 0
	}
	return float64(len(s.samples)/s.channels) / float64(s.rate)
}

// Close ends the stream for good (an abandoned render), waking readers.
func (s *Stream) Close() error {
	s.mu.Lock()
	s.closed = true
	s.cond.Broadcast()
	s.mu.Unlock()
	return nil
}

// NewReader returns a decoder of the stream from its start.
func (s *Stream) NewReader() *StreamReader { return &StreamReader{s: s} }

// StreamReader reads a Stream: a mygo/audio Decoder. Closing it (the
// player does when it closes) wakes a Read that waits, and leaves the
// stream to other readers.
type StreamReader struct {
	s      *Stream
	pos    int // next sample (guarded by s.mu)
	closed bool
}

// SampleRate implements mygo/audio.Decoder.
func (r *StreamReader) SampleRate() int { return r.s.rate }

// Channels implements mygo/audio.Decoder.
func (r *StreamReader) Channels() int { return r.s.channels }

// Read implements mygo/audio.Decoder: it waits until there is audio to
// read, the stream is finished, or either is closed.
func (r *StreamReader) Read(buf []float32) (int, error) {
	s := r.s
	s.mu.Lock()
	defer s.mu.Unlock()
	for r.pos >= len(s.samples) && !s.done && !s.closed && !r.closed {
		s.cond.Wait()
	}
	if r.closed || s.closed || r.pos >= len(s.samples) {
		return 0, io.EOF
	}
	n := min(len(buf), len(s.samples)-r.pos)
	n -= n % s.channels
	for i := range n {
		buf[i] = float32(s.samples[r.pos+i]) / 32768
	}
	r.pos += n
	return n, nil
}

// SetPosition implements mygo/audio.Decoder; a frame past the audio that
// arrived so far moves to its end.
func (r *StreamReader) SetPosition(frame int64) error {
	s := r.s
	s.mu.Lock()
	defer s.mu.Unlock()
	r.pos = min(max(int(frame), 0)*s.channels, len(s.samples))
	return nil
}

// Len implements mygo/audio.Decoder: unknown (-1) until the stream is
// finished.
func (r *StreamReader) Len() int64 {
	s := r.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.done {
		return -1
	}
	return int64(len(s.samples) / s.channels)
}

// Close implements io.Closer.
func (r *StreamReader) Close() error {
	s := r.s
	s.mu.Lock()
	r.closed = true
	s.cond.Broadcast()
	s.mu.Unlock()
	return nil
}
