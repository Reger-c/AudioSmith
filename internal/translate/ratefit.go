package translate

import (
	"math"
	"strings"
	"sync"
	"unicode"

	"github.com/hehehai/AudioSmith/internal/lang"
	"github.com/hehehai/AudioSmith/internal/text/markup"
)

// Speech-rate fit: predict how long a TTS voice takes to say a line, and
// compare it with the line's time slot.
//
// The unit is the syllable (morae for Japanese, characters for Chinese,
// syllable blocks for Korean, aksharas for Indic scripts), counted with
// script rules; each language has a rate in units per second at speed 1.
// Pauses come from punctuation and [pause] tags. The defaults are tuned
// for neural TTS at its normal speed (slower than natural reading
// speech); Calibrate learns a voice's real rate from rendered lines.

// Rate is a language's speaking rate.
type Rate struct {
	PerSecond float64 `json:"perSecond"` // units per second at speed 1
	// CharsPerUnit converts units to written characters (spaces and
	// punctuation included) for "at most N characters" prompt budgets.
	CharsPerUnit float64 `json:"charsPerUnit"`
}

// DefaultRates are the built-in rates by internal/lang code.
var DefaultRates = map[string]Rate{
	"en": {3.9, 3.1}, "de": {4.0, 3.3}, "nl": {4.2, 3.1}, "sv": {4.4, 3.0}, "da": {4.4, 3.0}, "nb": {4.4, 3.0}, "no": {4.4, 3.0}, "is": {4.2, 3.0},
	"fr": {5.0, 2.7}, "es": {5.2, 2.5}, "it": {5.0, 2.5}, "pt": {4.9, 2.6}, "ca": {5.0, 2.6}, "ro": {5.0, 2.6}, "gl": {5.0, 2.6},
	"pl": {4.6, 2.9}, "cs": {4.6, 2.8}, "sk": {4.6, 2.8}, "sl": {4.6, 2.8}, "hr": {4.6, 2.8}, "sr": {4.6, 2.8}, "bs": {4.6, 2.8},
	"ru": {4.6, 2.9}, "uk": {4.6, 2.9}, "be": {4.6, 2.9}, "bg": {4.6, 2.8}, "mk": {4.6, 2.8},
	"el": {4.8, 2.7}, "tr": {5.0, 2.8}, "az": {5.0, 2.8}, "fi": {4.6, 2.9}, "et": {4.6, 2.9}, "hu": {4.6, 2.9}, "lt": {4.6, 2.9}, "lv": {4.6, 2.9},
	"id": {5.0, 2.6}, "ms": {5.0, 2.6}, "tl": {5.0, 2.6}, "sw": {5.0, 2.6}, "vi": {4.0, 3.4},
	"zh": {4.5, 1.1}, "yue": {4.5, 1.1}, "ja": {6.5, 0.75}, "ko": {5.5, 1.25},
	"th": {4.5, 3.0}, "lo": {4.5, 3.0}, "km": {4.2, 3.0}, "my": {4.2, 3.0},
	"ar": {4.4, 2.2}, "he": {4.6, 2.2}, "fa": {4.6, 2.2}, "ur": {4.6, 2.2},
	"hi": {4.8, 2.0}, "bn": {4.8, 2.0}, "mr": {4.8, 2.0}, "ne": {4.8, 2.0}, "gu": {4.8, 2.0}, "pa": {4.8, 2.0},
	"ta": {4.6, 2.0}, "te": {4.6, 2.0}, "kn": {4.6, 2.0}, "ml": {4.4, 2.0},
}

// FallbackRate is used for languages without an entry.
var FallbackRate = Rate{4.5, 2.8}

// Pause lengths added by punctuation, in seconds.
const (
	PauseComma     = 0.15 // , ; : 、 ， ； ： — (and a dash)
	PauseSentence  = 0.30 // . ! ? 。 ！ ？ … between sentences (not after the last)
	PauseNonVerbal = 0.5  // [laughter], [sigh]…
)

// Verdict classifies a fit.
type Verdict string

const (
	FitUnknown  Verdict = ""         // no slot
	FitOK       Verdict = "ok"       // comfortably inside the slot
	FitTight    Verdict = "tight"    // close to (or slightly over) the slot; time-stretch can absorb it
	FitOverflow Verdict = "overflow" // won't fit without rewriting
)

