package telephony

// Twilio Media Streams (bidirectional, <Connect><Stream>): JSON text
// messages over the WebSocket.
//
// Twilio → us: connected, start (stream and call ids, custom parameters,
// media format), media (base64 μ-law, 20 ms, the caller's audio), mark
// (a mark we sent was reached in playback, or the buffer was cleared),
// dtmf, stop.
// Us → Twilio: media (base64 μ-law to play, queued), mark (tell me when
// playback reaches here), clear (drop what is queued: barge-in).

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// StreamMessage is one message of the stream, either way.
type StreamMessage struct {
	Event     string       `json:"event"`
	StreamSID string       `json:"streamSid,omitempty"`
	Sequence  string       `json:"sequenceNumber,omitempty"`
	Start     *StreamStart `json:"start,omitempty"`
	Media     *StreamMedia `json:"media,omitempty"`
	Mark      *StreamMark  `json:"mark,omitempty"`
	DTMF      *StreamDTMF  `json:"dtmf,omitempty"`
	Stop      *StreamStop  `json:"stop,omitempty"`
	Protocol  string       `json:"protocol,omitempty"` // connected
	Version   string       `json:"version,omitempty"`  // connected
}

// StreamStart describes the stream (start message).
type StreamStart struct {
	AccountSID string            `json:"accountSid"`
	StreamSID  string            `json:"streamSid"`
	CallSID    string            `json:"callSid"`
	Tracks     []string          `json:"tracks"`
	Params     map[string]string `json:"customParameters"`
	Format     StreamFormat      `json:"mediaFormat"`
}

// StreamFormat is the audio's format: audio/x-mulaw, 8000 Hz, 1 channel.
type StreamFormat struct {
	Encoding   string `json:"encoding"`
	SampleRate int    `json:"sampleRate"`
	Channels   int    `json:"channels"`
}

// StreamMedia is a frame of audio. Track, Chunk and Timestamp are set on
// Twilio's frames only.
type StreamMedia struct {
	Track     string `json:"track,omitempty"`
	Chunk     string `json:"chunk,omitempty"`
	Timestamp string `json:"timestamp,omitempty"` // ms since the stream started
	Payload   string `json:"payload"`
}

// StreamMark names a point in the played audio.
type StreamMark struct {
	Name string `json:"name"`
}

// StreamDTMF is a key the caller pressed.
type StreamDTMF struct {
	Track string `json:"track,omitempty"`
	Digit string `json:"digit"`
}

// StreamStop ends the stream.
type StreamStop struct {
	AccountSID string `json:"accountSid"`
	CallSID    string `json:"callSid"`
}

// MediaStream is the server side of one Media Stream, after its start
// message. Send methods are goroutine-safe; Next must be called from one
// goroutine.
type MediaStream struct {
	ws    *WSConn
	Start StreamStart
}

// ErrStreamStopped: Twilio stopped the stream (the call ended).
var ErrStreamStopped = errors.New("media stream stopped")

// readStart reads up to the start message (connected comes first),
// within timeout.
func readStart(ws *WSConn, timeout time.Duration) (StreamStart, error) {
	ws.SetReadDeadline(time.Now().Add(timeout))
	defer ws.SetReadDeadline(time.Time{})
	for range 4 {
		_, data, err := ws.ReadMessage()
		if err != nil {
			return StreamStart{}, err
		}
		var m StreamMessage
		if err := json.Unmarshal(data, &m); err != nil {
			return StreamStart{}, fmt.Errorf("media stream: %w", err)
		}
		switch m.Event {
		case "connected":
			continue
		case "start":
			if m.Start == nil {
				return StreamStart{}, errors.New("media stream: start without details")
			}
			if m.Start.StreamSID == "" {
				m.Start.StreamSID = m.StreamSID
			}
			return *m.Start, nil
		default:
			return StreamStart{}, fmt.Errorf("media stream: %q before start", m.Event)
		}
	}
	return StreamStart{}, errors.New("media stream: no start message")
}

// Event is what Next returns: a frame of the caller's audio (μ-law), a
// mark reached, a key pressed. Stop is reported as ErrStreamStopped.
type Event struct {
	Audio []byte // μ-law, 8 kHz
	At    time.Duration
	Mark  string
	Digit string
}

// Next waits for the next event of the stream.
func (s *MediaStream) Next() (Event, error) {
	for {
		_, data, err := s.ws.ReadMessage()
		if err != nil {
			return Event{}, err
		}
		var m StreamMessage
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.Event {
		case "media":
			if m.Media == nil || (m.Media.Track != "" && m.Media.Track != "inbound") {
				continue
			}
			b, err := base64.StdEncoding.DecodeString(m.Media.Payload)
			if err != nil {
				continue
			}
			ms, _ := strconv.Atoi(m.Media.Timestamp)
			return Event{Audio: b, At: time.Duration(ms) * time.Millisecond}, nil
		case "mark":
			if m.Mark == nil {
				continue
			}
			return Event{Mark: m.Mark.Name}, nil
		case "dtmf":
			if m.DTMF != nil {
				return Event{Digit: m.DTMF.Digit}, nil
			}
		case "stop":
			return Event{}, ErrStreamStopped
		}
	}
}

func (s *MediaStream) send(m StreamMessage) error {
	m.StreamSID = s.Start.StreamSID
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return s.ws.WriteText(data)
}

// SendAudio queues μ-law audio for playback, in 20 ms frames.
func (s *MediaStream) SendAudio(mu []byte) error {
	for len(mu) > 0 {
		n := min(len(mu), FrameBytes*10) // up to 200 ms per message
		if err := s.send(StreamMessage{Event: "media", Media: &StreamMedia{Payload: base64.StdEncoding.EncodeToString(mu[:n])}}); err != nil {
			return err
		}
		mu = mu[n:]
	}
	return nil
}

// SendMark asks to be told when playback reaches this point.
func (s *MediaStream) SendMark(name string) error {
	return s.send(StreamMessage{Event: "mark", Mark: &StreamMark{Name: name}})
}

// Clear drops the audio queued for playback (barge-in). Twilio echoes
// the marks that were queued.
func (s *MediaStream) Clear() error {
	return s.send(StreamMessage{Event: "clear"})
}

// Close ends the stream from our side: with <Connect><Stream> followed by
// <Hangup/>, the call hangs up.
func (s *MediaStream) Close() { s.ws.CloseWith(1000, "done") }
