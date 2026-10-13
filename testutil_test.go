package main

// Test helpers shared by the view tests: a test app over a temporary
// library (SQLite store + services) and fake engines (enginefake_test.go),
// and a WAV writer. Thread A owns this file; B and C add their fakes in files of
// their own.

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/core"
)

// openTestLibrary opens a library in a temporary directory, closed when
// the test ends. dir may be "" for a new one.
func openTestLibrary(t *testing.T, dir ...string) *library {
	t.Helper()
	d := t.TempDir()
	if len(dir) > 0 {
		d = dir[0]
	}
	lib, err := openLibrary(d, filepath.Join(d, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lib.close)
	return lib
}

func newTestApp(t *testing.T) (*app, *fakeEngine, *ui.Tester) {
	t.Helper()
	return newTestAppWith(t, openTestLibrary(t))
}

func newTestAppWith(t *testing.T, lib *library) (*app, *fakeEngine, *ui.Tester) {
	t.Helper()
	eng := newFakeEngine() // enginefake_test.go (thread B)
	a := newApp(eng.host, lib)
	eng.a = a
	a.connectEngine(eng.host)
	a.autoplay = false
	eng.ready()
	t.Cleanup(a.pl.Stop)
	return a, eng, ui.NewTester(a.view, 1100, 720)
}

// addTake stores a take with a test WAV, as a finished generation would;
// the snapshot follows from the TakeCreated event.
func addTake(t *testing.T, a *app, take core.Take, seconds float64) {
	t.Helper()
	take.File = take.ID + ".wav"
	writeTestWAV(t, a.takeStore.AudioPath(take), 24000, seconds)
	if _, err := a.lib.svc.Takes.Add(context.Background(), take); err != nil {
		t.Fatal(err)
	}
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
