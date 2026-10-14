package sqlitestore

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/store"
)

func TestCalls(t *testing.T) {
	s := openTest(t, t.TempDir())
	ctx := context.Background()
	r := s.Calls()
	now := time.UnixMilli(1_700_000_000_000)
	put := func(c store.CallRecord) {
		t.Helper()
		if c.Doc == nil {
			c.Doc = json.RawMessage(`{"id":"` + c.ID + `"}`)
		}
		if err := r.Put(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	put(store.CallRecord{ID: "a", CreatedAt: now, UpdatedAt: now, Direction: "outbound", Number: "+442079460958",
		Title: "Book a table for two on Friday", Status: "dialing"})
	put(store.CallRecord{ID: "b", CreatedAt: now.Add(time.Minute), UpdatedAt: now.Add(time.Minute), Direction: "inbound",
		Number: "+15550100111", Title: "Incoming call", Status: "live", ProviderSID: "CA2"})
	// The call is answered and ends.
	put(store.CallRecord{ID: "a", CreatedAt: now, UpdatedAt: now.Add(2 * time.Minute), Direction: "outbound", Number: "+442079460958",
		Title: "Book a table for two on Friday", Status: "ended", Outcome: "booked", Summary: "Friday 7 pm under Sam.",
		ProviderSID: "CA1", Duration: 74.5, Doc: json.RawMessage(`{"id":"a","transcript":[]}`)})
	c, err := r.Get(ctx, "a")
	if err != nil || c.Outcome != "booked" || c.Duration != 74.5 || c.ProviderSID != "CA1" || string(c.Doc) != `{"id":"a","transcript":[]}` {
		t.Fatalf("%+v %v", c, err)
	}
	if c, err := r.BySID(ctx, "CA2"); err != nil || c.ID != "b" || c.Doc == nil {
		t.Errorf("by sid %+v %v", c, err)
	}
	if _, err := r.BySID(ctx, ""); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("empty sid %v", err)
	}
	l, err := r.List(ctx, store.CallQuery{})
	if err != nil || len(l) != 2 || l[0].ID != "b" || l[0].Doc != nil {
		t.Fatalf("list %+v %v", l, err)
	}
	if l, _ := r.List(ctx, store.CallQuery{Limit: 1}); len(l) != 1 {
		t.Errorf("limit %d", len(l))
	}
	lib := s.Library()
	items, err := lib.Search(ctx, store.LibraryQuery{Category: store.LibCall})
	if err != nil || len(items) != 2 || items[0].Source != "call" || items[0].ID != "a" || items[0].Detail != "booked|ended" {
		t.Errorf("library %+v %v", items, err)
	}
	if items, _ := lib.Search(ctx, store.LibraryQuery{Search: "under Sam"}); len(items) != 1 || items[0].ID != "a" {
		t.Errorf("search summary %+v", items)
	}
	if items, _ := lib.Search(ctx, store.LibraryQuery{Search: "In"}); len(items) != 1 || items[0].ID != "b" {
		t.Errorf("short search %+v", items)
	}
	if n, _ := lib.Counts(ctx, ""); n[store.LibCall] != 2 {
		t.Errorf("counts %v", n)
	}
	if err := r.Put(ctx, store.CallRecord{ID: "x", Doc: json.RawMessage(`{`)}); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("bad doc %v", err)
	}
	if err := r.Delete(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(ctx, "a"); !errors.Is(err, core.ErrNotFound) {
		t.Error(err)
	}
}
