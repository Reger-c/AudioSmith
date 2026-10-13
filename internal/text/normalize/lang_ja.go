package normalize

import (
	"strings"
)

var jaDigits = [10]string{"零", "一", "二", "三", "四", "五", "六", "七", "八", "九"}

// jaSection reads 0 < n < 10000 without 一 before 十百千 (千二百三十四).
func jaSection(n uint64) string {
	units := [4]string{"", "十", "百", "千"}
	var b strings.Builder
	div := uint64(1000)
	for pos := 3; pos >= 0; pos-- {
		d := n / div % 10
		div /= 10
		if d == 0 {
			continue
		}
		if d != 1 || pos == 0 {
			b.WriteString(jaDigits[d])
		}
		b.WriteString(units[pos])
	}
	return b.String()
}

// jaCardinal reads n < 10^16 in kanji with 万/億/兆 grouping
// (12345 → 一万二千三百四十五, 1000 → 千, 0 → 零).
func jaCardinal(n uint64) string {
	if n == 0 {
		return "零"
	}
	units := [4]string{"", "万", "億", "兆"}
	var groups [4]uint64
	for i := 0; i < 4; i++ {
		groups[i] = n % 10000
		n /= 10000
	}
	var b strings.Builder
	for i := 3; i >= 0; i-- {
		g := groups[i]
		if g == 0 {
			continue
		}
		sec := jaSection(g)
		if i > 0 && g == 1 {
			sec = "一" // 一万, 一億
		} else if i > 0 && g >= 1000 && g/1000 == 1 {
			sec = "一" + sec // 一千万
		}
		b.WriteString(sec)
		b.WriteString(units[i])
	}
	return b.String()
}

var jaMoney = map[Currency]string{
	"JPY": "円", "USD": "ドル", "EUR": "ユーロ", "GBP": "ポンド", "CNY": "元", "KRW": "ウォン",
	"HKD": "香港ドル", "TWD": "台湾ドル", "INR": "ルピー", "RUB": "ルーブル",
}

func init() {
	var l *Language
	l = &Language{
		Code:        "ja",
		Cardinal:    jaCardinal,
		Digits:      jaDigits,
		DecimalWord: "点",
		Minus:       "マイナス",
		Percent:     func(w string) string { return w + "パーセント" },
		Time: func(h, m, sec int, _ string) string {
			out := jaCardinal(uint64(h)) + "時"
			if m > 0 {
				out += jaCardinal(uint64(m)) + "分"
			}
			if sec >= 0 {
				out += jaCardinal(uint64(sec)) + "秒"
			}
			return out
		},
		Date: func(y, m, d int) string {
			return jaCardinal(uint64(y)) + "年" + jaCardinal(uint64(m)) + "月" + jaCardinal(uint64(d)) + "日"
		},
		DateOrder: "YMD",
		Symbols: map[string]Currency{"¥": "JPY", "￥": "JPY", "円": "JPY", "$": "USD", "US$": "USD", "€": "EUR",
			"£": "GBP", "₩": "KRW", "HK$": "HKD", "NT$": "TWD", "₹": "INR", "₽": "RUB", "元": "CNY"},
		MoneyScales: `千|万|億|兆`,
		Money: func(c Currency, n Num, scale string) string {
			return l.Number(n) + scale + jaMoney[c]
		},
		Range: func(a, b string) string { return a + "から" + b },
		Temperature: func(w string, unit byte) string {
			if unit == 'F' {
				return "華氏" + w + "度"
			}
			return w + "度"
		},
	}
	l.Extra = cjkExtra(l, nil, nil, func(num, den string) string { return den + "分の" + num },
		func(w string) string { return w + "パーミル" })
	Register(l)
}
