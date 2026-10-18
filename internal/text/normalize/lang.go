package normalize

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Num is a parsed written number.
type Num struct {
	Neg  bool
	Int  string // digits without separators (may have leading zeros)
	Frac string // digits after the decimal separator; "" when none
}

// Value returns the integer part when it fits the readers (< 10^15).
func (n Num) Value() (uint64, bool) {
	if len(n.Int) == 0 || len(n.Int) > 15 {
		return 0, false
	}
	v, err := strconv.ParseUint(n.Int, 10, 64)
	return v, err == nil
}

// Currency is a currency a symbol stands for, as ISO 4217 code.
type Currency string

// Abbrev is an abbreviation and its expansion. Next restricts where it
// applies: 0 anywhere, 'N' before a capitalised word (titles: "Dr. Smith"),
// 'D' before a number ("No. 5"), 'S' anywhere, and its period is kept when
// it also ends the sentence ("etc." before a capital or at the end).
type Abbrev struct {
	From, To string
	Next     byte
}

// Language holds one language's readers. Required: Code, Cardinal,
// Digits. Every other field is optional; a nil reader disables its rule.
type Language struct {
	Code string
	// Spaced: words are joined with spaces (false for zh, ja).
	Spaced bool
	// DecimalComma: "3,5" is three and a half, "10.000" ten thousand.
	DecimalComma bool
	// SpaceGroups: a plain space may group thousands ("10 000"); NBSP and
	// narrow NBSP always may.
	SpaceGroups bool

	// Cardinal reads 0 ≤ n < 10^15.
	Cardinal func(n uint64) string
	// Digits are the names of 0–9, for digit-by-digit reading.
	Digits [10]string
	// DecimalWord separates the integer and fraction ("point", "点").
	DecimalWord string
	// FracAsNumber reads a fraction of one or two digits without a leading
	// zero as a number ("tres coma catorce"); otherwise digit by digit.
	FracAsNumber bool
	// Minus prefixes negative numbers.
	Minus string
	// Percent wraps a read number ("five percent", "百分之五").
	Percent func(words string) string
	// Ordinal reads n written with suffix ("1st", "1er"); OrdinalSuffix is
	// the regexp alternation of the suffixes it accepts.
	Ordinal       func(n uint64, suffix string) (string, bool)
	OrdinalSuffix string
	// Time reads a clock time; sec < 0 when absent. suffix is the text
	// TimeSuffix matched after it ("pm", "Uhr"), lower-cased.
	Time       func(h, m, sec int, suffix string) string
	TimeSuffix string
	// Date reads a calendar date (y may be 0 when absent).
	Date func(y, m, d int) string
	// DateOrder of slash dates: "MDY", "DMY" or "YMD"; DotDates also
	// accepts "05.03.2024" (day first).
	DateOrder string
	DotDates  bool
	// Symbols maps currency symbols to currencies; Money reads an amount.
	// Scale is a scale word written after the amount ("million"), or "".
	Symbols map[string]Currency
	Money   func(c Currency, n Num, scale string) string
	// MoneyScales is the alternation of scale words Money accepts.
	MoneyScales string
	// Range joins the two ends of "3–5" / "3~5".
	Range func(a, b string) string
	// Temperature reads "25°C"; unit is 'C' or 'F'.
	Temperature func(words string, unit byte) string
	// Abbrevs are expanded where they appear as whole words.
	Abbrevs []Abbrev
	// Pre rules run before the shared rules (forms those would misread);
	// Extra rules run after the shared date/time/money/percent rules and
	// before abbreviations and plain numbers.
	Pre   []Rule
	Extra []Rule

	rules []Rule
}

