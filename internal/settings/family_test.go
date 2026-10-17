package settings

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDictationEnabledAt(t *testing.T) {
	at := time.Date(2026, 10, 17, 10, 0, 0, 500, time.FixedZone("CST", 8*3600))
	now = func() time.Time { return at }
	t.Cleanup(func() { now = time.Now })
	s := NewMemory()
	ctx := context.Background()
	want := at.UTC().Truncate(time.Second)
	update := func(fn func(*Settings)) Dictation {
		t.Helper()
		if err := s.Update(ctx, fn); err != nil {
			t.Fatal(err)
		}
		return s.Get().Dictation
	}
	// On by default, but when is unknown: zero. Turning it on stamps it.
	if d := s.Get().Dictation; !d.Enabled || !d.EnabledAt.IsZero() {
		t.Errorf("defaults: %+v", d)
	}
	update(func(s *Settings) { s.Dictation.Enabled = false })
	if d := update(func(s *Settings) { s.Dictation.Enabled = true }); !d.EnabledAt.Equal(want) || d.EnabledAt.Location() != time.UTC {
		t.Errorf("on: %v", d.EnabledAt)
	}
	// Other changes keep the time; a new shortcut stamps it again.
	at = at.Add(time.Hour)
	if d := update(func(s *Settings) { s.Dictation.Mode = DictationHold; s.Dictation.Sounds = true }); !d.EnabledAt.Equal(want) {
		t.Errorf("mode change moved it: %v", d.EnabledAt)
	}
	if d := update(func(s *Settings) { s.Dictation.Shortcut = "Cmd+Alt+D" }); !d.EnabledAt.Equal(at.UTC().Truncate(time.Second)) {
		t.Errorf("new keys: %v", d.EnabledAt)
	}
	// Off: kept as it was (the manifest says it is off).
	at2 := at.Add(time.Hour)
	at = at2
	if d := update(func(s *Settings) { s.Dictation.Enabled = false; s.Dictation.Shortcut = "Cmd+Alt+E" }); d.EnabledAt.Equal(at2.UTC().Truncate(time.Second)) {
		t.Errorf("stamped while off: %v", d.EnabledAt)
	}
}

func TestFamilySettingsJSON(t *testing.T) {
	data, err := json.Marshal(Defaults())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"family"`) || strings.Contains(string(data), `"enabledAt"`) {
		t.Errorf("empty family fields are written: %s", data)
	}
	s := NewMemory()
	if err := s.Update(context.Background(), func(s *Settings) {
		s.Family.ConflictShown = map[string]bool{"dictation|dev.hehehai.auralink|transcribe": true}
	}); err != nil {
		t.Fatal(err)
	}
	got := s.Get()
	got.Family.ConflictShown["x"] = true // a copy
	if s.Get().Family.ConflictShown["x"] {
		t.Error("Get shares the map")
	}
	data, _ = json.Marshal(s.Get())
	var back Settings
	if err := json.Unmarshal(data, &back); err != nil || !back.Family.ConflictShown["dictation|dev.hehehai.auralink|transcribe"] {
		t.Errorf("round trip: %s %v", data, err)
	}
}
