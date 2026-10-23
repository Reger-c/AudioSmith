package main

import (
	"context"
	"errors"
	"testing"

	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// TestSnapshotsBeta renders Settings → Diagnostics and about with the
// update channel (thread #52): a beta copy (Beta badge, switch on, the
// note that turning it off never goes back) and a release copy, in
// English and Chinese, light and dark, at 1100 wide and tall enough for
// the whole section; and a copy built on the Mac, which cannot update
// itself:
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/beta go test -run TestSnapshotsBeta .
func TestSnapshotsBeta(t *testing.T) { runSnapshots(t, snapshotsBeta) }

func snapshotsBeta(t *testing.T, out string) {
	t.Cleanup(func() { i18n.SetLocale("en") })
	for _, run := range b1Runs {
		if run.size != "1100" {
			continue
		}
		run.h = 1300
		for _, version := range []string{"0.4.0-beta.1", "0.4.0"} {
			s := &b1Shooter{t: t, out: out, run: run}
			i18n.SetLocale("en")
			e := newUpdatesApp(t, version, true)
			s.setup(e.a, e.tt)
			e.a.navigate("/settings/about")
			name := "beta-settings-about"
			if version == "0.4.0" {
				name = "release-settings-about"
			}
			s.save(e.tt, name)
		}
	}
	// A copy built on the Mac (no updates: the note at the top, the
	// switches off and disabled) at about the width the user's window
	// had (thread #97), in English and Chinese, and in the smallest
	// window at 125 %.
	for _, run := range b1Runs {
		switch {
		case run.size == "1100" && !run.dark:
			run.size, run.w = "1000", 1000
		case run.size == "min125" && !run.dark:
		default:
			continue
		}
		run.h = 900
		s := &b1Shooter{t: t, out: out, run: run}
		i18n.SetLocale("en")
		e := newUpdatesApp(t, "0.4.0-beta.6", false)
		s.setup(e.a, e.tt)
		e.a.navigate("/settings/about")
		e.tt.Frame()
		e.tt.Frame()
		// Down to Software updates, at the foot of the page.
		e.tt.Scroll(float32(run.w)*0.6, float32(run.h)/3, 0, 4000)
		s.save(e.tt, "local-settings-about")
	}
}

// TestSnapshotsUpdates renders the in-app updater (updates_view.go): the
// sidebar's "Update available" and the rail's button, the update panel
// (available with actnow.dev's notes, downloading, installed, failed), the
// notes in English in a Chinese window, and the changelog's history, in
// English and Chinese, light and dark:
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/upd go test -run TestSnapshotsUpdates .
func TestSnapshotsUpdates(t *testing.T) { runSnapshots(t, snapshotsUpdates) }

func snapshotsUpdates(t *testing.T, out string) {
	t.Cleanup(func() { i18n.SetLocale("en") })
	for _, run := range b1Runs {
		if run.size != "1100" {
			continue
		}
		s := &b1Shooter{t: t, out: out, run: run}
		i18n.SetLocale("en")
		e := newUpdatesApp(t, "0.4.0-beta.5", true)
		s.setup(e.a, e.tt)
		e.feed.offer("0.4.0")
		e.a.updates.mgr.Check(context.Background())
		s.save(e.tt, "updates-sidebar")
		e.a.setRail(true)
		s.save(e.tt, "updates-rail")
		e.a.setRail(false)
		e.a.updates.panelOpen = true
		s.save(e.tt, "updates-panel")
		e.feed.during = func() { s.save(e.tt, "updates-downloading") }
		e.a.installUpdate()
		s.save(e.tt, "updates-ready")

		// A failed download.
		e = newUpdatesApp(t, "0.4.0", true)
		s.setup(e.a, e.tt)
		e.feed.offer("0.4.1")
		e.feed.installErr = errors.New("the archive's signature does not match")
		e.a.checkForUpdates()
		e.a.installUpdate()
		s.save(e.tt, "updates-failed")

		// A version whose notes are English only (in Chinese, marked).
		if run.lang == "zh-Hans" {
			e = newUpdatesApp(t, "0.4.0-beta.5", true)
			s.setup(e.a, e.tt)
			e.feed.offer("0.4.0-beta.6")
			e.a.checkForUpdates()
			s.save(e.tt, "updates-panel-english")
		}

		// Settings → About, then the changelog.
		e = newUpdatesApp(t, "0.4.0-beta.5", true)
		s.setup(e.a, e.tt)
		e.tt.SetSize(run.w, 1300)
		e.a.updateSettings(func(st *settings.Settings) { st.Updates.LastCheck = snapEpoch })
		e.a.navigate("/settings/about")
		s.save(e.tt, "updates-settings")
		e.tt.SetSize(run.w, run.h)
		e.a.openHistory()
		s.save(e.tt, "updates-history")
	}
}
