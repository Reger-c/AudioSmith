package main

// Fakes of the engine's voice tools (voiceTools) for the voice pages'
// tests, until thread E's enginetest fakes for media.*, vad.detect,
// stt.transcribe, enhance.run, capture.* and tts.encodeVoice land.

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/hehehai/AudioSmith/internal/core"
)

type fakeTools struct {
	mu            sync.Mutex
	can           map[string]bool
	transcript    string
	transcribeErr error
	transcribed   []string // paths
	enhanced      int
	devices       []captureDevice
	recorded      []captureOptions
	recSeconds    float64
	encoded       []string // models
	decodedOther  int
}

func newFakeTools() *fakeTools {
	return &fakeTools{
		can:        map[string]bool{toolDecodeAny: true, toolTranscribe: true, toolEnhance: true, toolCapture: true, toolEncode: true},
		transcript: "This is what my voice sounds like.",
		devices:    []captureDevice{{ID: "mic1", Name: "Studio Mic"}},
		recSeconds: 6,
	}
}

func (f *fakeTools) Can(tool string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.can[tool]
}

func (f *fakeTools) Decode(ctx context.Context, src, out string) (float64, error) {
	if isWAVPath(src) {
		return decodeWAV(src, out)
	}
	if !f.Can(toolDecodeAny) {
		return 0, errNeedsEngine
	}
	f.mu.Lock()
	f.decodedOther++
	f.mu.Unlock()
	// A video or compressed file: the media worker would decode it.
	writeSpeech(out, 8, []span{{0.5, 7.5}})
	return 8, nil
}

func (f *fakeTools) Speech(ctx context.Context, path string) ([]span, error) {
	return goTools{}.Speech(ctx, path)
}

func (f *fakeTools) Transcribe(ctx context.Context, path, lang string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transcribed = append(f.transcribed, path)
	return f.transcript, f.transcribeErr
}

func (f *fakeTools) Enhance(ctx context.Context, in, out string) error {
	f.mu.Lock()
	f.enhanced++
	f.mu.Unlock()
	return copyWAV(in, out)
}

func (f *fakeTools) Devices(context.Context) ([]captureDevice, error) { return f.devices, nil }

func (f *fakeTools) Record(ctx context.Context, o captureOptions, out string, level func(float32)) (recording, error) {
	f.mu.Lock()
	f.recorded = append(f.recorded, o)
	secs := f.recSeconds
	f.mu.Unlock()
	level(0.1)
	return &fakeRecording{out: out, seconds: secs}, nil
}

func (f *fakeTools) EncodeVoice(ctx context.Context, model, ref, refText, out string) error {
	f.mu.Lock()
	f.encoded = append(f.encoded, model)
	f.mu.Unlock()
	return os.WriteFile(out, []byte("prompt"), 0o644)
}

type fakeRecording struct {
	out     string
	seconds float64
}

func (r *fakeRecording) Stop() error {
	return writeSpeech(r.out, r.seconds, []span{{0.3, r.seconds - 0.3}})
}

func (r *fakeRecording) Cancel() { os.Remove(r.out) }

var _ voiceTools = (*fakeTools)(nil)

// writeSpeech writes a mono 16-bit WAV of seconds with a voiced tone in
// the speech spans and near silence elsewhere.
func writeSpeech(path string, seconds float64, speech []span) error {
	const rate = 16000
	n := int(seconds * rate)
	data := make([]byte, 44+2*n)
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(36+2*n))
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], 1)
	binary.LittleEndian.PutUint32(data[24:], rate)
	binary.LittleEndian.PutUint32(data[28:], rate*2)
	binary.LittleEndian.PutUint16(data[32:], 2)
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], uint32(2*n))
	for i := range n {
		tm := float64(i) / rate
		amp := 30.0 // a faint hiss
		for _, s := range speech {
			if tm >= s.Start && tm < s.End {
				amp = 12000
			}
		}
		v := amp * math.Sin(2*math.Pi*180*tm) * (0.7 + 0.3*math.Sin(2*math.Pi*3*tm))
		if amp < 100 {
			v = amp * math.Sin(float64(i)*1.7)
		}
		binary.LittleEndian.PutUint16(data[44+2*i:], uint16(int16(v)))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// newVoiceTestApp is newTestApp with fake voice tools.
func newVoiceTestApp(t *testing.T) (*app, *fakeEngine, *fakeTools) {
	t.Helper()
	a, eng, _ := newTestApp(t)
	ft := newFakeTools()
	a.tools = ft
	return a, eng, ft
}

// addVoice saves a voice through the service, with a reference clip of
// refSeconds for clones.
func addVoice(t *testing.T, a *app, v core.Voice, refSeconds float64) core.Voice {
	t.Helper()
	var f core.VoiceFiles
	if refSeconds > 0 {
		f.Reference = filepath.Join(t.TempDir(), "ref.wav")
		if err := writeSpeech(f.Reference, refSeconds, []span{{0.2, refSeconds - 0.2}}); err != nil {
			t.Fatal(err)
		}
		v.RefSeconds = refSeconds
	}
	saved, err := a.lib.svc.Voices.SaveFiles(context.Background(), v, f)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

// acceptLicences accepts every licence (OmniVoice is gated).
func acceptLicences(t *testing.T, a *app) {
	t.Helper()
	for _, e := range voiceCatalog().Entries {
		if e.Licence.RequiresAcceptance {
			if err := a.lib.licences.Accept(context.Background(), core.LicenceAcceptance{Licence: e.Licence.ID}); err != nil {
				t.Fatal(err)
			}
		}
	}
	a.loadAcceptances()
}
