package normalize

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	enOnes = [...]string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten",
		"eleven", "twelve", "thirteen", "fourteen", "fifteen", "sixteen", "seventeen", "eighteen", "nineteen"}
	enTens   = [...]string{"", "", "twenty", "thirty", "forty", "fifty", "sixty", "seventy", "eighty", "ninety"}
	enScales = [...]string{"", "thousand", "million", "billion", "trillion"}
	enMonths = [...]string{"", "January", "February", "March", "April", "May", "June", "July", "August",
		"September", "October", "November", "December"}
)

func enBelow1000(n uint64) string {
	var parts []string
	if n >= 100 {
		parts = append(parts, enOnes[n/100], "hundred")
		n %= 100
	}
	switch {
	case n == 0:
	case n < 20:
		parts = append(parts, enOnes[n])
	case n%10 == 0:
		parts = append(parts, enTens[n/10])
	default:
		parts = append(parts, enTens[n/10]+"-"+enOnes[n%10])
	}
	return strings.Join(parts, " ")
}

// enCardinal reads n < 10^15 ("one thousand two hundred thirty-four").
func enCardinal(n uint64) string {
	if n == 0 {
		return "zero"
	}
	var parts []string
	for i := len(enScales) - 1; i >= 0; i-- {
		div := pow1000(i)
		if g := n / div; g > 0 {
			parts = append(parts, enBelow1000(g))
			if enScales[i] != "" {
				parts = append(parts, enScales[i])
			}
			n %= div
		}
	}
	return strings.Join(parts, " ")
}

func pow1000(i int) uint64 {
	v := uint64(1)
	for ; i > 0; i-- {
		v *= 1000
	}
	return v
}

// enOrdinalWords turns cardinal words into ordinal words
// ("twenty-one" → "twenty-first").
func enOrdinalWords(w string) string {
	i := strings.LastIndexAny(w, " -")
	head, last := w[:i+1], w[i+1:]
	switch last {
	case "one":
		last = "first"
	case "two":
		last = "second"
	case "three":
		last = "third"
	case "five":
		last = "fifth"
	case "eight":
		last = "eighth"
	case "nine":
		last = "ninth"
	case "twelve":
		last = "twelfth"
	default:
		if strings.HasSuffix(last, "y") {
			last = last[:len(last)-1] + "ieth"
		} else {
			last += "th"
		}
	}
	return head + last
}

// enYear reads a year: "nineteen ninety-nine", "two thousand five",
// "twenty twenty-four", "nineteen hundred", "nineteen oh five".
func enYear(y int) string {
	switch {
	case y >= 2000 && y < 2010, y < 1000 || y >= 10000, y%1000 == 0:
		return enCardinal(uint64(y))
	}
	hi, lo := y/100, y%100
	switch {
	case lo == 0:
		return enBelow1000(uint64(hi)) + " hundred"
	case lo < 10:
		return enBelow1000(uint64(hi)) + " oh " + enOnes[lo]
	}
	return enBelow1000(uint64(hi)) + " " + enBelow1000(uint64(lo))
}

func enPlural(n Num, one, many string) string {
	if n.Int == "1" && n.Frac == "" {
		return one
	}
	return many
}

