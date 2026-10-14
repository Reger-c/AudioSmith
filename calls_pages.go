package main

// The M6b pages in the shell (one call each from shell*.go, app.go and
// workflow_pages.go), and the Projects page's calls.

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/settings"
	"github.com/hehehai/AudioSmith/internal/telephony"
)

var callsIcon = icon(`<path d="M4.2 1.8 6 4.6 4.8 6a7.6 7.6 0 0 0 5.2 5.2l1.4-1.2 2.8 1.8-.8 2.2c-.2.5-.7.8-1.2.7A11.6 11.6 0 0 1 1.3 3.8c-.1-.5.2-1 .7-1.2z"/>`)

// callsPageTitle translates the M6b pages' titles.
func callsPageTitle(path string) (string, bool) {
	switch path {
	case "/calls":
		return i18n.T("Calls"), true
	case "/calls/setup", "/integrations/twilio":
		return i18n.T("Twilio setup"), true
	}
	return "", false
}

// callsRoute builds /calls, /calls/setup and /integrations/twilio (before
// the Integrations page's own route).
func (a *app) callsRoute(c *ui.Context, r *ui.Route) bool {
	switch {
	case r.Match("/calls"):
		r.Title(i18n.T("Calls"))
		a.callsPage(c)
	case r.Match("/calls/setup"), r.Match("/integrations/twilio"):
		r.Title(i18n.T("Twilio setup"))
		a.twilioPage(c)
	default:
		return false
	}
	return true
}

// callsSidebarItem is "Calls" in the sidebar's Automate section; a live
// call shows as a badge.
func (a *app) callsSidebarItem(c *ui.Context) {
	item := ui.SidebarItem(c, "/calls", callsIcon, i18n.T("Calls"))
	n := 0
	for _, call := range a.callFull {
		if call.Live() {
			n++
		}
	}
	if n > 0 {
		item.Children(func() { ui.Badge(c, i18n.T("Live")) })
	}
}

// callsCommands adds the palette's entries.
func (a *app) callsCommands(nav string, add func(id, group, title, en, keys string, enabled bool, run func())) {
	add("go/calls", nav, i18n.T("Calls"), "Calls phone", "", true, func() { a.navigate("/calls") })
	group := i18n.T("Calls")
	add("calls-setup", group, i18n.T("Set up Twilio"), "Set up Twilio phone calls", "", true, func() { a.navigate("/calls/setup") })
	add("calls-new", group, i18n.T("New call"), "New phone call", "", true, func() {
		a.navigate("/calls")
		a.callTab = 0
	})
	add("calls-inbound", group, i18n.T("Incoming calls…"), "Incoming calls", "", true, func() {
		a.navigate("/calls")
		a.openInbound()
	})
	live := ""
	for id, call := range a.callFull {
		if call.Live() {
			live = id
		}
	}
	add("calls-hangup", group, i18n.T("Hang up"), "Hang up the call", "", live != "", func() { a.hangupCall(live) })
}

// callItemDetail is a call's state on the Projects page (the item's
// detail is "<outcome>|<phase>").
func callItemDetail(detail string) string {
	outcome, phase, _ := strings.Cut(detail, "|")
	switch phase {
	case services.CallDialing, services.CallRinging, services.CallLive:
		return i18n.T("Live")
	}
	if outcome == "" {
		return callPhaseText(phase)
	}
	return callOutcomeText(outcome)
}

// callOpenPage opens a call on the Calls page (Projects, deep links).
func (a *app) callOpenPage(id string) {
	a.navigate("/calls")
	a.selectCall(id)
}

// callMentionsRecording: whether a disclosure says the call is recorded.
func callMentionsRecording(s string) bool { return services.MentionsRecording(s) }

// callMasked shows a number masked.
func callMasked(n string) string { return telephony.MaskNumber(n) }

// twilioBlocks are the Twilio card's setup blocks (its page is the
// guided setup; the blocks serve the integration list's checks).
func twilioBlocks(integEnv) []integBlock {
	_, run := tunnelCommands(0, settings.DefaultGatewayPort)
	return []integBlock{{Title: i18n.T("Tunnel to the call gateway"), Hint: i18n.T("Then finish the setup on the Twilio setup page."), Lang: "shell", Code: run}}
}
