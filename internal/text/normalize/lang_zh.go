package normalize

import (
	"regexp"
	"strconv"
	"strings"
)

var zhDigits = [10]string{"零", "一", "二", "三", "四", "五", "六", "七", "八", "九"}

// zhSection reads 0 < n < 10000 with 千百十 and inner 零 ("一千零五").
func zhSection(n uint64) string {
	units := [4]string{"", "十", "百", "千"}
	var b strings.Builder
	zero, started := false, false
	for pos := 3; pos >= 0; pos-- {
		div := uint64(1)
		for i := 0; i < pos; i++ {
			div *= 10
		}
		d := n / div % 10
		if d == 0 {
			if started {
				zero = true
			}
			continue
		}
		if zero {
			b.WriteString("零")
			zero = false
		}
		b.WriteString(zhDigits[d])
		b.WriteString(units[pos])
		started = true
	}
	return b.String()
}

// zhCardinal reads n < 10^16 with 万/亿 grouping: 12345 → 一万二千三百四十五,
// 100010 → 十万零一十, 2000 → 两千, 15 → 十五.
func zhCardinal(n uint64) string {
	if n == 0 {
		return "零"
	}
	units := [4]string{"", "万", "亿", "万亿"}
	var groups [4]uint64
	for i := 0; i < 4; i++ {
		groups[i] = n % 10000
		n /= 10000
	}
	var b strings.Builder
	pendingZero := false
	for i := 3; i >= 0; i-- {
		g := groups[i]
		if g == 0 {
			if b.Len() > 0 {
				pendingZero = true
			}
			continue
		}
		if b.Len() > 0 && (pendingZero || g < 1000) {
			b.WriteString("零")
		}
		b.WriteString(zhSection(g))
		b.WriteString(units[i])
		pendingZero = false
	}
	out := b.String()
	if strings.HasPrefix(out, "一十") {
		out = strings.TrimPrefix(out, "一")
	}
	for _, p := range []string{"二千", "二万", "二亿"} {
		if strings.HasPrefix(out, p) {
			out = "两" + strings.TrimPrefix(out, "二")
			break
		}
	}
	return out
}

func zhYearDigits(y string) string {
	var b strings.Builder
	for _, c := range y {
		b.WriteString(zhDigits[c-'0'])
	}
	return b.String()
}

var zhMoney = map[Currency]string{
	"CNY": "元", "USD": "美元", "EUR": "欧元", "GBP": "英镑", "JPY": "日元", "KRW": "韩元",
	"HKD": "港元", "TWD": "新台币", "INR": "卢比", "RUB": "卢布",
}

func init() {
	var l *Language
	l = &Language{
		Code:        "zh",
		Cardinal:    zhCardinal,
		Digits:      zhDigits,
		DecimalWord: "点",
		Minus:       "负",
		Percent:     func(w string) string { return "百分之" + w },
		Time: func(h, m, sec int, _ string) string {
			hour := zhCardinal(uint64(h))
			if h == 2 {
				hour = "两"
			}
			out := hour + "点"
			switch {
			case m == 0 && sec < 0:
			case m < 10:
				out += "零" + zhDigits[m] + "分"
			default:
				out += zhCardinal(uint64(m)) + "分"
			}
			if sec >= 0 {
				out += zhCardinal(uint64(sec)) + "秒"
			}
			return out
		},
		Date: func(y, m, d int) string {
			return zhYearDigits(strconv.Itoa(y)) + "年" + zhCardinal(uint64(m)) + "月" + zhCardinal(uint64(d)) + "日"
		},
		DateOrder: "YMD",
		Symbols: map[string]Currency{"¥": "CNY", "￥": "CNY", "元": "CNY", "$": "USD", "US$": "USD", "€": "EUR",
			"£": "GBP", "₩": "KRW", "HK$": "HKD", "NT$": "TWD", "₹": "INR", "₽": "RUB", "円": "JPY"},
		MoneyScales: `千|万|亿`,
		Money: func(c Currency, n Num, scale string) string {
			return l.Number(n) + scale + zhMoney[c]
		},
		Range: func(a, b string) string { return a + "到" + b },
		Temperature: func(w string, unit byte) string {
			if unit == 'F' {
				return w + "华氏度"
			}
			return w + "摄氏度"
		},
	}
	l.Extra = cjkExtra(l, regexp.MustCompile(`(\d{4})年`), func(y string) string { return zhYearDigits(y) + "年" },
		func(num, den string) string { return den + "分之" + num }, func(w string) string { return "千分之" + w })
	Register(l)
}

// cjkExtra are the rules zh and ja share: years read digit by digit (zh
// only: year != nil), fractions "1/2", per-mille, and long digit strings
// (phone numbers, ids: 11+ digits) read digit by digit.
func cjkExtra(l *Language, yearRe *regexp.Regexp, year func(string) string, frac func(num, den string) string, permille func(string) string) []Rule {
	var rules []Rule
	if yearRe != nil {
		rules = append(rules, Rule{Re: yearRe, Fn: func(s string, m []int) (string, bool) {
			if gluedBefore(s, m[0]) {
				return "", false
			}
			return year(group(s, m, 1)), true
		}})
	}
	rules = append(rules,
		Rule{Re: regexp.MustCompile(`(\d{1,6})/(\d{1,6})`), Fn: func(s string, m []int) (string, bool) {
			if glued(s, m[0], m[1]) {
				return "", false
			}
			a, _ := strconv.ParseUint(group(s, m, 1), 10, 64)
			b, _ := strconv.ParseUint(group(s, m, 2), 10, 64)
			if b == 0 {
				return "", false
			}
			return frac(l.Cardinal(a), l.Cardinal(b)), true
		}},
		Rule{Re: regexp.MustCompile(`(` + l.NumPattern() + `)` + sp + `?‰`), Fn: func(s string, m []int) (string, bool) {
			if gluedBefore(s, m[0]) {
				return "", false
			}
			return permille(l.Number(l.ParseNum(group(s, m, 1)))), true
		}},
		Rule{Re: regexp.MustCompile(`\d{11,}`), Fn: func(s string, m []int) (string, bool) {
			if glued(s, m[0], m[1]) {
				return "", false
			}
			return l.DigitsOf(s[m[0]:m[1]]), true
		}},
	)
	return rules
}
