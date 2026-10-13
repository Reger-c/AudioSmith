package notify

import (
	"context"
	"errors"
	"testing"

	"github.com/hehehai/AudioSmith/internal/core"
)

func TestCenter(t *testing.T) {
	ctx := context.Background()
	bus := core.NewBus()
	var posted []core.Notification
	core.On(bus, func(e core.NotificationPosted) { posted = append(posted, e.Notification) })
	c := New(bus)
	c.Post(core.Notification{Title: "first"})
	c.Post(core.Notification{ID: "disk", Severity: core.SeverityWarning, Title: "Disk low"})
	c.Post(core.Notification{Title: "third"})
	if len(posted) != 3 || posted[0].ID == "" || posted[0].Time.IsZero() || posted[0].Severity != core.SeverityInfo {
		t.Fatalf("posted %+v", posted)
	}
	list, _ := c.List(ctx)
	if len(list) != 3 || list[0].Title != "third" || list[2].Title != "first" {
		t.Fatalf("list %+v", list)
	}
	if err := c.MarkRead(ctx, "disk"); err != nil {
		t.Fatal(err)
	}
	// The same ID again replaces it, unread, on top.
	c.Post(core.Notification{ID: "disk", Severity: core.SeverityWarning, Title: "Disk still low"})
	list, _ = c.List(ctx)
	if len(list) != 3 || list[0].Title != "Disk still low" || list[0].Read {
		t.Fatalf("list %+v", list)
	}
	if err := c.MarkRead(ctx, "nope"); !errors.Is(err, core.ErrNotFound) {
		t.Error(err)
	}
	c.MarkRead(ctx, "")
	list, _ = c.List(ctx)
	for _, n := range list {
		if !n.Read {
			t.Error("unread after MarkRead(\"\")", n)
		}
	}
	for range Max + 5 {
		c.Post(core.Notification{Title: "x"})
	}
	if list, _ = c.List(ctx); len(list) != Max {
		t.Errorf("kept %d", len(list))
	}
	c.Clear(ctx)
	if list, _ = c.List(ctx); len(list) != 0 {
		t.Error("not cleared")
	}
}
