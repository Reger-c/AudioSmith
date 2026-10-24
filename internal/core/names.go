package core

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxName is the longest name, in runes, CleanName keeps.
const MaxName = 80

// CleanName is a name from anywhere (an imported voice file, the API, an
// MCP client, the UI) as AudioSmith keeps it: control characters, line
// breaks and other whitespace become one space, invisible bidi controls go,
// the ends are trimmed and the result has at most MaxName runes (security
// review #100, M2: names reach the agents' skill and prompts).
func CleanName(s string) string { return TruncateName(cleanRunes(s), MaxName) }

func cleanRunes(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case r == utf8.RuneError, bidiControl(r):
			continue
		case unicode.IsSpace(r) || unicode.IsControl(r) || unicode.In(r, unicode.Zl, unicode.Zp):
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// bidiControl reports the invisible characters that reorder text.
func bidiControl(r rune) bool {
	return r == '\u200e' || r == '\u200f' || r == '\u061c' || r == '\ufeff' ||
		(r >= '\u202a' && r <= '\u202e') || (r >= '\u2066' && r <= '\u2069')
}

// TruncateName cuts s to at most n runes, ending in "…" when cut.
func TruncateName(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimRightFunc(string(r[:n-1]), unicode.IsSpace) + "…"
}
