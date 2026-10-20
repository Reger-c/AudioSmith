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
// same voice again. Blobs (reference audio, rendered sample, consent
// recording, cached prompts, portrait) are files in the store's voice
// directory, VoiceDir/<id>/, referenced by name.
type Voice struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Kind       VoiceKind `json:"kind"`
	Model      string    `json:"model"`              // repo the voice is for; "" = any that supports Kind
	Speaker    string    `json:"speaker,omitempty"`  // builtin speaker id
	RefAudio   string    `json:"refAudio,omitempty"` // clone reference: file name in the voice directory
	RefText    string    `json:"refText,omitempty"`  // what the reference says ("" = let the engine listen)
	RefSeconds float64   `json:"refSeconds,omitempty"`
	// RefOriginal is the reference before it was cleaned automatically
	// (denoised, its silences trimmed, evened out; thread #83): a file
	// name, kept so the cleaning can be undone. RefCheck is what the
	// reference checker found (of the reference as given).
	RefOriginal string      `json:"refOriginal,omitempty"`
	RefCheck    *RefQuality `json:"refCheck,omitempty"`
	// Style is a delivery hint ("whisper", "calm and slow") sent as the
	// instruction of engines that take one; tag engines get it reduced to
	// their vocabulary.
	Style string `json:"style,omitempty"`
	// Design is the description as written (designed voices) and
	// DesignAttrs the effective attributes (voicedesign.Attributes JSON):
	// with Seed they are the voice's voicedesign.Recipe.
	Design      string          `json:"design,omitempty"`
	DesignAttrs json.RawMessage `json:"designAttrs,omitempty"`
	Language    string          `json:"language,omitempty"`
	Seed        int64           `json:"seed,omitempty"`
	Locked      bool            `json:"locked,omitempty"`   // identity locked to LockTake + Seed
	LockTake    string          `json:"lockTake,omitempty"` // take id the lock reproduces
	Consent     VoiceConsent    `json:"consent,omitzero"`
	Portrait    string          `json:"portrait,omitempty"` // file name, 256×256 PNG
	// Sample is a rendered sample of the voice (file name): a designed
	// voice's identity render, or the locked take's audio; SampleText is
	// what it says. A locked voice is re-rendered by cloning its sample.
	Sample     string `json:"sample,omitempty"`
	SampleText string `json:"sampleText,omitempty"`
	// Prompts are cached voice prompts by model repo (tts.encodeVoice):
	// file names in the voice directory.
	Prompts map[string]string `json:"prompts,omitempty"`
	// CreatedWith is the repo of the model that made the voice (a design
	// render, the lock take), for licence notes on export.
	CreatedWith string `json:"createdWith,omitempty"`
	// Origin says where the voice came from: "gallery:<archetype id>",
	// "asvoice:<persona id>", "ovsvoice:<id>"; "" = made here.
	Origin    string    `json:"origin,omitempty"`
	Tags      []string  `json:"tags,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// RefQuality is what the reference checker (dsp/refcheck) found in a
// voice's reference: its grade ("good", "ok", "poor"), its hints' codes,
// what the automatic cleaning did ("denoised", "tidied") and how long the
// original was. TextChecked: the recognizer heard the reference say its
// RefText (thread #93: references made from a voice itself are checked
// once; one that did not say its text is made again or gets what it
// says).
type RefQuality struct {
	Grade           string   `json:"grade,omitempty"`
	Hints           []string `json:"hints,omitempty"`
	Cleaned         []string `json:"cleaned,omitempty"`
	OriginalSeconds float64  `json:"originalSeconds,omitempty"`
	TextChecked     bool     `json:"textChecked,omitempty"`
}

// RefHeard is a voice's reference as the recognizer heard it: its file
// and the text it had.
type RefHeard struct {
	Audio, Text string
}

// Verified reports whether the profile has a consent recording made here.
func (v Voice) Verified() bool { return v.Consent.State == ConsentVerified }

// ConsentState is how far a voice's ownership is attested.
type ConsentState string

const (
	ConsentNone ConsentState = ""
	// ConsentVerified: the user recorded the consent statement here.
	ConsentVerified ConsentState = "verified"
	// ConsentIncluded: an imported voice file carried a consent record; it
	// is the exporter's claim until the user records one here.
	ConsentIncluded ConsentState = "included-unverified"
)

// VoiceConsent is a voice's ownership attestation: the statement read,
// its recording (a file name in the voice directory) and when.
type VoiceConsent struct {
	State     ConsentState `json:"state,omitempty"`
	Recording string       `json:"recording,omitempty"`
	Statement string       `json:"statement,omitempty"`
	Locale    string       `json:"locale,omitempty"` // language the statement was read in
	At        time.Time    `json:"at,omitzero"`
}

// UnmarshalJSON also reads M0's "consent": true|false.
func (c *VoiceConsent) UnmarshalJSON(b []byte) error {
	switch string(b) {
	case "true":
		*c = VoiceConsent{State: ConsentVerified}
		return nil
	case "false", "null":
		*c = VoiceConsent{}
		return nil
	}
	type plain VoiceConsent
	return json.Unmarshal(b, (*plain)(c))
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
	// Seq numbers the lines a log appended, from 1: a line's identity in
	// the logs view (its selection) while lines come and go.
	Seq uint64 `json:"-"`
}
