package main

import (
	"os"
	"testing"

	"github.com/hehehai/actnowkit/sounds"
)

// TestBundledSounds checks the family's sounds an official build ships
// (resources/sounds, fetched by scripts/sync-sounds.sh, not in the
// repository): the catalogue loads, every file is there and matches its
// SHA-256, and it has dictation's defaults. Without the copy it is skipped.
func TestBundledSounds(t *testing.T) {
	if _, err := os.Stat("resources/sounds/catalog.json"); err != nil {
		t.Skip("no resources/sounds (scripts/sync-sounds.sh): a source build")
	}
	cat, err := sounds.Load(os.DirFS("resources"), "sounds")
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range cat.Check() {
		t.Error(err)
	}
	for _, ev := range []sounds.Event{dictStartEvent, dictStopEvent, dictErrorEvent} {
		if _, ok := cat.Sound(ev.Default); !ok {
			t.Errorf("%s: no %s", ev.ID, ev.Default)
		}
	}
}
