package normalize

// Chinese forms the shared rules misread or leave for the engine (thread
// #80: SoulX-Podcast, like most Chinese TTS models, reads Arabic numerals
// and Latin units poorly). They run before the shared rules (Language.Pre):
//
//   - phone numbers with separators or a +86 prefix (138-0013-8000,
//     +86 138 0013 8000, 010-12345678, (010)12345678, 400-123-4567), and
//     digits after 电话/热线/拨打… (拨打10086), digit by digit;
//   - dates written with dots (2024.03.05);
//   - scores (比分3:2, 以3：2获胜, a bare 3:2 that is no time) as 三比二;
//   - ranges with an ASCII hyphen (3-5天, 10%-20%, 2024-2025年, 1-2个);
//   - numbers with metric and common units (5km, 3.5kg, 100m², 60km/h,
//     220V, 128GB): 五公里, 三点五千克…; 2 before a unit is 两;
//   - one digit with G or K (5G, 4K): 五G.

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// zhUnits are the units read after a number, longest first in the
// alternation (zhUnitPattern).
var zhUnits = map[string]string{
	"km/h": "公里每小时", "m/s": "米每秒", "km²": "平方公里", "km2": "平方公里", "m²": "平方米", "m2": "平方米",
	"cm²": "平方厘米", "cm2": "平方厘米", "m³": "立方米", "m3": "立方米", "km": "公里", "cm": "厘米", "mm": "毫米",
	"nm": "纳米", "μm": "微米", "um": "微米", "m": "米", "kg": "千克", "mg": "毫克", "g": "克", "mL": "毫升",
	"ml": "毫升", "L": "升", "kWh": "千瓦时", "kW": "千瓦", "W": "瓦", "V": "伏", "mAh": "毫安时", "dB": "分贝",
	"kHz": "千赫兹", "MHz": "兆赫兹", "GHz": "吉赫兹", "Hz": "赫兹", "ms": "毫秒", "min": "分钟",
	"TB": "T", "GB": "G", "MB": "兆", "KB": "K",
}

// zhMeasure: words after which a 2 is read 两 (the shared rule's list).
var zhMeasure = regexp.MustCompile(`^(个|位|只|次|天|岁|本|张|件|条|种|名|台|辆|家|口|斤|公斤|千克|公里|小时|分钟|周|倍|点|杯|双|对|份|间|座|首|部|场|遍|片|块|样|句|步|声|眼|下|百|千|万|亿)`)

func zhUnitPattern() string {
	keys := make([]string, 0, len(zhUnits))
	for k := range zhUnits {
		keys = append(keys, regexp.QuoteMeta(k))
	}
	// Longest first, so "km/h" wins over "km" and "m".
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && len(keys[j]) > len(keys[j-1]); j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return strings.Join(keys, "|")
}

