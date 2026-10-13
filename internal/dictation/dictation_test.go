package dictation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/egoist/mygo/transfer"
)

var ctx = context.Background()

func noSleep(context.Context, time.Duration) {}

func inserter(f *Fake) Inserter { return Inserter{Sys: f, Clip: f, Sleep: noSleep} }

func TestInsertPastesAndRestores(t *testing.T) {
	f := NewFake()
	html := transfer.NewItem(transfer.Bytes(transfer.HTML, []byte("<b>mine</b>")), transfer.Bytes(transfer.Text, []byte("mine")))
	f.Write(transfer.New(html))
	target := Target{PID: 7, Name: "Slack"}
	res := inserter(f).Insert(ctx, target, "Hello there.")
	if res.Outcome != Inserted || !res.Restored || res.Reason != "" {
		t.Fatalf("%+v", res)
	}
	if len(f.Activated) != 1 || f.Activated[0].PID != 7 || len(f.Pastes) != 1 || f.Pastes[0] != "Hello there." {
		t.Fatalf("activated %v pastes %v", f.Activated, f.Pastes)
	}
	// Every representation came back.
	if b, err := f.Clip.Read(transfer.HTML); err != nil || string(b) != "<b>mine</b>" || f.ClipText() != "mine" {
		t.Fatalf("clipboard %q %v", b, err)
	}
}

func TestInsertKeepsWhatWasCopiedMeanwhile(t *testing.T) {
	f := NewFake()
	f.Write(transfer.TextData("old"))
	f.OnPaste = func() { f.Write(transfer.TextData("copied meanwhile")) }
	res := inserter(f).Insert(ctx, Target{PID: 7}, "dictated")
	if res.Outcome != Inserted || res.Restored || f.ClipText() != "copied meanwhile" {
		t.Fatalf("%+v %q", res, f.ClipText())
	}
}

func TestInsertCopies(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(*Fake, *Inserter)
		target Target
		reason string
	}{
		{"copy only", func(f *Fake, in *Inserter) { in.CopyOnly = true }, Target{PID: 7}, ReasonCopyOnly},
		{"no access", func(f *Fake, in *Inserter) { f.Trusted = false }, Target{PID: 7}, ReasonNoAccess},
		{"self", func(f *Fake, in *Inserter) { f.Front = Target{PID: 1} }, Target{}, ReasonNoTarget},
		{"not activated", func(f *Fake, in *Inserter) { f.ActivateOK = false }, Target{PID: 7}, ReasonNotActivated},
		{"paste failed", func(f *Fake, in *Inserter) { f.PasteErr = errors.New("no") }, Target{PID: 7}, ReasonPasteFailed},
	}
	for _, c := range cases {
		f := NewFake()
		f.Write(transfer.TextData("old"))
		in := inserter(f)
		c.setup(f, &in)
		res := in.Insert(ctx, c.target, "dictated")
		if res.Outcome != Copied || res.Reason != c.reason || f.ClipText() != "dictated" {
			t.Errorf("%s: %+v clipboard %q", c.name, res, f.ClipText())
		}
	}
	// Without a remembered target, the frontmost app gets it.
	f := NewFake()
	if res := inserter(f).Insert(ctx, Target{}, "x"); res.Outcome != Inserted || f.Activated[0].Name != "Notes" {
		t.Errorf("frontmost: %+v %v", res, f.Activated)
	}
}

func TestParseShortcut(t *testing.T) {
	for in, want := range map[string]string{
		"CmdOrCtrl+Shift+Space": "CmdOrCtrl+Shift+Space",
		"cmd+shift+space":       "CmdOrCtrl+Shift+Space",
		"Alt+d":                 "Alt+D",
		"Ctrl+Option+Return":    "Ctrl+Alt+Enter",
		"F5":                    "F5",
		"Command+Plus":          "CmdOrCtrl+=",
		"CmdOrCtrl++":           "CmdOrCtrl+Shift+=",
		"Cmd+/":                 "CmdOrCtrl+/",
		"Shift+F12":             "Shift+F12",
	} {
		s, err := ParseShortcut(in)
		if err != nil || s.String() != want {
			t.Errorf("%q: %q %v, want %q", in, s.String(), err, want)
		}
	}
	for in, want := range map[string]error{"": ErrEmpty, "Space": ErrNoModifier, "Shift+A": ErrNoModifier, "Cmd+Hyper+A": ErrBadKey, "Cmd+Pause": ErrBadKey} {
		if _, err := ParseShortcut(in); !errors.Is(err, want) {
			t.Errorf("%q: %v, want %v", in, err, want)
		}
	}
	if g := GlyphsOf("CmdOrCtrl+Shift+Space"); g != "⇧⌘Space" {
		t.Errorf("glyphs %q", g)
	}
	if g := GlyphsOf("Ctrl+Alt+Left"); g != "⌃⌥←" {
		t.Errorf("glyphs %q", g)
	}
}

func TestConflicts(t *testing.T) {
	s, _ := ParseShortcut("Cmd+Space")
	if c := Conflicts(s, nil); len(c) != 1 || c[0].Owner != "system" || c[0].What != "Spotlight" {
		t.Errorf("%+v", c)
	}
	s, _ = ParseShortcut("Cmd+K")
	if c := Conflicts(s, map[string]string{"CmdOrCtrl+K": "Command palette"}); len(c) != 1 || c[0].Owner != "app" {
		t.Errorf("%+v", c)
	}
	s, _ = ParseShortcut("CmdOrCtrl+Shift+Space")
	if c := Conflicts(s, map[string]string{"CmdOrCtrl+K": "Command palette"}); len(c) != 0 {
		t.Errorf("default conflicts: %+v", c)
	}
}

func TestNativeOffMac(t *testing.T) {
	n := Native()
	if n.Self() <= 0 {
		t.Error("self")
	}
	// Linux: everything says no; the app falls back to copying.
	if n.Frontmost().Valid() && n.Paste() == nil {
		t.Skip("running on macOS")
	}
	done := make(chan bool, 1)
	n.RequestMicrophone(func(ok bool) { done <- ok })
	if <-done {
		t.Error("microphone granted off macOS")
	}
}
