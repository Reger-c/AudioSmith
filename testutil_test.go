package main

// Test helpers shared by the view tests: a fake protocol-1 engine, a test
// app over a temporary history, and a WAV writer. Thread A owns this file;
// B and C add their fakes in files of their own.

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/store/historyjson"
)

// fakeEngine records requests instead of running audiosmith-engine.
type fakeEngine struct {
	sent     []engine.Request
	restarts int
}

func (f *fakeEngine) Send(req engine.Request) (string, error) {
	if req.ID == "" {
		req.ID = string(rune('a' + len(f.sent)))
	}
	f.sent = append(f.sent, req)
	return req.ID, nil
}
func (f *fakeEngine) Restart() error { f.restarts++; return nil }
func (f *fakeEngine) Stop()          {}
func (f *fakeEngine) Path() string   { return "/fake/audiosmith-engine" }

func (f *fakeEngine) last(t *testing.T) engine.Request {
	t.Helper()
	if len(f.sent) == 0 {
		t.Fatal("nothing was sent to the engine")
	}
	return f.sent[len(f.sent)-1]
}

func newTestApp(t *testing.T) (*app, *fakeEngine, *ui.Tester) {
	t.Helper()
	lib, err := historyjson.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	eng := &fakeEngine{}
	a := newApp(eng, lib)
	a.autoplay = false
	a.handleEvent(engine.Event{Event: "ready", Version: "test", PID: 42})
	return a, eng, ui.NewTester(a.view, 1100, 720)
}

// addTake stores a take with a test WAV, as a finished generation would.
func addTake(t *testing.T, a *app, take core.Take, seconds float64) {
	t.Helper()
	take.File = take.ID + ".wav"
	writeTestWAV(t, a.takeStore.AudioPath(take), 24000, seconds)
	if err := a.takeStore.Put(context.Background(), take); err != nil {
		t.Fatal(err)
	}
	a.reloadTakes()
}

// writeTestWAV writes a mono 16-bit sine, as the engine would write a take.
func writeTestWAV(t *testing.T, path string, rate int, seconds float64) {
	t.Helper()
	n := int(float64(rate) * seconds)
	data := make([]byte, 44+2*n)
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(36+2*n))
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], 1)
	binary.LittleEndian.PutUint32(data[24:], uint32(rate))
	binary.LittleEndian.PutUint32(data[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(data[32:], 2)
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], uint32(2*n))
	for i := range n {
		// A tone that swells, so the waveform has a shape.
		v := math.Sin(2*math.Pi*220*float64(i)/float64(rate)) * float64(i) / float64(n)
		binary.LittleEndian.PutUint16(data[44+2*i:], uint16(int16(v*20000)))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
