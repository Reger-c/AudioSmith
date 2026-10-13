package textenc

import (
	"testing"
	"unicode/utf16"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
)

func enc(t *testing.T, e encoding.Encoding, s string) []byte {
	t.Helper()
	b, err := e.NewEncoder().Bytes([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func u16(s string, be bool, bom bool) []byte {
	var out []byte
	if bom {
		if be {
			out = append(out, 0xFE, 0xFF)
		} else {
			out = append(out, 0xFF, 0xFE)
		}
	}
	for _, u := range utf16.Encode([]rune(s)) {
		if be {
			out = append(out, byte(u>>8), byte(u))
		} else {
			out = append(out, byte(u), byte(u>>8))
		}
	}
	return out
}

func TestDecode(t *testing.T) {
	zh := "第一章 开始\r\n他说：“今天天气很好，我们去公园散步吧。”\r\n"
	zhLF := "第一章 开始\n他说：“今天天气很好，我们去公园散步吧。”\n"
	ja := "第一章\nこんにちは、世界。今日はいい天気ですね。"
	ko := "제1장\n안녕하세요, 세계. 오늘은 날씨가 좋네요."
	tw := "第一章\n他說：「今天天氣很好，我們去公園散步吧。」"
	cases := []struct {
		name string
		in   []byte
		want string
		enc  string
	}{
		{"utf8", []byte("héllo\r\nworld\rend"), "héllo\nworld\nend", UTF8},
		{"utf8 bom", append([]byte{0xEF, 0xBB, 0xBF}, "你好"...), "你好", UTF8},
		{"utf16le bom", u16("Hi 你好", false, true), "Hi 你好", UTF16LE},
		{"utf16be bom", u16("Hi 你好", true, true), "Hi 你好", UTF16BE},
		{"utf16le no bom", u16("Hello, world. This is text.", false, false), "Hello, world. This is text.", UTF16LE},
		{"gbk", enc(t, simplifiedchinese.GBK, zh), zhLF, GB18030},
		{"shift_jis", enc(t, japanese.ShiftJIS, ja), ja, ShiftJIS},
		{"euc-kr", enc(t, korean.EUCKR, ko), ko, EUCKR},
		{"big5", enc(t, traditionalchinese.Big5, tw), tw, Big5},
		{"windows-1252", enc(t, charmap.Windows1252, "Café – naïve “quotes”"), "Café – naïve “quotes”", Windows1252},
	}
	for _, c := range cases {
		got, e := Decode(c.in)
		if got != c.want || e != c.enc {
			t.Errorf("%s: got %q (%s), want %q (%s)", c.name, got, e, c.want, c.enc)
		}
	}
}
