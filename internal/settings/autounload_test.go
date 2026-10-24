package settings

import (
	"context"
	"testing"
)

// #109: a new install unloads idle models after 30 minutes.
func TestAutoUnloadDefault(t *testing.T) {
	if got := Defaults().Models.AutoUnload; got != DefaultAutoUnload || DefaultAutoUnload != 30 {
		t.Fatalf("default %d", got)
	}
	s, err := Open(context.Background(), &memDocs{}, nil)
	if err != nil || s.Get().Models.AutoUnload != 30 || s.Migrations() != nil {
		t.Fatalf("fresh: %d %v %v", s.Get().Models.AutoUnload, s.Migrations(), err)
	}
}

// #109: a version-1 document's 0 (the old default) becomes 30 once and is
// saved; a choice made before (15) is kept; "never" chosen after the
// migration stays.
func TestAutoUnloadMigration(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, doc string
		want      int
		migrated  bool
	}{
		{"untouched", `{"version":1,"models":{"cacheDir":"","endpoints":null,"autoUnload":0}}`, 30, true},
		{"no version", `{"models":{"autoUnload":0}}`, 30, true},
		{"chosen", `{"version":1,"models":{"autoUnload":15}}`, 15, false},
		{"never after migration", `{"version":2,"models":{"autoUnload":0}}`, 0, false},
	} {
		docs := &memDocs{m: map[string][]byte{Key: []byte(tc.doc)}}
		s, err := Open(ctx, docs, nil)
		if err != nil {
			t.Fatal(tc.name, err)
		}
		if got := s.Get().Models.AutoUnload; got != tc.want {
			t.Errorf("%s: autoUnload %d, want %d", tc.name, got, tc.want)
		}
		m := s.Migrations()
		if tc.migrated != (len(m) == 1 && m[0].Field == "models.autoUnload" && m[0].From == 0 && m[0].To == 30) {
			t.Errorf("%s: migrations %+v", tc.name, m)
		}
		if !tc.migrated {
			continue
		}
		// Saved at once: the next start does not migrate again, and
		// "never" chosen now stays.
		if err := s.Update(ctx, func(s *Settings) { s.Models.AutoUnload = 0 }); err != nil {
			t.Fatal(err)
		}
		again, err := Open(ctx, docs, nil)
		if err != nil || again.Get().Models.AutoUnload != 0 || again.Migrations() != nil {
			t.Errorf("%s: reopened %d %v %v", tc.name, again.Get().Models.AutoUnload, again.Migrations(), err)
		}
	}
	// Opened twice without a change in between: migrated once.
	docs := &memDocs{m: map[string][]byte{Key: []byte(`{"version":1,"models":{"autoUnload":0}}`)}}
	if s, _ := Open(ctx, docs, nil); len(s.Migrations()) != 1 {
		t.Fatal("not migrated")
	}
	if s, _ := Open(ctx, docs, nil); s.Migrations() != nil || s.Get().Models.AutoUnload != 30 {
		t.Fatalf("second open: %v %d", s.Migrations(), s.Get().Models.AutoUnload)
	}
}
