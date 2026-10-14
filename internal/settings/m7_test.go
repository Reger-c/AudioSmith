package settings

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestSetupMigration(t *testing.T) {
	if Defaults().Setup.Done {
		t.Error("a fresh install skips the setup")
	}
	s, _ := Migrate([]byte(`{"version":1,"general":{"language":"en"}}`))
	if !s.Setup.Done {
		t.Error("an install from before the wizard sees it")
	}
	s, _ = Migrate([]byte(`{"version":1,"setup":{"done":false}}`))
	if s.Setup.Done {
		t.Error("an unfinished setup counts as done")
	}
}

// TestPaletteDropped: 0.3 removed the accent palette; a document saved
// with one still loads, and the next save leaves it out.
func TestPaletteDropped(t *testing.T) {
	s, err := Migrate([]byte(`{"version":1,"setup":{"done":true},"appearance":{"theme":"dark","palette":"violet","rail":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.Appearance.Theme != "dark" || !s.Appearance.Rail {
		t.Fatalf("appearance: %+v", s.Appearance)
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "palette") {
		t.Errorf("saved again with the palette: %s", data)
	}
}

func TestNormalizeM7(t *testing.T) {
	st := NewMemory()
	err := st.Update(context.Background(), func(s *Settings) {
		s.Appearance.SidebarWidth = 40
		s.Appearance.ClosedSections = []string{" Voices", "Voices", ""}
		s.Media.FFmpeg = "  "
		s.Media.Origin = MediaSystem
		s.Setup.Pack = "huge"
	})
	if err != nil {
		t.Fatal(err)
	}
	got := st.Get()
	if got.Appearance.SidebarWidth != MinSidebarWidth ||
		!reflect.DeepEqual(got.Appearance.ClosedSections, []string{"Voices"}) || got.Media.Origin != "" || got.Setup.Pack != "" {
		t.Fatalf("normalized: %+v %+v %+v", got.Appearance, got.Media, got.Setup)
	}
	got.Appearance.ClosedSections[0] = "x"
	if st.Get().Appearance.ClosedSections[0] != "Voices" {
		t.Error("Get shares ClosedSections")
	}
}
