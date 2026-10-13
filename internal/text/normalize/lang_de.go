package normalize

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	deOnes = [...]string{"null", "eins", "zwei", "drei", "vier", "fünf", "sechs", "sieben", "acht", "neun", "zehn",
		"elf", "zwölf", "dreizehn", "vierzehn", "fünfzehn", "sechzehn", "siebzehn", "achtzehn", "neunzehn"}
	deTens   = [...]string{"", "", "zwanzig", "dreißig", "vierzig", "fünfzig", "sechzig", "siebzig", "achtzig", "neunzig"}
	deMonths = [...]string{"", "Januar", "Februar", "März", "April", "Mai", "Juni", "Juli", "August", "September",
		"Oktober", "November", "Dezember"}
)

// deBelow100 reads 0 < n < 100; final=false gives "ein" for 1.
func deBelow100(n uint64, final bool) string {
	switch {
	case n == 1 && !final:
		return "ein"
	case n < 20:
		return deOnes[n]
	case n%10 == 0:
		return deTens[n/10]
	}
	u := deOnes[n%10]
	if n%10 == 1 {
		u = "ein"
	}
	return u + "und" + deTens[n/10]
}

func deBelow1000(n uint64, final bool) string {
	h, r := n/100, n%100
	out := ""
	if h > 0 {
		out = deBelow100(h, false) + "hundert"
	}
	if r > 0 {
		out += deBelow100(r, final)
	}
	return out
}

// deCardinal reads n < 10^15: numbers below a million are one word
// ("zweitausenddreihundertvierundfünfzig"), larger scales are separate
// ("zwei Millionen dreihunderttausend").
func deCardinal(n uint64) string {
	if n == 0 {
		return "null"
	}
	var parts []string
	scales := []struct {
		div        uint64
		one, other string
	}{{1e12, "eine Billion", "Billionen"}, {1e9, "eine Milliarde", "Milliarden"}, {1e6, "eine Million", "Millionen"}}
	for _, sc := range scales {
		switch g := n / sc.div % 1000; {
		case g == 1:
			parts = append(parts, sc.one)
		case g > 1:
			parts = append(parts, deBelow1000(g, true)+" "+sc.other)
		}
	}
	w := ""
	if th := n / 1000 % 1000; th > 0 {
		w = deBelow1000(th, false) + "tausend"
	}
	if r := n % 1000; r > 0 {
		w += deBelow1000(r, true)
	}
	if w != "" {
		parts = append(parts, w)
	}
	return strings.Join(parts, " ")
}

// deOrdinalStem: 1 erst, 3 dritt, 7 siebt, 8 acht, n < 20 +t, else +st.
func deOrdinalStem(n uint64) string {
	switch n {
	case 1:
		return "erst"
	case 3:
		return "dritt"
	case 7:
		return "siebt"
	case 8:
		return "acht"
	}
	if n < 20 {
		return deCardinal(n) + "t"
	}
	return deCardinal(n) + "st"
}

func deYear(y int) string {
	if y >= 1100 && y < 2000 {
		w := deBelow100(uint64(y/100), true) + "hundert"
		if r := y % 100; r > 0 {
			w += deBelow100(uint64(r), true)
		}
		return w
	}
	return deCardinal(uint64(y))
}

var deMoneyNames = map[Currency][4]string{
	"EUR": {"Euro", "Euro", "Cent", "Cent"},
	"USD": {"Dollar", "Dollar", "Cent", "Cent"},
	"GBP": {"Pfund", "Pfund", "Penny", "Pence"},
	"CHF": {"Franken", "Franken", "Rappen", "Rappen"},
	"JPY": {"Yen", "Yen", "", ""},
	"CNY": {"Yuan", "Yuan", "", ""},
	"KRW": {"Won", "Won", "", ""},
	"INR": {"Rupie", "Rupien", "", ""},
	"RUB": {"Rubel", "Rubel", "", ""},
}

func deTime(h, m int) string {
	hour := deCardinal(uint64(h))
	if h == 1 {
		hour = "ein"
	}
	out := hour + " Uhr"
	if m > 0 {
		out += " " + deCardinal(uint64(m))
	}
	return out
}

// deDate reads day, month and year; dative after "am", "vom", "zum"…
func deDate(d, m, y int, dative bool) string {
	out := deOrdinalStem(uint64(d)) + "er"
	if dative {
		out = deOrdinalStem(uint64(d)) + "en"
	}
	if m > 0 {
		out += " " + deMonths[m]
	}
	if y > 0 {
		out += " " + deYear(y)
	}
	return out
}

