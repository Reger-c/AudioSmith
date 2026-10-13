package audio

import (
	"encoding/binary"
	"io"
	"sync"
)

// Stream is audio that grows while it plays: the Studio's preview of a
// take still rendering. The render's chunks are appended as they arrive
// (Append, AppendPCM16) and Finish marks the end. It is a mygo/audio
// Decoder: Read waits for audio that has not arrived yet (the player
// plays silence meanwhile) and ends after Finish. Samples are kept as
// 16-bit, up to a cap; audio past the cap is dropped (the take's file has
// it all).
//
// Append, Finish and Close may be called from any goroutine; the player
// reads from its own.
type Stream struct {
	mu       sync.Mutex
	cond     sync.Cond
	rate     int
	channels int
	samples  []int16
	pos      int // next sample to read
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

// Finish marks the end: Read returns io.EOF once it has read everything.
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

// Close ends the stream for good, waking a Read that waits.
func (s *Stream) Close() error {
	s.mu.Lock()
	s.closed = true
	s.cond.Broadcast()
	s.mu.Unlock()
	return nil
}

// SampleRate implements mygo/audio.Decoder.
func (s *Stream) SampleRate() int { return s.rate }

// Channels implements mygo/audio.Decoder.
func (s *Stream) Channels() int { return s.channels }

// Read implements mygo/audio.Decoder: it waits until there is audio to
// read, the stream is finished or it is closed.
func (s *Stream) Read(buf []float32) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.pos >= len(s.samples) && !s.done && !s.closed {
		s.cond.Wait()
	}
	if s.closed || s.pos >= len(s.samples) {
		return 0, io.EOF
	}
	n := min(len(buf), len(s.samples)-s.pos)
	n -= n % s.channels
	for i := range n {
		buf[i] = float32(s.samples[s.pos+i]) / 32768
	}
	s.pos += n
	return n, nil
}

// SetPosition implements mygo/audio.Decoder; a frame past the audio that
// arrived so far moves to its end.
func (s *Stream) SetPosition(frame int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pos = min(max(int(frame), 0)*s.channels, len(s.samples))
	return nil
}

// Len implements mygo/audio.Decoder: unknown (-1) until the stream is
// finished.
func (s *Stream) Len() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.done {
		return -1
	}
	return int64(len(s.samples) / s.channels)
}
