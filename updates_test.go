package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// fakeUpdates records what Settings asks of the updater.
type fakeUpdates struct {
	enabled, auto, download bool
	last                    time.Time
	checks                  int
	changed                 func()
}

func (f *fakeUpdates) Enabled() bool                 { return f.enabled }
func (f *fakeUpdates) Check()                        { f.checks++ }
func (f *fakeUpdates) AutomaticChecks() bool         { return f.auto }
func (f *fakeUpdates) SetAutomaticChecks(on bool)    { f.auto = on }
func (f *fakeUpdates) AutomaticDownloads() bool      { return f.download }
func (f *fakeUpdates) SetAutomaticDownloads(on bool) { f.download = on }
func (f *fakeUpdates) LastCheck() time.Time          { return f.last }
func (f *fakeUpdates) OnChange(fn func())            { f.changed = fn }

func TestSoftwareUpdateSettings(t *testing.T) {
	a, _, _ := newTestApp(t)
	tt := ui.NewTester(a.view, 1100, 2400)
	// A build without updates (tests, development, ad-hoc): it says so,
	// and its switches are off and disabled.
	a.navigate("/settings/about")
	tt.Frame()
	tt.Frame()
	if !tt.HasText("Software updates") || !tt.HasText("This copy doesn't update itself: development builds and copies built on this Mac don't. Releases from GitHub do.") {
		t.Fatalf("page %q", tt.Texts())
	}
	if err := tt.Click("Check once a day"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.updInfo.auto {
		t.Error("a disabled switch turned automatic checks on")
	}

	// A release: the preferences show and change the updater's.
	f := &fakeUpdates{enabled: true, auto: true, last: time.Date(2026, 10, 16, 9, 30, 0, 0, time.Local)}
	a.updates = f
	a.connectUpdates()
	tt.Frame()
	if !tt.HasText("Last checked Oct 16 09:30.") {
		t.Fatalf("no last check: %q", tt.Texts())
	}
	if err := tt.Click("Check for Updates…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if f.checks != 1 {
		t.Errorf("checks %d", f.checks)
	}
	if err := tt.Click("Install without asking"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !f.download {
		t.Error("automatic downloads not turned on")
	}
	if err := tt.Click("Check once a day"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if f.auto {
		t.Error("automatic checks not turned off")
	}
	// The updater's own changes (its window's checkbox) show again.
	f.download = false
	f.changed()
	tt.Frame()
	if a.updInfo.download {
		t.Error("OnChange did not refresh")
	}
}

func TestCheckForUpdatesCommand(t *testing.T) {
	a, _, _ := newTestApp(t)
	f := &fakeUpdates{}
	a.updates = f
	for _, c := range a.commands() {
		if c.id == "updates" {
			c.run()
		}
	}
	if f.checks != 1 {
		t.Fatalf("palette: %d checks", f.checks)
	}
}

// mygo build needs CHANGELOG.md's section of the version it builds for
// the release notes the update window shows ("## 0.3.0 — date"), and an
// updates section, when there is one, with a real public key and our
// repository.
func TestReleaseConfig(t *testing.T) {
	var cfg struct {
		Identifier string `json:"identifier"`
		Version    string `json:"version"`
		Updates    *struct {
			PublicKey string `json:"publicKey"`
			GitHub    string `json:"github"`
		} `json:"updates"`
	}
	data, err := os.ReadFile("mygo.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Identifier != "dev.hehehai.audiosmith" {
		t.Errorf("identifier %q: keep it once released", cfg.Identifier)
	}
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(cfg.Version) {
		t.Errorf("version %q", cfg.Version)
	}
	log, err := os.ReadFile("CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^## \[?` + regexp.QuoteMeta(cfg.Version) + `\b`).Match(log) {
		t.Errorf("CHANGELOG.md has no \"## %s\" section, which mygo build makes the update's release notes", cfg.Version)
	}
	if u := cfg.Updates; u != nil {
		if key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(u.PublicKey)); err != nil || len(key) != 32 {
			t.Errorf("updates.publicKey is not mygo-update.pub (an Ed25519 public key)")
		}
		if u.GitHub != "hehehai/AudioSmith" {
			t.Errorf("updates.github %q", u.GitHub)
		}
	}
}