func init() {
	l := &Language{
		Code:        "en",
		Spaced:      true,
		Cardinal:    enCardinal,
		Digits:      [10]string(enOnes[:10]),
		DecimalWord: "point",
		Minus:       "minus",
		Percent:     func(w string) string { return w + " percent" },
		Ordinal: func(n uint64, suffix string) (string, bool) {
			want := "th"
			if n%100 < 11 || n%100 > 13 {
				switch n % 10 {
				case 1:
					want = "st"
				case 2:
					want = "nd"
				case 3:
					want = "rd"
				}
			}
			if strings.ToLower(suffix) != want {
				return "", false
			}
			return enOrdinalWords(enCardinal(n)), true
		},
		OrdinalSuffix: `st|nd|rd|th|ST|ND|RD|TH`,
		TimeSuffix:    `[aApP]\.?[mM]\.?`,
		Time: func(h, m, sec int, suffix string) string {
			var out string
			switch {
			case m == 0 && suffix == "" && sec < 0:
				out = enCardinal(uint64(h)) + " o'clock"
			case m == 0:
				out = enCardinal(uint64(h))
			case m < 10:
				out = enCardinal(uint64(h)) + " oh " + enOnes[m]
			default:
				out = enCardinal(uint64(h)) + " " + enCardinal(uint64(m))
			}
			if sec >= 0 {
				out += " and " + enCardinal(uint64(sec)) + " " + map[bool]string{true: "second", false: "seconds"}[sec == 1]
			}
			if suffix != "" {
				out += " " + string(suffix[0]) + " m"
			}
			return out
		},
		Date: func(y, m, d int) string {
			return enMonths[m] + " " + enOrdinalWords(enCardinal(uint64(d))) + ", " + enYear(y)
		},
		DateOrder: "MDY",
		Symbols: map[string]Currency{"$": "USD", "US$": "USD", "€": "EUR", "£": "GBP", "¥": "JPY", "￥": "JPY",
			"₩": "KRW", "₹": "INR", "₽": "RUB", "C$": "CAD", "A$": "AUD", "HK$": "HKD", "NT$": "TWD"},
		MoneyScales: `thousand|million|billion|trillion|k|m|bn`,
		Money:       enMoney,
		Range:       func(a, b string) string { return a + " to " + b },
		Temperature: func(w string, unit byte) string {
			deg := "degrees"
			if w == "one" {
				deg = "degree"
			}
			if unit == 'F' {
				return w + " " + deg + " Fahrenheit"
			}
			return w + " " + deg + " Celsius"
		},
		Abbrevs: []Abbrev{
			{"Mr.", "Mister", 'N'}, {"Mrs.", "Missus", 'N'}, {"Ms.", "Miz", 'N'}, {"Dr.", "Doctor", 'N'},
			{"Prof.", "Professor", 'N'}, {"Mt.", "Mount", 'N'}, {"Gen.", "General", 'N'}, {"Capt.", "Captain", 'N'},
			{"Sgt.", "Sergeant", 'N'}, {"Lt.", "Lieutenant", 'N'}, {"Rev.", "Reverend", 'N'}, {"Hon.", "Honorable", 'N'},
			{"Jr.", "Junior", 0}, {"Sr.", "Senior", 0},
			{"vs.", "versus", 0}, {"etc.", "et cetera", 'S'}, {"e.g.", "for example", 0}, {"i.e.", "that is", 0},
			{"approx.", "approximately", 0}, {"No.", "number", 'D'}, {"no.", "number", 'D'}, {"Nos.", "numbers", 'D'},
		},
	}
	// "in 1999", "since 2010", "the 1990s".
	yearCtx := regexp.MustCompile(`(?i)\b(in|since|by|from|until|till|of|during|year|around|circa|before|after|the|early|late|mid)` + sp + `+(1[1-9]\d\d|20\d\d)(s|'s)?`)
	l.Extra = []Rule{{Re: yearCtx, Fn: func(s string, m []int) (string, bool) {
		if gluedAfter(s, m[1]) || (m[6] < 0 && strings.EqualFold(group(s, m, 1), "the")) {
			return "", false
		}
		y, _ := strconv.Atoi(group(s, m, 2))
		w := enYear(y)
		if m[6] >= 0 { // a decade
			if strings.HasSuffix(w, "y") {
				w = w[:len(w)-1] + "ies"
			} else {
				w += "s"
			}
		}
		return s[m[0]:m[4]] + w, true
	}}}
	Register(l)
}

var enMoneyNames = map[Currency][4]string{ // unit, units, subunit, subunits
	"USD": {"dollar", "dollars", "cent", "cents"},
	"CAD": {"Canadian dollar", "Canadian dollars", "cent", "cents"},
	"AUD": {"Australian dollar", "Australian dollars", "cent", "cents"},
	"HKD": {"Hong Kong dollar", "Hong Kong dollars", "cent", "cents"},
	"TWD": {"New Taiwan dollar", "New Taiwan dollars", "", ""},
	"EUR": {"euro", "euros", "cent", "cents"},
	"GBP": {"pound", "pounds", "penny", "pence"},
	"JPY": {"yen", "yen", "", ""},
	"CNY": {"yuan", "yuan", "", ""},
	"KRW": {"won", "won", "", ""},
	"INR": {"rupee", "rupees", "", ""},
	"RUB": {"ruble", "rubles", "", ""},
}

func enMoney(c Currency, n Num, scale string) string {
	names := enMoneyNames[c]
	l := lookup("en")
	switch scale {
	case "k":
		scale = "thousand"
	case "m":
		scale = "million"
	case "bn":
		scale = "billion"
	}
	if scale != "" {
		return l.Number(n) + " " + scale + " " + names[1]
	}
	if names[2] != "" && len(n.Frac) == 2 || names[2] != "" && len(n.Frac) == 1 {
		frac := n.Frac
		if len(frac) == 1 {
			frac += "0"
		}
		cents, _ := strconv.ParseUint(frac, 10, 64)
		whole := Num{Int: n.Int}
		var parts []string
		if v, _ := whole.Value(); v > 0 || cents == 0 {
			parts = append(parts, l.Number(whole)+" "+enPlural(whole, names[0], names[1]))
		}
		if cents > 0 {
			parts = append(parts, enCardinal(cents)+" "+map[bool]string{true: names[2], false: names[3]}[cents == 1])
		}
		out := strings.Join(parts, " and ")
		if n.Neg {
			out = "minus " + out
		}
		return out
	}
	return l.Number(n) + " " + enPlural(n, names[0], names[1])
}
