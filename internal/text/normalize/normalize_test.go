package normalize

import (
	"slices"
	"strings"
	"testing"
)

type tc struct{ in, want string }

func run(t *testing.T, lang string, cases []tc) {
	t.Helper()
	for _, c := range cases {
		if got := Text(c.in, lang, Options{}); got != c.want {
			t.Errorf("%s: Text(%q)\n got  %q\n want %q", lang, c.in, got, c.want)
		}
	}
}

func TestEnglish(t *testing.T) {
	run(t, "en-US", []tc{
		{"I have 3 apples.", "I have three apples."},
		{"0 and 7 and 13 and 20 and 21 and 99", "zero and seven and thirteen and twenty and twenty-one and ninety-nine"},
		{"1,234,567", "one million two hundred thirty-four thousand five hundred sixty-seven"},
		{"It costs 3.14 now", "It costs three point one four now"},
		{"Temperature is -5 today", "Temperature is minus five today"},
		{"100 and 1000 and 1001 and 1100", "one hundred and one thousand and one thousand one and one thousand one hundred"},
		{"10%", "ten percent"},
		{"Grew 12.5 % this year", "Grew twelve point five percent this year"},
		{"the 1st, 2nd, 3rd, 4th, 11th, 12th, 13th, 21st, 22nd, 100th", "the first, second, third, fourth, eleventh, twelfth, thirteenth, twenty-first, twenty-second, one hundredth"},
		{"the 2th", "the 2th"},
		{"Meet at 3:30.", "Meet at three thirty."},
		{"At 3:05 pm and 10:00 AM and 7:00", "At three oh five p m and ten a m and seven o'clock"},
		{"Run 1:02:03", "Run one oh two and three seconds"},
		{"On 2024-03-05 we met", "On March fifth, twenty twenty-four we met"},
		{"Due 12/25/2023.", "Due December twenty-fifth, twenty twenty-three."},
		{"It was $5.", "It was five dollars."},
		{"$1 and $1.50 and $0.99 and $2,000", "one dollar and one dollar and fifty cents and ninety-nine cents and two thousand dollars"},
		{"€5 and £3.20 and ¥500", "five euros and three pounds and twenty pence and five hundred yen"},
		{"$5 million raised and $2.5bn more", "five million dollars raised and two point five billion dollars more"},
		{"5 €", "five euros"},
		{"pages 3–5 and 10~20%", "pages three to five and ten percent to twenty percent"},
		{"Dr. Smith and Mr. Jones vs. Mrs. Brown", "Doctor Smith and Mister Jones versus Missus Brown"},
		{"apples, pears, etc. And more", "apples, pears, et cetera. And more"},
		{"apples, etc.", "apples, et cetera."},
		{"e.g. this and No. 5", "for example this and number five"},
		{"the dr. said no.", "the dr. said no."},
		{"In 1999 and since 2010 and the 1990s, 2024 people", "In nineteen ninety-nine and since twenty ten and the nineteen nineties, two thousand twenty-four people"},
		{"in 1905 and in 2005 and by 1900", "in nineteen oh five and in two thousand five and by nineteen hundred"},
		{"It's 25°C or 77 °F", "It's twenty-five degrees Celsius or seventy-seven degrees Fahrenheit"},
	})
}

func TestEnglishLeftAlone(t *testing.T) {
	run(t, "en", []tc{
		{"MP3 and 4K and COVID-19", "MP3 and 4K and COVID-19"},
		{"v1.2.3 and 192.168.1.1", "v1.2.3 and 192.168.1.1"},
		{"call 555-1234 or 24/7", "call 555-1234 or 24/7"},
		{"007 agent", "zero zero seven agent"},
		{"1234567890123456789", "one two three four five six seven eight nine zero one two three four five six seven eight nine"},
		{"Wait [pause 1.5s] for 2 [[GIF|jif]] [voice:Agent 47]", "Wait [pause 1.5s] for two [[GIF|jif]] [voice:Agent 47]"},
		{"x\x00y​z", "xyz"},
	})
}

func TestOff(t *testing.T) {
	if got := Text("I have 3 apples", "en", Options{Off: true}); got != "I have 3 apples" {
		t.Errorf("Off: %q", got)
	}
	if got := Text("a\x01 5", "xx", Options{}); got != "a 5" {
		t.Errorf("unsupported language: %q", got)
	}
	if !Supported("zh-Hans") || Supported("xx") {
		t.Error("Supported")
	}
	if got := Languages(); !slices.Equal(got, []string{"de", "en", "es", "fr", "ja", "ko", "zh"}) {
		t.Errorf("Languages %q", got)
	}
	for in, want := range map[string]string{"こんにちは世界": "ja", "안녕": "ko", "你好": "zh", "hello": "en"} {
		if g := Guess(in); g != want {
			t.Errorf("Guess(%q) = %s", in, g)
		}
	}
}

// TestNoPanicsAndTagsKept throws random mixes of tricky pieces at every
// language: nothing panics and bracket tags survive byte for byte.
func TestNoPanicsAndTagsKept(t *testing.T) {
	pieces := []string{"1", "23", "4,567", "8.9", "-", "−", "%", "$", "€", "¥", "£", "₩", "元", "円", "원", ":", "/",
		".", ",", " ", " ", "\n", "–", "~", "〜", "年", "月", "日", "h", "Uhr", "am", "pm", "st", "e", "º", "°C", "‰",
		"[pause 1.5s]", "[[3|three]]", "abc", "中文", "かな", "한글", "Dr.", "etc.", "z. B.", "M.", "2024", "12/31/1999",
		"2024-02-30", "99:99", "0", "00", "1e", "\x00"}
	seed := uint32(1)
	next := func() int {
		seed = seed*1664525 + 1013904223
		return int(seed >> 8)
	}
	for _, lang := range Languages() {
		for i := 0; i < 3000; i++ {
			var b []byte
			for n := next()%12 + 1; n > 0; n-- {
				b = append(b, pieces[next()%len(pieces)]...)
			}
			in := string(b)
			out := Text(in, lang, Options{})
			for _, tag := range []string{"[pause 1.5s]", "[[3|three]]"} {
				if strings.Count(in, tag) != strings.Count(out, tag) {
					t.Fatalf("%s: tag lost: %q → %q", lang, in, out)
				}
			}
		}
	}
}
