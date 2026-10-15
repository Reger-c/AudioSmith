package main

import (
	"testing"

	"github.com/hehehai/AudioSmith/internal/dub"
)

// TestDubAdvanced: each step keeps its main choices in view and the rest
// under one Advanced the page remembers; its changes are counted and
// restored per step; the panels' duplicate buttons are gone (they are in
// the editor's ⋯ menu).
func TestDubAdvanced(t *testing.T) {
	d := newDubTest(t, 4)
	a, tt := d.a, d.tt
	tt.SetSize(1100, 1400)
	a.dubSourceFile(d.src)
	a.dubSide.set, a.dubSide.open = true, true
	a.dubStep = dubStepSource
	tt.Frame()
	if !tt.HasText("Spoken language") || tt.HasText("Review after each step") || !tt.HasText("Advanced") {
		t.Fatalf("source: %q", tt.Texts())
	}
	a.dubSet(func(p *dub.Project) { p.Speakers = 2 })
	tt.Click("Advanced")
	tt.Frame()
	if !a.advancedOpen("dub") || !tt.HasText("Review after each step") || !tt.HasText("1 change") {
		t.Fatalf("source, advanced: %q", tt.Texts())
	}
	tt.Click("Restore recommended")
	tt.Frame()
	if a.dub.Speakers != 0 {
		t.Errorf("speakers %d", a.dub.Speakers)
	}

	a.dubStart()
	a.dubStep = dubStepTranslate
	tt.Frame()
	if !tt.HasText("Dub into") || !tt.HasText("Style notes") || tt.HasText("Translate everything again") || tt.HasText("Paste translation") {
		t.Errorf("translate: %q", tt.Texts())
	}
	a.dubSettings(func(s *dub.Settings) { s.Model, s.Steps, s.Guidance = entry(t, "omnivoice-bf16").Repo, 16, 2.5 })
	a.dubStep = dubStepGenerate
	tt.Frame()
	if !tt.HasText("2 changes") || !tt.HasText("Steps: 16") {
		t.Errorf("generate: %q", tt.Texts())
	}
	tt.Click("Restore recommended")
	tt.Frame()
	if s := a.dub.Settings; s.Steps != 0 || s.Guidance != 0 {
		t.Errorf("generate restored %+v", s)
	}
}