// Fit is a duration prediction against a slot.
type Fit struct {
	Predicted float64 `json:"predicted"` // seconds at Speed
	Slot      float64 `json:"slot"`
	Ratio     float64 `json:"ratio"`          // Predicted / Slot
	Over      float64 `json:"over,omitempty"` // seconds over the slot, 0 when it fits
	Verdict   Verdict `json:"verdict"`
}

// Thresholds on Predicted/Slot.
type Thresholds struct {
	OK    float64 // ≤ OK → ok (default 0.95)
	Tight float64 // ≤ Tight → tight, above → overflow (default 1.10)
}

// Predictor predicts spoken durations. The zero value uses DefaultRates.
// Goroutine-safe.
type Predictor struct {
	// Rates override DefaultRates per language code.
	Rates      map[string]Rate
	Thresholds Thresholds

	mu      sync.Mutex
	learned map[string]Rate
}

// DefaultPredictor is shared by the package's translators.
var DefaultPredictor = &Predictor{}

func (p *Predictor) rate(code string) Rate {
	p.mu.Lock()
	r, ok := p.learned[code]
	p.mu.Unlock()
	if ok {
		return r
	}
	if r, ok := p.Rates[code]; ok && r.PerSecond > 0 {
		return r
	}
	if r, ok := DefaultRates[code]; ok {
		return r
	}
	if l, ok := lang.Lookup(code); ok && l.Macro != "" {
		if r, ok := DefaultRates[l.Macro]; ok {
			return r
		}
	}
	return FallbackRate
}

// RateFor returns the rate used for lang (any tag).
func (p *Predictor) RateFor(tag string) Rate { return p.rate(codeOf(tag)) }

func codeOf(tag string) string {
	if c := lang.Canonical(tag); c != "" {
		return c
	}
	return strings.ToLower(tag)
}

// Units counts the speech units of s in lang, plus the seconds of pauses
// (punctuation, [pause], non-verbal tags) it implies.
func Units(s, tag string) (units, pause float64) {
	code := codeOf(tag)
	doc := markup.Parse(s, &markup.Options{NoChapters: true})
	factor := 1.0 // [slow]/[fast] scale units
	spell := false
	for _, t := range doc.Tokens {
		switch t.Kind {
		case markup.Text:
			if spell {
				units += float64(countLetters(t.Text)) * 1.3 * factor
				continue
			}
			u, p := textUnits(t.Text, code)
			units += u * factor
			pause += p
		case markup.Override:
			u, _ := textUnits(t.Say, code)
			units += u * factor
		case markup.Pause:
			pause += float64(t.Millis) / 1000
		case markup.NonVerbal:
			pause += PauseNonVerbal
		case markup.StyleOn:
			switch t.Name {
			case markup.Slow:
				factor = 1 / 0.85
			case markup.Fast:
				factor = 1 / 1.15
			case markup.Spell:
				spell = true
			}
		case markup.StyleOff:
			switch t.Name {
			case markup.Slow, markup.Fast:
				factor = 1
			case markup.Spell:
				spell = false
			}
		case markup.Paragraph:
			pause += PauseSentence
		}
	}
	// No pause after the line's final punctuation.
	trimmed := strings.TrimRightFunc(doc.Plain(), func(r rune) bool { return unicode.IsSpace(r) || isQuote(r) })
	if r := lastRune(trimmed); isSentenceEnd(r) {
		pause -= PauseSentence
	} else if isCommaLike(r) {
		pause -= PauseComma
	}
	return units, math.Max(pause, 0)
}

// Duration predicts the spoken length of s in seconds at speed (0 = 1).
func (p *Predictor) Duration(s, tag string, speed float64) float64 {
	if speed <= 0 {
		speed = 1
	}
	u, pause := Units(s, tag)
	r := p.rate(codeOf(tag))
	return (u/r.PerSecond + pause) / speed
}

