package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/i18n"
)

// TestSnapshotsC renders what phase C of the 0.3 redesign changed that
// the phase A/B sets do not show: toasts over a page (now at the top,
// under the header), the Audiobook's read-along in Chinese, the
// self-check and network marks, and the page for a missing path:
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/c go test -run TestSnapshotsC .
//	AUDIOSMITH_B1_SIZES=1100 …
func TestSnapshotsC(t *testing.T) {
	out := os.Getenv("AUDIOSMITH_SNAPSHOTS")
	if out == "" {
		t.Skip("set AUDIOSMITH_SNAPSHOTS to a directory")
	}
	os.MkdirAll(out, 0o755)
	t.Cleanup(func() { i18n.SetLocale("en") })
	var sizes []string
	if v := os.Getenv("AUDIOSMITH_B1_SIZES"); v != "" {
		sizes = strings.Split(v, ",")
	}
	for _, run := range b1Runs {
		if len(sizes) > 0 && !strings.Contains(","+strings.Join(sizes, ",")+",", ","+run.size+",") {
			continue
		}
		s := &b1Shooter{t: t, out: out, run: run}
		i18n.SetLocale("en")
		a, eng, tt := newTestApp(t)
		useLfSynth(a)
		s.setup(a, tt)
		at := time.Date(2026, 10, 13, 21, 50, 0, 0, time.Local)
		addTake(t, a, core.Take{ID: "T1", CreatedAt: at, Text: i18n.T("The last ferry left the harbour at eleven. [pause 500ms] Fog had settled on the water like wool."),
			Model: kokoro, Voice: "af_heart", SampleRate: 24000, AudioSeconds: 4}, 1)
		a.lastClipID = "T1"

		a.navigate("/studio")
		a.toasts = nil
		a.toast(i18n.T("Diagnostics copied"))
		s.save(tt, "c-studio-toast")

		a.navigate("/audiobook")
		b := a.lfBook
		a.loadSample(b)
		a.startLf(b, "")
		playLongform(t, a, eng)
		b.open["book"], b.open["cast"], b.open["setup"] = false, false, false
		a.toasts = nil
		a.toastUndo(i18n.T("Diagnostics copied"), func() {})
		s.save(tt, "c-audiobook-toast")
		a.toasts = nil
		tt.Frame()
		s.save(tt, "c-audiobook-ready")

		a.checks = []selfCheck{
			{id: "library", title: i18n.T("Library"), status: "pass", detail: "audiosmith.db"},
			{id: "engine", title: i18n.T("Engine"), status: "warn", detail: "test"},
			{id: "ffmpeg", title: "ffmpeg", status: "fail", detail: "—"},
		}
		a.navigate("/settings/about")
		s.save(tt, "c-settings-about")
		a.netChecks = []netCheck{{host: "https://huggingface.co", ok: true, latency: 120 * time.Millisecond},
			{host: "https://hf-mirror.com", err: "timeout"}}
		a.navigate("/settings/network")
		s.save(tt, "c-settings-network")
		a.navigate("/studio")

		a.navigate("/nowhere")
		s.save(tt, "c-not-found")
	}
}
