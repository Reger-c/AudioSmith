package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hehehai/AudioSmith/internal/dub"
)

// TestDubLinesBuildOnlyWhatShows: the editor's list builds the lines in
// view, not all of them (review #102: the list sat in the workspace's
// scroll, took the height of every line, and built 600 lines a frame);
// moving to a line far down shows it.
func TestDubLinesBuildOnlyWhatShows(t *testing.T) {
	d := newDubTest(t, 6)
	a, tt := d.a, d.tt
	a.dubSourceFile(d.src)
	a.dubSet(func(p *dub.Project) { p.Targets = []string{"es"} })
	a.dubStart()
	a.dubTranslate(false)
	tt.Frame()
	if a.dub == nil || len(a.dub.Segments) == 0 {
		t.Fatal("no dub")
	}
	a.dubSet(func(p *dub.Project) {
		base := p.Segments
		var segs []dub.Segment
		for i := range 600 {
			s := base[i%len(base)]
			s.ID = fmt.Sprintf("s%04d", i)
			s.Start, s.End = float64(i)*3, float64(i)*3+2.5
			s.Lines = map[string]dub.Line{"es": {Text: fmt.Sprintf("Línea número %d.", i)}}
			segs = append(segs, s)
		}
		p.Segments = segs
	})
	a.dubLang = "es"
	a.dubStep = dubStepTranslate
	tt.Frame()
	tt.Frame()
	built := func() (n int) {
		for _, x := range tt.Texts() {
			if strings.HasPrefix(x, "Línea número") {
				n++
			}
		}
		return n
	}
	if n := built(); n == 0 || n >= 100 {
		t.Fatalf("%d of 600 lines built, want some and fewer than 100", n)
	}
	a.dubCur = "s0590"
	a.dubScrollTo("s0590")
	for range 3 {
		tt.Frame()
	}
	if !tt.HasText("Línea número 590.") {
		t.Fatalf("line 590 not shown after scrolling to it")
	}
	if n := built(); n >= 100 {
		t.Fatalf("%d lines built after scrolling", n)
	}
}
