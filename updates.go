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

import (
	"log"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	"github.com/egoist/mygo/plugins/updater/native"
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

// connectUpdates hears the updater's changes; it runs once the window is
// open.
func (a *app) connectUpdates() {
	a.updates.OnChange(func() { a.post(a.refreshUpdates) })
	a.refreshUpdates()
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
