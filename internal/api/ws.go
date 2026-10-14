package api

// Streaming speech over a WebSocket (/ws/tts), and the small RFC 6455
// server it needs (no dependency): text frames carry JSON, binary frames
// PCM.
//
// Protocol: the client sends a request as a JSON text message
//
//	{"text":"Hello","model":"kokoro-82m-bf16","voice":"af_heart","speed":1.1}
//
// (the fields of /api/generate, plus "sample_rate": 24000 by default, or
// 0 for the model's own rate). The server answers
//
//	{"type":"start","take_id":…,"job_id":…,"model":…,"sample_rate":24000,"channels":1,"encoding":"pcm_s16le"}
//	binary frames of PCM as the take is made (a sentence or so each)
//	{"type":"done","take_id":…,"audio_seconds":…,"ttfa_ms":…,"rtf":…,"seed":…}
//
// or {"type":"error","error":{…}}. {"type":"cancel"} stops the take being
// made. The socket stays open for the next request.

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hehehai/AudioSmith/internal/engine/proto"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// WebSocket opcodes.
const (
	wsCont   = 0x0
	wsText   = 0x1
	wsBinary = 0x2
	wsClose  = 0x8
	wsPing   = 0x9
	wsPong   = 0xA
)

// wsMaxMessage bounds a client message.
const wsMaxMessage = 1 << 20

type wsConn struct {
	conn net.Conn
	br   *bufio.Reader
	wmu  sync.Mutex
}

// wsUpgrade answers the opening handshake and takes the connection over.
func wsUpgrade(w http.ResponseWriter, r *http.Request) (*wsConn, error) {
	if !headerHas(r.Header, "Connection", "upgrade") || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return nil, errInvalid("", "This is a WebSocket endpoint: connect with ws://.")
	}
	if r.Header.Get("Sec-WebSocket-Version") != "13" {
		w.Header().Set("Sec-WebSocket-Version", "13")
		return nil, errInvalid("", "Only WebSocket version 13 is supported.")
	}
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if key == "" {
		return nil, errInvalid("", "Sec-WebSocket-Key is missing.")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("api: the connection cannot be taken over")
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}
	sum := sha1.Sum([]byte(key + wsGUID))
	resp := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " +
		base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n"
	if _, err := conn.Write([]byte(resp)); err != nil {
		conn.Close()
		return nil, err
	}
	conn.SetDeadline(time.Time{})
	return &wsConn{conn: conn, br: rw.Reader}, nil
}

func headerHas(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, t := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), token) {
				return true
			}
		}
	}
	return false
}

// readMessage reads the next data message, answering pings and closes.
func (c *wsConn) readMessage() (int, []byte, error) {
	var msg []byte
	op := -1
	for {
		var h [2]byte
		if _, err := io.ReadFull(c.br, h[:]); err != nil {
			return 0, nil, err
		}
		fin := h[0]&0x80 != 0
		opcode := int(h[0] & 0x0F)
		masked := h[1]&0x80 != 0
		n := uint64(h[1] & 0x7F)
		switch n {
		case 126:
			var b [2]byte
			if _, err := io.ReadFull(c.br, b[:]); err != nil {
				return 0, nil, err
			}
			n = uint64(binary.BigEndian.Uint16(b[:]))
		case 127:
			var b [8]byte
			if _, err := io.ReadFull(c.br, b[:]); err != nil {
				return 0, nil, err
			}
			n = binary.BigEndian.Uint64(b[:])
		}
		if !masked {
			c.close(1002, "frames from the client must be masked")
			return 0, nil, errors.New("ws: unmasked frame")
		}
		if n > wsMaxMessage || uint64(len(msg))+n > wsMaxMessage {
			c.close(1009, "message too big")
			return 0, nil, errors.New("ws: message too big")
		}
		var mask [4]byte
		if _, err := io.ReadFull(c.br, mask[:]); err != nil {
			return 0, nil, err
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(c.br, payload); err != nil {
			return 0, nil, err
		}
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
		switch opcode {
		case wsPing:
			c.write(wsPong, payload)
			continue
		case wsPong:
			continue
		case wsClose:
			code := 1000
			if len(payload) >= 2 {
				code = int(binary.BigEndian.Uint16(payload))
			}
			c.close(code, "")
			return 0, nil, io.EOF
		case wsText, wsBinary:
			if op != -1 {
				c.close(1002, "expected a continuation")
				return 0, nil, errors.New("ws: protocol error")
			}
			op = opcode
		case wsCont:
			if op == -1 {
				c.close(1002, "unexpected continuation")
				return 0, nil, errors.New("ws: protocol error")
			}
		default:
			c.close(1002, "unknown opcode")
			return 0, nil, errors.New("ws: unknown opcode")
		}
		msg = append(msg, payload...)
		if fin {
			return op, msg, nil
		}
	}
}

// write sends one unmasked frame.
func (c *wsConn) write(op int, data []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	h := make([]byte, 0, 10)
	h = append(h, 0x80|byte(op))
	switch n := len(data); {
	case n < 126:
		h = append(h, byte(n))
	case n <= 0xFFFF:
		h = append(h, 126)
		h = binary.BigEndian.AppendUint16(h, uint16(n))
	default:
		h = append(h, 127)
		h = binary.BigEndian.AppendUint64(h, uint64(n))
	}
	c.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	if _, err := c.conn.Write(h); err != nil {
		return err
	}
	_, err := c.conn.Write(data)
	return err
}

func (c *wsConn) writeJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.write(wsText, data)
}

