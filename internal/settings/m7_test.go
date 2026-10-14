package settings

import (
	"context"
	"reflect"
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

func TestNormalizeM7(t *testing.T) {
	st := NewMemory()
	err := st.Update(context.Background(), func(s *Settings) {
		s.Appearance.Palette = "neon"
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
	if got.Appearance.Palette != "" || got.Appearance.SidebarWidth != MinSidebarWidth ||
		!reflect.DeepEqual(got.Appearance.ClosedSections, []string{"Voices"}) || got.Media.Origin != "" || got.Setup.Pack != "" {
		t.Fatalf("normalized: %+v %+v %+v", got.Appearance, got.Media, got.Setup)
	}
	got.Appearance.ClosedSections[0] = "x"
	if st.Get().Appearance.ClosedSections[0] != "Voices" {
		t.Error("Get shares ClosedSections")
	}
}