// zhPre are the rules above, for l (zh).
func zhPre(l *Language) []Rule {
	num := `(` + l.NumPattern() + `)`
	digits := func(s string) string {
		var parts []string
		for _, g := range regexp.MustCompile(`\d+`).FindAllString(s, -1) {
			parts = append(parts, l.DigitsOf(g))
		}
		return strings.Join(parts, " ")
	}
	twoOr := func(n Num, rest string) string {
		if n.Int == "2" && n.Frac == "" && !n.Neg && (rest == "" || zhMeasure.MatchString(rest)) {
			return "两"
		}
		return l.Number(n)
	}
	var rules []Rule
	add := func(re string, fn func(s string, m []int) (string, bool)) {
		rules = append(rules, Rule{Re: regexp.MustCompile(re), Fn: fn})
	}
	// Phone numbers: mobile (with +86), landline with its area code, 400/800.
	phone := func(s string, m []int) (string, bool) {
		if glued(s, m[0], m[1]) {
			return "", false
		}
		out := digits(s[m[0]:m[1]])
		if strings.HasPrefix(s[m[0]:m[1]], "+") {
			out = "加" + out
		}
		return out, true
	}
	add(`(?:\+?86[ -]?)?1[3-9]\d[ -]\d{4}[ -]\d{4}|\+86[ -]?1[3-9]\d{9}`, phone)
	add(`(?:\(0\d{2,3}\)|0\d{2,3}-)\d{7,8}(?:-\d{1,5})?`, phone)
	add(`[48]00[ -]\d{3}[ -]\d{4}`, phone)
	add(`(电话|热线|拨打|号码|手机号?|致电|呼叫|客服|报警|分机|电话号码)(是|为|：|:)?[ \t]*(\d[\d -]{1,18}\d)`, func(s string, m []int) (string, bool) {
		if gluedAfter(s, m[1]) {
			return "", false
		}
		return group(s, m, 1) + group(s, m, 2) + digits(group(s, m, 3)), true
	})
	// Dates with dots.
	add(`(\d{4})\.(\d{1,2})\.(\d{1,2})`, func(s string, m []int) (string, bool) {
		return l.date(s, m, group(s, m, 1), group(s, m, 2), group(s, m, 3))
	})
	// Scores.
	add(`(比分|以|战成|打成)[ \t]*(\d{1,3})[:比](\d{1,3})`, func(s string, m []int) (string, bool) {
		if gluedAfter(s, m[1]) {
			return "", false
		}
		return group(s, m, 1) + zhCardinal(atoi(group(s, m, 2))) + "比" + zhCardinal(atoi(group(s, m, 3))), true
	})
	add(`(\d{1,3}):(\d)`, func(s string, m []int) (string, bool) {
		if glued(s, m[0], m[1]) {
			return "", false
		}
		return zhCardinal(atoi(group(s, m, 1))) + "比" + zhCardinal(atoi(group(s, m, 2))), true
	})
	// Ranges with an ASCII hyphen (after phones and dates).
	add(num+`(%?)-`+num+`(%?)(年)?`, func(s string, m []int) (string, bool) {
		a, b := group(s, m, 1), group(s, m, 3)
		if glued(s, m[0], m[1]) || strings.HasPrefix(b, "-") || len(strings.Split(a, ".")[0]) > 4 || len(strings.Split(b, ".")[0]) > 4 {
			return "", false
		}
		na, nb := l.ParseNum(a), l.ParseNum(b)
		if group(s, m, 5) == "年" && len(a) == 4 && len(b) == 4 {
			return zhYearDigits(a) + "到" + zhYearDigits(b) + "年", true
		}
		if group(s, m, 2) == "%" || group(s, m, 4) == "%" {
			return l.Range(l.Percent(l.Number(na)), l.Percent(l.Number(nb))) + group(s, m, 5), true
		}
		return l.Range(l.Number(na), twoOr(nb, s[m[1]:])) + group(s, m, 5), true
	})
	// Units.
	add(num+`[ \t]?(`+zhUnitPattern()+`)`, func(s string, m []int) (string, bool) {
		if gluedBefore(s, m[0]) || latinAfter(s, m[1]) {
			return "", false
		}
		n := l.ParseNum(group(s, m, 1))
		w := l.Number(n)
		if n.Int == "2" && n.Frac == "" && !n.Neg {
			w = "两"
		}
		return w + zhUnits[group(s, m, 2)], true
	})
	// 5G, 4K.
	add(`(\d)([GK])`, func(s string, m []int) (string, bool) {
		if gluedBefore(s, m[0]) || latinAfter(s, m[1]) {
			return "", false
		}
		return l.Digits[s[m[0]]-'0'] + group(s, m, 2), true
	})
	return rules
}

// latinAfter: a letter of a spaced script or a digit directly after b
// (Han characters may follow a unit: 5km路).
func latinAfter(s string, b int) bool {
	if b >= len(s) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s[b:])
	return isGlueLetter(r) || unicode.IsDigit(r)
}

func atoi(s string) uint64 {
	var n uint64
	for _, c := range s {
		n = n*10 + uint64(c-'0')
	}
	return n
}
