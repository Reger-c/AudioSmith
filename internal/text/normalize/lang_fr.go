package normalize

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	frUnits = [...]string{"zéro", "un", "deux", "trois", "quatre", "cinq", "six", "sept", "huit", "neuf", "dix",
		"onze", "douze", "treize", "quatorze", "quinze", "seize"}
	frTens   = [...]string{"", "dix", "vingt", "trente", "quarante", "cinquante", "soixante"}
	frMonths = [...]string{"", "janvier", "février", "mars", "avril", "mai", "juin", "juillet", "août",
		"septembre", "octobre", "novembre", "décembre"}
)

func frBelow100(n uint64) string {
	switch {
	case n < 17:
		return frUnits[n]
	case n < 20:
		return "dix-" + frUnits[n-10]
	case n < 70:
		t, u := n/10, n%10
		switch u {
		case 0:
			return frTens[t]
		case 1:
			return frTens[t] + " et un"
		}
		return frTens[t] + "-" + frUnits[u]
	case n < 80:
		if n == 71 {
			return "soixante et onze"
		}
		return "soixante-" + frBelow100(n-60)
	case n == 80:
		return "quatre-vingts"
	default:
		return "quatre-vingt-" + frBelow100(n-80)
	}
}

// frBelow1000 reads 0 < n < 1000; final=false drops the plural s of
// "cents"/"quatre-vingts" before "mille".
func frBelow1000(n uint64, final bool) string {
	h, r := n/100, n%100
	var out string
	switch {
	case h == 0:
		out = frBelow100(r)
	case h == 1:
		out = "cent"
	default:
		out = frUnits[h] + " cent"
	}
	if h > 0 {
		if r == 0 {
			if h > 1 && final {
				out += "s"
			}
		} else {
			out += " " + frBelow100(r)
		}
	}
	if !final && strings.HasSuffix(out, "quatre-vingts") {
		out = strings.TrimSuffix(out, "s")
	}
	return out
}

func frCardinal(n uint64) string {
	if n == 0 {
		return "zéro"
	}
	var parts []string
	scales := []struct {
		div        uint64
		one, other string
	}{{1e12, "un billion", "billions"}, {1e9, "un milliard", "milliards"}, {1e6, "un million", "millions"}}
	for _, sc := range scales {
		switch g := n / sc.div % 1000; {
		case g == 1:
			parts = append(parts, sc.one)
		case g > 1:
			parts = append(parts, frBelow1000(g, true)+" "+sc.other)
		}
	}
	if th := n / 1000 % 1000; th > 0 {
		if th == 1 {
			parts = append(parts, "mille")
		} else {
			parts = append(parts, frBelow1000(th, false)+" mille")
		}
	}
	if r := n % 1000; r > 0 {
		parts = append(parts, frBelow1000(r, true))
	}
	return strings.Join(parts, " ")
}

// frFeminine turns a final "un" into "une" ("vingt et une heures").
func frFeminine(w string) string {
	if w == "un" || strings.HasSuffix(w, " un") || strings.HasSuffix(w, "-un") {
		return w + "e"
	}
	return w
}

func frOrdinal(n uint64) string {
	if n == 1 {
		return "premier"
	}
	w := frCardinal(n)
	i := strings.LastIndexAny(w, " -")
	head, last := w[:i+1], w[i+1:]
	switch {
	case last == "cinq":
		last = "cinquième"
	case last == "neuf":
		last = "neuvième"
	case strings.HasSuffix(last, "s") && last != "trois" && last != "six":
		last = strings.TrimSuffix(last, "s") + "ième"
	case strings.HasSuffix(last, "e"):
		last = strings.TrimSuffix(last, "e") + "ième"
	default:
		last += "ième"
	}
	return head + last
}

var frMoneyNames = map[Currency][4]string{
	"EUR": {"euro", "euros", "centime", "centimes"},
	"USD": {"dollar", "dollars", "cent", "cents"},
	"GBP": {"livre", "livres", "penny", "pence"},
	"JPY": {"yen", "yens", "", ""},
	"CNY": {"yuan", "yuans", "", ""},
	"KRW": {"won", "wons", "", ""},
	"CHF": {"franc", "francs", "centime", "centimes"},
	"INR": {"roupie", "roupies", "", ""},
	"RUB": {"rouble", "roubles", "", ""},
}

