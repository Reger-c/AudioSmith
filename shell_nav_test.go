package main

import (
	"slices"
	"testing"
	"time"
)

// The sidebar's sections, the ⌘ numbers and the palette agree (M7a).
func TestSidebarGroups(t *testing.T) {
	a, _, tt := newTestApp(t)
	tt.Frame()
	for _, title := range []string{"Create", "Voices", "Library", "Automate", "Utilities", "System",
		"Studio", "Stories", "Audiobook", "Dub", "Design", "Gallery", "Projects", "History", "Transcribe",
		"Workflows", "Batch", "Tools", "Integrations", "Models", "Engine", "Logs", "Settings"} {
		if !tt.HasText(title) {
			t.Errorf("sidebar lacks %q", title)
		}
	}
	if len(shortcutPages) != 9 {
		t.Fatalf("%d shortcut pages", len(shortcutPages))
	}
	cmds := a.commands()
	for _, p := range pages {
		i := slices.IndexFunc(cmds, func(c command) bool { return c.id == "go"+p.path })
		if i < 0 {
			t.Errorf("no palette entry for %s", p.path)
			continue
		}
		if cmds[i].keys != pageKeys(p.path) {
			t.Errorf("%s: keys %q", p.path, cmds[i].keys)
		}
		if n := 0; true {
			for _, c := range cmds {
				if c.id == "go"+p.path {
					n++
				}
			}
			if n != 1 {
				t.Errorf("%s: %d palette entries", p.path, n)
			}
		}
	}
	if pageKeys("/history") != "" || pageKeys("/transcripts") != "⌘9" {
		t.Errorf("keys: history %q transcripts %q", pageKeys("/history"), pageKeys("/transcripts"))
	}
	// The sheet lists the new numbers.
	a.sheetOpen = true
	tt.Frame()
	if !tt.HasText("Studio, Stories, Audiobook, Dub") || tt.HasText("Studio, History, Models, Engine, Logs") {
		t.Errorf("sheet: %q", tt.Texts())
	}
}

// The icon rail replaces the sidebar, and comes back; a closed section
// is remembered.
func TestSidebarRail(t *testing.T) {
	a, _, tt := newTestApp(t)
	tt.Frame()
	if err := tt.Click("Show icons only"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.prefs.Appearance.Rail || !a.settings.Get().Appearance.Rail {
		t.Fatal("rail not saved")
	}
	if tt.HasText("Create") {
		t.Error("the rail shows section titles")
	}
	if err := tt.Click("Projects"); err != nil {
		t.Fatal(err)
	}
	if a.currentPage() != "projects" {
		t.Errorf("rail click: %s", a.router.Path())
	}
	if err := tt.Click("Show the sidebar"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.prefs.Appearance.Rail || !tt.HasText("Create") {
		t.Fatal("the sidebar did not come back")
	}
	// Close a section: its pages go, and the settings remember it.
	*a.sectionOpen("Utilities") = false
	tt.Frame()
	tt.Frame()
	if tt.HasText("Integrations") {
		t.Error("closed section still shows its pages")
	}
	if !slices.Contains(a.settings.Get().Appearance.ClosedSections, "Utilities") {
		t.Errorf("closed sections %v", a.settings.Get().Appearance.ClosedSections)
	}
	// The resized width is saved.
	a.sideW = 260 * a.sideWScale
	a.sideWAt = time.Now().Add(-time.Second) // a drag that rested
	tt.Frame()
	if w := a.settings.Get().Appearance.SidebarWidth; w != 260 {
		t.Errorf("width %v", w)
	}
}