// Fit predicts s against a slot of seconds; Verdict is FitUnknown when slot ≤ 0.
func (p *Predictor) Fit(s, tag string, slot, speed float64) Fit {
	d := p.Duration(s, tag, speed)
	f := Fit{Predicted: round2(d), Slot: slot}
	if slot <= 0 {
		return f
	}
	f.Ratio = round2(d / slot)
	if d > slot {
		f.Over = round2(d - slot)
	}
	ok, tight := p.Thresholds.OK, p.Thresholds.Tight
	if ok <= 0 {
		ok = 0.95
	}
	if tight <= 0 {
		tight = 1.10
	}
	switch r := d / slot; {
	case r <= ok:
		f.Verdict = FitOK
	case r <= tight:
		f.Verdict = FitTight
	default:
		f.Verdict = FitOverflow
	}
	return f
}

// Budget returns the units and approximate characters that fit in seconds
// at speed, after reserving pause, for "at most N characters" hints.
func (p *Predictor) Budget(tag string, seconds, speed, pause float64) (units, chars int) {
	if speed <= 0 {
		speed = 1
	}
	r := p.rate(codeOf(tag))
	u := math.Max(seconds*speed-pause, 0) * r.PerSecond
	return int(u), int(u * r.CharsPerUnit)
}

// Calibrate learns lang's rate from a rendered line: s took seconds at
// speed. It keeps a moving average (new evidence weighs 30%) and ignores
// lines shorter than 4 units or 1 s.
func (p *Predictor) Calibrate(s, tag string, seconds, speed float64) {
	if speed <= 0 {
		speed = 1
	}
	u, pause := Units(s, tag)
	speech := seconds*speed - pause
	if u < 4 || seconds < 1 || speech <= 0.2 {
		return
	}
	code := codeOf(tag)
	cur := p.rate(code)
	obs := u / speech
	if obs < cur.PerSecond/3 || obs > cur.PerSecond*3 {
		return // a silence-padded or broken render
	}
	cur.PerSecond = 0.7*cur.PerSecond + 0.3*obs
	p.mu.Lock()
	if p.learned == nil {
		p.learned = map[string]Rate{}
	}
	p.learned[code] = cur
	p.mu.Unlock()
}

// ResetCalibration forgets learned rates.
func (p *Predictor) ResetCalibration() {
	p.mu.Lock()
	p.learned = nil
	p.mu.Unlock()
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }

// textUnits counts units and punctuation pauses in plain text.
func textUnits(s, code string) (units, pause float64) {
	rs := []rune(s)
	var word []rune
	flushWord := func() {
		if len(word) > 0 {
			units += alphaUnits(word, code)
			word = word[:0]
		}
	}
	digits := 0
	flushDigits := func() {
		if digits > 0 {
			units += 1.3 * float64(digits)
			if code == "zh" || code == "ja" || code == "yue" {
				units -= 0.3 * float64(digits) // 二零二四: one character per digit
			}
			digits = 0
		}
	}
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case unicode.IsDigit(r):
			flushWord()
			digits++
			continue
		case r == '.' || r == ',':
			if digits > 0 && i+1 < len(rs) && unicode.IsDigit(rs[i+1]) {
				continue // 3.14, 1,000
			}
		}
		flushDigits()
		switch {
		case unicode.Is(unicode.Han, r):
			flushWord()
			if code == "ja" {
				units += 1.7 // on-yomi / kun-yomi average in morae
			} else {
				units++
			}
		case unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r):
			flushWord()
			if !strings.ContainsRune("ぁぃぅぇぉゃゅょゎァィゥェォャュョヮ・", r) {
				units++
			}
		case unicode.Is(unicode.Hangul, r):
			flushWord()
			if r >= 0xAC00 && r <= 0xD7A3 {
				units++
			}
		case unicode.Is(unicode.Thai, r) || unicode.Is(unicode.Lao, r) || unicode.Is(unicode.Khmer, r) || unicode.Is(unicode.Myanmar, r):
			flushWord()
			if unicode.IsLetter(r) {
				units += 1.0 / 3
			}
		case isIndic(r):
			flushWord()
			if unicode.Is(unicode.Lo, r) && !(i+1 < len(rs) && isVirama(rs[i+1])) {
				units++
			}
		case unicode.Is(unicode.Arabic, r) || unicode.Is(unicode.Hebrew, r) || unicode.Is(unicode.Syriac, r) || unicode.Is(unicode.Thaana, r):
			flushWord()
			if unicode.IsLetter(r) {
				units += 0.55 // abjads: about one syllable per two letters
			}
		case unicode.IsLetter(r) || unicode.Is(unicode.Mn, r) || r == '\'' || r == '’':
			word = append(word, r)
		default:
			flushWord()
			switch {
			case isSentenceEnd(r):
				// "?!" and "..." count once.
				if i == 0 || !isSentenceEnd(rs[i-1]) {
					pause += PauseSentence
				}
			case isCommaLike(r):
				pause += PauseComma
			case r == '—' || r == '–':
				pause += PauseComma
			}
		}
	}
	flushWord()
	flushDigits()
	return units, pause
}

