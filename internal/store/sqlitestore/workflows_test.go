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

func TestWorkflows(t *testing.T) {
	s := openTest(t, t.TempDir())
	ctx := context.Background()
	r := s.Workflows()
	now := time.UnixMilli(1_700_000_000_000)
	put := func(id, name string, at time.Time) {
		t.Helper()
		err := r.PutDoc(ctx, store.WorkflowRecord{ID: id, CreatedAt: now, UpdatedAt: at, Name: name, Summary: "Start → Speak → Export",
			Doc: json.RawMessage(`{"id":"` + id + `"}`)})
		if err != nil {
			t.Fatal(err)
		}
	}
	put("a", "Batch narration", now)
	put("b", "Subtitles", now.Add(time.Minute))
	if err := r.PutRun(ctx, store.WorkflowRecord{ID: "a", State: "done", Items: 3, Done: 2, Run: json.RawMessage(`{"id":"r1"}`)}); err != nil {
		t.Fatal(err)
	}
	// Saving the document keeps the run.
	put("a", "Narration", now.Add(2*time.Minute))
	w, err := r.Get(ctx, "a")
	if err != nil || string(w.Run) != `{"id":"r1"}` || w.State != "done" || w.Done != 2 || w.Name != "Narration" || string(w.Doc) != `{"id":"a"}` {
		t.Fatalf("%+v %v", w, err)
	}
	if w, _ := r.Get(ctx, "b"); w.Run != nil {
		t.Errorf("no run yet: %s", w.Run)
	}
	l, err := r.List(ctx)
	if err != nil || len(l) != 2 || l[0].ID != "a" || l[0].Doc != nil || l[0].Items != 3 {
		t.Fatalf("%+v %v", l, err)
	}
	lib := s.Library()
	items, err := lib.Search(ctx, store.LibraryQuery{Category: store.LibWorkflow})
	if err != nil || len(items) != 2 || items[0].Source != "workflow" || items[0].Category != store.LibWorkflow || items[0].Detail != "done" {
		t.Errorf("library %+v %v", items, err)
	}
	if items, _ := lib.Search(ctx, store.LibraryQuery{Search: "Subtit"}); len(items) != 1 || items[0].ID != "b" {
		t.Errorf("search %+v", items)
	}
	if items, _ := lib.Search(ctx, store.LibraryQuery{Search: "Su"}); len(items) != 1 {
		t.Errorf("short search %+v", items)
	}
	if n, _ := lib.Counts(ctx, ""); n[store.LibWorkflow] != 2 {
		t.Errorf("counts %v", n)
	}
	if err := r.PutRun(ctx, store.WorkflowRecord{ID: "zz"}); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("run of nothing: %v", err)
	}
	if err := r.PutDoc(ctx, store.WorkflowRecord{ID: "x", Doc: json.RawMessage(`{`)}); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("bad doc %v", err)
	}
	if err := r.Delete(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(ctx, "a"); !errors.Is(err, core.ErrNotFound) {
		t.Error(err)
	}
	if err := r.Delete(ctx, "a"); !errors.Is(err, core.ErrNotFound) {
		t.Error(err)
	}
}
