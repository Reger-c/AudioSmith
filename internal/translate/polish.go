package translate

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// PolishOptions tune Polish.
type PolishOptions struct {
	// KeepFillers leaves unambiguous hesitation sounds (um, uh, erm, 嗯, 呃)
	// in place. By default they are removed.
	KeepFillers bool
	// CJKLatinSpace puts a space between CJK and Latin letters or digits
	// ("用Python写" → "用 Python 写"). By default existing spacing is kept.
	CJKLatinSpace bool
	// NoTerminal leaves a missing final full stop missing.
	NoTerminal bool
}

var (
	reSpaces       = regexp.MustCompile(`[ \t\x{00A0}\x{3000}]+`)
	reBlankLines   = regexp.MustCompile(`\n{3,}`)
	reSpaceBefore  = regexp.MustCompile(` +([,.;:!?%)\]}])`)
	reEnFiller     = regexp.MustCompile(`(?i)(^|[\s,;])(?:u+m+|u+h+m*|e+r+m+|e+r+|h+m+|m+h*m+)(?:[,.;]*)(\s|$)`)
	reZhFiller     = regexp.MustCompile(`(^|[，,。！？\s])(?:嗯+|呃+|额+|唔+)[，,、\s]*`)
	reDupComma     = regexp.MustCompile(`([,，])\s*[,，]`)
	reLeadingPunct = regexp.MustCompile(`^[\s,，、;；:：]+`)
	reEnI          = regexp.MustCompile(`(^|[\s(“"])i(['’](?:m|d|ll|ve)\b|[\s,;:!?]|$)`)
)

// Polish is the deterministic dictation polish that needs no model:
// whitespace, filler sounds, punctuation spacing, CJK spacing and
// full-width punctuation, sentence capitalisation, "i" → "I" (English)
// and a terminal full stop. lang is a BCP-47 tag or "" to infer from the
// script. It is idempotent.
func Polish(s, lang string, opt PolishOptions) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(reSpaces.ReplaceAllString(l, " "))
	}
	s = strings.TrimSpace(reBlankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
	if s == "" {
		return ""
	}
	code := codeOf(lang)
	cjk := code == "zh" || code == "ja" || code == "yue" || (lang == "" && cjkShare(s) > 0.3)
	if !opt.KeepFillers {
		// English hesitations only in English (or unknown, non-CJK) text:
		// "er" is German for "he".
		if code == "en" || lang == "" && !cjk || cjk && cjkShare(s) < 1 {
			for range 2 { // adjacent fillers share a separator
				s = reEnFiller.ReplaceAllString(s, "$1$2")
			}
		}
		if cjk {
			s = reZhFiller.ReplaceAllString(s, "$1")
		}
		s = reDupComma.ReplaceAllString(s, "$1")
		s = reLeadingPunct.ReplaceAllString(s, "")
		s = strings.TrimSpace(reSpaces.ReplaceAllString(s, " "))
	}
	s = reSpaceBefore.ReplaceAllString(s, "$1")
	s = cjkSpacing(s, opt.CJKLatinSpace)
	if code == "en" || code == "" && !cjk {
		s = reEnI.ReplaceAllString(s, "${1}I$2")
	}
	s = capitalise(s)
	if !opt.NoTerminal {
		s = terminal(s, cjk)
	}
	return s
}

func cjkShare(s string) float64 {
	n, c := 0, 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			n++
			if isCJKRune(r) {
				c++
			}
		}
	}
	if n == 0 {
		return 0
	}
	return float64(c) / float64(n)
}

func isCJKRune(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Bopomofo, r)
}

var fullWidth = map[rune]rune{',': '，', '.': '。', '?': '？', '!': '！', ':': '：', ';': '；'}

// cjkSpacing removes spaces between CJK characters (and CJK punctuation),
// turns ASCII punctuation after a CJK character into full width, and
// optionally spaces CJK from Latin letters and digits.
func cjkSpacing(s string, latinSpace bool) string {
	rs := []rune(s)
	var b strings.Builder
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		prev := rune(0)
		if b.Len() > 0 {
			prev, _ = utf8.DecodeLastRuneInString(b.String())
		}
		if r == ' ' {
			next := rune(0)
			if i+1 < len(rs) {
				next = rs[i+1]
			}
			if (isCJKRune(prev) || isCJKPunct(prev)) && (isCJKRune(next) || isCJKPunct(next)) {
				continue
			}
			if isCJKPunct(prev) || isCJKPunct(next) {
				continue
			}
			b.WriteRune(r)
			continue
		}
		if fw, ok := fullWidth[r]; ok && isCJKRune(prev) {
			// Not inside numbers or URLs: "3.14", "v1.2".
			next := rune(0)
			if i+1 < len(rs) {
				next = rs[i+1]
			}
			if !(r == '.' && (unicode.IsDigit(next) || unicode.IsLetter(next) && !isCJKRune(next))) {
				b.WriteRune(fw)
				if i+1 < len(rs) && rs[i+1] == ' ' {
					i++
				}
				continue
			}
		}
		if latinSpace && prev != 0 {
			if (isCJKRune(prev) && isLatinOrDigit(r)) || (isLatinOrDigit(prev) && isCJKRune(r)) {
				b.WriteRune(' ')
			}
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isCJKPunct(r rune) bool {
	return strings.ContainsRune("，。！？：；、“”‘’「」『』（）《》【】…—", r)
}

func isLatinOrDigit(r rune) bool {
	return r < 0x250 && (unicode.IsLetter(r) || unicode.IsDigit(r))
}

// capitalise upper-cases the first letter of the text and of each
// sentence, for cased scripts only.
func capitalise(s string) string {
	rs := []rune(s)
	start := true
	for i, r := range rs {
		switch {
		case start && unicode.IsLetter(r):
			if unicode.IsLower(r) && !looksLikeCode(rs, i) {
				rs[i] = unicode.ToUpper(r)
			}
			start = false
		case start && (unicode.IsDigit(r) || isCJKRune(r)):
			start = false
		case r == '.' || r == '!' || r == '?' || r == '\n':
			// "e.g." / "3.5" / "example.com" are not sentence ends.
			if r == '.' && i+1 < len(rs) && !unicode.IsSpace(rs[i+1]) {
				continue
			}
			if r == '.' && i >= 2 && rs[i-2] == '.' {
				continue
			}
			start = true
		}
	}
	return string(rs)
}

// looksLikeCode spots identifiers that must keep their case: "iPhone",
// "macOS", "npm", "git".
func looksLikeCode(rs []rune, i int) bool {
	end := i
	for end < len(rs) && (unicode.IsLetter(rs[end]) || unicode.IsDigit(rs[end]) || rs[end] == '_' || rs[end] == '-' || rs[end] == '.') {
		end++
	}
	word := string(rs[i:end])
	for _, r := range word[1:] {
		if unicode.IsUpper(r) {
			return true // camelCase / iPhone
		}
	}
	return strings.ContainsAny(word, "_") || strings.Count(word, ".") > 0 && !strings.HasSuffix(word, ".")
}

// terminal adds a final full stop when the text ends in a letter or digit.
func terminal(s string, cjk bool) string {
	last, _ := utf8.DecodeLastRuneInString(s)
	if !(unicode.IsLetter(last) || unicode.IsDigit(last)) {
		return s
	}
	if cjk || isCJKRune(last) {
		return s + "。"
	}
	return s + "."
}
