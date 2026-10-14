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

func TestDubs(t *testing.T) {
	s := openTest(t, t.TempDir())
	ctx := context.Background()
	r := s.Dubs()
	now := time.UnixMilli(1_700_000_000_000)
	put := func(id, batch string, at time.Time) {
		t.Helper()
		err := r.Put(ctx, store.DubRecord{ID: id, CreatedAt: now, UpdatedAt: at, Batch: batch, Title: "T " + id, Source: "/v/" + id + ".mp4",
			Languages: []string{"es", "fr"}, Phase: "editing", Duration: 61.5, Doc: json.RawMessage(`{"id":"` + id + `"}`)})
		if err != nil {
			t.Fatal(err)
		}
	}
	put("a", "", now)
	put("b", "", now.Add(time.Minute))
	put("c", "queue", now.Add(2*time.Minute))
	l, err := r.List(ctx, store.DubQuery{})
	if err != nil || len(l) != 2 || l[0].ID != "b" || l[0].Doc != nil || l[0].Duration != 61.5 || len(l[0].Languages) != 2 {
		t.Fatalf("%+v %v", l, err)
	}
	if l, _ := r.List(ctx, store.DubQuery{Batch: "queue"}); len(l) != 1 {
		t.Errorf("batch %+v", l)
	}
	if l, _ := r.List(ctx, store.DubQuery{AllBatches: true, Limit: 2}); len(l) != 2 || l[0].ID != "c" {
		t.Errorf("all %+v", l)
	}
	lib := s.Library()
	items, err := lib.Search(ctx, store.LibraryQuery{Category: store.LibDub})
	if err != nil || len(items) != 2 || items[0].Source != "dub" || items[0].Category != store.LibDub || items[0].Detail != "editing" {
		t.Errorf("library %+v %v", items, err)
	}
	if items, _ := lib.Search(ctx, store.LibraryQuery{Search: "T b"}); len(items) != 1 || items[0].ID != "b" {
		t.Errorf("search %+v", items)
	}
	if n, _ := lib.Counts(ctx, ""); n[store.LibDub] != 2 {
		t.Errorf("counts %v", n)
	}
	d, err := r.Get(ctx, "a")
	if err != nil || string(d.Doc) != `{"id":"a"}` || d.Title != "T a" {
		t.Errorf("%+v %v", d, err)
	}
	put("a", "", now.Add(3*time.Minute)) // upsert
	if l, _ := r.List(ctx, store.DubQuery{}); l[0].ID != "a" {
		t.Error("upsert order")
	}
	if err := r.Put(ctx, store.DubRecord{ID: "x", Doc: json.RawMessage(`{`)}); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("bad doc %v", err)
	}
	if err := r.Delete(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(ctx, "a"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("deleted %v", err)
	}
	if err := r.Delete(ctx, "a"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("twice %v", err)
	}
}
