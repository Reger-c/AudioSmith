package telephony

import (
	"encoding/xml"
	"maps"
	"slices"
	"strings"
)

// TwiML: the XML answers to Twilio's voice webhooks.

// StreamTwiML connects the call to a bidirectional Media Stream at
// streamURL (wss://…), passing params as <Parameter>s (they arrive in the
// stream's start message; Twilio does not pass query strings). When the
// stream ends the call hangs up.
func StreamTwiML(streamURL string, params map[string]string) string {
	var b strings.Builder
	b.WriteString(xml.Header)
	b.WriteString("<Response><Connect><Stream url=\"")
	b.WriteString(xmlEscape(streamURL))
	b.WriteString("\">")
	keys := sortedKeys(params)
	for _, k := range keys {
		b.WriteString("<Parameter name=\"")
		b.WriteString(xmlEscape(k))
		b.WriteString("\" value=\"")
		b.WriteString(xmlEscape(params[k]))
		b.WriteString("\"/>")
	}
	b.WriteString("</Stream></Connect><Hangup/></Response>")
	return b.String()
}

// RejectTwiML refuses a call; reason is "busy" or "rejected".
func RejectTwiML(reason string) string {
	if reason != "busy" {
		reason = "rejected"
	}
	return xml.Header + `<Response><Reject reason="` + reason + `"/></Response>`
}

// HangupTwiML ends a call.
func HangupTwiML() string { return xml.Header + "<Response><Hangup/></Response>" }

func xmlEscape(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

func sortedKeys(m map[string]string) []string { return slices.Sorted(maps.Keys(m)) }