func (c *wsConn) close(code int, reason string) {
	b := binary.BigEndian.AppendUint16(nil, uint16(code))
	c.write(wsClose, append(b, reason...))
	c.conn.Close()
}

// wsRequest is a /ws/tts request.
type wsRequest struct {
	Type string `json:"type"` // "" or "speak"; "cancel"
	generateBody
	// SampleRate of the PCM sent: 24000 (default), or 0 for the model's.
	SampleRate *int `json:"sample_rate"`
}

// wsTTS is GET /ws/tts.
func (s *Server) wsTTS(w http.ResponseWriter, r *http.Request) {
	c, err := wsUpgrade(w, r)
	if err != nil {
		writeError(w, err)
		return
	}
	defer c.conn.Close()
	client := r.Header.Get(HeaderClient)
	msgs := make(chan []byte, 8)
	readErr := make(chan error, 1)
	go func() {
		for {
			op, data, err := c.readMessage()
			if err != nil {
				readErr <- err
				close(msgs)
				return
			}
			if op == wsText {
				msgs <- data
			}
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for {
		var data []byte
		select {
		case d, ok := <-msgs:
			if !ok {
				return
			}
			data = d
		case <-readErr:
			return
		}
		var req wsRequest
		if err := json.Unmarshal(data, &req); err != nil {
			c.writeJSON(map[string]any{"type": "error", "error": errorBody(errInvalid("", "The message is not JSON: %v", err))["error"]})
			continue
		}
		if req.Type == "cancel" {
			continue // nothing running
		}
		if !s.wsSpeak(ctx, c, req, client, msgs) {
			return
		}
	}
}

// wsSpeak runs one request; false when the socket is gone.
func (s *Server) wsSpeak(ctx context.Context, c *wsConn, req wsRequest, client string, msgs <-chan []byte) bool {
	t0 := time.Now()
	b := req.generateBody
	p := SpeechParams{Text: b.Text, Model: b.Model, Voice: string(b.Voice), Instructions: b.Instructions, Language: b.Language,
		Speed: b.Speed, Seed: b.Seed, Duration: b.Duration, Steps: b.Steps, Temperature: b.Temperature, TopP: b.TopP,
		Guidance: b.Guidance, Exaggeration: b.Exaggeration, Client: client}
	fail := func(err error) bool {
		return c.writeJSON(map[string]any{"type": "error", "error": errorBody(errorOf(err))["error"]}) == nil
	}
	sp, err := s.StartSpeech(ctx, p)
	if err != nil {
		return fail(err)
	}
	rate := PCMRate
	if req.SampleRate != nil {
		rate = *req.SampleRate
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	var gone atomic.Bool
	go func() { // a cancel, or the socket closing, while the take is made
		for {
			select {
			case d, ok := <-msgs:
				if !ok {
					gone.Store(true)
					stop()
					return
				}
				var m struct {
					Type string `json:"type"`
				}
				if json.Unmarshal(d, &m) == nil && m.Type == "cancel" {
					stop()
					return
				}
				c.writeJSON(map[string]any{"type": "error", "error": errorBody(&Error{Status: http.StatusConflict, Code: "busy",
					Message: "One request at a time per socket: wait for done, or send {\"type\":\"cancel\"}."})["error"]})
			case <-runCtx.Done():
				return
			}
		}
	}()
	started := false
	var ttfa time.Duration
	var conv *pcmConverter
	sendStart := func(sr, ch int) error {
		started = true
		outRate, outCh := sr, ch
		if rate == PCMRate {
			conv = newPCMConverter(sr, ch)
			outRate, outCh = PCMRate, 1
		}
		return c.writeJSON(map[string]any{"type": "start", "take_id": sp.Recipe.ID, "job_id": sp.JobID, "model": sp.Model.ID,
			"voice": sp.VoiceName, "ignored": sp.Ignored, "sample_rate": outRate, "channels": outCh, "encoding": "pcm_s16le"})
	}
	res, err := sp.Wait(runCtx, func(ch proto.ChunkParams) error {
		if !started {
			if err := sendStart(ch.SampleRate, ch.Channels); err != nil {
				return err
			}
		}
		data := ch.Data
		if conv != nil {
			data = conv.convert(data)
		}
		if ttfa == 0 && len(data) > 0 {
			ttfa = time.Since(t0)
		}
		return c.write(wsBinary, data)
	})
	if gone.Load() {
		return false
	}
	if err != nil {
		return fail(err)
	}
	if !started { // no chunks came: send the file
		if err := sendStart(res.Take.SampleRate, 1); err != nil {
			return false
		}
		pcm, err := filePCM(res.Output, rate)
		if err != nil {
			return fail(err)
		}
		ttfa = time.Since(t0)
		if c.write(wsBinary, pcm) != nil {
			return false
		}
	} else if conv != nil {
		if tail := conv.flush(); len(tail) > 0 && c.write(wsBinary, tail) != nil {
			return false
		}
	}
	wall := time.Since(t0).Seconds()
	rtf := 0.0
	if wall > 0 {
		rtf = round2(res.Take.AudioSeconds / wall)
	}
	return c.writeJSON(map[string]any{"type": "done", "take_id": res.Take.ID, "job_id": sp.JobID, "audio_seconds": res.Take.AudioSeconds,
		"gen_seconds": round2(wall), "ttfa_ms": ttfa.Milliseconds(), "rtf": rtf, "seed": res.Take.Seed}) == nil
}