// Number reads a parsed number: digit by digit when it has leading zeros
// or more than 15 digits, else as a cardinal with an optional fraction.
func (l *Language) Number(n Num) string {
	var words string
	v, ok := n.Value()
	switch {
	case !ok || (len(n.Int) > 1 && n.Int[0] == '0' && n.Frac == ""):
		words = l.DigitsOf(n.Int)
	default:
		words = l.Cardinal(v)
	}
	if n.Frac != "" {
		words = l.Join(words, l.DecimalWord, l.FracOf(n.Frac))
	}
	if n.Neg {
		words = l.Join(l.Minus, words)
	}
	return words
}

// FracOf reads the digits after a decimal separator.
func (l *Language) FracOf(frac string) string {
	if l.FracAsNumber && len(frac) <= 2 && frac[0] != '0' {
		v, _ := strconv.ParseUint(frac, 10, 64)
		return l.Cardinal(v)
	}
	return l.DigitsOf(frac)
}

// DigitsOf reads a digit string digit by digit.
func (l *Language) DigitsOf(digits string) string {
	parts := make([]string, 0, len(digits))
	for _, c := range digits {
		if c >= '0' && c <= '9' {
			parts = append(parts, l.Digits[c-'0'])
		}
	}
	return l.Join(parts...)
}

