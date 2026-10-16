package main

import (
	"os"
	"testing"

	"github.com/hehehai/AudioSmith/internal/i18n"
)

// TestSnapshotsBeta renders Settings → Diagnostics and about with the
// update channel (thread #52): a beta copy (Beta badge, switch on, the
// note that turning it off never goes back) and a release copy, in
// English and Chinese, light and dark, at 1100 wide and tall enough for
// the whole section:
//
//	AUDIOSMITH_SNAPSHOTS=/tmp/beta go test -run TestSnapshotsBeta .
func TestSnapshotsBeta(t *testing.T) {
	out := os.Getenv("AUDIOSMITH_SNAPSHOTS")
	if out == "" {
		t.Skip("set AUDIOSMITH_SNAPSHOTS to a directory")
	}
	os.MkdirAll(out, 0o755)
	t.Cleanup(func() { i18n.SetLocale("en") })
	for _, run := range b1Runs {
		if run.size != "1100" {
			continue
		}
		run.h = 1300
		for _, version := range []string{"0.4.0-beta.1", "0.4.0"} {
			s := &b1Shooter{t: t, out: out, run: run}
			i18n.SetLocale("en")
			a, _, tt := newTestApp(t)
			s.setup(a, tt)
			a.version = version
			a.updates = &fakeUpdates{enabled: true, auto: true}
			a.connectUpdates()
			a.navigate("/settings/about")
			name := "beta-settings-about"
			if version == "0.4.0" {
				name = "release-settings-about"
			}
			s.save(tt, name)
		}
	}
}
