package store

import (
	"context"
	"encoding/json"
	"time"
)

// DubRecord is a dub project as the store keeps it (M4): the document the
// dub service owns (internal/dub.Project as JSON; the store does not read
// it) and the columns lists show and filter on.
type DubRecord struct {
	ID        string
	CreatedAt time.Time
	UpdatedAt time.Time
	// Batch is the batch queue the project belongs to ("" = the Dub page's).
	Batch     string
	Title     string
	Source    string   // the source's path or URL
	Languages []string // the target languages
	Phase     string   // the service's summary of where the project is
	Duration  float64  // the source's length, seconds
	// Doc is the project document; List leaves it nil.
	Doc json.RawMessage
}

// DubQuery selects dub projects to list.
type DubQuery struct {
	// Batch lists one batch queue's projects; "" the Dub page's.
	Batch string
	// AllBatches lists every project (Batch is ignored).
	AllBatches bool
	Limit      int
	Offset     int
}

// Dubs stores dub projects. Like Transcripts, it is not part of Store:
// sqlitestore.(*Store).Dubs returns it.
type Dubs interface {
	Put(ctx context.Context, r DubRecord) error
	Get(ctx context.Context, id string) (DubRecord, error)
	// List returns matching projects, most recently changed first,
	// without their documents.
	List(ctx context.Context, q DubQuery) ([]DubRecord, error)
	Delete(ctx context.Context, id string) error
	// Dir is where the projects' files live (<dir>/<id>/). Pure.
	Dir() string
}
