package main

import (
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/actnowkit/shortcuts"
)

// TestSnapshotsFamilyShortcuts renders the shortcut coordination with
// another ActNow app's release build installed (thread #73, family spec
// §10.4): the card over a page (AudioSmith set ⌘⇧Space after Auralink's
// Transcribe), the warning on Settings → Dictation, and the recorder
// hearing keys Auralink uses. Without another app nothing of it shows
// (the other snapshot sets stay as they were):
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/fam go test -run TestSnapshotsFamilyShortcuts .
func TestSnapshotsFamilyShortcuts(t *testing.T) { runSnapshots(t, snapshotsFamilyShortcuts) }

func snapshotsFamilyShortcuts(t *testing.T, out string) {
	t.Cleanup(func() { i18n.SetLocale("en") })
	at := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	for _, run := range b1Runs {
		if run.size != "1100" && !(run.size == "min" && run.lang == "en") {
			continue
		}
		s := &b1Shooter{t: t, out: out, run: run}
		i18n.SetLocale("en")
		a, _, tt := newTestApp(t)
		s.setup(a, tt)
		f, fx := testFamily(t, a, false)
		auralink(fx, "dev.hehehai.auralink", "Option+Command+J", at)
		f.Rediscover()
		conflict := shortcuts.Conflict{
			Mine:   shortcuts.Shortcut{ID: "dictation", Title: "Dictation", Keys: "Shift+Command+Space", Enabled: true},
			Other:  shortcuts.Holder{BundleID: "dev.hehehai.auralink", Name: "Auralink", Links: map[string]string{"shortcuts": "auralink://settings"}},
			Theirs: shortcuts.Shortcut{ID: "transcribe", Title: "Transcribe", Keys: "Shift+Command+Space", Enabled: true},
			Prompt: true,
		}

		a.navigate("/studio")
		a.setShortcutConflicts([]shortcuts.Conflict{conflict})
		a.toasts = nil
		s.save(tt, "family-shortcut-card")

		a.famPrompt = nil
		a.navigate("/settings/dictation")
		s.save(tt, "family-shortcut-warning")

		a.startShortcutRecording()
		a.dictSet.pending = "Cmd+Alt+J"
		s.save(tt, "family-shortcut-recorder")
		a.stopShortcutRecording()
	}
}
