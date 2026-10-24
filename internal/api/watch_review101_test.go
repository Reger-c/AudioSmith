package api

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/jobs"
)

// #101 L5: a watch whose reader stopped reading is cut off past its
// bounds instead of growing without end; one that keeps up is not.
func TestWatchBounded(t *testing.T) {
	saved := watchMaxEvents
	watchMaxEvents = 100
	t.Cleanup(func() { watchMaxEvents = saved })
	bus := core.NewBus()
	w := newWatcher(bus)
	defer w.close()
	slow, live := w.owner("take:1"), w.owner("take:1")
	defer slow.stop()
	defer live.stop()
	ctx := context.Background()
	for i := range 150 {
		bus.Publish(jobs.Updated{Job: jobs.Job{ID: "j", Spec: jobs.Spec{Owner: "take:1"}}, Chunk: json.RawMessage(`{}`)})
		if _, ok, err := live.next(ctx, 0); !ok || err != nil {
			t.Fatalf("event %d to the reader that keeps up: %v", i, err)
		}
	}
	slow.mu.Lock()
	n := len(slow.q)
	slow.mu.Unlock()
	if n > 100 {
		t.Fatalf("%d events queued", n)
	}
	if _, _, err := slow.next(ctx, 0); !errors.Is(err, errWatchLagged) {
		t.Fatalf("the slow reader: %v", err)
	}
}
