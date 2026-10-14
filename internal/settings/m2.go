package settings

// M2 sections (thread #17): writing help (LLM providers and routing),
// transcription and dictation. The LLM types mirror internal/llm's
// ProviderConfig and Route field for field (this package imports nothing
// internal); the app converts them.

import (
	"maps"
	"slices"
	"strings"
	"unicode/utf8"
)

// LLM: the language models that help with writing (dictation clean-up,
// later translation). Keys are not settings: they live in the Keychain as
// "llm.<provider>".
type LLM struct {
	// Providers the user configured, in their order.
	Providers []LLMProvider `json:"providers,omitempty"`
	// Default routes every skill without routes of its own; nil and Auto
	// = automatic: a local Ollama when it answers, else none (cloud
	// providers only by an explicit choice).
	Default []LLMRoute `json:"default,omitempty"`
	// Skills routes one skill (llm.Skill: "dictation_cleanup",
	// "translate"…) to its own providers.
	Skills map[string][]LLMRoute `json:"skills,omitempty"`
	// Manual: Default was chosen by the user (even empty: no LLM).
	Manual bool `json:"manual,omitempty"`
	// LocalModel is the catalogue id of the engine's own language model
	// (provider "local"); "" = catalog.DefaultLLM.
	LocalModel string `json:"localModel,omitempty"`
}

// LLMProvider mirrors llm.ProviderConfig.
type LLMProvider struct {
	ID       string            `json:"id"`
	Preset   string            `json:"preset,omitempty"`
	Name     string            `json:"name,omitempty"`
	BaseURL  string            `json:"baseURL,omitempty"`
	Model    string            `json:"model,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	JSONMode string            `json:"jsonMode,omitempty"`
}

// LLMRoute mirrors llm.Route.
type LLMRoute struct {
	Provider    string   `json:"provider"`
	Model       string   `json:"model,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
}

// Transcribe: the Transcribe page's choices.
type Transcribe struct {
	Mode     string `json:"mode"`               // "fast" | "accurate"
	Language string `json:"language,omitempty"` // BCP-47; "" = detect
	// FastModel and AccurateModel are catalogue ids; "" = the defaults.
	FastModel     string `json:"fastModel,omitempty"`
	AccurateModel string `json:"accurateModel,omitempty"`
	Device        string `json:"device,omitempty"` // capture device id; "" = the system's
}

// Dictation: system-wide dictation.
type Dictation struct {
	// Enabled registers the global shortcut.
	Enabled bool `json:"enabled"`
	// Shortcut is an accelerator ("CmdOrCtrl+Shift+Space").
	Shortcut string `json:"shortcut"`
	Mode     string `json:"mode"`               // "toggle" | "hold"
	Model    string `json:"model,omitempty"`    // catalogue id; "" = the default
	Language string `json:"language,omitempty"` // BCP-47; "" = detect
	// Cleanup asks the LLM to clean the text (fillers, self-corrections)
	// when one is set up; the deterministic polish always runs.
	Cleanup bool `json:"cleanup"`
	// Vocabulary: names and jargon to expect, at most 1000 characters.
	Vocabulary string `json:"vocabulary,omitempty"`
	Output     string `json:"output"` // "paste" | "copy"
	Sounds     bool   `json:"sounds"`
	Device     string `json:"device,omitempty"` // capture device id; "" = the system's
}

// Dictation modes and outputs.
const (
	DictationToggle = "toggle"
	DictationHold   = "hold"
	OutputPaste     = "paste"
	OutputCopy      = "copy"
	// DefaultShortcut is ⌘⇧Space on macOS.
	DefaultShortcut = "CmdOrCtrl+Shift+Space"
	// MaxVocabulary bounds Dictation.Vocabulary, in characters.
	MaxVocabulary = 1000
)

// Transcription modes.
const (
	TranscribeFast     = "fast"
	TranscribeAccurate = "accurate"
)

func defaultsM2(s *Settings) {
	s.Transcribe = Transcribe{Mode: TranscribeAccurate}
	s.Dictation = Dictation{Enabled: true, Shortcut: DefaultShortcut, Mode: DictationToggle, Cleanup: true, Output: OutputPaste, Sounds: true}
}

func normalizeM2(s *Settings) {
	if s.Transcribe.Mode != TranscribeFast {
		s.Transcribe.Mode = TranscribeAccurate
	}
	d := &s.Dictation
	if d.Mode != DictationHold {
		d.Mode = DictationToggle
	}
	if d.Output != OutputCopy {
		d.Output = OutputPaste
	}
	d.Shortcut = strings.TrimSpace(d.Shortcut)
	d.Vocabulary = strings.TrimSpace(d.Vocabulary)
	if utf8.RuneCountInString(d.Vocabulary) > MaxVocabulary {
		d.Vocabulary = string([]rune(d.Vocabulary)[:MaxVocabulary])
	}
}

func cloneM2(s *Settings) {
	l := &s.LLM
	l.Providers = slices.Clone(l.Providers)
	for i := range l.Providers {
		l.Providers[i].Headers = maps.Clone(l.Providers[i].Headers)
	}
	l.Default = slices.Clone(l.Default)
	if l.Skills != nil {
		m := make(map[string][]LLMRoute, len(l.Skills))
		for k, v := range l.Skills {
			m[k] = slices.Clone(v)
		}
		l.Skills = m
	}
}
