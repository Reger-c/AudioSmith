package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/icons"
)

// TestButtonHeights: the kit's text buttons — ink, secondary, ghost, with
// or without an icon, record — stand the same height, so a row of them
// lines up (an empty state's "Load sample" beside "Add the first line").
func TestButtonHeights(t *testing.T) {
	var els []ui.Element
	var hs []float32
	names := []string{"ui.Button", "primary", "primaryIcon", "secondaryIcon", "ghost", "ghostIcon", "dropButton", "record"}
	view := func(c *ui.Context) {
		th := *c.Theme()
		styleTheme(&th)
		c.SetTheme(&th)
		els = els[:0]
		ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
			els = append(els,
				ui.Button(c.Key("a"), "Secondary"),
				primaryButton(c.Key("b"), "Primary"),
				primaryIconButton(c.Key("c"), icons.Play, "Primary icon"),
				secondaryIconButton(c.Key("d"), icons.AddCircle, "Secondary icon"),
				ghostButton(c.Key("e"), "Ghost"),
				ghostIconButton(c.Key("f"), icons.AddCircle, "Ghost icon"),
				dropButton(c.Key("g"), icons.AddCircle, "Drop"),
				recordButton(c.Key("h"), "Record"),
			)
		})
		hs = hs[:0]
		for _, e := range els {
			hs = append(hs, e.Bounds().H) // the last frame's layout
		}
	}
	for _, scale := range []float32{1, 1.25} {
		tt := ui.NewTester(view, 1200, 200)
		tt.SetScale(scale)
		tt.Frame()
		tt.Frame()
		tt.Frame()
		want := hs[0]
		for i, h := range hs {
			if h != want {
				t.Errorf("scale %v: %s is %.1f high, ui.Button %.1f", scale, names[i], h, want)
			}
		}
	}
}
