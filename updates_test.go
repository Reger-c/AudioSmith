package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// fakeUpdates records what Settings asks of the updater.
type fakeUpdates struct {
	enabled, auto, download bool
	last                    time.Time
	checks                  int
	changed                 func()
	prereleases             []bool // each SetPrereleases
}

func (f *fakeUpdates) Enabled() bool                 { return f.enabled }
func (f *fakeUpdates) Check()                        { f.checks++ }
func (f *fakeUpdates) AutomaticChecks() bool         { return f.auto }
func (f *fakeUpdates) SetAutomaticChecks(on bool)    { f.auto = on }
func (f *fakeUpdates) AutomaticDownloads() bool      { return f.download }
func (f *fakeUpdates) SetAutomaticDownloads(on bool) { f.download = on }
func (f *fakeUpdates) LastCheck() time.Time          { return f.last }
func (f *fakeUpdates) OnChange(fn func())            { f.changed = fn }
func (f *fakeUpdates) SetPrereleases(on bool)        { f.prereleases = append(f.prereleases, on) }

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
	// A release (0.4.0) or a beta (0.4.0-beta.1, a GitHub prerelease).
	if !regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`).MatchString(cfg.Version) {
		t.Errorf("version %q", cfg.Version)
	}
	log, err := os.ReadFile("CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	// The heading's first word is the version, exactly: "## 0.4.0-beta.1"
	// is not 0.4.0's section (MyGo's update.ReleaseNotes).
	if !regexp.MustCompile(`(?m)^## \[?` + regexp.QuoteMeta(cfg.Version) + `(\]|\s|$)`).Match(log) {
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

// The channel (thread #52): betas are on in a beta copy and off in a
// release until the user chooses.
func TestBetaUpdatesDefault(t *testing.T) {
	on, off := true, false
	for _, c := range []struct {
		version string
		beta    *bool
		want    bool
	}{
		{"0.4.0-beta.1", nil, true},
		{"0.4.0-rc.2+build.7", nil, true},
		{"0.4.0", nil, false},
		{"0.4.0+build-7", nil, false},
		{"", nil, false}, // development builds
		{"0.4.0-beta.1", &off, false},
		{"0.4.0", &on, true},
	} {
		var s settings.Settings
		s.Updates.Beta = c.beta
		if got := betaUpdates(s, c.version); got != c.want {
			t.Errorf("betaUpdates(%s, %v) = %t", c.version, c.beta, got)
		}
	}
	// The setting survives a save: nil stays nil (the default follows the
	// copy), a choice stays.
	data, _ := json.Marshal(settings.Defaults())
	if s, err := settings.Migrate(data); err != nil || s.Updates.Beta != nil {
		t.Errorf("defaults: %+v, %v", s.Updates, err)
	}
	s := settings.Defaults()
	s.Updates.Beta = &off
	data, _ = json.Marshal(s)
	if s, err := settings.Migrate(data); err != nil || s.Updates.Beta == nil || *s.Updates.Beta {
		t.Errorf("a choice: %+v, %v", s.Updates, err)
	}
}

func TestBetaUpdatesSettings(t *testing.T) {
	a, _, _ := newTestApp(t)
	tt := ui.NewTester(a.view, 1100, 2400)
	beta := "Betas get new features sooner but may be less stable. Turned off, AudioSmith only updates to releases."
	stays := "Betas get new features sooner but may be less stable. This copy is a beta: turned off, AudioSmith never goes back to an older version, and stays on this one until a release newer than it is out."

	// A beta copy takes betas until the user turns them off.
	a.version = "0.4.0-beta.1"
	f := &fakeUpdates{enabled: true, auto: true}
	a.updates = f
	a.connectUpdates()
	if !slices.Equal(f.prereleases, []bool{true}) {
		t.Fatalf("a beta copy: SetPrereleases %v", f.prereleases)
	}
	a.navigate("/settings/about")
	tt.Frame()
	tt.Frame()
	if !tt.HasText("Receive beta updates") || !tt.HasText(stays) {
		t.Fatalf("page %q", tt.Texts())
	}
	if !slices.Contains(tt.Texts(), "0.4.0-beta.1") || !slices.Contains(tt.Texts(), "Beta") {
		t.Errorf("About has no Beta badge: %q", tt.Texts())
	}
	if err := tt.Click("Beta updates"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if f.prereleases[len(f.prereleases)-1] {
		t.Errorf("turned off: SetPrereleases %v", f.prereleases)
	}
	if b := a.settings.Get().Updates.Beta; b == nil || *b {
		t.Errorf("saved %v", b)
	}
	// The next launch keeps the choice.
	a.connectUpdates()
	if f.prereleases[len(f.prereleases)-1] {
		t.Error("the choice was not kept")
	}

	// A release copy: off until chosen, no badge, no note about staying.
	a2, _, _ := newTestApp(t)
	tt2 := ui.NewTester(a2.view, 1100, 2400)
	a2.version = "0.4.0"
	f2 := &fakeUpdates{enabled: true, auto: true}
	a2.updates = f2
	a2.connectUpdates()
	if !slices.Equal(f2.prereleases, []bool{false}) {
		t.Fatalf("a release copy: SetPrereleases %v", f2.prereleases)
	}
	a2.navigate("/settings/about")
	tt2.Frame()
	tt2.Frame()
	if !tt2.HasText(beta) || tt2.HasText(stays) || slices.Contains(tt2.Texts(), "Beta") {
		t.Errorf("a release copy: %q", tt2.Texts())
	}
	if err := tt2.Click("Beta updates"); err != nil {
		t.Fatal(err)
	}
	tt2.Frame()
	if !f2.prereleases[len(f2.prereleases)-1] {
		t.Errorf("turned on: SetPrereleases %v", f2.prereleases)
	}

	// A copy that can't update itself shows the switch off and disabled.
	a3, _, _ := newTestApp(t)
	tt3 := ui.NewTester(a3.view, 1100, 2400)
	a3.version = "0.4.0-beta.1"
	f3 := &fakeUpdates{}
	a3.updates = f3
	a3.connectUpdates()
	a3.navigate("/settings/about")
	tt3.Frame()
	tt3.Frame()
	tt3.Click("Beta updates")
	tt3.Frame()
	if len(f3.prereleases) != 1 || a3.prefs.Updates.Beta != nil {
		t.Errorf("a disabled switch changed the channel: %v, %v", f3.prereleases, a3.prefs.Updates.Beta)
	}
}
