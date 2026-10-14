package main

import (
	"image/png"
	"os"
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestRailClearsTheTrafficLights: with the icon rail, the rail's button and
// the page's title keep clear of the macOS window controls, which reach
// past the rail.
func TestRailClearsTheTrafficLights(t *testing.T) {
	a, _, tt := newTestApp(t)
	a.prefs.Appearance.Rail = true
	tt.SetTitleBar(ui.TitleBar{Height: 52, Left: 78})
	a.navigate("/integrations/claude-code")
	tt.Frame()
	tt.Frame()
	if out := os.Getenv("AUDIOSMITH_RAIL_SHOT"); out != "" {
		f, _ := os.Create(out)
		png.Encode(f, tt.Image())
		f.Close()
	}
	title, ok := tt.Find("Claude Code")
	if !ok {
		t.Fatalf("no title: %q", tt.Texts())
	}
	if title.X < 78 {
		t.Errorf("the title starts at x=%.0f, under the traffic lights (to 78)", title.X)
	}
	b, ok := tt.Find("Show the sidebar")
	if ok && b.Y < 52 && b.X < 78 {
		t.Errorf("the rail's button at %v is under the traffic lights", b)
	}
}