func frTime(h, m int) string {
	hour := frFeminine(frCardinal(uint64(h))) + " heures"
	if h == 1 {
		hour = "une heure"
	}
	if m > 0 {
		hour += " " + frCardinal(uint64(m))
	}
	return hour
}

func init() {
	var l *Language
	l = &Language{
		Code:         "fr",
		Spaced:       true,
		DecimalComma: true,
		SpaceGroups:  true,
		Cardinal:     frCardinal,
		Digits:       [10]string(frUnits[:10]),
		DecimalWord:  "virgule",
		FracAsNumber: true,
		Minus:        "moins",
		Percent:      func(w string) string { return w + " pour cent" },
		Ordinal: func(n uint64, suffix string) (string, bool) {
			switch strings.ToLower(suffix) {
			case "er":
				if n != 1 {
					return "", false
				}
				return "premier", true
			case "re", "ère":
				if n != 1 {
					return "", false
				}
				return "première", true
			case "nd":
				if n != 2 {
					return "", false
				}
				return "second", true
			case "nde":
				if n != 2 {
					return "", false
				}
				return "seconde", true
			}
			if n == 1 {
				return "", false
			}
			return frOrdinal(n), true
		},
		OrdinalSuffix: `ère|ème|er|re|nde|nd|e|è|ᵉ|ʳᵉ|ᵉʳ`,
		Time: func(h, m, sec int, _ string) string {
			return frTime(h, m)
		},
		Date: func(y, m, d int) string {
			day := frCardinal(uint64(d))
			if d == 1 {
				day = "premier"
			}
			return day + " " + frMonths[m] + " " + frCardinal(uint64(y))
		},
		DateOrder: "DMY",
		Symbols: map[string]Currency{"€": "EUR", "$": "USD", "US$": "USD", "£": "GBP", "¥": "JPY", "￥": "JPY",
			"₩": "KRW", "₹": "INR", "₽": "RUB", "CHF": "CHF"},
		Money: func(c Currency, n Num, _ string) string {
			names := frMoneyNames[c]
			card := func(v uint64, _ bool) string { return frCardinal(v) }
			if c == "GBP" {
				card = func(v uint64, _ bool) string { return frFeminine(frCardinal(v)) }
			}
			de := "de"
			if strings.ContainsAny(names[1][:1], "aeiouy") {
				de = "d'"
			}
			return latinMoney(l, names, n, card, de, " ", false)
		},
		Range: func(a, b string) string { return a + " à " + b },
		Temperature: func(w string, unit byte) string {
			g := "degrés"
			if w == "un" {
				g = "degré"
			}
			if unit == 'F' {
				return w + " " + g + " Fahrenheit"
			}
			return w + " " + g + " Celsius"
		},
		Abbrevs: []Abbrev{
			{"M.", "Monsieur", 'N'}, {"MM.", "Messieurs", 'N'}, {"Mme", "Madame", 'N'}, {"Mmes", "Mesdames", 'N'},
			{"Mlle", "Mademoiselle", 'N'}, {"Mlles", "Mesdemoiselles", 'N'}, {"Dr", "Docteur", 'N'},
			{"Pr", "Professeur", 'N'}, {"etc.", "et cetera", 'S'}, {"n°", "numéro", 'D'}, {"nº", "numéro", 'D'},
			{"N°", "numéro", 'D'}, {"p. ex.", "par exemple", 0}, {"c.-à-d.", "c'est-à-dire", 0}, {"env.", "environ", 0},
			{"av. J.-C.", "avant Jésus-Christ", 0}, {"apr. J.-C.", "après Jésus-Christ", 0},
		},
	}
	// "15h30", "15 h 30", "9h".
	hours := regexp.MustCompile(`(\d{1,2})` + sp + `?h(?:` + sp + `?(\d{2}))?`)
	l.Extra = []Rule{{Re: hours, Fn: func(s string, m []int) (string, bool) {
		if gluedBefore(s, m[0]) || letterAfter(s, m[1]) || gluedAfter(s, m[1]) {
			return "", false
		}
		h, _ := strconv.Atoi(group(s, m, 1))
		mi := 0
		if g := group(s, m, 2); g != "" {
			mi, _ = strconv.Atoi(g)
		}
		if h > 24 || mi > 59 {
			return "", false
		}
		return frTime(h, mi), true
	}}}
	Register(l)
}
