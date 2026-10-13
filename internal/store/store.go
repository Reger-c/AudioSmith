// Package store is AudioSmith's persistence contract: repositories for
// each kind of record, behind which the database lives. The app uses
// internal/store/sqlitestore (SQLite through the MyGo plugin
// github.com/egoist/mygo/plugins/sqlite; schema in its migrations/*.sql),
// which imports the MVP's history.json on first run with
// internal/store/historyjson (an implementation of Takes alone, kept for
// that import and for tests).
//
// Rules:
//   - Repositories are goroutine-safe and may block on I/O: never call
//     them from a MyGo build pass. The service layer (internal/services)
//     is their only caller once it exists; the UI goes through services.
//   - Values in, values out: a repository copies what it is given and
//     returns copies.
//   - Not found is an error wrapping core.ErrNotFound.
//   - Blobs (audio, portraits, voice prompts) are files under the store's
//     directories, referenced by name; the store deletes a record's files
//     with the record.
//
// Imports: core, jobs.
package store

import (
	"context"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/jobs"
)

// Store is the whole database. Methods returning a repository never fail;
// a store that lacks one returns a repository whose methods fail with
// core.ErrUnsupported.
type Store interface {
	Takes() Takes
	Voices() Voices
	Projects() Projects
	Licences() Licences
	// Documents holds small JSON documents by key: "settings:app" (the
	// settings.Settings document) and "draft:<page>" (core.DraftService).
	Documents() Documents
	Favorites() Favorites
	Jobs() jobs.Store
	Close() error
}

// Takes stores takes and their audio files.
type Takes interface {
	// Put inserts or replaces a take (by ID). Its audio file must already
	// be in AudioDir.
	Put(ctx context.Context, t core.Take) error
	Get(ctx context.Context, id string) (core.Take, error)
	// List returns matching takes, newest first.
	List(ctx context.Context, q core.TakeQuery) ([]core.Take, error)
	// Delete removes the take and its audio file.
	Delete(ctx context.Context, id string) error
	// AudioDir is the directory take audio lives in (the engine writes
	// there). AudioPath joins it with t.File. Both are pure.
	AudioDir() string
	AudioPath(t core.Take) string
}

// Voices stores voice profiles; their blobs live in VoiceDir()/<id>/.
type Voices interface {
	Put(ctx context.Context, v core.Voice) error
	Get(ctx context.Context, id string) (core.Voice, error)
	List(ctx context.Context, q core.VoiceQuery) ([]core.Voice, error)
	Delete(ctx context.Context, id string) error
	// Usage counts the takes and project segments that use the voice.
	Usage(ctx context.Context, id string) (core.VoiceUsage, error)
	VoiceDir() string
}

// Favorites stores sets of favorite keys by scope ("gallery": archetype
// ids, later "community:<repo>:<id>").
type Favorites interface {
	List(ctx context.Context, scope string) ([]string, error)
	// Set adds (on) or removes a key; both are idempotent.
	Set(ctx context.Context, scope, key string, on bool) error
}

// Projects stores projects; segments, renders and exports are rows of
// their own tables keyed by project id (see sqlitestore/migrations) with repositories
// added by the milestone that needs them.
type Projects interface {
	Put(ctx context.Context, p core.Project) error
	Get(ctx context.Context, id string) (core.Project, error)
	List(ctx context.Context, kind core.ProjectKind, search string) ([]core.Project, error)
	Delete(ctx context.Context, id string) error
}

// Licences stores licence acceptances.
type Licences interface {
	Put(ctx context.Context, a core.LicenceAcceptance) error
	Delete(ctx context.Context, licence, model string) error
	List(ctx context.Context) ([]core.LicenceAcceptance, error)
}

// Documents is a key → JSON store for small documents.
type Documents interface {
	// Get returns the document and whether it exists.
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Put(ctx context.Context, key string, doc []byte) error
	Delete(ctx context.Context, key string) error
}
