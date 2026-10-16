package main

// Software updates (thread #50): MyGo's updater plugin, with its window
// drawn in native UI (plugins/updater/native), as MyGo's docs recommend
// for apps without web pages. It checks GitHub's latest release of
// hehehai/AudioSmith once a day, shows the version's CHANGELOG.md section
// and installs the signed archive (or a delta), checked against the public
// key `mygo build` links in from mygo.json's updates.publicKey.
//
// Builds without updates (no updates section: development builds, the
// ad-hoc builds of scripts/mac.sh, `go test`) never check, and
// mygo.Updater.Enabled() is false in them; "Check for Updates…" then says
// why. docs/release.md tells how releases are signed and published.
//
// The app menu has "Check for Updates…" (shell_commands.go) and Settings →
// Diagnostics and about a Software updates section (settings_about.go).
//
// Beta channel (thread #52): betas are GitHub prereleases (0.4.0-beta.1),
// which never become the latest release. With Settings → Receive beta
// updates on, MyGo's updater (patch 0003, mygo.Updater.SetPrereleases)
// finds the newest version among releases and prereleases through the
// GitHub API instead; the window and the daily check follow it. The
// setting (settings.Updates.Beta) defaults to on in a beta copy, off in a
// release. Turning it off never goes back to an older version: a beta
// stays until a release newer than it is out.

import (
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	"github.com/egoist/mygo/plugins/updater/native"

	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/settings"
)

// updateService is what AudioSmith uses of the updater plugin; tests use
// noUpdates or a fake.
type updateService interface {
	// Enabled reports whether this build can update itself: built with
	// updates, and installed where it can write (mygo.Updater.Enabled). It
	// touches the disk: call it off the main thread, now and then.
	Enabled() bool
	// Check checks as the user asked: the plugin's window shows at once
	// and says when AudioSmith is up to date, or why it can't update.
	Check()
	AutomaticChecks() bool
	SetAutomaticChecks(on bool)
	AutomaticDownloads() bool
	SetAutomaticDownloads(on bool)
	// LastCheck is when a check last succeeded, zero before.
	LastCheck() time.Time
	// OnChange calls fn on the main thread when a preference or the time
	// of the last check changed.
	OnChange(fn func())
	// SetPrereleases chooses the channel: betas and releases, or releases
	// only (mygo.Updater.SetPrereleases). AudioSmith keeps the choice
	// (settings.Updates.Beta) and sets it at every launch.
	SetPrereleases(on bool)
	// SetHTTPClient is the client of checks and downloads (MyGo patch
	// 0007): AudioSmith's, which follows Settings → Models' proxy and the
	// macOS system proxy, which Go's default client ignores.
	SetHTTPClient(c *http.Client)
}

// updateInfo is what Settings shows of the updater, read off the main
// thread (refreshUpdates).
type updateInfo struct {
	read       bool // read at least once
	enabled    bool
	auto       bool
	download   bool
	lastCheck  time.Time
	refreshing bool
}

// useUpdates uses the updater plugin; it runs in main, before App.Run.
func useUpdates() updateService {
	mygo.Use(native.New(updater.Options{
		// Once a day (the plugin's default), the first time 10 s after
		// launch when one is due. The window speaks the app's language
		// (App.Locale, which applyLocale sets).
		Interval: 24 * time.Hour,
	}))
	return pluginUpdates{}
}

// pluginUpdates is the updater plugin, once used.
type pluginUpdates struct{}

