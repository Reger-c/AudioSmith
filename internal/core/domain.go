package core

import (
	"encoding/json"
	"time"
)

// VoiceKind says how a voice profile speaks.
type VoiceKind string

const (
	VoiceBuiltin  VoiceKind = "builtin"  // a model's named speaker (Kokoro af_heart, Qwen3 Vivian)
	VoiceCloned   VoiceKind = "cloned"   // from reference audio
	VoiceDesigned VoiceKind = "designed" // from a text description / attributes
)

// Voice is a saved voice profile (M1): what to pass to a model to get the
// same voice again. Blobs (reference audio, cached prompt, portrait) are
// files in the store's voice directory, referenced by name.
type Voice struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      VoiceKind `json:"kind"`
	Model     string    `json:"model"`              // repo the voice is for; "" = any that supports Kind
	Speaker   string    `json:"speaker,omitempty"`  // builtin speaker id or style prompt
	RefAudio  string    `json:"refAudio,omitempty"` // file name in the voice directory
	RefText   string    `json:"refText,omitempty"`
	Design    string    `json:"design,omitempty"` // free-text description (designed voices)
	Language  string    `json:"language,omitempty"`
	Seed      int64     `json:"seed,omitempty"`
	Locked    bool      `json:"locked,omitempty"`   // identity locked to LockTake + Seed
	LockTake  string    `json:"lockTake,omitempty"` // take id the lock reproduces
	Consent   bool      `json:"consent,omitempty"`  // verified consent recording on file
	Portrait  string    `json:"portrait,omitempty"` // file name, 256×256 PNG
	Tags      []string  `json:"tags,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ProjectKind is the workspace a project opens in.
type ProjectKind string

const (
	ProjectStory      ProjectKind = "story"
	ProjectAudiobook  ProjectKind = "audiobook"
	ProjectDub        ProjectKind = "dub"
	ProjectTranscript ProjectKind = "transcript"
	ProjectWorkflow   ProjectKind = "workflow"
)

// Project is a saved multi-step document (M3+). Its body is owned by the
// module of its Kind and stored as versioned JSON; segments, renders and
// exports hang off it in their own tables.
type Project struct {
	ID        string          `json:"id"`
	Kind      ProjectKind     `json:"kind"`
	Title     string          `json:"title"`
	Body      json.RawMessage `json:"body,omitempty"` // module-defined, includes its own "version"
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

// LicenceAcceptance records that the user accepted a model licence that
// requires it (catalog.Licence.RequiresAcceptance), e.g. CC-BY-NC weights.
// Revoking deletes it. Checked by LicenceService for the UI and the API.
type LicenceAcceptance struct {
	Licence    string    `json:"licence"` // catalog.Licence.ID, e.g. "cc-by-nc-4.0"
	Model      string    `json:"model"`   // repo the user accepted it for; "" = every model under it
	AcceptedAt time.Time `json:"acceptedAt"`
}

// Severity grades notifications and log lines.
type Severity string

const (
	SeverityDebug   Severity = "debug"
	SeverityInfo    Severity = "info"
	SeveritySuccess Severity = "success"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// Notification is a message for the notification centre (and a toast, or
// a system notification when the app is in the background).
type Notification struct {
	ID       string    `json:"id"`
	Severity Severity  `json:"severity"`
	Title    string    `json:"title"`
	Body     string    `json:"body,omitempty"`
	Link     string    `json:"link,omitempty"` // router path / audiosmith:// deep link, e.g. "/settings/models"
	JobID    string    `json:"jobId,omitempty"`
	Time     time.Time `json:"time"`
	Read     bool      `json:"read,omitempty"`
}

// LogEntry is one line of the logs panel: from the app or from an engine
// worker (its "log" notifications and stderr).
type LogEntry struct {
	Time    time.Time `json:"time"`
	Level   Severity  `json:"level"`
	Source  string    `json:"source"` // "app", "engine:gpu", "engine:listen", "engine:media", "hub"…
	Message string    `json:"message"`
	JobID   string    `json:"jobId,omitempty"`
}
