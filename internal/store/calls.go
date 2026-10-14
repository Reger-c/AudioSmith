package store

import (
	"context"
	"encoding/json"
	"time"
)

// LibCall is the Projects page's category of phone calls (M6b).
const LibCall = "call"

// CallRecord is a phone call as the store keeps it (M6b): the document
// the calls service owns (services.Call as JSON: brief, voice,
// disclosure, transcript, timeline, latency; the store does not read it)
// and the columns lists show, filter and search on.
type CallRecord struct {
	ID        string
	CreatedAt time.Time
	UpdatedAt time.Time
	Direction string // "outbound" | "inbound"
	Number    string // the other party's number (E.164)
	Title     string // the brief's first line, for lists
	Status    string // the service's phase: dialing, ringing, live, ended, failed
	Outcome   string // booked, done, not_done, needs_you, failed, or ""
	Summary   string
	// ProviderSID is Twilio's Call SID.
	ProviderSID string
	Duration    float64 // seconds talked
	// Doc is the call document; List leaves it nil.
	Doc json.RawMessage
}

// CallQuery selects calls to list.
type CallQuery struct {
	Limit int // 0 = all
}

// Calls stores phone calls. Like Dubs, it is not part of Store:
// sqlitestore.(*Store).Calls returns it.
type Calls interface {
	Put(ctx context.Context, r CallRecord) error
	Get(ctx context.Context, id string) (CallRecord, error)
	// BySID finds a call by Twilio's Call SID (core.ErrNotFound).
	BySID(ctx context.Context, sid string) (CallRecord, error)
	// List returns calls, most recent first, without documents.
	List(ctx context.Context, q CallQuery) ([]CallRecord, error)
	Delete(ctx context.Context, id string) error
	// Dir is where the calls' files (recordings) live. Pure.
	Dir() string
}
