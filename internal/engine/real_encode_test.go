package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine/enginetest"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
)

// TestRealEncodeAACRates runs media.encode (AAC in M4A) over the sample
// rates, channel counts and bitrates the Export audio dialog offers, and
// reports which the AAC encoder takes: the dialog keeps its bitrates within
// what each rate and layout allows (aacMaxBitrate in the app).
// AUDIOSMITH_E2E=1, macOS.
func TestRealEncodeAACRates(t *testing.T) {
	if os.Getenv("AUDIOSMITH_E2E") == "" {
		t.Skip("set AUDIOSMITH_E2E=1 to run the real engine")
	}
	bin := Find(filepath.Join("..", "..", "resources"))
	if bin == "" {
		t.Fatal("no engine: run scripts/build-engine.sh or set AUDIOSMITH_ENGINE")
	}
	dir := t.TempDir()
	sup := NewSupervisor(Config{Bin: bin, Bus: core.NewBus(), Roles: []proto.Role{proto.RoleMedia}, LogDir: dir, StatsEvery: -1, Logf: t.Logf})
	sup.Start(context.Background())
	defer sup.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	enc := Executor(sup, proto.RoleMedia, proto.MethodMediaEncode)
	for _, rate := range []int{24000, 44100, 48000} {
		for _, ch := range []int{1, 2} {
			in := filepath.Join(dir, fmt.Sprintf("in-%d-%d.wav", rate, ch))
			if err := enginetest.WriteWAV(in, enginetest.Tone(rate, ch, 2, 440, 0.3), 32); err != nil {
				t.Fatal(err)
			}
			for _, br := range []int{64000, 96000, 128000, 160000, 192000, 256000, 320000} {
				out := filepath.Join(dir, fmt.Sprintf("out-%d-%d-%d.m4a", rate, ch, br))
				res, err := runJob(ctx, enc, proto.EncodeParams{Input: in, Output: proto.Output{Path: out, Format: "m4a"}, Codec: "aac", Bitrate: br,
					Metadata: proto.EncodeMetadata{Title: "Tone", Author: "AudioSmith"}})
				var er proto.EncodeResult
				if err == nil {
					decodeInto(t, res, &er)
				}
				t.Logf("AAC %d Hz × %d @ %d b/s: ok=%v bytes=%d bitrate=%d %v", rate, ch, br, err == nil, er.Bytes, er.Bitrate, err)
				os.Remove(out)
			}
		}
	}
}
