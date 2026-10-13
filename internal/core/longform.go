package core

import (
	"encoding/json"
	"time"
)

// RenderState is where a longform render (Stories, Audiobook) is.
type RenderState string

const (
	RenderRunning RenderState = "running" // chapters rendering or the book assembling
	RenderDone    RenderState = "done"    // every chapter and the book are written
	RenderFailed  RenderState = "failed"  // a chapter or the assembly failed: Retry re-renders what is missing
	RenderStopped RenderState = "stopped" // stopped by the user: finished chapters are kept
)

// Render is one render of a longform project (M3): the manifest it was
// started with and what it produced, as JSON owned by the longform
// service (services.RenderBody), plus the columns the library lists and
// filters on.
type Render struct {
	ID        string      `json:"id"`
	ProjectID string      `json:"projectId,omitempty"` // "" for a draft never saved as a project
	Kind      ProjectKind `json:"kind"`                // ProjectStory or ProjectAudiobook
	Title     string      `json:"title"`
	State     RenderState `json:"state"`
	// Audition marks a render of one chapter or line, for listening: it
	// writes no book and is not listed in the library.
	Audition     bool            `json:"audition,omitempty"`
	AudioSeconds float64         `json:"audioSeconds,omitempty"`
	Body         json.RawMessage `json:"body,omitempty"`
	CreatedAt    time.Time       `json:"createdAt"`
	UpdatedAt    time.Time       `json:"updatedAt"`
}

// Export records that a file was written out of the library: what, where
// and when, for the Projects page's Exports ("what was exported where").
// Deleting the record keeps the file.
type Export struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	// Kind is what was exported: "take", "book", "chapter", "stem",
	// "cues", "voice", "transcript"…
	Kind      string `json:"kind"`
	SourceID  string `json:"sourceId,omitempty"` // the take, render, voice… exported
	ProjectID string `json:"projectId,omitempty"`
	Title     string `json:"title"`
	Path      string `json:"path"`             // the file written (absolute)
	Format    string `json:"format,omitempty"` // "wav", "m4b", "txt"…
	Bytes     int64  `json:"bytes,omitempty"`
}

// RenderChanged is published when a longform render starts, changes state
// or makes progress (chapters done, the current chapter's fraction).
// Topic "render.changed".
type RenderChanged struct {
	Render  Render
	Deleted bool
}

func (RenderChanged) Topic() string { return "render.changed" }

// ExportChanged is published when an export was recorded or its record
// deleted. Topic "export.changed".
type ExportChanged struct {
	Export  Export
	Deleted bool
}

func (ExportChanged) Topic() string { return "export.changed" }
