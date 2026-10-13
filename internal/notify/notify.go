// Package notify is the notification centre: core.NotificationService in
// memory. Posting stores the notification (newest first, at most Max) and
// publishes core.NotificationPosted; the shell turns that into a toast and
// a badge on the bell of the status bar.
//
// Thread C owns this package. Notifications do not survive a restart yet
// (VoiceStudio keeps info and warnings until dismissed): that arrives with
// the SQLite store.
//
// Imports: core.
package notify

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
)

// Max is how many notifications the centre keeps; older ones drop off.
const Max = 200

// Center implements core.NotificationService.
type Center struct {
	bus *core.Bus
	now func() time.Time

	mu    sync.Mutex
	items []core.Notification // newest first
}

var _ core.NotificationService = (*Center)(nil)

// New returns an empty centre publishing on bus (which may be nil).
func New(bus *core.Bus) *Center { return &Center{bus: bus, now: time.Now} }

// Post stores n, filling its ID and Time when empty, and publishes
// NotificationPosted. A notification with the ID of one stored replaces it
// (as unread), so a repeated problem shows once. It never blocks.
func (c *Center) Post(n core.Notification) {
	if n.ID == "" {
		n.ID = core.NewID(c.now())
	}
	if n.Time.IsZero() {
		n.Time = c.now()
	}
	if n.Severity == "" {
		n.Severity = core.SeverityInfo
	}
	n.Read = false
	c.mu.Lock()
	c.items = slices.DeleteFunc(c.items, func(x core.Notification) bool { return x.ID == n.ID })
	c.items = slices.Insert(c.items, 0, n)
	if len(c.items) > Max {
		c.items = c.items[:Max]
	}
	c.mu.Unlock()
	c.bus.Publish(core.NotificationPosted{Notification: n})
}

// List returns the notifications, newest first.
func (c *Center) List(context.Context) ([]core.Notification, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.items), nil
}

// MarkRead marks one notification read; "" marks them all.
func (c *Center) MarkRead(_ context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	found := id == ""
	for i := range c.items {
		if id == "" || c.items[i].ID == id {
			c.items[i].Read = true
			found = true
		}
	}
	if !found {
		return fmt.Errorf("notification %s: %w", id, core.ErrNotFound)
	}
	return nil
}

// Clear removes every notification.
func (c *Center) Clear(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = nil
	return nil
}