func (pluginUpdates) Enabled() bool                 { return mygo.Updater.Enabled() }
func (pluginUpdates) Check()                        { updater.CheckForUpdates() }
func (pluginUpdates) AutomaticChecks() bool         { return updater.AutomaticChecks() }
func (pluginUpdates) SetAutomaticChecks(on bool)    { updater.SetAutomaticChecks(on) }
func (pluginUpdates) AutomaticDownloads() bool      { return updater.AutomaticDownloads() }
func (pluginUpdates) SetAutomaticDownloads(on bool) { updater.SetAutomaticDownloads(on) }
func (pluginUpdates) LastCheck() time.Time          { return updater.LastCheck() }
func (pluginUpdates) OnChange(fn func())            { updater.OnChange(fn) }
func (pluginUpdates) SetPrereleases(on bool)        { mygo.Updater.SetPrereleases(on) }
func (pluginUpdates) SetHTTPClient(c *http.Client)  { mygo.Updater.SetHTTPClient(c) }

// noUpdates is a build without the plugin: tests, and the MCP relay.
type noUpdates struct{}

func (noUpdates) Enabled() bool              { return false }
func (noUpdates) Check()                     {}
func (noUpdates) AutomaticChecks() bool      { return false }
func (noUpdates) SetAutomaticChecks(bool)    {}
func (noUpdates) AutomaticDownloads() bool   { return false }
func (noUpdates) SetAutomaticDownloads(bool) {}
func (noUpdates) LastCheck() time.Time       { return time.Time{} }
func (noUpdates) OnChange(func())            {}
func (noUpdates) SetPrereleases(bool)        {}
func (noUpdates) SetHTTPClient(*http.Client) {}

// connectUpdates sets the channel and hears the updater's changes; it
// runs once the window is open (a.version is known), before the plugin's
// first check (10 s after launch at the soonest).
func (a *app) connectUpdates() {
	a.updates.SetPrereleases(betaUpdates(a.prefs, a.version))
	if a.lib != nil {
		set := a.lib.settings
		a.updates.SetHTTPClient(updateHTTPClient(func() string { return set.Get().Models.Proxy }))
	}
	a.updates.OnChange(func() { a.post(a.refreshUpdates) })
	a.refreshUpdates()
}

// betaUpdates reports whether AudioSmith takes beta versions: as the user
// chose, else when this copy is a beta itself (someone who installed a
// beta wants the next one).
func betaUpdates(s settings.Settings, version string) bool {
	if b := s.Updates.Beta; b != nil {
		return *b
	}
	return isPrerelease(version)
}

// setBetaUpdates keeps the user's choice of channel and tells the updater.
func (a *app) setBetaUpdates(on bool) {
	a.updateSettings(func(s *settings.Settings) {
		v := on
		s.Updates.Beta = &v
	})
	a.updates.SetPrereleases(on)
}

// isPrerelease reports whether a version such as 0.4.0-beta.1 is a
// prerelease (semantic versioning, as MyGo's updater compares them).
func isPrerelease(version string) bool {
	version, _, _ = strings.Cut(version, "+")
	_, pre, _ := strings.Cut(version, "-")
	return pre != ""
}

// refreshUpdates reads the updater's state in the background.
func (a *app) refreshUpdates() {
	if a.updInfo.refreshing {
		return
	}
	a.updInfo.refreshing = true
	u := a.updates
	a.async(func() {
		info := updateInfo{read: true, enabled: u.Enabled()}
		info.auto, info.download, info.lastCheck = u.AutomaticChecks(), u.AutomaticDownloads(), u.LastCheck()
		a.post(func() { a.updInfo = info })
	})
}

// checkForUpdates checks as the user asked (menu, palette, Settings).
func (a *app) checkForUpdates() {
	log.Print("updates: check asked")
	a.updates.Check()
}

// updateHTTPClient is the updater's HTTP client: through the proxy that
// Settings → Models names at the time of each request ("" automatic: the
// environment, then the macOS system proxy), with a bound on the wait for
// response headers; downloads themselves are bounded by their context.
func updateHTTPClient(mode func() string) *http.Client {
	c := hub.NewHTTPClient("", 30*time.Second)
	if t, ok := c.Transport.(*http.Transport); ok {
		t.Proxy = func(r *http.Request) (*url.URL, error) { return hub.ProxyFunc(mode())(r) }
	}
	return c
}
