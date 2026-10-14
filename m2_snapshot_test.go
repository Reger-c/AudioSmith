package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dictation"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// TestSnapshotsM2 renders M2's views (Transcribe, the dictation pill,
// Settings → Dictation, Writing help, Permissions) light and dark, in
// English and Chinese, as TestSnapshots does the others:
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/shots go test -run TestSnapshots .
func TestSnapshotsM2(t *testing.T) {
	out := os.Getenv("AUDIOSMITH_SNAPSHOTS")
	if out == "" {
		t.Skip("set AUDIOSMITH_SNAPSHOTS to a directory")
	}
	os.MkdirAll(out, 0o755)
	t.Cleanup(func() { i18n.SetLocale("en") })
	for _, run := range []struct {
		lang string
		dark bool
	}{{"en", false}, {"en", true}, {"zh-Hans", false}, {"zh-Hans", true}} {
		i18n.SetLocale("en")
		m := newM2Test(t)
		a, tt, eng := m.a, m.tt, m.eng
		if run.lang != "en" {
			a.updateSettings(func(s *settings.Settings) { s.General.Language = run.lang })
		}
		tt.SetDark(run.dark)
		tt.SetScale(2)
		suffix := "light"
		if run.dark {
			suffix = "dark"
		}
		if run.lang != "en" {
			suffix += "-" + run.lang
		}
		shot := func(tt *ui.Tester, name string) {
			tt.Frame()
			tt.Frame()
			f, err := os.Create(filepath.Join(out, name+"-"+suffix+".png"))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := png.Encode(f, tt.Image()); err != nil {
				t.Fatal(err)
			}
		}
		save := func(name string) { shot(tt, name) }

		a.navigate("/transcripts")
		save("transcribe-empty")

		now := time.Now()
		d := core.Transcript{ID: "d1", CreatedAt: now.Add(-3 * time.Minute), UpdatedAt: now, Source: core.SourceDictation, App: "Slack",
			Mode: "fast", Language: "zh", Duration: 3.2, Segments: []core.TranscriptSegment{{Start: 0, End: 3.2, Text: "明天下午三点开会"}}, Clean: "明天下午三点开会。"}
		putTranscript(t, a, d)
		long := sampleTranscript("t1", now.Add(-26*time.Hour))
		long.Duration = 74
		long.Segments = append(long.Segments,
			core.TranscriptSegment{Start: 4.1, End: 9.8, Text: "You are a bold one. I have been looking forward to this.", Edited: true},
			core.TranscriptSegment{Start: 10.5, End: 14, Text: "So uncivilized."})
		putTranscript(t, a, long)
		a.selectTranscript("t1")
		a.trDetail.Clean = "Hello there. General Kenobi. You're a bold one — I've been looking forward to this. So uncivilized."
		a.trDetail.CleanBy = "Ollama · qwen3:4b"
		a.trOrig = true
		save("transcribe-detail")
		a.trOrig = false

		// A transcription running, then one that needs a download.
		a.selectTranscript("")
		src := filepath.Join(t.TempDir(), "keynote.mov")
		os.WriteFile(src, []byte("mov"), 0o644)
		a.transcribeFile(src)
		r, _ := transcribeReq(t, eng)
		eng.progress(r.ID, "aligning", 0.62, 0)
		save("transcribe-running")
		eng.cancelled(r.ID)
		a.toasts = nil
		e := removeModel(t, a, "qwen3-forcedaligner-0.6b-4bit")
		a.trErr, a.trNeed = i18n.T("%s is not downloaded yet.", e.Label), &e
		save("transcribe-error")
		a.trErr, a.trNeed = "", nil

		// Settings.
		a.dictVerified = true
		a.navigate("/settings/dictation")
		save("settings-dictation")
		a.dictSet.recording, a.dictSet.pending = true, "Alt+Space"
		save("settings-dictation-record")
		a.stopShortcutRecording()
		a.llmOllama = []string{"qwen3:4b", "nomic-embed-text"}
		a.addProvider("OpenAI")
		a.llmForm.test["openai"] = i18n.T("No API key: paste one and save it.")
		a.llmForm.testErr["openai"] = true
		a.applyLLM()
		a.syncLLMForm()
		a.navigate("/settings/writing")
		save("settings-writing")
		m.sys.Mic, m.sys.Trusted = dictation.PermissionGranted, false
		a.recheckPermissions()
		a.navigate("/settings/permissions")
		save("settings-permissions")

		// The pill.
		pill := ui.NewTester(a.pillView, pillWidth, pillHeight)
		pill.SetScale(2)
		s := &dictSession{gen: 1, phase: dictListening, started: now.Add(-4 * time.Second), level: -18, target: dictation.Target{Name: "Notes"}}
		a.dict = s
		shot(pill, "pill-listening")
		s.phase, s.stage = dictTranscribing, i18n.T("Loading model…")
		shot(pill, "pill-transcribing")
		s.phase, s.text = dictInserted, "Meet me at five tomorrow."
		shot(pill, "pill-inserted")
		s.phase, s.note = dictCopied, copiedReason(dictation.Result{Reason: dictation.ReasonNoAccess})
		shot(pill, "pill-copied")
		e2 := a.dictModel()
		s.phase, s.note, s.need = dictError, i18n.T("%s is not downloaded yet.", e2.Label), &e2
		shot(pill, "pill-error")
		_ = services.ExportTXT
	}
}
