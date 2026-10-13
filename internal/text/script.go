package text

import (
	"strings"
	"time"
	"unicode"
)

// IsCJK reports whether r is a Han ideograph, kana, Bopomofo, Hangul or a
// CJK symbol/full-width form: characters that are typeset without spaces
// and counted one per "word" in CJK runtime estimates.
func IsCJK(r rune) bool {
	switch {
	case r < 0x2E80:
		return false
	case unicode.Is(unicode.Han, r), unicode.Is(unicode.Hiragana, r), unicode.Is(unicode.Katakana, r),
		unicode.Is(unicode.Bopomofo, r), unicode.Is(unicode.Hangul, r):
		return true
	case r >= 0x3000 && r <= 0x303F: // CJK symbols and punctuation
		return true
	case r >= 0xFF00 && r <= 0xFFEF: // half/full-width forms
		return true
	}
	return false
}

// IsUnspaced reports whether r belongs to a script written without spaces
// between words (Han, kana, Thai, Lao, Khmer, Myanmar, Tibetan), where a
// term may match anywhere instead of only at word boundaries. Hangul is
// spaced and is not included.
func IsUnspaced(r rune) bool {
	if r < 0x0E00 {
		return false
	}
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Thai, r) || unicode.Is(unicode.Lao, r) || unicode.Is(unicode.Khmer, r) ||
		unicode.Is(unicode.Myanmar, r) || unicode.Is(unicode.Tibetan, r) || unicode.Is(unicode.Bopomofo, r) ||
		(r >= 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFFEF)
}

// IsWordRune reports whether r is part of a word: a letter, a digit or a
// combining mark.
func IsWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r)
}

// Count counts the words of s the way a script editor's stats line does:
// words is the number of space-separated words in spaced scripts (Latin,
// Cyrillic, Hangul…); chars is the number of characters in unspaced
// scripts (Han, kana, Thai…), each of which is read on its own. Bracket
// tags are not counted.
func Count(s string) (words, chars int) {
	inWord := false
	for _, t := range splitOutside(s) {
		for _, r := range t {
			switch {
			case IsUnspaced(r):
				if unicode.IsLetter(r) || unicode.IsDigit(r) {
					chars++
				}
				inWord = false
			case IsWordRune(r) || (inWord && (r == '\'' || r == '’' || r == '-')):
				if !inWord {
					words++
					inWord = true
				}
			default:
				inWord = false
			}
		}
		inWord = false
	}
	return words, chars
}

// SplitOverride splits an inline pronunciation override "[[word|say]]" into
// its parts (trimmed). ok is false when tag is not of that form.
func SplitOverride(tag string) (word, say string, ok bool) {
	if len(tag) < 5 || tag[:2] != "[[" || tag[len(tag)-2:] != "]]" {
		return "", "", false
	}
	inner := tag[2 : len(tag)-2]
	i := strings.IndexByte(inner, '|')
	if i < 0 {
		return strings.TrimSpace(inner), "", false
	}
	return strings.TrimSpace(inner[:i]), strings.TrimSpace(inner[i+1:]), true
}

// Reading rates used by Estimate: 155 words a minute for spaced scripts
// (the audiobook industry's narration rate) and 4.5 characters a second
// for unspaced ones (Mandarin/Japanese narration).
const (
	WordsPerMinute = 155
	CharsPerSecond = 4.5
)

// Estimate is the expected narration time for words spaced-script words
// and chars unspaced-script characters.
func Estimate(words, chars int) time.Duration {
	sec := float64(words)*60/WordsPerMinute + float64(chars)/CharsPerSecond
	return time.Duration(sec * float64(time.Second))
}

func splitOutside(s string) []string {
	tags := Tags(s)
	if len(tags) == 0 {
		return []string{s}
	}
	out := make([]string, 0, len(tags)+1)
	prev := 0
	for _, t := range tags {
		out = append(out, s[prev:t.Start])
		if w, _, ok := SplitOverride(t.Of(s)); ok {
			out = append(out, w) // an override's word is spoken
		}
		prev = t.End
	}
	return append(out, s[prev:])
}
