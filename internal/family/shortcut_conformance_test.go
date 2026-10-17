package family

// The actnowkit conformance suite against AudioSmith's shortcut
// coordination (P5: conformance.ShortcutApp). The model scenarios need
// the adapter of P2 (deletes); this one implements the shortcut part
// only, so the suite skips the others here.

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/hehehai/actnowkit/conformance"
	"github.com/hehehai/actnowkit/models"
	"github.com/hehehai/actnowkit/shortcuts"
)

// shortcutApp drives AudioSmith's Family as conformance.ShortcutApp.
type shortcutApp struct {
	f       *Family
	in      Inputs
	started bool
}

func (a *shortcutApp) BundleID() string { return a.f.BundleID }

// Declare is the user setting the dictation shortcut (AudioSmith has
// that one): the manifest is written (Start, then Update).
func (a *shortcutApp) Declare(ctx context.Context, sc []shortcuts.Shortcut) error {
	a.in.Shortcut, a.in.ShortcutEnabled, a.in.ShortcutHold = "", false, false
	if len(sc) > 0 {
		a.in.Shortcut, a.in.ShortcutEnabled = sc[0].Keys, sc[0].Enabled
		a.in.ShortcutHold = shortcuts.NormalizeTrigger(sc[0].Trigger) == shortcuts.Hold
		a.in.ShortcutSince = time.Now().UTC().Truncate(time.Second)
	}
	none := func(string) []models.Claim { return nil }
	if !a.started {
		a.started = true
		return a.f.Start(ctx, a.in, none)
	}
	return a.f.Update(ctx, a.in, none)
}

// Conflicts is what the settings row warns about (every installed
// variant but dev builds: the suite's peers are test builds).
func (a *shortcutApp) Conflicts(ctx context.Context) ([]string, error) {
	a.f.Rediscover()
	var out []string
	for _, c := range a.f.ShortcutConflicts(false) {
		out = append(out, c.Other.BundleID)
	}
	return out, nil
}

func TestShortcutConformance(t *testing.T) {
	conformance.Run(t, func(t testing.TB, fx *conformance.Fixture, bundleID string) any {
		app, err := conformance.FakeApp(filepath.Join(filepath.Dir(fx.Family.Root), "Applications", bundleID), "AudioSmith", bundleID)
		if err != nil {
			t.Fatal(err)
		}
		team := ""
		f := New(Options{Dir: fx.Family, BundleID: bundleID, System: conformance.FixtureSystem{}, AppPath: app, TeamID: &team, Logf: t.Logf})
		t.Cleanup(f.Close)
		return &shortcutApp{f: f, in: Inputs{Version: "0.4.0-test", Hub: fx.Hub}}
	})
}
