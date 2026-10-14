package settings

// M6b section (thread #23): AI phone calls over Twilio. The auth token is
// not a setting: it lives in the Keychain as "twilio.token".

import (
	"strconv"
	"strings"
)

// Calls: the Twilio account, the gateway and how calls behave.
type Calls struct {
	// Enabled runs the call gateway (Twilio can reach AudioSmith). Off by
	// default.
	Enabled bool `json:"enabled,omitempty"`
	// AccountSID is the Twilio account ("AC" + 32 hex digits).
	AccountSID string `json:"accountSid,omitempty"`
	// PublicURL is the tunnel's public https origin, which forwards to the
	// gateway ("https://x.trycloudflare.com").
	PublicURL string `json:"publicUrl,omitempty"`
	// Tunnel is the tool the setup page shows commands for: "cloudflared"
	// ("") or "ngrok".
	Tunnel string `json:"tunnel,omitempty"`
	// GatewayPort is the gateway's port; 0 = DefaultGatewayPort.
	GatewayPort int `json:"gatewayPort,omitempty"`
	// GatewayAll listens on every interface instead of 127.0.0.1 (a
	// tunnel running on another machine).
	GatewayAll bool `json:"gatewayAll,omitempty"`
	// Number is the Twilio number calls come from and to (E.164), and
	// NumberSID its id.
	Number    string `json:"number,omitempty"`
	NumberSID string `json:"numberSid,omitempty"`
	// Voice is the saved voice the agent speaks with (a designed voice,
	// or a cloned one with a verified consent recording); Model the TTS
	// model ("" = the voice's own, else the Studio's).
	Voice string `json:"voice,omitempty"`
	Model string `json:"model,omitempty"`
	// STTModel transcribes the caller ("" = dictation's model).
	STTModel string `json:"sttModel,omitempty"`
	// UserName is who the agent works for ("{name}" in the disclosure).
	UserName string `json:"userName,omitempty"`
	// Disclosure is the AI disclosure's template ("" = the default).
	Disclosure string `json:"disclosure,omitempty"`
	// Greeting is what callers hear in greeting mode.
	Greeting string `json:"greeting,omitempty"`
	// Inbound is what happens to incoming calls: "greeting" ("") or "agent".
	Inbound      string `json:"inbound,omitempty"`
	InboundBrief string `json:"inboundBrief,omitempty"`
	// MaxMinutes is the default limit of a new call (2–30; 0 = 5);
	// InboundMinutes that of incoming agent calls (0 = 5).
	MaxMinutes     int `json:"maxMinutes,omitempty"`
	InboundMinutes int `json:"inboundMinutes,omitempty"`
	// MaxConcurrent is how many calls may run at once (1–2; 0 = 1).
	MaxConcurrent int `json:"maxConcurrent,omitempty"`
	// Record keeps a stereo recording of each call, only when the
	// disclosure spoken says the call is recorded.
	Record bool `json:"record,omitempty"`
	// Verified is when a webhook last passed the signature check (unix
	// seconds; 0 = never): Twilio reached AudioSmith.
	Verified int64 `json:"verified,omitempty"`
}

// DefaultGatewayPort is the call gateway's default port.
const DefaultGatewayPort = 3950

// DefaultCallMinutes is a call's default limit.
const DefaultCallMinutes = 5

// Inbound modes.
const (
	InboundGreeting = "greeting"
	InboundAgent    = "agent"
)

// MaxGreeting is the longest greeting.
const MaxGreeting = 1000

// Port is the gateway's port.
func (c Calls) Port() int {
	if c.GatewayPort <= 0 || c.GatewayPort > 65535 {
		return DefaultGatewayPort
	}
	return c.GatewayPort
}

// GatewayAddr is the address the gateway listens on.
func (c Calls) GatewayAddr() string {
	host := "127.0.0.1"
	if c.GatewayAll {
		host = "0.0.0.0"
	}
	return host + ":" + strconv.Itoa(c.Port())
}

// Minutes is the default limit of a new outbound call.
func (c Calls) Minutes() int { return clampMinutes(c.MaxMinutes) }

// InboundLimit is the limit of an incoming agent call.
func (c Calls) InboundLimit() int { return clampMinutes(c.InboundMinutes) }

func clampMinutes(n int) int {
	if n <= 0 {
		return DefaultCallMinutes
	}
	return min(max(n, 2), 30)
}

// Concurrent is how many calls may run at once.
func (c Calls) Concurrent() int { return min(max(c.MaxConcurrent, 1), 2) }

// InboundMode is "greeting" or "agent".
func (c Calls) InboundMode() string {
	if c.Inbound == InboundAgent {
		return InboundAgent
	}
	return InboundGreeting
}

// TunnelTool is "cloudflared" or "ngrok".
func (c Calls) TunnelTool() string {
	if c.Tunnel == "ngrok" {
		return "ngrok"
	}
	return "cloudflared"
}

// Public is the public URL without a trailing slash.
func (c Calls) Public() string { return strings.TrimRight(strings.TrimSpace(c.PublicURL), "/") }
