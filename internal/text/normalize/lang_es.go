package normalize

import (
	"strconv"
	"strings"
)

var (
	esUnits = [...]string{"cero", "uno", "dos", "tres", "cuatro", "cinco", "seis", "siete", "ocho", "nueve", "diez",
		"once", "doce", "trece", "catorce", "quince", "dieciséis", "diecisiete", "dieciocho", "diecinueve", "veinte",
		"veintiuno", "veintidós", "veintitrés", "veinticuatro", "veinticinco", "veintiséis", "veintisiete",
		"veintiocho", "veintinueve"}
	esTens     = [...]string{"", "", "", "treinta", "cuarenta", "cincuenta", "sesenta", "setenta", "ochenta", "noventa"}
	esHundreds = [...]string{"", "ciento", "doscientos", "trescientos", "cuatrocientos", "quinientos", "seiscientos",
		"setecientos", "ochocientos", "novecientos"}
	esMonths = [...]string{"", "enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto",
		"septiembre", "octubre", "noviembre", "diciembre"}
	esOrdinals = [...]string{"", "primer", "segund", "tercer", "cuart", "quint", "sext", "séptim", "octav", "noven", "décim"}
)

// esBelow1000 reads 0 < n < 1000; apocope shortens a final "uno" to "un"
// ("veintiún euros", "un millón").
func esBelow1000(n uint64, apocope bool) string {
	var parts []string
	h, r := n/100, n%100
	if h > 0 {
		if h == 1 && r == 0 {
			parts = append(parts, "cien")
		} else {
			parts = append(parts, esHundreds[h])
		}
	}
	switch {
	case r == 0:
	case r < 30:
		w := esUnits[r]
		if apocope && r == 1 {
			w = "un"
		} else if apocope && r == 21 {
			w = "veintiún"
		}
		parts = append(parts, w)
	default:
		w := esTens[r/10]
		if u := r % 10; u > 0 {
			uw := esUnits[u]
			if apocope && u == 1 {
				uw = "un"
			}
			w += " y " + uw
		}
		parts = append(parts, w)
	}
	return strings.Join(parts, " ")
}

func esBelowMillion(n uint64, apocope bool) string {
	th, r := n/1000, n%1000
	var parts []string
	if th == 1 {
		parts = append(parts, "mil")
	} else if th > 1 {
		parts = append(parts, esBelow1000(th, true)+" mil")
	}
	if r > 0 {
		parts = append(parts, esBelow1000(r, apocope))
	}
	return strings.Join(parts, " ")
}

func esCard(n uint64, apocope bool) string {
	if n == 0 {
		return "cero"
	}
	var parts []string
	if b := n / 1e12; b > 0 {
		if b == 1 {
			parts = append(parts, "un billón")
		} else {
			parts = append(parts, esBelowMillion(b, true)+" billones")
		}
	}
	if m := n / 1e6 % 1e6; m > 0 {
		if m == 1 {
			parts = append(parts, "un millón")
		} else {
			parts = append(parts, esBelowMillion(m, true)+" millones")
		}
	}
	if r := n % 1e6; r > 0 {
		parts = append(parts, esBelowMillion(r, apocope))
	}
	return strings.Join(parts, " ")
}

func esCardinal(n uint64) string { return esCard(n, false) }

func esYear(y int) string { return esCardinal(uint64(y)) }

var esMoneyNames = map[Currency][4]string{
	"EUR": {"euro", "euros", "céntimo", "céntimos"},
	"USD": {"dólar", "dólares", "centavo", "centavos"},
	"GBP": {"libra", "libras", "penique", "peniques"},
	"JPY": {"yen", "yenes", "", ""},
	"CNY": {"yuan", "yuanes", "", ""},
	"KRW": {"won", "wones", "", ""},
	"INR": {"rupia", "rupias", "", ""},
	"RUB": {"rublo", "rublos", "", ""},
}

