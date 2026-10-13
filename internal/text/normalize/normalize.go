// Package normalize rewrites written forms a TTS model may misread into
// spoken words, conservatively and per language: numbers (with grouping and
// decimals), negative numbers, percentages, ordinals, times ("3:30"),
// simple dates, currency ("$5", "¥5", "5 €"), number ranges ("3–5",
// "3~5"), temperatures and common abbreviations ("Dr.", "z. B.").
//
// Bracketed markup ("[pause 1s]", "[[word|say]]") is never touched. Forms
// it is unsure about (numbers glued to Latin letters such as "MP3", version
// numbers, phone numbers with dashes, IP addresses) are left as written for
// the engine's own front end.
//
// Languages: zh, en, ja, ko, es, fr, de. Adding one means writing a
// Language value (see lang_en.go for the shape) and calling Register from
// an init function.
package normalize

import (
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/hehehai/AudioSmith/internal/text"
)

// Options configure Text.
type Options struct {
	// Off disables normalisation: Text returns its input unchanged.
	Off bool
}

// Text normalises s for language lang (a BCP-47 tag; only the primary
// subtag is used). With an unsupported language only control characters
// are removed. An empty lang ("auto") is guessed from the script of s.
func Text(s, lang string, opt Options) string {
	if opt.Off || s == "" {
		return s
	}
	if lang == "" || strings.EqualFold(lang, "auto") {
		lang = Guess(s)
	}
	l := lookup(lang)
	return text.Outside(s, func(run string) string {
		run = prepare(run)
		if l == nil {
			return run
		}
		return l.apply(run)
	})
}

// Supported reports whether lang has normalisation rules.
func Supported(lang string) bool { return lookup(lang) != nil }

// Languages lists the language codes with rules, sorted.
func Languages() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

var (
	mu       sync.RWMutex
	registry = map[string]*Language{}
)

// Register adds (or replaces) a language's rules. It compiles the rules
// once; call it from an init function.
func Register(l *Language) {
	l.compile()
	mu.Lock()
	registry[l.Code] = l
	mu.Unlock()
}

func lookup(lang string) *Language {
	base := strings.ToLower(lang)
	if i := strings.IndexAny(base, "-_"); i >= 0 {
		base = base[:i]
	}
	switch base { // macrolanguage members that share rules
	case "cmn", "yue", "wuu":
		base = "zh"
	case "jpn":
		base = "ja"
	case "kor":
		base = "ko"
	}
	mu.RLock()
	defer mu.RUnlock()
	return registry[base]
}

// Guess picks a language from the script of s: kana → ja, Hangul → ko,
// Han → zh, otherwise en.
func Guess(s string) string {
	han := false
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r):
			return "ja"
		case unicode.Is(unicode.Hangul, r):
			return "ko"
		case unicode.Is(unicode.Han, r):
			han = true
		}
	}
	if han {
		return "zh"
	}
	return "en"
}

// prepare strips control characters (keeping tab and newlines) and folds
// full-width digits and the symbols the rules look for to ASCII.
func prepare(s string) string {
	clean := true
	for _, r := range s {
		if isControl(r) || (r >= '０' && r <= '９') || r == '％' || r == '＄' || r == '：' || r == '．' {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	rs := []rune(s)
	for i, r := range rs {
		switch {
		case isControl(r):
			continue
		case r >= '０' && r <= '９':
			b.WriteRune('0' + r - '０')
		case r == '％':
			b.WriteByte('%')
		case r == '＄':
			b.WriteByte('$')
		case (r == '：' || r == '．') && i > 0 && i+1 < len(rs) && isDigitish(rs[i-1]) && isDigitish(rs[i+1]):
			if r == '：' {
				b.WriteByte(':')
			} else {
				b.WriteByte('.')
			}
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isDigitish(r rune) bool { return (r >= '0' && r <= '9') || (r >= '０' && r <= '９') }

func isControl(r rune) bool {
	if r == '\t' || r == '\n' || r == '\r' {
		return false
	}
	if unicode.IsControl(r) {
		return true
	}
	switch r {
	case '\u200b', '\u2060', '\ufeff', '\u00ad': // zero-width space, word joiner, BOM, soft hyphen
		return true
	}
	return false
}

// Rule is one rewrite: every match of Re is passed to Fn with the whole
// run (for context checks) and the match's submatch indices; Fn returns
// the replacement, or ok=false to keep the original.
type Rule struct {
	Re *regexp.Regexp
	Fn func(s string, m []int) (out string, ok bool)
}

func (r Rule) apply(s string) string {
	ms := r.Re.FindAllStringSubmatchIndex(s, -1)
	if ms == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range ms {
		out, ok := r.Fn(s, m)
		if !ok {
			continue
		}
		b.WriteString(s[last:m[0]])
		b.WriteString(out)
		last = m[1]
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// group returns submatch n of m in s, or "".
func group(s string, m []int, n int) string {
	if 2*n+1 >= len(m) || m[2*n] < 0 {
		return ""
	}
	return s[m[2*n]:m[2*n+1]]
}

// glued reports whether the match s[a:b] touches something that makes it
// unsafe to rewrite (see gluedBefore and gluedAfter).
func glued(s string, a, b int) bool { return gluedBefore(s, a) || gluedAfter(s, b) }

// gluedBefore: s[a:] follows a letter of a spaced script other than Hangul
// ("MP3", "COVID-19"), a digit, or a digit and one of ".,:/-_" ("1.2.3",
// "192.168.1.1", "555-1234").
func gluedBefore(s string, a int) bool {
	if a == 0 {
		return false
	}
	r, n := utf8.DecodeLastRuneInString(s[:a])
	if isGlueLetter(r) || unicode.IsDigit(r) {
		return true
	}
	if strings.ContainsRune(".,:/-_", r) && a-n > 0 {
		r2, _ := utf8.DecodeLastRuneInString(s[:a-n])
		return unicode.IsDigit(r2)
	}
	return false
}

// gluedAfter: s[:b] is followed by such a letter or digit, or by one of
// ".,:/-_" and a digit ("4K", "1.2.3", "24/7").
func gluedAfter(s string, b int) bool {
	if b >= len(s) {
		return false
	}
	r, n := utf8.DecodeRuneInString(s[b:])
	if isGlueLetter(r) || unicode.IsDigit(r) {
		return true
	}
	if strings.ContainsRune(".,:/-_", r) && b+n < len(s) {
		r2, _ := utf8.DecodeRuneInString(s[b+n:])
		return unicode.IsDigit(r2)
	}
	return false
}

func isGlueLetter(r rune) bool {
	return unicode.IsLetter(r) && !text.IsUnspaced(r) && !unicode.Is(unicode.Hangul, r)
}

// letterBefore / letterAfter: a word character directly before a / after b.
func letterBefore(s string, a int) bool {
	if a == 0 {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(s[:a])
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

func letterAfter(s string, b int) bool {
	if b >= len(s) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s[b:])
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}