// Join joins words with a space in spaced languages, directly otherwise.
// Empty parts are skipped.
func (l *Language) Join(parts ...string) string {
	sep := ""
	if l.Spaced {
		sep = " "
	}
	out := parts[:0:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

// ParseNum parses a number as matched by the language's number pattern.
func (l *Language) ParseNum(s string) Num {
	var n Num
	if strings.HasPrefix(s, "-") {
		n.Neg, s = true, s[1:]
	} else if strings.HasPrefix(s, "−") {
		n.Neg, s = true, s[len("−"):]
	}
	dec := byte('.')
	if l.DecimalComma {
		dec = ','
	}
	if i := strings.LastIndexByte(s, dec); i >= 0 {
		n.Frac = s[i+1:]
		s = s[:i]
	}
	var b strings.Builder
	for _, c := range s {
		if c >= '0' && c <= '9' {
			b.WriteRune(c)
		}
	}
	n.Int = b.String()
	return n
}

// NumPattern is the regexp (without groups) of a written number in this
// language: optional sign, digits with optional thousands grouping, and
// an optional fraction.
func (l *Language) NumPattern() string {
	grp, dec := `,`, `\.`
	if l.DecimalComma {
		grp, dec = `\.`, `,`
	}
	seps := `\x{00a0}\x{202f}`
	if l.SpaceGroups {
		seps += ` `
	}
	return `[-−]?(?:\d{1,3}(?:` + grp + `\d{3})+|\d{1,3}(?:[` + seps + `]\d{3})+|\d+)(?:` + dec + `\d+)?`
}

const sp = `[ \t\x{00a0}\x{202f}]` // a space inside a written form

func (l *Language) compile() {
	num := `(` + l.NumPattern() + `)`
	rules := append([]Rule(nil), l.Pre...)
	add := func(re string, fn func(s string, m []int) (string, bool)) {
		rules = append(rules, Rule{Re: regexp.MustCompile(re), Fn: fn})
	}
	if l.Date != nil {
		add(`(\d{4})-(\d{1,2})-(\d{1,2})`, func(s string, m []int) (string, bool) {
			return l.date(s, m, group(s, m, 1), group(s, m, 2), group(s, m, 3))
		})
		if l.DateOrder != "" {
			add(`(\d{1,4})/(\d{1,2})/(\d{1,4})`, func(s string, m []int) (string, bool) {
				a, b, c := group(s, m, 1), group(s, m, 2), group(s, m, 3)
				switch l.DateOrder {
				case "MDY":
					return l.date(s, m, c, a, b)
				case "DMY":
					return l.date(s, m, c, b, a)
				default:
					return l.date(s, m, a, b, c)
				}
			})
		}
		if l.DotDates {
			add(`(\d{1,2})\.(\d{1,2})\.(\d{4})`, func(s string, m []int) (string, bool) {
				return l.date(s, m, group(s, m, 3), group(s, m, 2), group(s, m, 1))
			})
		}
	}
	if l.Time != nil {
		re := `(\d{1,2}):(\d{2})(?::(\d{2}))?`
		if l.TimeSuffix != "" {
			re += `(?:` + sp + `?(` + l.TimeSuffix + `))?`
		}
		add(re, func(s string, m []int) (string, bool) {
			end := m[5]
			if m[7] >= 0 {
				end = m[7]
			}
			suffix, rest := "", s[end:m[1]]
			if len(m) > 8 && m[8] >= 0 && !letterAfter(s, m[1]) {
				suffix, rest = strings.ToLower(s[m[8]:m[9]]), ""
			}
			if gluedBefore(s, m[0]) || (suffix == "" && gluedAfter(s, end)) {
				return "", false
			}
			h, _ := strconv.Atoi(group(s, m, 1))
			mi, _ := strconv.Atoi(group(s, m, 2))
			sec := -1
			if m[7] >= 0 {
				sec, _ = strconv.Atoi(s[m[6]:m[7]])
			}
			if h > 24 || mi > 59 || sec > 59 {
				return "", false
			}
			return l.Time(h, mi, sec, suffix) + rest, true
		})
	}
	if l.Money != nil && len(l.Symbols) > 0 {
		syms := make([]string, 0, len(l.Symbols))
		for k := range l.Symbols {
			syms = append(syms, regexp.QuoteMeta(k))
		}
		slices.SortFunc(syms, func(a, b string) int { return len(b) - len(a) })
		alt := strings.Join(syms, "|")
		scale := ""
		if l.MoneyScales != "" {
			scale = `(?:` + sp + `?(` + l.MoneyScales + `))?`
		}
		add(`(`+alt+`)`+sp+`?`+num+scale, func(s string, m []int) (string, bool) {
			numEnd, end, sc := m[5], m[5], ""
			if len(m) > 6 && m[6] >= 0 && !letterAfter(s, m[7]) {
				end, sc = m[7], strings.ToLower(s[m[6]:m[7]])
			}
			if gluedBefore(s, m[0]) || (sc == "" && gluedAfter(s, numEnd)) {
				return "", false
			}
			return l.Money(l.Symbols[group(s, m, 1)], l.ParseNum(group(s, m, 2)), sc) + s[end:m[1]], true
		})
		add(num+sp+`?(`+alt+`)`, func(s string, m []int) (string, bool) {
			if gluedBefore(s, m[0]) || (isGlueLetter(lastRune(s[m[4]:m[5]])) && letterAfter(s, m[1])) {
				return "", false
			}
			return l.Money(l.Symbols[group(s, m, 2)], l.ParseNum(group(s, m, 1)), ""), true
		})
	}
	if l.Range != nil {
		add(num+sp+`?[–~～〜]`+sp+`?`+num+`(`+sp+`?%)?`, func(s string, m []int) (string, bool) {
			if gluedBefore(s, m[0]) || gluedAfter(s, m[1]) {
				return "", false
			}
			a, b := l.Number(l.ParseNum(group(s, m, 1))), l.Number(l.ParseNum(group(s, m, 2)))
			if m[6] >= 0 && l.Percent != nil {
				return l.Range(l.Percent(a), l.Percent(b)), true
			}
			return l.Range(a, b) + group(s, m, 3), true
		})
	}
	if l.Percent != nil {
		add(num+sp+`?[%％]`, func(s string, m []int) (string, bool) {
			if gluedBefore(s, m[0]) {
				return "", false
			}
			return l.Percent(l.Number(l.ParseNum(group(s, m, 1)))), true
		})
	}
	if l.Temperature != nil {
		add(num+sp+`?(°`+sp+`?[CF]|℃|℉)`, func(s string, m []int) (string, bool) {
			if gluedBefore(s, m[0]) || letterAfter(s, m[1]) {
				return "", false
			}
			unit := byte('C')
			if u := group(s, m, 2); strings.HasSuffix(u, "F") || u == "℉" {
				unit = 'F'
			}
			return l.Temperature(l.Number(l.ParseNum(group(s, m, 1))), unit), true
		})
	}
	if l.Ordinal != nil && l.OrdinalSuffix != "" {
		add(`(\d+)(`+l.OrdinalSuffix+`)`, func(s string, m []int) (string, bool) {
			if gluedBefore(s, m[0]) || letterAfter(s, m[1]) {
				return "", false
			}
			n, err := strconv.ParseUint(group(s, m, 1), 10, 64)
			if err != nil || n >= 1e15 {
				return "", false
			}
			return l.Ordinal(n, group(s, m, 2))
		})
	}
	rules = append(rules, l.Extra...)
	if len(l.Abbrevs) > 0 {
		rules = append(rules, l.abbrevRule())
	}
	add(num, func(s string, m []int) (string, bool) {
		if glued(s, m[0], m[1]) {
			return "", false
		}
		return l.Number(l.ParseNum(group(s, m, 1))), true
	})
	l.rules = rules
}

func lastRune(s string) rune {
	r, _ := utf8.DecodeLastRuneInString(s)
	return r
}

func (l *Language) date(s string, m []int, ys, ms, ds string) (string, bool) {
	if glued(s, m[0], m[1]) {
		return "", false
	}
	y, _ := strconv.Atoi(ys)
	mo, _ := strconv.Atoi(ms)
	d, _ := strconv.Atoi(ds)
	if len(ys) != 4 || y < 1000 || mo < 1 || mo > 12 || d < 1 || d > daysIn(mo) {
		return "", false
	}
	return l.Date(y, mo, d), true
}

func daysIn(m int) int {
	switch m {
	case 2:
		return 29
	case 4, 6, 9, 11:
		return 30
	}
	return 31
}

func (l *Language) abbrevRule() Rule {
	sorted := slices.Clone(l.Abbrevs)
	slices.SortStableFunc(sorted, func(a, b Abbrev) int { return len(b.From) - len(a.From) })
	alts := make([]string, len(sorted))
	byFrom := map[string]Abbrev{}
	for i, a := range sorted {
		alts[i] = regexp.QuoteMeta(a.From)
		byFrom[a.From] = a
	}
	re := regexp.MustCompile(`(` + strings.Join(alts, "|") + `)`)
	return Rule{Re: re, Fn: func(s string, m []int) (string, bool) {
		a := byFrom[s[m[0]:m[1]]]
		if letterBefore(s, m[0]) {
			return "", false
		}
		if !strings.HasSuffix(a.From, ".") && letterAfter(s, m[1]) {
			return "", false
		}
		rest := s[m[1]:]
		trimmed := strings.TrimLeft(rest, " \t ")
		switch a.Next {
		case 'N':
			r, _ := utf8.DecodeRuneInString(trimmed)
			if len(trimmed) == len(rest) || !unicode.IsUpper(r) {
				return "", false
			}
			return a.To, true
		case 'D':
			r, _ := utf8.DecodeRuneInString(trimmed)
			if !unicode.IsDigit(r) {
				return "", false
			}
			return a.To, true
		}
		// A period that also ends the sentence is kept.
		if a.Next == 'S' && strings.HasSuffix(a.From, ".") {
			if trimmed == "" || strings.HasPrefix(trimmed, "\n") || strings.HasPrefix(rest, "\n") {
				return a.To + ".", true
			}
			if r, _ := utf8.DecodeRuneInString(trimmed); len(trimmed) < len(rest) && unicode.IsUpper(r) {
				return a.To + ".", true
			}
		}
		return a.To, true
	}}
}

func (l *Language) apply(s string) string {
	for _, r := range l.rules {
		s = r.apply(s)
	}
	return s
}
