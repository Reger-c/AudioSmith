package api

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/jobs"
)

// wsClient is a minimal WebSocket client for the tests.
type wsClient struct {
	c  net.Conn
	br *bufio.Reader
}

func dialWS(t *testing.T, addr, path string) *wsClient {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.SetDeadline(time.Now().Add(10 * time.Second))
	req := "GET " + path + " HTTP/1.1\r\nHost: " + addr + "\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n"
	c.Write([]byte(req))
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 101 || resp.Header.Get("Sec-WebSocket-Accept") != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("handshake %d %v", resp.StatusCode, resp.Header)
	}
	return &wsClient{c: c, br: br}
}

func (w *wsClient) send(op byte, data []byte) {
	h := []byte{0x80 | op}
	switch n := len(data); {
	case n < 126:
		h = append(h, 0x80|byte(n))
	default:
		h = append(h, 0x80|126)
		h = binary.BigEndian.AppendUint16(h, uint16(n))
	}
	mask := []byte{1, 2, 3, 4}
	h = append(h, mask...)
	out := make([]byte, len(data))
	for i := range data {
		out[i] = data[i] ^ mask[i%4]
	}
	w.c.Write(append(h, out...))
}

func (w *wsClient) read(t *testing.T) (byte, []byte) {
	t.Helper()
	var h [2]byte
	if _, err := io.ReadFull(w.br, h[:]); err != nil {
		t.Fatal(err)
	}
	n := uint64(h[1] & 0x7F)
	switch n {
	case 126:
		var b [2]byte
		io.ReadFull(w.br, b[:])
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		io.ReadFull(w.br, b[:])
		n = binary.BigEndian.Uint64(b[:])
	}
	data := make([]byte, n)
	io.ReadFull(w.br, data)
	return h[0] & 0x0F, data
}

func TestWebSocketTTS(t *testing.T) {
	f := newFake(t)
	_, ts := f.server()
	addr := strings.TrimPrefix(ts.URL, "http://")
	ws := dialWS(t, addr, "/ws/tts")
	for round := 0; round < 2; round++ { // the socket serves several requests
		ws.send(wsText, []byte(`{"text":"Streaming over a socket.","voice":"af_heart"}`))
		op, data := ws.read(t)
		var start map[string]any
		json.Unmarshal(data, &start)
		if op != wsText || start["type"] != "start" || start["sample_rate"] != float64(24000) || start["encoding"] != "pcm_s16le" {
			t.Fatalf("start %d %s", op, data)
		}
		pcm := 0
		for {
			op, data = ws.read(t)
			if op == wsBinary {
				pcm += len(data)
				continue
			}
			break
		}
		var done map[string]any
		json.Unmarshal(data, &done)
		if done["type"] != "done" || done["take_id"] != start["take_id"] || done["ttfa_ms"] == nil || done["rtf"] == nil {
			t.Fatalf("done %s", data)
		}
		if want := 2 * 24000 * len("Streaming over a socket.") / 20; pcm < want-64 || pcm > want+64 {
			t.Fatalf("pcm %d, want ≈%d", pcm, want)
		}
	}
	// Errors keep the socket.
	ws.send(wsText, []byte(`{"text":""}`))
	_, data := ws.read(t)
	if !strings.Contains(string(data), `"type":"error"`) {
		t.Fatalf("error %s", data)
	}
	ws.send(wsPing, []byte("hi"))
	if op, data := ws.read(t); op != wsPong || string(data) != "hi" {
		t.Fatalf("pong %d %q", op, data)
	}
	ws.send(wsClose, []byte{0x03, 0xE8})
	if op, _ := ws.read(t); op != wsClose {
		t.Fatalf("close %d", op)
	}
	// Plain HTTP gets an error.
	r, _ := http.Get(ts.URL + "/ws/tts")
	r.Body.Close()
	if r.StatusCode != 400 {
		t.Fatal(r.StatusCode)
	}
}

// A request sent as soon as the previous one failed is served, not
// answered busy: the socket stops watching for cancels before it reports
// the end of a request, an error as well as done.
func TestWebSocketTTSAfterError(t *testing.T) {
	f := newFake(t)
	_, ts := f.server()
	ws := dialWS(t, strings.TrimPrefix(ts.URL, "http://"), "/ws/tts")
	ws.c.SetDeadline(time.Now().Add(60 * time.Second))
	for round := range 40 {
		fail := round%2 == 0
		f.mu.Lock()
		f.failRender = nil
		if fail {
			f.failRender = &jobs.Error{Code: "internal", Message: "the engine failed"}
		}
		f.mu.Unlock()
		ws.send(wsText, []byte(`{"text":"Again.","voice":"af_heart"}`))
		op, data := ws.read(t)
		var m map[string]any
		json.Unmarshal(data, &m)
		if fail {
			if op != wsText || m["type"] != "error" || strings.Contains(string(data), "busy") {
				t.Fatalf("round %d: %d %s", round, op, data)
			}
			continue
		}
		if op != wsText || m["type"] != "start" {
			t.Fatalf("round %d: start %d %s", round, op, data)
		}
		for {
			if op, data = ws.read(t); op != wsBinary {
				break
			}
		}
		if json.Unmarshal(data, &m); m["type"] != "done" {
			t.Fatalf("round %d: done %s", round, data)
		}
	}
}