// latinMoney reads an amount in es/fr/de style: "<whole> <unit>[ sep
// <cents> [subunit]]". card(n, true) reads a number before a noun.
func latinMoney(l *Language, names [4]string, n Num, card func(uint64, bool) string, de, sep string, subunitAfter bool) string {
	whole, ok := (Num{Int: n.Int}).Value()
	if !ok || len(n.Frac) > 2 || (n.Frac != "" && names[2] == "") {
		return l.Number(n) + " " + names[1]
	}
	unit := names[1]
	if whole == 1 {
		unit = names[0]
	}
	out := card(whole, true) + " "
	if whole >= 1e6 && whole%1e6 == 0 && de != "" {
		if strings.HasSuffix(de, "'") {
			out += de + unit
		} else {
			out += de + " " + unit
		}
	} else {
		out += unit
	}
	if n.Frac != "" {
		frac := n.Frac
		if len(frac) == 1 {
			frac += "0"
		}
		cents, _ := strconv.ParseUint(frac, 10, 64)
		if cents > 0 {
			sub := names[3]
			if cents == 1 {
				sub = names[2]
			}
			if whole == 0 {
				out = card(cents, true) + " " + sub
			} else if subunitAfter {
				out += sep + card(cents, true) + " " + sub
			} else {
				out += sep + card(cents, false)
			}
		}
	}
	if n.Neg {
		out = l.Minus + " " + out
	}
	return out
}

func init() {
	var l *Language
	l = &Language{
		Code:         "es",
		Spaced:       true,
		DecimalComma: true,
		Cardinal:     esCardinal,
		Digits:       [10]string(esUnits[:10]),
		DecimalWord:  "coma",
		FracAsNumber: true,
		Minus:        "menos",
		Percent:      func(w string) string { return w + " por ciento" },
		Ordinal: func(n uint64, suffix string) (string, bool) {
			fem := strings.Contains(suffix, "ª") || strings.HasSuffix(suffix, "a")
			if n < 1 || n > 10 {
				return esCardinal(n), true
			}
			if fem {
				return esOrdinals[n] + "a", true
			}
			return esOrdinals[n] + "o", true
		},
		OrdinalSuffix: `\.?[ºª°]`,
		Time: func(h, m, sec int, _ string) string {
			hour := esCardinal(uint64(h))
			if strings.HasSuffix(hour, "uno") {
				hour = strings.TrimSuffix(hour, "o") + "a" // la una, las veintiuna
			}
			if m == 0 {
				return hour + " en punto"
			}
			return hour + " y " + esCardinal(uint64(m))
		},
		Date: func(y, m, d int) string {
			return esCardinal(uint64(d)) + " de " + esMonths[m] + " de " + esYear(y)
		},
		DateOrder: "DMY",
		Symbols: map[string]Currency{"€": "EUR", "$": "USD", "US$": "USD", "£": "GBP", "¥": "JPY", "￥": "JPY",
			"₩": "KRW", "₹": "INR", "₽": "RUB"},
		Money: func(c Currency, n Num, _ string) string {
			return latinMoney(l, esMoneyNames[c], n, esCard, "de", " con ", true)
		},
		Range: func(a, b string) string { return a + " a " + b },
		Temperature: func(w string, unit byte) string {
			g := "grados"
			if w == "uno" {
				w, g = "un", "grado"
			}
			if unit == 'F' {
				return w + " " + g + " Fahrenheit"
			}
			return w + " " + g + " Celsius"
		},
		Abbrevs: []Abbrev{
			{"Sr.", "señor", 'N'}, {"Sra.", "señora", 'N'}, {"Srta.", "señorita", 'N'}, {"Dr.", "doctor", 'N'},
			{"Dra.", "doctora", 'N'}, {"Prof.", "profesor", 'N'}, {"Ud.", "usted", 0}, {"Uds.", "ustedes", 0},
			{"etc.", "etcétera", 'S'}, {"p. ej.", "por ejemplo", 0}, {"aprox.", "aproximadamente", 0},
			{"núm.", "número", 'D'}, {"n.º", "número", 'D'}, {"Nº", "número", 'D'}, {"nº", "número", 'D'},
			{"pág.", "página", 'D'},
		},
	}
	Register(l)
}
