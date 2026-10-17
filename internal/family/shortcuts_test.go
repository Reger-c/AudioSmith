package family

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/hehehai/actnowkit/conformance"
	"github.com/hehehai/actnowkit/registry"
	"github.com/hehehai/actnowkit/shortcuts"
)

func TestManifestShortcutNormalized(t *testing.T) {
	fx := conformance.New(t)
	f := newFamily(t, fx, "")
	at := time.Date(2026, 10, 17, 9, 30, 0, 0, time.FixedZone("CST", 8*3600))
	for _, c := range []struct {
		in, want string
		hold     bool
	}{
		{"CmdOrCtrl+Shift+Space", "Shift+Command+Space", false},
		{"Cmd+Alt+D", "Option+Command+D", true},
		{"Ctrl+Shift+Cmd+F5", "Control+Shift+Command+F5", false},
	} {
		in := inputs(fx.Hub)
		in.Shortcut, in.ShortcutHold, in.ShortcutSince = c.in, c.hold, at
		m := f.Manifest(in)
		if len(m.Shortcuts) != 1 {
			t.Fatalf("%s: shortcuts %+v", c.in, m.Shortcuts)
		}
		s := m.Shortcuts[0]
		trigger := shortcuts.Press
		if c.hold {
			trigger = shortcuts.Hold
		}
		if s.Keys != c.want || s.Trigger != trigger || s.ID != "dictation" || s.Mechanism != shortcuts.Carbon || s.Exclusive || !s.Enabled {
			t.Errorf("%s: %+v", c.in, s)
		}
		if s.EnabledAt == nil || !s.EnabledAt.Equal(at) || s.EnabledAt.Location() != time.UTC {
			t.Errorf("%s: enabledAt %v", c.in, s.EnabledAt)
		}
	}
	in := inputs(fx.Hub)
	in.ShortcutSince = time.Time{}
	if s := f.Manifest(in).Shortcuts[0]; s.EnabledAt != nil {
		t.Errorf("unknown time written: %v", s.EnabledAt)
	}
	// The family's default table gives AudioSmith ⌘⇧Space, which no
	// other app's default takes.
	mine := shortcuts.DefaultsOf(AppID)
	if len(mine) != 1 || mine[0].Keys != f.Manifest(in).Shortcuts[0].Keys {
		t.Errorf("family defaults for AudioSmith: %+v", mine)
	}
}

// transcribe is Auralink's transcription shortcut on keys, turned on at.
func transcribe(keys string, at time.Time) shortcuts.Shortcut {
	return shortcuts.Shortcut{ID: "transcribe", Title: "Transcribe", Keys: keys, Trigger: shortcuts.Press, Enabled: true,
		Mechanism: shortcuts.EventTap, EnabledAt: &at}
}

func hitNames(hits []shortcuts.Hit) []string {
	var out []string
	for _, h := range hits {
		out = append(out, h.App.Name+"/"+h.Shortcut.Title)
	}
	return out
}

func TestUsedBy(t *testing.T) {
	fx := conformance.New(t)
	f := newFamily(t, fx, "")
	at := time.Now().Add(-time.Hour)
	fx.Register("dev.hehehai.auralink", transcribe("Shift+Command+Space", at))
	fx.Register("dev.hehehai.voxtnew.dev", transcribe("Option+Command+D", at)) // a dev build
	fx.Register("dev.hehehai.pik.m7", transcribe("Control+Command+P", at))     // a test build
	f.Rediscover()
	if got := hitNames(f.UsedBy("CmdOrCtrl+Shift+Space")); !slices.Equal(got, []string{"Auralink/Transcribe"}) {
		t.Errorf("⌘⇧Space: %v", got)
	}
	if got := f.UsedBy("Cmd+Alt+D"); len(got) != 0 {
		t.Errorf("a dev build's shortcut counts: %v", hitNames(got))
	}
	if got := f.UsedBy("Ctrl+Cmd+P"); len(got) != 0 {
		t.Errorf("the UI names a test build: %v", hitNames(got))
	}
	if got := f.UsedBy("Cmd+Shift+K"); len(got) != 0 {
		t.Errorf("free keys: %v", hitNames(got))
	}
	if f.UsedBy("") != nil {
		t.Error("no keys")
	}
	// Turned off in Auralink: not used.
	off := transcribe("Shift+Command+Space", at)
	off.Enabled = false
	fx.Register("dev.hehehai.auralink", off)
	f.Rediscover()
	if got := f.UsedBy("CmdOrCtrl+Shift+Space"); len(got) != 0 {
		t.Errorf("a disabled shortcut: %v", hitNames(got))
	}
}

