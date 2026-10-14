package main

import (
	"os"
	"testing"

	"github.com/hehehai/AudioSmith/internal/i18n"
)

// TestSnapshotsStudioHint renders the Studio on Qwen3-TTS 1.7B Base with
// the designed voice 温晴 (it has a sample) picked, and with no voice, in
// Chinese and English at the default window (thread #36):
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/hint go test -run TestSnapshotsStudioHint .
func TestSnapshotsStudioHint(t *testing.T) {
	out := os.Getenv("AUDIOSMITH_SNAPSHOTS")
	if out == "" {
		t.Skip("set AUDIOSMITH_SNAPSHOTS to a directory")
	}
	os.MkdirAll(out, 0o755)
	t.Cleanup(func() { i18n.SetLocale("en") })
	for _, run := range []b1Run{{"1100", 1100, 720, 1, "zh-Hans", false}, {"1100", 1100, 720, 1, "en", false}} {
		i18n.SetLocale("en")
		a, _, tt := newTestApp(t)
		s := &b1Shooter{t: t, out: out, run: run}
		s.setup(a, tt)
		wenqing := voiceWithSample(t, a, "温晴", true)
		a.selectModel(entryByID(t, "qwen3-tts-1.7b-base-8bit"))
		a.text = "大家好，欢迎来到 AudioSmith。"
		a.script.set(a.text)
		a.studioVoice = wenqing.ID
		s.save(tt, "studio-hint-wenqing")
		a.studioVoice = ""
		s.save(tt, "studio-hint-novoice")
	}
}
