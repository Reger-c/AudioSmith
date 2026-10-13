package store

import (
	"context"

	"github.com/hehehai/AudioSmith/internal/core"
)

// Transcripts stores transcripts (M2) and the audio kept with them. It is
// not part of Store, so the milestones that add repositories don't touch
// the same interface: sqlitestore.(*Store).Transcripts returns it.
type Transcripts interface {
	// Put inserts or replaces a transcript (by ID). Its audio file, if
	// any, must already be in AudioDir.
	Put(ctx context.Context, t core.Transcript) error
	// Get returns the whole transcript, word timings included.
	Get(ctx context.Context, id string) (core.Transcript, error)
	// List returns matching transcripts, newest first, without word
	// timings (Get has them).
	List(ctx context.Context, q core.TranscriptQuery) ([]core.Transcript, error)
	// Delete removes the transcript and its audio file.
	Delete(ctx context.Context, id string) error
	// AudioDir is where transcript audio lives; AudioPath joins it with
	// t.Audio ("" when there is none). Both are pure.
	AudioDir() string
	AudioPath(t core.Transcript) string
}
