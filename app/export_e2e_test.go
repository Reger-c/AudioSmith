package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/services"
)

// TestRealExportM4A exports a 24 kHz take as M4A through the Export audio
// dialog and the real engine's media.encode (AVFoundation): at each
// bitrate offered, mono and stereo, at 44.1 kHz (Apple's AAC encoder takes
// no more than 64 kb/s for 24 kHz mono), with a loudness target and tags;
// afinfo reads the files back. macOS with a built engine, AUDIOSMITH_E2E=1
// (AUDIOSMITH_ENGINE may name the engine):
//
//	AUDIOSMITH_E2E=1 go test -run TestRealExportM4A -v .
func TestRealExportM4A(t *testing.T) {
	if os.Getenv("AUDIOSMITH_E2E") == "" {
		t.Skip("set AUDIOSMITH_E2E=1 to run the real engine")
	}
	bin := engine.Find("../resources")
	if bin == "" {
		t.Fatal("no engine: run scripts/build-engine.sh or set AUDIOSMITH_ENGINE")
	}
	a, _, _ := newTestApp(t)
	sup := engine.NewSupervisor(engine.Config{Bin: bin, Bus: core.NewBus(), Roles: []proto.Role{proto.RoleMedia}, LogDir: t.TempDir(), StatsEvery: -1, Logf: t.Logf})
	sup.Start(t.Context())
	defer sup.Stop()
	a.lib.useEncoder(func() jobs.Executor { return engine.Executor(sup, proto.RoleMedia, services.KindEncode) })
	tk := exportTake(t, a, "T1", core.TakeStudio)
	dir := t.TempDir()
	exportSaveDir = dir
	t.Cleanup(func() { exportSaveDir = "" })
	for _, stereo := range []int{0, 1} {
		for bi, br := range exportFormats[formatIndex("m4a")].bitrates {
			a.toasts = nil
			a.openExport(tk)
			f := a.exp
			f.format = formatIndex("m4a")
			f.bitrate[f.format], f.channels = bi, stereo
			f.loud = loudnessLabel("podcast")
			f.name = strings.Join([]string{"m4a", itoa(br), itoa(stereo)}, "-")
			o := f.options()
			a.exportChoose(f)
			path := filepath.Join(dir, f.name+".m4a")
			info, err := exec.Command("afinfo", path).CombinedOutput()
			t.Logf("%d kb/s, %d ch at %d Hz: toasts %q\n%s", br, o.Channels, o.SampleRate, a.toasts, info)
			// afinfo's "bit rate" is the file's average, which a short
			// clip of speech keeps well under the encoder's target.
			if ch := 1 + stereo; err != nil || o.SampleRate != 44100 || !strings.Contains(string(info), itoa(ch)+" ch,  44100 Hz, aac") {
				t.Errorf("%d kb/s stereo=%d: %v", br, stereo, err)
			}
		}
	}
}
