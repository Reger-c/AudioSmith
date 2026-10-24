package main

import (
	"context"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/services"
)

// savedStory loads the sample story in the Stories page and saves it.
func savedStory(t *testing.T) (*app, *lfPage, *ui.Tester) {
	t.Helper()
	a, _, tt := newTestApp(t)
	tt.SetSize(1300, 1000)
	p := a.lfStories
	a.navigate("/stories")
	tt.Frame()
	if err := tt.Click("Load sample"); err != nil {
		t.Fatal(err)
	}
	p.doc.Title = "Night Ferry"
	a.saveLf(p, false)
	if p.project == "" || p.unsaved() {
		t.Fatalf("saved %q, unsaved %v", p.project, p.unsaved())
	}
	return a, p, tt
}

// #101 M6: a rename made elsewhere while the page has unsaved changes
// takes the new title and keeps the changes unsaved.
func TestLfRenameElsewhereKeepsUnsaved(t *testing.T) {
	a, p, _ := savedStory(t)
	p.doc.Lines[0].Text = "An edit not saved yet."
	if !p.unsaved() {
		t.Fatal("the edit is not unsaved")
	}
	if _, err := a.lib.svc.Library.Rename(context.Background(), p.project, "Night Crossing"); err != nil {
		t.Fatal(err)
	}
	if p.doc.Title != "Night Crossing" {
		t.Errorf("title %q", p.doc.Title)
	}
	if !p.unsaved() {
		t.Fatal("the rename marked the unsaved edit saved")
	}
	// Without changes of its own, the rename leaves nothing unsaved.
	a.saveLf(p, false)
	if _, err := a.lib.svc.Library.Rename(context.Background(), p.project, "Night Ferry 2"); err != nil {
		t.Fatal(err)
	}
	if p.unsaved() || p.doc.Title != "Night Ferry 2" {
		t.Errorf("after a rename: unsaved %v, title %q", p.unsaved(), p.doc.Title)
	}
}

// #101 M7: saving over a project an agent saved meanwhile does not drop
// the agent's change silently: the page asks, and saving a copy or
// replacing keeps the page's edits.
func TestLfSaveAfterChangeElsewhere(t *testing.T) {
	a, p, tt := savedStory(t)
	ctx := context.Background()
	lib := a.lib.svc.Library
	p.doc.Lines[0].Text = "The page's edit."
	// An agent (the Local API) changes the saved project meanwhile.
	if _, _, err := lib.UpdateDoc(ctx, p.project, func(_ *core.Project, d *services.LongformDoc) error {
		d.Lines[1].Text = "The agent's edit."
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !p.unsaved() || p.doc.Lines[0].Text != "The page's edit." {
		t.Fatal("the page lost its edit")
	}
	a.saveLf(p, false)
	_, d, _ := lib.LoadDoc(ctx, p.project)
	if d.Lines[1].Text != "The agent's edit." {
		t.Fatal("the page overwrote the agent's change without asking")
	}
	if p.confirm != "changed" || !p.confirmOpen {
		t.Fatalf("no question (confirm %q)", p.confirm)
	}
	tt.Frame()
	if !hasAnyText(tt.Texts(), "“Night Ferry” was changed elsewhere") {
		t.Errorf("no dialog in %q", tt.Texts())
	}
	// Replace: the page's version is saved.
	orig := p.project
	a.saveLfAs(p, false, true)
	if _, d, _ = lib.LoadDoc(ctx, orig); d.Lines[0].Text != "The page's edit." || p.unsaved() {
		t.Errorf("replace: %q, unsaved %v", d.Lines[0].Text, p.unsaved())
	}
	// A copy keeps both.
	p.confirm, p.confirmOpen = "", false
	p.doc.Lines[2].Text = "A second page edit."
	if _, _, err := lib.UpdateDoc(ctx, orig, func(_ *core.Project, d *services.LongformDoc) error {
		d.Lines[1].Text = "Another agent edit."
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	a.saveLf(p, false)
	if p.confirm != "changed" {
		t.Fatal("no question the second time")
	}
	a.saveLf(p, true)
	if p.project == orig || p.unsaved() {
		t.Fatalf("copy: project %q unsaved %v", p.project, p.unsaved())
	}
	if _, d, _ = lib.LoadDoc(ctx, orig); d.Lines[1].Text != "Another agent edit." || strings.Contains(d.Lines[2].Text, "second page") {
		t.Errorf("the original after the copy: %+v", d.Lines[:3])
	}
}
