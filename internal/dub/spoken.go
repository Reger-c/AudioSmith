package dub

import (
	"strings"
	"unicode"
)

// Spoken is what a recognizer heard in a made line, measured against the
// text the line should say (thread #90: a clone can slip into its
// reference's language). The recognizer is asked for the line's
// language; a line in another language still comes back as nonsense in
// it (high Drift) or in that language's own script (Foreign).
type Spoken struct {
	Heard string  `json:"heard,omitempty"`
	Drift float64 `json:"drift"` // error rate (Drift)
	// Foreign is the script most of what was heard is in, when the
	// line's language is not written in it ("kana", "hangul", "han",
	// "latin", "cyrillic"…); "" when the script fits.
	Foreign string `json:"foreign,omitempty"`
}

// OK reports whether the line says its text in its language: the script
// fits and the error rate is within the check's limit (DriftLimit, as
// the dub's Verify flags lines).
func (s Spoken) OK() bool { return s.Foreign == "" && s.Drift <= DriftLimit }

// Worse reports whether s is a worse take of a line than o.
func (s Spoken) Worse(o Spoken) bool {
	if (s.Foreign == "") != (o.Foreign == "") {
		return s.Foreign != ""
	}
	return s.Drift > o.Drift
}

// CheckSpoken measures heard (a transcript in lang, BCP-47) against text.
func CheckSpoken(lang, text, heard string) Spoken {
	heard = strings.TrimSpace(heard)
	return Spoken{Heard: heard, Drift: Drift(text, heard), Foreign: ForeignScript(lang, heard)}
}

// foreignShare is the share of letters in scripts lang does not write
// above which a transcript is in another language.
const foreignShare = 0.3

// ForeignScript is the script most of s's foreign letters are in, when
// they are more than foreignShare of its letters, for a text in lang
// (BCP-47); "" when they are fewer or lang's scripts are not known.
func ForeignScript(lang, s string) string {
	own := langScripts(lang)
	if own == nil {
		return ""
	}
	counts := map[string]int{}
	letters := 0
	for _, r := range s {
		sc := scriptOf(r)
		if sc == "" {
			continue
		}
		letters++
		if !own[sc] {
			counts[sc]++
		}
	}
	best, n, foreign := "", 0, 0
	for sc, c := range counts {
		foreign += c
		if c > n || c == n && sc < best {
			best, n = sc, c
		}
	}
	if letters == 0 || float64(foreign) <= foreignShare*float64(letters) {
		return ""
	}
	return best
}

// scriptOf is the script of a letter ("" for anything else).
func scriptOf(r rune) string {
	switch {
	case unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || r == 'ー':
		return "kana"
	case unicode.Is(unicode.Hangul, r):
		return "hangul"
	case unicode.Is(unicode.Han, r):
		return "han"
	case unicode.Is(unicode.Latin, r):
		return "latin"
	case unicode.Is(unicode.Cyrillic, r):
		return "cyrillic"
	case unicode.Is(unicode.Arabic, r):
		return "arabic"
	case unicode.Is(unicode.Thai, r):
		return "thai"
	case unicode.Is(unicode.Devanagari, r):
		return "devanagari"
	case unicode.Is(unicode.Greek, r):
		return "greek"
	case unicode.Is(unicode.Hebrew, r):
		return "hebrew"
	}
	return ""
}

// langScripts are the scripts a language is written in (Latin too for
// the ones that mix in names and loanwords); nil when not known.
func langScripts(lang string) map[string]bool {
	base, _, _ := strings.Cut(strings.ToLower(lang), "-")
	set := func(s ...string) map[string]bool {
		m := map[string]bool{}
		for _, x := range s {
			m[x] = true
		}
		return m
	}
	switch base {
	case "zh", "yue":
		return set("han", "latin")
	case "ja":
		return set("han", "kana", "latin")
	case "ko":
		return set("hangul", "han", "latin")
	case "ru", "uk", "bg", "sr", "be", "kk":
		return set("cyrillic", "latin")
	case "ar", "fa", "ur":
		return set("arabic", "latin")
	case "th":
		return set("thai", "latin")
	case "hi", "mr", "ne":
		return set("devanagari", "latin")
	case "el":
		return set("greek", "latin")
	case "he":
		return set("hebrew", "latin")
	case "en", "fr", "es", "de", "it", "pt", "nl", "sv", "da", "no", "nb", "fi", "pl", "cs", "sk", "hu", "ro",
		"tr", "id", "ms", "vi", "tl", "ca", "hr", "sl", "et", "lv", "lt", "sw":
		return set("latin")
	}
	return nil
}
