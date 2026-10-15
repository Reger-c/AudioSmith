package translate

import (
	"strings"
	"unicode"

	"github.com/hehehai/AudioSmith/internal/llm"
)

// Shortened lines are checked before they replace a translation: small
// local models shortening to a time budget write broken mixtures such as
// 「水 overnight 就涨起来」 or 「How熬过去？」 and fragments far shorter than
// the slot needs (thread #44). A rejected rewrite keeps the full
// translation and warns WarnFitFailed.

// Reasons a rewrite is rejected.
const (
	fitRejectEmpty    = "empty"
	fitRejectMixed    = "mixed_script" // words in another script that the translation did not have
	fitRejectTooShort = "too_short"    // cut far below what the slot needs
)

// minBudgetShare: a rewrite shorter than this share of the slot's budget
// (and of the original) cut more than the slot needed.
const minBudgetShare = 0.5

// fitRejected says why cand must not replace orig ("" = it may). budget
// is the speech units that fit the slot.
func fitRejected(orig, cand string, tgt Lang, budget float64) string {
	cand = strings.TrimSpace(cand)
	if cand == "" {
		return fitRejectEmpty
	}
	if foreignWords(cand, orig, tgt) {
		return fitRejectMixed
	}
	ou, _ := Units(orig, tgt.Tag)
	cu, _ := Units(cand, tgt.Tag)
	if budget > 0 && cu < minBudgetShare*budget && cu < minBudgetShare*ou {
		return fitRejectTooShort
	}
	return ""
}

// cjkCode reports languages written without Latin letters (names, brands
// and terms aside).
func cjkCode(code string) bool {
	switch code {
	case "zh", "yue", "ja", "ko":
		return true
	}
	return false
}

func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

// foreignWords reports whether cand has words in a script foreign to tgt
// that orig does not have: Latin words in a CJK line ("overnight"), CJK
// characters in a Latin-script line ("熬").
func foreignWords(cand, orig string, tgt Lang) bool {
	if cjkCode(tgt.Code) {
		have := map[string]bool{}
		for _, w := range latinWords(orig) {
			have[strings.ToLower(w)] = true
		}
		for _, w := range latinWords(cand) {
			if !have[strings.ToLower(w)] {
				return true
			}
		}
		return false
	}
	for _, r := range cand {
		if isCJK(r) && !strings.ContainsRune(orig, r) {
			return true
		}
	}
	return false
}

// latinWords are the runs of two or more Latin letters in s (single
// letters such as "A" in "A股" or "T 恤" stay).
func latinWords(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) >= 2 {
			out = append(out, string(cur))
		}
		cur = cur[:0]
	}
	for _, r := range s {
		if unicode.Is(unicode.Latin, r) {
			cur = append(cur, r)
			continue
		}
		flush()
	}
	flush()
	return out
}

// localRoute reports whether skill's first route runs on this Mac (the
// engine, Ollama, LM Studio): small models that over-shorten when shown
// a time budget.
func localRoute(r *llm.Router, s llm.Skill) bool {
	if r == nil || r.Registry == nil {
		return false
	}
	routes := r.Routes(s)
	if len(routes) == 0 {
		return false
	}
	b, ok := r.Registry.Backend(routes[0].Provider)
	return ok && b.Info().Local
}