func isSentenceEnd(r rune) bool { return strings.ContainsRune(".!?。！？…؟।", r) }
func isCommaLike(r rune) bool   { return strings.ContainsRune(",;:、，；：،", r) }
func isQuote(r rune) bool       { return strings.ContainsRune("\"'”’」』)）»", r) }

func lastRune(s string) rune {
	rs := []rune(s)
	if len(rs) == 0 {
		return 0
	}
	return rs[len(rs)-1]
}

func isIndic(r rune) bool { return r >= 0x0900 && r <= 0x0DFF }

func isVirama(r rune) bool {
	switch r {
	case 0x094D, 0x09CD, 0x0A4D, 0x0ACD, 0x0B4D, 0x0BCD, 0x0C4D, 0x0CCD, 0x0D4D, 0x0DCA:
		return true
	}
	return false
}

func countLetters(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			n++
		}
	}
	return n
}

const (
	latinVowels    = "aeiouyàáâãäåæèéêëìíîïòóôõöøœùúûüýÿāăąēĕėęěīĭįıōŏőūŭůűųǎǐǒǔ"
	cyrillicVowels = "аеёиоуыэюяіїєў"
	greekVowels    = "αεηιουωάέήίόύώϊϋΐΰ"
)

// alphaUnits counts syllables in one word of an alphabetic script as
// vowel groups, with a few language rules.
func alphaUnits(w []rune, code string) float64 {
	if len(w) >= 2 && len(w) <= 5 && string(w) == strings.ToUpper(string(w)) && strings.ToUpper(string(w)) != strings.ToLower(string(w)) {
		return float64(len(w)) * 1.2 // an acronym, spelled out: "USA", "NHK"
	}
	word := strings.ToLower(string(w))
	vowels := latinVowels
	switch {
	case strings.ContainsAny(word, cyrillicVowels+"бвгджзклмнпрстфхцчшщ"):
		vowels = cyrillicVowels
	case strings.ContainsAny(word, greekVowels+"βγδζθκλμνξπρστφχψ"):
		vowels = greekVowels
	}
	rs := []rune(word)
	n := 0
	prev := false
	for i, r := range rs {
		v := strings.ContainsRune(vowels, r)
		if r == 'y' && code != "en" && code != "fr" && code != "nl" && code != "pl" && code != "cs" && code != "sk" && code != "fi" && code != "da" && code != "sv" && code != "nb" && code != "no" {
			v = true
		}
		if r == 'y' && code == "en" && i == 0 {
			v = false // "yes", "you"
		}
		if v && !prev {
			n++
		}
		prev = v
	}
	switch code {
	case "en":
		// Silent final e ("make"), but not "-le" ("table") or "the".
		if n > 1 && strings.HasSuffix(word, "e") && !strings.HasSuffix(word, "le") && !strings.HasSuffix(word, "ee") {
			n--
		}
		if n > 1 && (strings.HasSuffix(word, "es") || strings.HasSuffix(word, "ed")) && !strings.HasSuffix(word, "ted") && !strings.HasSuffix(word, "ded") && !strings.HasSuffix(word, "ses") && !strings.HasSuffix(word, "zes") && !strings.HasSuffix(word, "ces") && !strings.HasSuffix(word, "ges") {
			n--
		}
	case "fr":
		// Mute final e / es / ent ("parle", "parles", "parlent").
		if n > 1 && (strings.HasSuffix(word, "e") || strings.HasSuffix(word, "es") || strings.HasSuffix(word, "ent")) {
			n--
		}
	}
	if n == 0 {
		// Acronyms and vowel-less words ("NHK", "hmm", "Brno") still take time.
		return math.Max(1, float64(len(rs))*0.6)
	}
	return float64(n)
}
