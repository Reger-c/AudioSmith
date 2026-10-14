package main

import "testing"

// TestCollapsibleSectionsToggle: the inspector's sections (and every other
// kit collapsible fed from a map) open and close on a click. They used to
// copy their state into a local the click was applied to after the frame,
// so nothing happened.
func TestCollapsibleSectionsToggle(t *testing.T) {
	a, _, tt := newTestApp(t)
	a.navigate("/stories")
	tt.Frame()
	p := a.lfStories
	if p.open["production"] {
		t.Fatal("Production starts open")
	}
	if err := tt.Click("Production"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !p.open["production"] {
		t.Fatal("a click did not open Production")
	}
	if err := tt.Click("Production"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if p.open["production"] {
		t.Fatal("a second click did not close Production")
	}
	if !p.open["setup"] {
		t.Fatal("Setup starts open")
	}
	if err := tt.Click("Setup"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if p.open["setup"] {
		t.Fatal("a click did not close Setup")
	}
}