func init() {
	var l *Language
	l = &Language{
		Code:         "de",
		Spaced:       true,
		DecimalComma: true,
		Cardinal:     deCardinal,
		Digits:       [10]string(deOnes[:10]),
		DecimalWord:  "Komma",
		Minus:        "minus",
		Percent:      func(w string) string { return w + " Prozent" },
		TimeSuffix:   `Uhr`,
		Time: func(h, m, sec int, _ string) string {
			return deTime(h, m)
		},
		Date: func(y, m, d int) string { return deDate(d, m, y, false) },
		Symbols: map[string]Currency{"€": "EUR", "$": "USD", "US$": "USD", "£": "GBP", "¥": "JPY", "￥": "JPY",
			"₩": "KRW", "₹": "INR", "₽": "RUB", "CHF": "CHF", "Fr.": "CHF"},
		Money: func(c Currency, n Num, _ string) string {
			card := func(v uint64, noun bool) string {
				w := deCardinal(v)
				if noun && strings.HasSuffix(w, "eins") {
					w = strings.TrimSuffix(w, "s")
				}
				return w
			}
			return latinMoney(l, deMoneyNames[c], n, card, "", " ", false)
		},
		Range: func(a, b string) string { return a + " bis " + b },
		Temperature: func(w string, unit byte) string {
			if unit == 'F' {
				return w + " Grad Fahrenheit"
			}
			return w + " Grad Celsius"
		},
		Abbrevs: []Abbrev{
			{"z. B.", "zum Beispiel", 0}, {"z.B.", "zum Beispiel", 0}, {"d. h.", "das heißt", 0}, {"d.h.", "das heißt", 0},
			{"u. a.", "unter anderem", 0}, {"u.a.", "unter anderem", 0}, {"usw.", "und so weiter", 'S'},
			{"bzw.", "beziehungsweise", 0}, {"ca.", "circa", 0}, {"evtl.", "eventuell", 0}, {"ggf.", "gegebenenfalls", 0},
			{"vgl.", "vergleiche", 0}, {"inkl.", "inklusive", 0}, {"bspw.", "beispielsweise", 0},
			{"Nr.", "Nummer", 'D'}, {"Dr.", "Doktor", 'N'}, {"Prof.", "Professor", 'N'}, {"Hr.", "Herr", 'N'},
			{"Fr.", "Frau", 'N'}, {"St.", "Sankt", 'N'}, {"Mio.", "Millionen", 0}, {"Mrd.", "Milliarden", 0},
			{"Tsd.", "Tausend", 0},
		},
	}
	months := strings.Join(deMonths[1:], "|")
	dative := `(?:([Aa]m|[Vv]om|[Zz]um|[Ss]eit dem|[Aa]b dem|[Bb]is zum)` + sp + `+)?`
	dotDate := regexp.MustCompile(dative + `(\d{1,2})\.` + sp + `?(\d{1,2})\.(?:` + sp + `?(\d{4}))?`)
	nameDate := regexp.MustCompile(dative + `(\d{1,2})\.` + sp + `?(` + months + `)(?:` + sp + `(\d{4}))?`)
	dotTime := regexp.MustCompile(`(\d{1,2})\.(\d{2})` + sp + `?Uhr`)
	prefix := func(s string, m []int) string {
		if m[2] < 0 {
			return ""
		}
		return s[m[0]:m[4]]
	}
	l.Extra = []Rule{
		{Re: dotTime, Fn: func(s string, m []int) (string, bool) {
			h, _ := strconv.Atoi(group(s, m, 1))
			mi, _ := strconv.Atoi(group(s, m, 2))
			if gluedBefore(s, m[0]) || h > 24 || mi > 59 {
				return "", false
			}
			return deTime(h, mi), true
		}},
		{Re: dotDate, Fn: func(s string, m []int) (string, bool) {
			start := m[0]
			if m[2] >= 0 {
				start = m[4]
			}
			if gluedBefore(s, start) || gluedAfter(s, m[1]) {
				return "", false
			}
			d, _ := strconv.Atoi(group(s, m, 2))
			mo, _ := strconv.Atoi(group(s, m, 3))
			y := 0
			if g := group(s, m, 4); g != "" {
				y, _ = strconv.Atoi(g)
			} else if m[2] < 0 {
				return "", false // "3.2." alone may be a section number
			}
			if d < 1 || mo < 1 || mo > 12 || d > daysIn(mo) {
				return "", false
			}
			return prefix(s, m) + deDate(d, mo, y, m[2] >= 0), true
		}},
		{Re: nameDate, Fn: func(s string, m []int) (string, bool) {
			start := m[0]
			if m[2] >= 0 {
				start = m[4]
			}
			if gluedBefore(s, start) {
				return "", false
			}
			d, _ := strconv.Atoi(group(s, m, 2))
			if d < 1 || d > 31 {
				return "", false
			}
			mo := 0
			for i, name := range deMonths {
				if name == group(s, m, 3) {
					mo = i
				}
			}
			y := 0
			if g := group(s, m, 4); g != "" {
				y, _ = strconv.Atoi(g)
			}
			return prefix(s, m) + deDate(d, mo, y, m[2] >= 0), true
		}},
	}
	Register(l)
}
