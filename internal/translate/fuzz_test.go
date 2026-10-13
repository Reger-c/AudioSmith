package translate

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func FuzzMaskRoundTrip(f *testing.F) {
	for _, s := range []string{"Hello [laughter] world", "[[GIF|jif]] files [pause 1s]", "⟦1⟧ literal", "[a [b]", "你好[sigh]世界"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if !utf8.ValidString(s) {
			return
		}
		m := mask(s)
		got, ok := m.unmask(m.Text)
		if markerRE.MatchString(s) {
			return // literal markers in the source are ambiguous by design
		}
		norm := func(x string) string { return strings.TrimSpace(collapseSpaces(strings.TrimSpace(x))) }
		if !ok || norm(got) != norm(s) {
			t.Fatalf("round trip %q → %q → %q (ok %v)", s, m.Text, got, ok)
		}
	})
}

func FuzzPolishAndUnits(f *testing.F) {
	for _, s := range []string{"um so i think", "嗯，我觉得 这个", "3.14 is pi.", "[pause 2s] wow!!", "नमस्ते दुनिया", "e.g. this"} {
		f.Add(s, "en")
		f.Add(s, "zh")
	}
	f.Fuzz(func(t *testing.T, s, lang string) {
		if !utf8.ValidString(s) {
			return
		}
		p := Polish(s, lang, PolishOptions{})
		if !utf8.ValidString(p) {
			t.Fatalf("invalid UTF-8 from %q", s)
		}
		u, pause := Units(s, lang)
		if u < 0 || pause < 0 {
			t.Fatalf("negative units/pause for %q", s)
		}
		_ = ParseDirection(s)
	})
}
