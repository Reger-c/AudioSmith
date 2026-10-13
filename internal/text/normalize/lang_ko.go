package normalize

import (
	"regexp"
	"strconv"
	"strings"
)

var koDigits = [10]string{"영", "일", "이", "삼", "사", "오", "육", "칠", "팔", "구"}

// koSection reads 0 < n < 10000 in Sino-Korean without 일 before 십백천.
func koSection(n uint64) string {
	units := [4]string{"", "십", "백", "천"}
	var b strings.Builder
	div := uint64(1000)
	for pos := 3; pos >= 0; pos-- {
		d := n / div % 10
		div /= 10
		if d == 0 {
			continue
		}
		if d != 1 || pos == 0 {
			b.WriteString(koDigits[d])
		}
		b.WriteString(units[pos])
	}
	return b.String()
}

// koCardinal reads n < 10^16 in Sino-Korean, spacing after 만/억/조
// groups (12345 → 만 이천삼백사십오, 10000 → 만, 100000000 → 일억).
func koCardinal(n uint64) string {
	if n == 0 {
		return "영"
	}
	units := [4]string{"", "만", "억", "조"}
	var groups [4]uint64
	for i := 0; i < 4; i++ {
		groups[i] = n % 10000
		n /= 10000
	}
	var parts []string
	for i := 3; i >= 0; i-- {
		g := groups[i]
		if g == 0 {
			continue
		}
		sec := koSection(g)
		if g == 1 && i >= 2 {
			sec = "일"
		}
		if g == 1 && i == 1 {
			sec = ""
		}
		parts = append(parts, sec+units[i])
	}
	return strings.Join(parts, " ")
}

var (
	koNativeOnes = [10]string{"", "한", "두", "세", "네", "다섯", "여섯", "일곱", "여덟", "아홉"}
	koNativeTens = [10]string{"", "열", "스물", "서른", "마흔", "쉰", "예순", "일흔", "여든", "아흔"}
)

// koNative reads 1 ≤ n ≤ 99 in native Korean, attributive form (before a
// counter): 1 한, 20 스무, 21 스물한.
func koNative(n int) string {
	if n == 20 {
		return "스무"
	}
	return koNativeTens[n/10] + koNativeOnes[n%10]
}

func koMonth(m int) string {
	switch m {
	case 6:
		return "유월"
	case 10:
		return "시월"
	}
	return koCardinal(uint64(m)) + "월"
}

var koMoney = map[Currency]string{
	"KRW": "원", "USD": "달러", "EUR": "유로", "GBP": "파운드", "JPY": "엔", "CNY": "위안",
	"HKD": "홍콩 달러", "TWD": "대만 달러", "INR": "루피", "RUB": "루블",
}

func init() {
	var l *Language
	l = &Language{
		Code:        "ko",
		Spaced:      true,
		Cardinal:    koCardinal,
		Digits:      koDigits,
		DecimalWord: "점",
		Minus:       "마이너스",
		Percent:     func(w string) string { return w + " 퍼센트" },
		Time: func(h, m, sec int, _ string) string {
			hour := koCardinal(uint64(h))
			if h >= 1 && h <= 24 {
				hour = koNative(h)
			}
			out := hour + " 시"
			if m > 0 {
				out += " " + koCardinal(uint64(m)) + " 분"
			}
			if sec >= 0 {
				out += " " + koCardinal(uint64(sec)) + " 초"
			}
			return out
		},
		Date: func(y, m, d int) string {
			return koCardinal(uint64(y)) + "년 " + koMonth(m) + " " + koCardinal(uint64(d)) + "일"
		},
		DateOrder: "YMD",
		Symbols: map[string]Currency{"₩": "KRW", "원": "KRW", "$": "USD", "US$": "USD", "€": "EUR", "£": "GBP",
			"¥": "JPY", "￥": "JPY", "엔": "JPY", "HK$": "HKD", "NT$": "TWD", "₹": "INR", "₽": "RUB"},
		MoneyScales: `천|만|억|조`,
		Money: func(c Currency, n Num, scale string) string {
			return l.Number(n) + scale + " " + koMoney[c]
		},
		Range: func(a, b string) string { return a + "에서 " + b },
		Temperature: func(w string, unit byte) string {
			if unit == 'F' {
				return "화씨 " + w + "도"
			}
			return "섭씨 " + w + "도"
		},
	}
	counters := regexp.MustCompile(`(\d{1,2})` + sp + `?(시간|시|살|개|명|마리|번째|잔|권|병|송이)`)
	month := regexp.MustCompile(`(\d{1,2})` + sp + `?월`)
	l.Extra = []Rule{
		{Re: counters, Fn: func(s string, m []int) (string, bool) {
			n, _ := strconv.Atoi(group(s, m, 1))
			if n == 0 || gluedBefore(s, m[0]) {
				return "", false
			}
			c := group(s, m, 2)
			if c == "번째" && n == 1 {
				return "첫 번째", true
			}
			return koNative(n) + " " + c, true
		}},
		{Re: month, Fn: func(s string, m []int) (string, bool) {
			n, _ := strconv.Atoi(group(s, m, 1))
			if n < 1 || n > 12 || gluedBefore(s, m[0]) {
				return "", false
			}
			return koMonth(n), true
		}},
	}
	Register(l)
}