func TestShortcutConflicts(t *testing.T) {
	fx := conformance.New(t)
	f := newFamily(t, fx, "")
	if got := f.ShortcutConflicts(true); got != nil {
		t.Errorf("before the manifest: %+v", got)
	}
	mine := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	in := inputs(fx.Hub)
	in.ShortcutSince = mine
	if err := f.Start(context.Background(), in, wantedOf()); err != nil {
		t.Fatal(err)
	}
	// Auralink took ⌘⇧Space before AudioSmith: AudioSmith, the later
	// one, prompts.
	fx.Register("dev.hehehai.auralink", transcribe("Shift+Command+Space", mine.Add(-time.Minute)))
	f.Rediscover()
	cs := f.ShortcutConflicts(true)
	if len(cs) != 1 || !cs[0].Prompt || cs[0].Other.Name != "Auralink" || cs[0].Theirs.Title != "Transcribe" || cs[0].Mine.ID != "dictation" {
		t.Fatalf("conflicts %+v", cs)
	}
	if cs[0].Key() != "dictation|dev.hehehai.auralink|transcribe" {
		t.Errorf("pair key %q", cs[0].Key())
	}
	if link := ShortcutLink(cs[0].Other); link != "auralink://settings" {
		t.Errorf("their settings link %q", link)
	}
	// Auralink took it after: only Auralink prompts; AudioSmith warns.
	fx.Register("dev.hehehai.auralink", transcribe("Shift+Command+Space", mine.Add(time.Minute)))
	f.Rediscover()
	if cs := f.ShortcutConflicts(true); len(cs) != 1 || cs[0].Prompt {
		t.Errorf("the earlier app prompts: %+v", cs)
	}
	// Without enabledAt, AudioSmith's manifest time stands in.
	in.ShortcutSince = time.Time{}
	if err := f.Update(context.Background(), in, wantedOf()); err != nil {
		t.Fatal(err)
	}
	me, _ := registry.Read(f.Dir, ReleaseBundleID)
	fx.Register("dev.hehehai.auralink", transcribe("Shift+Command+Space", me.Updated.Add(-time.Second)))
	f.Rediscover()
	if cs := f.ShortcutConflicts(true); len(cs) != 1 || !cs[0].Prompt {
		t.Errorf("manifest time: %+v", cs)
	}

	// Dev builds never count; test builds count for the data only.
	fx.Uninstall("dev.hehehai.auralink")
	fx.Register("dev.hehehai.auralink.dev", transcribe("Shift+Command+Space", mine))
	fx.Register("dev.hehehai.voxtnew.m7", transcribe("Shift+Command+Space", mine))
	f.Rediscover()
	if cs := f.ShortcutConflicts(true); len(cs) != 0 {
		t.Errorf("UI conflicts with dev/test builds: %+v", cs)
	}
	cs = f.ShortcutConflicts(false)
	if len(cs) != 1 || cs[0].Other.BundleID != "dev.hehehai.voxtnew.m7" {
		t.Errorf("data conflicts: %+v", cs)
	}
	// Dictation off: no conflict.
	in.ShortcutEnabled = false
	if err := f.Update(context.Background(), in, wantedOf()); err != nil {
		t.Fatal(err)
	}
	if cs := f.ShortcutConflicts(false); len(cs) != 0 {
		t.Errorf("a disabled shortcut conflicts: %+v", cs)
	}
}
