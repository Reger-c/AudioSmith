// Package persona reads and writes .asvoice files: a portable voice (a
// cloned or designed voice profile) as one ZIP that carries its identity,
// recipe and engine parameters, licence, consent attestation, a preview
// clip, and optionally the reference clip and a portrait.
//
// Layout (all members at the root):
//
//	manifest.json   required, written first; Manifest
//	preview.wav     required; a short rendered sample (WAV)
//	reference.<ext> optional; the clone reference (wav, flac, mp3, m4a, ogg)
//	portrait.<ext>  optional; png, jpg or webp
//	consent.wav     optional; the creator reading the consent statement
//
// The manifest lists every member with its size and SHA-256; readers
// verify both. Unknown members and unknown manifest fields are ignored so
// minor versions can add things; a newer SchemaVersion is rejected.
//
// ReadOVS imports VoiceStudio's .ovsvoice bundles as far as their public
// documentation describes them (see ovs.go).
package persona

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	vd "github.com/hehehai/AudioSmith/internal/voicedesign"
)

const (
	Ext           = ".asvoice"
	MediaType     = "application/vnd.audiosmith.voice+zip"
	Format        = "audiosmith.voice"
	SchemaVersion = 1

	ManifestName = "manifest.json"
	PreviewName  = "preview.wav"
	ConsentName  = "consent.wav"

	// DefaultLicence is used when the creator picks none: personal use,
	// no redistribution.
	DefaultLicence = "LicenseRef-AudioSmith-Personal"

	MaxName = 200 // runes
	MaxTags = 32
	MaxTag  = 64 // runes per tag
)

// Errors returned (wrapped) by Read and Write.
var (
	ErrNotPersona  = errors.New("persona: not a voice file")
	ErrUnsupported = errors.New("persona: made by a newer version")
	ErrTooLarge    = errors.New("persona: too large")
	ErrCorrupt     = errors.New("persona: damaged file")
	ErrInvalid     = errors.New("persona: invalid")
)

// Kind is how the voice is made.
type Kind string

const (
	KindClone  Kind = "clone"  // from reference audio
	KindDesign Kind = "design" // from a recipe
)

// Manifest is manifest.json.
type Manifest struct {
	Format        string     `json:"format"`        // Format
	SchemaVersion int        `json:"schemaVersion"` // SchemaVersion
	ID            string     `json:"id"`            // stable persona id (kept across re-exports)
	Name          string     `json:"name"`
	Description   string     `json:"description,omitempty"`
	Kind          Kind       `json:"kind"`
	Language      string     `json:"language,omitempty"` // BCP-47 of the voice's main language
	Tags          []string   `json:"tags,omitempty"`
	Recipe        *vd.Recipe `json:"recipe,omitempty"` // design voices
	Engine        Engine     `json:"engine"`
	Licence       Licence    `json:"licence"`
	Consent       *Consent   `json:"consent,omitempty"`
	CreatedBy     CreatedBy  `json:"createdBy"`
	CreatedAt     time.Time  `json:"createdAt"`
	Files         Files      `json:"files"`
}

// Engine records how the voice was rendered, so it can be reproduced.
type Engine struct {
	Model  string `json:"model,omitempty"`  // catalogue id ("omnivoice-bf16")
	Repo   string `json:"repo,omitempty"`   // Hugging Face repo
	Family string `json:"family,omitempty"` // engine family
	Voice  string `json:"voice,omitempty"`  // the voice/instruct field as sent (lowered recipe, speaker…)
	Seed   *int64 `json:"seed,omitempty"`
	Locked bool   `json:"locked,omitempty"` // identity locked to the preview/reference + seed
	// Params are other generation knobs (steps, temperature…), as the
	// engine protocol names them.
	Params map[string]any `json:"params,omitempty"`
}

// Licence is the creator's licence for the voice (metadata; not enforced).
type Licence struct {
	SPDX        string `json:"spdx"` // SPDX id or expression, or LicenseRef-…
	Attribution string `json:"attribution,omitempty"`
	URL         string `json:"url,omitempty"`
}

// Consent is the creator's attestation that they may share this voice.
type Consent struct {
	// Attested: the creator confirmed they own the voice or have the
	// speaker's permission.
	Attested   bool      `json:"attested"`
	Method     string    `json:"method,omitempty"`    // "recording" (consent.wav) or "checkbox"
	Statement  string    `json:"statement,omitempty"` // the text read or agreed to
	AttestedBy string    `json:"attestedBy,omitempty"`
	AttestedAt time.Time `json:"attestedAt,omitempty"`
}

// CreatedBy names the software (and optionally the person) that made the file.
type CreatedBy struct {
	App     string `json:"app"`
	Version string `json:"version,omitempty"`
	Author  string `json:"author,omitempty"`
}

// FileRef describes one member.
type FileRef struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Text   string `json:"text,omitempty"` // transcript of the audio, when known
}

// Files lists the members.
type Files struct {
	Preview   FileRef  `json:"preview"`
	Reference *FileRef `json:"reference,omitempty"`
	Portrait  *FileRef `json:"portrait,omitempty"`
	Consent   *FileRef `json:"consent,omitempty"`
}

// Bundle is a voice file in memory.
type Bundle struct {
	Manifest     Manifest
	Preview      []byte // WAV
	Reference    []byte // optional
	ReferenceExt string // "wav", "flac", "mp3", "m4a", "ogg"
	Portrait     []byte // optional
	PortraitExt  string // "png", "jpg", "webp"
	ConsentAudio []byte // optional, WAV
}

