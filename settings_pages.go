package main

// The settings pages M7a adds (thread #22), routed from settings_dialog.go.

import (
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/i18n"
)

// m7SettingsRoute builds Settings → Audio tools, Network, Privacy,
// Storage, Usage, Diagnostics and about.
func (a *app) m7SettingsRoute(c *ui.Context, r *ui.Route) bool {
	pages := []struct {
		id    string
		title func() string
		build func(*ui.Context)
	}{
		{"audio-tools", func() string { return i18n.T("Audio tools") }, a.audioToolsSettings},
		{"network", func() string { return i18n.T("Network") }, a.networkSettings},
		{"privacy", func() string { return i18n.T("Privacy") }, a.privacySettings},
		{"storage", func() string { return i18n.T("Storage") }, a.storageSettings},
		{"usage", func() string { return i18n.T("Usage") }, a.usageSettings},
		{"about", func() string { return i18n.T("Diagnostics and about") }, a.aboutSettings},
	}
	for _, p := range pages {
		if r.Match("/" + p.id) {
			r.Title(p.title())
			a.lastSettings = p.id
			p.build(c)
			return true
		}
	}
	return false
}
