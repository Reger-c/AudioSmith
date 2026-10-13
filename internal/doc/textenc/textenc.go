// Package textenc decodes text files of unknown encoding to UTF-8: BOMs,
// UTF-16 without a BOM, and the legacy encodings scripts arrive in
// (GB18030/GBK, Big5, Shift_JIS, EUC-KR, Windows-1252).
package textenc

import (
	"bytes"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
)

// Encoding names returned by Decode.
const (
	UTF8        = "utf-8"
	UTF16LE     = "utf-16le"
	UTF16BE     = "utf-16be"
	GB18030     = "gb18030"
	Big5        = "big5"
	ShiftJIS    = "shift_jis"
	EUCKR       = "euc-kr"
	Windows1252 = "windows-1252"
)

// Decode returns b as UTF-8 text with line endings normalised to "\n",
// and the encoding it chose. It never fails: undecodable bytes become
// U+FFFD in the last-resort Windows-1252 reading.
func Decode(b []byte) (string, string) {
	s, enc := decode(b)
	return normalizeNewlines(s), enc
}

func decode(b []byte) (string, string) {
	switch {
	case bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}):
		return toValidUTF8(b[3:]), UTF8
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}):
		return utf16Decode(b[2:], false), UTF16LE
	case bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		return utf16Decode(b[2:], true), UTF16BE
	}
	if bytes.IndexByte(b, 0) >= 0 {
		if le, ok := looksUTF16(b); ok {
			if le {
				return utf16Decode(b, false), UTF16LE
			}
			return utf16Decode(b, true), UTF16BE
		}
	}
	if utf8.Valid(b) {
		return string(b), UTF8
	}
	// Legacy CJK encodings decode each other's bytes into valid but
	// meaningless characters, so score each reading by how many of its
	// characters are among the language's most frequent ones.
	type cand struct {
		name  string
		enc   encoding.Encoding
		score func(string) float64
	}
	best, bestName, bestScore := "", "", 0.0
	for _, c := range []cand{
		{GB18030, simplifiedchinese.GB18030, func(s string) float64 { return share(s, isHan, zhCommon) }},
		{Big5, traditionalchinese.Big5, func(s string) float64 { return share(s, isHan, zhtCommon) }},
		{ShiftJIS, japanese.ShiftJIS, jaScore},
		{EUCKR, korean.EUCKR, func(s string) float64 { return share(s, isHangul, koCommon) }},
	} {
		out, err := c.enc.NewDecoder().Bytes(b)
		if err != nil || bytes.Contains(out, []byte("\uFFFD")) {
			continue
		}
		if sc := c.score(string(out)); sc > bestScore {
			best, bestName, bestScore = string(out), c.name, sc
		}
	}
	if bestScore >= 0.08 {
		return best, bestName
	}
	out, _ := charmap.Windows1252.NewDecoder().Bytes(b)
	return string(out), Windows1252
}

// Frequent characters: a genuine text has a large share of them, a
// misdecoded one almost none.
const (
	zhCommon  = "的一是不了在人有我他这个们中来上大为和国地到以说时要就出会可也你对生能而子那得于着下自之年过发后作里用道行所然家种事成方多经么去法学如都同现当没动面起看定天分还进好小部其些主样理心她本前开但因只从想实日"
	zhtCommon = "的一是不了在人有我他這個們中來上大為和國地到以說時要就出會可也你對生能而子那得於著下自之年過發後作裡用道行所然家種事成方多經麼去法學如都同現當沒動面起看定天分還進好小部其些主樣理心她本前開但因只從想實日"
	koCommon  = "이다는의에하고을가지서기리로사도한자시니어아수일대인요나습들를으은해있게보우정전장주상부것만국라내그제적여동성안할위세구면했과오비실개소경원무분생와화계문"
)

func isHan(r rune) bool    { return unicode.Is(unicode.Han, r) }
func isHangul(r rune) bool { return unicode.Is(unicode.Hangul, r) }

// share is the fraction of runes for which in is true that appear in
// common, scaled down when such runes are a minority of non-ASCII text.
func share(s string, in func(rune) bool, common string) float64 {
	n, k, other := 0, 0, 0
	for _, r := range s {
		switch {
		case r < 0x80:
		case in(r):
			n++
			if strings.ContainsRune(common, r) {
				k++
			}
		case !isCJKPunct(r):
			other++
		}
	}
	if n == 0 {
		return 0
	}
	return float64(k) / float64(n) * float64(n) / float64(n+other)
}

// jaScore is the share of hiragana among non-ASCII characters: Japanese
// prose is full of it, other encodings misread as Shift_JIS yield
// half-width katakana and rare kanji instead.
func jaScore(s string) float64 {
	n, k := 0, 0
	for _, r := range s {
		if r < 0x80 {
			continue
		}
		n++
		if unicode.Is(unicode.Hiragana, r) {
			k++
		}
	}
	if n == 0 {
		return 0
	}
	return float64(k) / float64(n)
}

func isCJKPunct(r rune) bool {
	return (r >= 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFFEF) || r == '“' || r == '”' || r == '‘' || r == '’' || r == '…' || r == '—' || r == '·'
}

// looksUTF16 detects BOM-less UTF-16 by the zero bytes ASCII leaves in
// every other position.
func looksUTF16(b []byte) (littleEndian, ok bool) {
	if len(b) < 4 || len(b)%2 != 0 {
		return false, false
	}
	n := min(len(b), 4096) &^ 1
	even, odd := 0, 0
	for i := 0; i < n; i += 2 {
		if b[i] == 0 {
			even++
		}
		if b[i+1] == 0 {
			odd++
		}
	}
	pairs := n / 2
	switch {
	case odd > pairs*3/10 && even < pairs/20:
		return true, true
	case even > pairs*3/10 && odd < pairs/20:
		return false, true
	}
	return false, false
}

func utf16Decode(b []byte, bigEndian bool) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		if bigEndian {
			u[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
		} else {
			u[i] = uint16(b[2*i+1])<<8 | uint16(b[2*i])
		}
	}
	return string(utf16.Decode(u))
}

func toValidUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	return string(bytes.ToValidUTF8(b, []byte("�")))
}

func normalizeNewlines(s string) string {
	if !bytes.ContainsRune([]byte(s), '\r') {
		return s
	}
	var b bytes.Buffer
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\r' {
			b.WriteByte('\n')
			if i+1 < len(s) && s[i+1] == '\n' {
				i++
			}
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}