// ConsentComplete reports whether the bundle carries a full consent
// record: an attestation with a statement and the recording. Even then it
// is the exporter's claim: the app shows imported voices as unverified
// until the user re-attests locally.
func (b *Bundle) ConsentComplete() bool {
	c := b.Manifest.Consent
	return c != nil && c.Attested && strings.TrimSpace(c.Statement) != "" && len(b.ConsentAudio) > 0
}

// Limits bound what Read accepts and Write produces.
type Limits struct {
	MaxFile      int64 // the whole ZIP
	MaxManifest  int64
	MaxPreview   int64
	MaxReference int64
	MaxPortrait  int64
	MaxConsent   int64
	MaxEntries   int
}

// DefaultLimits: 100 MB per file, as VoiceStudio's importer.
var DefaultLimits = Limits{
	MaxFile:      100 << 20,
	MaxManifest:  256 << 10,
	MaxPreview:   10 << 20,
	MaxReference: 60 << 20,
	MaxPortrait:  5 << 20,
	MaxConsent:   10 << 20,
	MaxEntries:   32,
}

var (
	referenceExts = []string{"wav", "flac", "mp3", "m4a", "ogg"}
	portraitExts  = []string{"png", "jpg", "webp"}
	spdxTerm      = regexp.MustCompile(`^(LicenseRef-[A-Za-z0-9.\-]+|[A-Za-z0-9][A-Za-z0-9.\-]*\+?)$`)
	idPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._\-]{0,127}$`)
)

// Licences are the choices offered when exporting (SPDX ids).
var Licences = []string{DefaultLicence, "CC0-1.0", "CC-BY-4.0", "CC-BY-SA-4.0", "CC-BY-NC-4.0", "CC-BY-NC-SA-4.0", "CC-BY-ND-4.0"}

// ValidSPDX checks the syntax of an SPDX licence id or simple expression
// ("CC-BY-4.0", "MIT OR Apache-2.0", "LicenseRef-Mine").
func ValidSPDX(s string) bool {
	s = strings.TrimSpace(strings.Trim(strings.TrimSpace(s), "()"))
	if s == "" || len(s) > 200 {
		return false
	}
	for _, f := range strings.Fields(s) {
		switch f {
		case "AND", "OR", "WITH":
			continue
		}
		if !spdxTerm.MatchString(strings.Trim(f, "()")) {
			return false
		}
	}
	return true
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, args...)...)
}

// Validate checks the manifest on its own (the file list is checked
// against the members by Read and filled in by Write).
func (m *Manifest) Validate() error {
	if m.Format != Format {
		return fmt.Errorf("%w: format %q", ErrNotPersona, m.Format)
	}
	if m.SchemaVersion < 1 {
		return invalid("schema version %d", m.SchemaVersion)
	}
	if m.SchemaVersion > SchemaVersion {
		return fmt.Errorf("%w (schema %d, this app reads up to %d)", ErrUnsupported, m.SchemaVersion, SchemaVersion)
	}
	if !idPattern.MatchString(m.ID) {
		return invalid("id %q", m.ID)
	}
	if n := utf8.RuneCountInString(strings.TrimSpace(m.Name)); n == 0 || n > MaxName {
		return invalid("name must have 1–%d characters", MaxName)
	}
	switch m.Kind {
	case KindDesign:
		if m.Recipe == nil {
			return invalid("a designed voice needs its recipe")
		}
		if err := m.Recipe.Validate(); err != nil {
			return invalid("recipe: %v", err)
		}
	case KindClone:
	default:
		return invalid("kind %q", m.Kind)
	}
	if len(m.Tags) > MaxTags {
		return invalid("more than %d tags", MaxTags)
	}
	for _, t := range m.Tags {
		if n := utf8.RuneCountInString(t); n == 0 || n > MaxTag {
			return invalid("tag %q", t)
		}
	}
	if !ValidSPDX(m.Licence.SPDX) {
		return invalid("licence %q is not an SPDX id", m.Licence.SPDX)
	}
	if m.Engine.Seed != nil && (*m.Engine.Seed < 0 || *m.Engine.Seed > vd.MaxSeed) {
		return invalid("seed %d", *m.Engine.Seed)
	}
	if c := m.Consent; c != nil && c.Attested && strings.TrimSpace(c.Statement) == "" && c.Method == "recording" {
		return invalid("a recorded consent needs its statement")
	}
	if m.CreatedBy.App == "" {
		return invalid("createdBy.app is empty")
	}
	return nil
}

// Sniffing: members must be what their names say.

func isWAV(b []byte) bool {
	return len(b) >= 12 && (string(b[:4]) == "RIFF" || string(b[:4]) == "RF64") && string(b[8:12]) == "WAVE"
}

func sniffAudio(b []byte, ext string) bool {
	switch ext {
	case "wav":
		return isWAV(b)
	case "flac":
		return len(b) >= 4 && string(b[:4]) == "fLaC"
	case "mp3":
		return len(b) >= 3 && (string(b[:3]) == "ID3" || (b[0] == 0xFF && b[1]&0xE0 == 0xE0))
	case "m4a":
		return len(b) >= 8 && string(b[4:8]) == "ftyp"
	case "ogg":
		return len(b) >= 4 && string(b[:4]) == "OggS"
	}
	return false
}

func sniffImage(b []byte, ext string) bool {
	switch ext {
	case "png":
		return len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n"
	case "jpg":
		return len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF
	case "webp":
		return len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP"
	}
	return false
}
