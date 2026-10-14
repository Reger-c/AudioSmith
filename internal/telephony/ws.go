package telephony

// A small RFC 6455 WebSocket implementation (no dependency): the
// gateway's server side for Twilio Media Streams, and a client side for
// tests and the fake Twilio of telephonytest. Text messages only matter
// (Media Streams speaks JSON); pings are answered, fragments joined.

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// WebSocket opcodes.
const (
	opCont   = 0x0
	opText   = 0x1
	opBinary = 0x2
	opClose  = 0x8
	opPing   = 0x9
	opPong   = 0xA
)

// wsMaxMessage bounds one message (a Media Streams frame is ~300 bytes).
const wsMaxMessage = 1 << 20

// ErrWSClosed: the other side closed the connection.
var ErrWSClosed = errors.New("websocket closed")

// WSConn is one WebSocket connection. ReadMessage must be called from one
// goroutine at a time; writes are serialised.
type WSConn struct {
	conn   net.Conn
	br     *bufio.Reader
	client bool // masks what it sends
	wmu    sync.Mutex
	closed bool
}

func acceptKey(key string) string {
	h := sha1.Sum([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(h[:])
}

func headerHas(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

// IsWebSocket reports whether r asks to upgrade to a WebSocket.
func IsWebSocket(r *http.Request) bool {
	return headerHas(r.Header, "Connection", "upgrade") && strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// AcceptWebSocket answers the opening handshake and takes the connection
// over. It writes an HTTP error itself when r is not a valid upgrade.
func AcceptWebSocket(w http.ResponseWriter, r *http.Request) (*WSConn, error) {
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if !IsWebSocket(r) || r.Header.Get("Sec-WebSocket-Version") != "13" || key == "" {
		w.Header().Set("Sec-WebSocket-Version", "13")
		http.Error(w, "websocket upgrade required", http.StatusBadRequest)
		return nil, errors.New("not a websocket upgrade")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "cannot upgrade", http.StatusInternalServerError)
		return nil, errors.New("connection cannot be hijacked")
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}
	resp := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + acceptKey(key) + "\r\n\r\n"
	if _, err := rw.WriteString(resp); err != nil {
		conn.Close()
		return nil, err
	}
	if err := rw.Flush(); err != nil {
		conn.Close()
		return nil, err
	}
	return &WSConn{conn: conn, br: rw.Reader}, nil
}

// DialWebSocket opens a client connection to a ws:// URL (tests; wss is
// not supported), sending header with the handshake.
func DialWebSocket(ctx context.Context, rawURL string, header http.Header) (*WSConn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "ws" {
		return nil, fmt.Errorf("websocket: only ws:// is supported, not %q", u.Scheme)
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return nil, err
	}
	kb := make([]byte, 16)
	rand.Read(kb)
	key := base64.StdEncoding.EncodeToString(kb)
	var b strings.Builder
	fmt.Fprintf(&b, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n", u.RequestURI(), u.Host, key)
	for k, vs := range header {
		for _, v := range vs {
			fmt.Fprintf(&b, "%s: %s\r\n", k, v)
		}
	}
	b.WriteString("\r\n")
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	if _, err := io.WriteString(conn, b.String()); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		conn.Close()
		return nil, err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols || resp.Header.Get("Sec-WebSocket-Accept") != acceptKey(key) {
		conn.Close()
		return nil, fmt.Errorf("websocket: handshake refused: %s", resp.Status)
	}
	conn.SetDeadline(time.Time{})
	return &WSConn{conn: conn, br: br, client: true}, nil
}

// SetReadDeadline bounds the next reads.
func (c *WSConn) SetReadDeadline(t time.Time) error { return c.conn.SetReadDeadline(t) }

// ReadMessage returns the next text or binary message. Pings are
// answered; a close frame is answered and returns ErrWSClosed.
func (c *WSConn) ReadMessage() (int, []byte, error) {
	var msg []byte
	msgOp := -1
	for {
		fin, op, payload, err := c.readFrame()
		if err != nil {
			return 0, nil, err
		}
		switch op {
		case opPing:
			c.write(opPong, payload)
			continue
		case opPong:
			continue
		case opClose:
			c.write(opClose, payload)
			c.Close()
			return 0, nil, ErrWSClosed
		case opText, opBinary:
			if msgOp != -1 {
				return 0, nil, errors.New("websocket: new message inside a fragmented one")
			}
			msgOp = op
			msg = payload
		case opCont:
			if msgOp == -1 {
				return 0, nil, errors.New("websocket: continuation without a message")
			}
			if len(msg)+len(payload) > wsMaxMessage {
				return 0, nil, errors.New("websocket: message too large")
			}
			msg = append(msg, payload...)
		default:
			return 0, nil, fmt.Errorf("websocket: unknown opcode %d", op)
		}
		if fin {
			return msgOp, msg, nil
		}
	}
}

func (c *WSConn) readFrame() (fin bool, op int, payload []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(c.br, h[:]); err != nil {
		return
	}
	fin = h[0]&0x80 != 0
	op = int(h[0] & 0x0F)
	masked := h[1]&0x80 != 0
	n := uint64(h[1] & 0x7F)
	switch n {
	case 126:
		var b [2]byte
		if _, err = io.ReadFull(c.br, b[:]); err != nil {
			return
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err = io.ReadFull(c.br, b[:]); err != nil {
			return
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	if n > wsMaxMessage {
		err = errors.New("websocket: frame too large")
		return
	}
	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(c.br, mask[:]); err != nil {
			return
		}
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(c.br, payload); err != nil {
		return
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return
}

func (c *WSConn) write(op int, data []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed {
		return ErrWSClosed
	}
	hdr := make([]byte, 0, 14)
	hdr = append(hdr, 0x80|byte(op))
	maskBit := byte(0)
	if c.client {
		maskBit = 0x80
	}
	switch n := len(data); {
	case n < 126:
		hdr = append(hdr, maskBit|byte(n))
	case n <= 0xFFFF:
		hdr = append(hdr, maskBit|126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, maskBit|127)
		hdr = binary.BigEndian.AppendUint64(hdr, uint64(n))
	}
	if c.client {
		var mask [4]byte
		rand.Read(mask[:])
		hdr = append(hdr, mask[:]...)
		masked := make([]byte, len(data))
		for i := range data {
			masked[i] = data[i] ^ mask[i%4]
		}
		data = masked
	}
	c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.conn.Write(append(hdr, data...)); err != nil {
		return err
	}
	return nil
}

// WriteText sends a text message.
func (c *WSConn) WriteText(data []byte) error { return c.write(opText, data) }

// CloseWith sends a close frame with code and reason, then closes.
func (c *WSConn) CloseWith(code int, reason string) {
	payload := binary.BigEndian.AppendUint16(nil, uint16(code))
	payload = append(payload, reason...)
	c.write(opClose, payload)
	c.Close()
}

// Close closes the connection without a close frame.
func (c *WSConn) Close() error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.conn.Close()
}
