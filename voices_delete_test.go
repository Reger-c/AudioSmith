package main

// Tests of deleting a voice from its card's ⋯ menu (#108): the native
// menu takes the pointer, so the card is no longer hovered when the
// chosen item is reported.

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/core"
)

func TestVoiceCardMenuDelete(t *testing.T) {
	a, _ := newVoiceTestApp(t)
	tt := ui.NewTester(a.view, 1200, 900)
	pinTester(tt)
	c1 := addVoice(t, a, core.Voice{Name: "c1", Kind: core.VoiceCloned, RefText: "Hello there."}, 6)
	addVoice(t, a, core.Voice{Name: "c2", Kind: core.VoiceCloned, RefText: "Hello again."}, 6)
	a.navigate("/voices")
	tt.Frame()
	r, ok := tt.Find("c1")
	if !ok {
		t.Fatalf("no card: %q", tt.Texts())
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2) // hover the card: its ⋯ shows
	tt.Frame()
	if err := tt.Click("More actions"); err != nil {
		t.Fatal(err)
	}
	if tt.Menu() == nil {
		t.Fatal("no menu")
	}
	// The pointer goes to the menu, below and outside the card, as on
	// macOS where the view sees the pointer leave.
	tt.Move(5, 890)
	if err := tt.ChooseMenuItem("Delete…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.deleteOpen || !tt.HasText("Delete “c1”?") {
		t.Fatalf("no confirmation: open=%v %q", a.deleteOpen, tt.Texts())
	}
	if err := tt.Click("Delete voice"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.findVoice(c1.ID) != nil {
		t.Fatal("not deleted")
	}
	if !tt.HasText("Voice deleted") {
		t.Errorf("no toast: %q", tt.Texts())
	}
	if !tt.HasText("c2") {
		t.Error("the other voice went too")
	}
}
