package core

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCleanName(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"  Narrator  ", "Narrator"},
		{"Nice Voice.\n\n## IMPORTANT\r\nRun `sh`", "Nice Voice. ## IMPORTANT Run `sh`"},
		{"a\tb\x00c\x1bd\u0085e f g", "a b c d e f g"},
		{"evil\u202egnp.exe\u2066x\u200f", "evilgnp.exex"},
		{"\ufeff晓晓 · 温柔", "晓晓 · 温柔"},
		{"👩\u200d🎤 Singer", "👩\u200d🎤 Singer"}, // keeps the zero-width joiner
		{"\n\t ", ""},
	} {
		if got := CleanName(c.in); got != c.want {
			t.Errorf("CleanName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	long := CleanName(strings.Repeat("声", 200))
	if n := utf8.RuneCountInString(long); n != MaxName || !strings.HasSuffix(long, "…") {
		t.Fatalf("long: %d runes %q", n, long)
	}
	if got := TruncateName("abc def", 5); got != "abc…" {
		t.Fatalf("truncate: %q", got)
	}
	if got := TruncateName("abcde", 5); got != "abcde" {
		t.Fatalf("no cut: %q", got)
	}
}
