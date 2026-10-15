package dub

import (
	"testing"
	"time"
)

// TestDefaultTiming: new dubs fit lines with smart timing (thread #41);
// a project saved without a timing (made before timings existed) keeps
// lip sync, the default it was made with.
func TestDefaultTiming(t *testing.T) {
	if p := New("x", time.Now()); p.Settings.Timing != TimingSmart {
		t.Errorf("new project: %q", p.Settings.Timing)
	}
	old := &Project{ID: "y"}
	old.Normalize()
	if old.Settings.Timing != TimingLipSync {
		t.Errorf("old project: %q", old.Settings.Timing)
	}
}
