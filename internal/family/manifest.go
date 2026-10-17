package family

// AudioSmith's manifest, apps/<bundle id>.json (spec §6.2).

import (
	"path/filepath"
	"strconv"
	"time"

	"github.com/hehehai/actnowkit/registry"
	"github.com/hehehai/actnowkit/shortcuts"
)

// Inputs are what the manifest says that the settings decide; the
// manifest is rewritten when they change (Update).
type Inputs struct {
	Version string // the app's version: "0.4.0-beta.6"
	Beta    bool   // the update channel is the beta one
	Hub     string // the model folder in use ($HUB)
	DataDir string // the app's data folder

	// Shortcut is the dictation shortcut, an accelerator
	// ("CmdOrCtrl+Shift+Space"); "" = none. Hold: hold to talk.
	Shortcut        string
	ShortcutHold    bool
	ShortcutEnabled bool
	// ShortcutSince is when the user last set the shortcut or turned
	// dictation on (the manifest's enabledAt: the later of two apps
	// with the same keys prompts, spec §10.4); zero = unknown.
	ShortcutSince time.Time

	// The Local API (spec §11.2: declared, not yet offered to others).
	APIEnabled bool
	APIPort    int
	APIKey     bool // clients need the API key (Bearer)
}

// URLs of the Settings pages and the Models page (shell_commands.go
// openURL).
const (
	linkModels    = "audiosmith://models"
	linkShortcuts = "audiosmith://settings/dictation"
	linkNetwork   = "audiosmith://settings/network"
)

// keychainService is where AudioSmith keeps its secrets (internal/hub
// token.go), and the Local API key's account in it.
const (
	keychainService = "AudioSmith"
	apiKeyAccount   = "api.key"
	apiPortFile     = "api.json" // in the data folder (internal/api PortFileName)
)

// Manifest is AudioSmith's manifest for in.
func (f *Family) Manifest(in Inputs) registry.Manifest {
	m := registry.Manifest{
		App: AppID, BundleID: f.BundleID, Name: "AudioSmith", Version: in.Version,
		Path: f.appPath, TeamID: f.teamID, URLScheme: "audiosmith",
		Links: map[string]string{
			registry.LinkModels:    linkModels,
			registry.LinkShortcuts: linkShortcuts,
			registry.LinkNetwork:   linkNetwork,
		},
		DataDir: in.DataDir,
		Capabilities: []string{registry.CapModelsHF, registry.CapASRLocal, registry.CapTTSLocal, registry.CapLLMLocal,
			registry.CapDictation, registry.CapAPILocal},
		Models: &registry.ModelsInfo{Root: in.Hub},
	}
	m.Channel = "stable"
	if in.Beta {
		m.Channel = "beta"
	}
	if in.Shortcut != "" {
		if keys, err := shortcuts.Normalize(in.Shortcut); err == nil {
			trigger := shortcuts.Press
			if in.ShortcutHold {
				trigger = shortcuts.Hold
			}
			m.Shortcuts = []shortcuts.Shortcut{{ID: "dictation", Title: "Dictation", Keys: keys, Trigger: trigger,
				Enabled: in.ShortcutEnabled, Mechanism: shortcuts.Carbon}}
			if !in.ShortcutSince.IsZero() {
				at := in.ShortcutSince.UTC()
				m.Shortcuts[0].EnabledAt = &at
			}
		} else {
			f.logf("family: dictation shortcut %q left out of the manifest: %v", in.Shortcut, err)
		}
	}
	if in.APIEnabled && in.APIPort > 0 {
		auth := &registry.ServiceAuth{Type: "none"}
		if in.APIKey {
			auth = &registry.ServiceAuth{Type: "bearer", Keychain: &registry.KeychainRef{Service: keychainService, Account: apiKeyAccount}}
		}
		svc := registry.Service{
			ID: "audiosmith.api", Kind: "openai-audio", URL: "http://127.0.0.1:" + strconv.Itoa(in.APIPort) + "/v1",
			Endpoints: []string{"/audio/transcriptions", "/audio/speech", "/models"}, Auth: auth,
		}
		if in.DataDir != "" {
			svc.PortFile = filepath.Join(in.DataDir, apiPortFile)
		}
		m.Services = []registry.Service{svc}
	}
	return m
}

// writeManifest writes the manifest for in (registry.Write keeps the
// members a newer AudioSmith added, and refuses a newer schema).
func (f *Family) writeManifest(in Inputs) error {
	if err := registry.Write(f.Dir, f.Manifest(in)); err != nil {
		f.logf("family: manifest: %v", err)
		return err
	}
	f.mu.Lock()
	f.written, f.hasMan = in, true
	f.mu.Unlock()
	return nil
}
