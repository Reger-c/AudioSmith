package settings

// M5 section (thread #20): the Local API (OpenAI-compatible /v1, the
// native /api, MCP) the app serves while it runs. The API key is not a
// setting: it lives in the Keychain as "api.key"; the LAN sharing PIN
// lives in memory only.

import (
	"slices"
	"strings"
)

// API: the local HTTP server.
type API struct {
	// Enabled starts the server with the app. On by default: it listens
	// on the loopback interface only.
	Enabled bool `json:"enabled"`
	// Port is the loopback port (127.0.0.1). 0 = DefaultAPIPort.
	Port int `json:"port,omitempty"`
	// RequireKey asks every client, local ones too, for the API key
	// (Authorization: Bearer). Off: local clients need none.
	RequireKey bool `json:"requireKey,omitempty"`
	// SharePort is the port LAN sharing listens on (all interfaces). 0 =
	// Port+1. Sharing itself is turned on per session, never saved.
	SharePort int `json:"sharePort,omitempty"`
	// Origins are extra browser origins allowed to call the API
	// ("http://localhost:5173"); the API's own origin always is.
	Origins []string `json:"origins,omitempty"`
	// QueueSeconds is how long a request waits for the engine behind
	// other work before it is answered 503 (Retry-After). 0 = 120.
	QueueSeconds int `json:"queueSeconds,omitempty"`

	// MCP: how tools hand back audio and where they write files.
	MCPOutput string `json:"mcpOutput,omitempty"` // "inline" | "file" | "both"; "" = MCPOutputBoth
	MCPDir    string `json:"mcpDir,omitempty"`    // file sandbox; "" = <data>/mcp
	// Bindings give each agent (MCP client id) its own voice.
	Bindings []APIBinding `json:"bindings,omitempty"`
}

// APIBinding is the voice of one agent.
type APIBinding struct {
	Client string `json:"client"`          // client id: X-AudioSmith-Client header, else the MCP client's name
	Label  string `json:"label,omitempty"` // shown in Settings
	Voice  string `json:"voice,omitempty"` // saved voice id; "" = the default
	Model  string `json:"model,omitempty"` // catalogue id; "" = the default
}

// DefaultAPIPort is the loopback port of a fresh install.
const DefaultAPIPort = 3901

// MCP output modes.
const (
	MCPOutputInline = "inline" // audio as base64 in the tool result
	MCPOutputFile   = "file"   // a file in the sandbox, its path in the result
	MCPOutputBoth   = "both"
)

// LoopbackPort is the loopback port in use.
func (a API) LoopbackPort() int {
	if a.Port <= 0 || a.Port > 65535 {
		return DefaultAPIPort
	}
	return a.Port
}

// LANPort is the port LAN sharing listens on.
func (a API) LANPort() int {
	if a.SharePort > 0 && a.SharePort <= 65535 && a.SharePort != a.LoopbackPort() {
		return a.SharePort
	}
	return a.LoopbackPort() + 1
}

// Output is the MCP output mode in use.
func (a API) Output() string {
	switch a.MCPOutput {
	case MCPOutputInline, MCPOutputFile:
		return a.MCPOutput
	}
	return MCPOutputBoth
}

// Binding finds the binding of a client id (case-insensitive).
func (a API) Binding(client string) (APIBinding, bool) {
	client = strings.TrimSpace(client)
	if client == "" {
		return APIBinding{}, false
	}
	i := slices.IndexFunc(a.Bindings, func(b APIBinding) bool { return strings.EqualFold(b.Client, client) })
	if i < 0 {
		return APIBinding{}, false
	}
	return a.Bindings[i], true
}

func defaultsAPI(s *Settings) {
	s.API = API{Enabled: true, Port: DefaultAPIPort}
}
