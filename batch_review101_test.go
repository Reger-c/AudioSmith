package main

import (
	"slices"
	"testing"

	"github.com/hehehai/AudioSmith/internal/services"
)

// A batch dub that finishes says so, whether its run ends in the save
// that makes its tracks ready or in the one after (#101 L12: the run is
// recorded before its jobs, and the stage's end ends it).
func TestBatchFinishedNotice(t *testing.T) {
	d := newDubTest(t, 6)
	a := d.a
	a.navigate("/batch")
	a.batchDropped([]string{d.src})
	a.batchSetSettings(func(s *services.BatchSettings) { s.Targets = []string{"es", "de"} })
	a.batchEnqueue()
	if !slices.Contains(a.toasts, "Batch dub finished: Interview") {
		t.Fatalf("toasts %q", a.toasts)
	}
}
