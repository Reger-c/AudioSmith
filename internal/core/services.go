package core

import (
	"context"
	"fmt"
)

// The service layer: what the UI and the Local API (M5) may do with the
// domain. Thread A implements these in internal/services over a
// store.Store and a jobs.Scheduler; until then the UI still drives the
// history.json store and the v1 engine itself (see docs/architecture.md).
//
// Every method is goroutine-safe, may block on I/O, and publishes the
// matching event on the Bus once its change is durable.

// TakeService is the take library and TTS generation.
type TakeService interface {
	// List returns takes matching q, newest first.
	List(ctx context.Context, q TakeQuery) ([]Take, error)
	// Get returns one take, or an error wrapping ErrNotFound.
	Get(ctx context.Context, id string) (Take, error)
	// Generate checks the request (text, model downloaded, licence
	// accepted), submits a "tts.synthesize" job and returns its id at
	// once. The take is saved, and TakeCreated published, when the job
	// succeeds; progress and failure arrive as jobs.Updated events.
	Generate(ctx context.Context, req TakeRequest) (jobID string, err error)
	// Delete removes the take and its audio.
	Delete(ctx context.Context, id string) error
	// SetStarred stars or unstars a take.
	SetStarred(ctx context.Context, id string, starred bool) error
	// AudioPath is the absolute path of the take's audio file. Pure: no I/O.
	AudioPath(t Take) string
	// Export copies the take's audio to dst (streamed, not read into memory).
	Export(ctx context.Context, id, dst string) error
}

// TakeRequest asks for a new take.
type TakeRequest struct {
	// Recipe holds the text, model, voice and parameters; its ID, File,
	// CreatedAt and result fields are ignored and filled in by the service.
	// Zero parameters are not sent, so the model's defaults apply.
	Recipe Take
	// Stream asks for audio chunks as they are generated (M1 streaming
	// preview): the job's events then carry tts.chunk payloads.
	Stream bool
	// Background queues behind interactive work (batch, longform). The
	// Studio and API requests leave it false.
	Background bool
}

// VoiceService is the voice library (M1).
type VoiceService interface {
	List(ctx context.Context, q VoiceQuery) ([]Voice, error)
	Get(ctx context.Context, id string) (Voice, error)
	// Save creates (empty ID) or updates a profile and returns it as stored.
	Save(ctx context.Context, v Voice) (Voice, error)
	Delete(ctx context.Context, id string) error
	// Import reads a .asvoice (or VoiceStudio .ovsvoice) package.
	Import(ctx context.Context, path string) (Voice, error)
	// Export writes a .asvoice package.
	Export(ctx context.Context, id, dst string) error
}

// VoiceQuery selects voices. The zero value is every voice, by name.
type VoiceQuery struct {
	Search string
	Kind   VoiceKind
	Model  string
}

// ProjectService is the project library (M3+).
type ProjectService interface {
	List(ctx context.Context, kind ProjectKind, search string) ([]Project, error)
	Get(ctx context.Context, id string) (Project, error)
	Save(ctx context.Context, p Project) (Project, error)
	Delete(ctx context.Context, id string) error
}

// LicenceService is the model licence gate. The UI asks before loading or
// generating; the API and the job executors check again, so nothing runs a
// gated model without an acceptance on record.
type LicenceService interface {
	// Check returns nil when model may be used, or a *LicenceRequiredError
	// (which wraps ErrLicenceRequired) naming the licence to show.
	Check(ctx context.Context, model string) error
	Accept(ctx context.Context, a LicenceAcceptance) error
	Revoke(ctx context.Context, licence, model string) error
	List(ctx context.Context) ([]LicenceAcceptance, error)
}

// LicenceRequiredError says which licence must be accepted for a model.
type LicenceRequiredError struct {
	Model   string
	Licence string // catalog.Licence.ID
	Name    string // human name, e.g. "CC BY-NC 4.0"
	URL     string
}

func (e *LicenceRequiredError) Error() string {
	return fmt.Sprintf("%s: the %s licence must be accepted first", e.Model, e.Name)
}

func (e *LicenceRequiredError) Unwrap() error { return ErrLicenceRequired }

// NotificationService is the notification centre. Post never blocks.
type NotificationService interface {
	Post(n Notification)
	List(ctx context.Context) ([]Notification, error)
	MarkRead(ctx context.Context, id string) error
	Clear(ctx context.Context) error
}

// LogService is the in-memory ring of log lines behind the logs panel,
// also written to rotated files in mygo.PathLogs. Append never blocks.
type LogService interface {
	Append(e LogEntry)
	// Tail returns up to n newest entries from source ("" = all), oldest first.
	Tail(n int, source string) []LogEntry
}

// DraftService autosaves editor state (scripts, forms) so it survives a
// restart. Save is debounced and asynchronous; Flush writes pending drafts
// (call it on quit).
type DraftService interface {
	// Load decodes the draft stored under key into v (JSON) and reports
	// whether there was one.
	Load(ctx context.Context, key string, v any) (bool, error)
	Save(key string, v any)
	Delete(ctx context.Context, key string) error
	Flush(ctx context.Context) error
}

// Services bundles the domain services for the UI and the Local API.
// Nil fields are services not implemented yet.
type Services struct {
	Bus           *Bus
	Takes         TakeService
	Voices        VoiceService
	Projects      ProjectService
	Licences      LicenceService
	Notifications NotificationService
	Logs          LogService
	Drafts        DraftService
}
