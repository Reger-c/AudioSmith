package subtitle

import (
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func TestParseSRT(t *testing.T) {
	src := "\ufeff1\r\n00:00:01,000 --> 00:00:02,500\r\n<i>Hello</i> {\\an8}world &amp; you\r\nsecond line\r\n\r\n" +
		"2\n00:00:03.000 --> 00:00:04,000 X1:10 X2:20\n你好，世界\n\n" +
		"garbage block\nwith no timing\n\n" +
		"00:01:05,5 --> 00:01:06,000\nno index\n\n" +
		"4\n00:00:09,000 --> 00:00:08,000\nbackwards\n"
	f, cues, ds := Parse(src)
	if f != FormatSRT || len(cues) != 4 {
		t.Fatalf("format %s, %d cues: %+v", f, len(cues), cues)
	}
	want := []Cue{
		{ID: "1", Start: ms(1000), End: ms(2500), Text: "Hello world & you\nsecond line"},
		{ID: "2", Start: ms(3000), End: ms(4000), Text: "你好，世界"},
		{ID: "", Start: ms(65500), End: ms(66000), Text: "no index"},
		{ID: "4", Start: ms(9000), End: ms(9000), Text: "backwards"},
	}
	for i := range want {
		if cues[i].ID != want[i].ID || cues[i].Start != want[i].Start || cues[i].End != want[i].End || cues[i].Text != want[i].Text {
			t.Errorf("cue %d = %+v, want %+v", i, cues[i], want[i])
		}
	}
	var codes []string
	for _, d := range ds {
		codes = append(codes, d.Code+"@"+d.Params["line"])
	}
	if !slices.Equal(codes, []string{CodeBadTiming + "@10", CodeBadOrder + "@16"}) {
		t.Errorf("diags %v", codes)
	}
}

func TestParseVTT(t *testing.T) {
	src := "WEBVTT - a title\nKind: captions\n\nNOTE this is a comment\nspanning lines\n\nSTYLE\n::cue { color: red }\n\n" +
		"intro\n00:01.000 --> 00:02.000 align:start position:10%\n<v Mara Lee>Hi <c.loud>there</c>&nbsp;friend\n\n" +
		"00:00:03.000 --> 00:00:05.000\n<00:00:03.000>Never <00:00:03.500>gonna <00:00:04.200>give\n\n" +
		"00:00:06.000 --> 00:00:07.000\n<ruby>漢字<rt>かんじ</rt></ruby>を読む\n"
	f, cues, ds := Parse(src)
	if f != FormatVTT || len(cues) != 3 || len(ds) != 0 {
		t.Fatalf("format %s, cues %+v, diags %+v", f, cues, ds)
	}
	if c := cues[0]; c.ID != "intro" || c.Start != ms(1000) || c.End != ms(2000) || c.Speaker != "Mara Lee" || c.Text != "Hi there friend" {
		t.Errorf("cue 0 %+v", c)
	}
	wantWords := []Word{{"Never", ms(3000), ms(3500)}, {"gonna", ms(3500), ms(4200)}, {"give", ms(4200), ms(5000)}}
	if c := cues[1]; c.Text != "Never gonna give" || !slices.Equal(c.Words, wantWords) {
		t.Errorf("cue 1 %+v", c)
	}
	if cues[2].Text != "漢字を読む" {
		t.Errorf("ruby: %q", cues[2].Text)
	}
}

func TestTimestamps(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"01:02:03,456":  time.Hour + 2*time.Minute + 3*time.Second + ms(456),
		"02:03.4":       2*time.Minute + 3*time.Second + ms(400),
		"100:00:00.000": 100 * time.Hour,
	} {
		if got, ok := ParseTimestamp(in); !ok || got != want {
			t.Errorf("ParseTimestamp(%q) = %v %v", in, got, ok)
		}
	}
	for _, bad := range []string{"aa:bb.ccc", "00:61:00.000", "5"} {
		if _, ok := ParseTimestamp(bad); ok {
			t.Errorf("ParseTimestamp(%q) ok", bad)
		}
	}
	if got := FormatTimestamp(time.Hour+ms(61234), ','); got != "01:01:01,234" {
		t.Errorf("FormatTimestamp = %s", got)
	}
}

func TestWriteRoundTrip(t *testing.T) {
	cues := []Cue{
		{Start: ms(1000), End: ms(2500), Text: "Hello <world> & you\nline two", Speaker: "Mara"},
		{Start: ms(3000), End: ms(4000), Text: "  "},
		{Start: ms(5000), End: ms(6000), Text: "你好\n\n世界", Words: []Word{{"你好", ms(5000), ms(5400)}, {"世界", ms(5400), ms(6000)}}},
	}
	var b strings.Builder
	if err := WriteSRT(&b, cues); err != nil {
		t.Fatal(err)
	}
	wantSRT := "1\n00:00:01,000 --> 00:00:02,500\nHello <world> & you\nline two\n\n2\n00:00:05,000 --> 00:00:06,000\n你好\n世界\n\n"
	if b.String() != wantSRT {
		t.Errorf("SRT\n%q\nwant\n%q", b.String(), wantSRT)
	}
	b.Reset()
	if err := WriteVTT(&b, cues, VTTOptions{Speakers: true, Karaoke: true, IDs: true}); err != nil {
		t.Fatal(err)
	}
	wantVTT := "WEBVTT\n\n1\n00:00:01.000 --> 00:00:02.500\n<v Mara>Hello &lt;world&gt; &amp; you\nline two\n\n" +
		"2\n00:00:05.000 --> 00:00:06.000\n你好<00:00:05.400>世界\n\n"
	if b.String() != wantVTT {
		t.Errorf("VTT\n%q\nwant\n%q", b.String(), wantVTT)
	}
	_, back, _ := Parse(b.String())
	if len(back) != 2 || back[0].Text != "Hello <world> & you\nline two" || back[0].Speaker != "Mara" || len(back[1].Words) != 2 {
		t.Errorf("VTT round trip %+v", back)
	}
}

func TestWriteASS(t *testing.T) {
	cues := []Cue{
		{Start: ms(1000), End: ms(3000), Text: "Hello brave\nnew world", Speaker: "A, B",
			Words: []Word{{"Hello", ms(1200), ms(1500)}, {"brave", ms(1600), ms(2000)}, {"new", ms(2000), ms(2300)}, {"world", ms(2300), ms(2900)}}},
		{Start: ms(4000), End: ms(5000), Text: "a {b} \\c"},
	}
	var b strings.Builder
	if err := WriteASS(&b, cues, ASSOptions{Karaoke: true, Title: "Test"}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"[Script Info]\nTitle: Test\nScriptType: v4.00+",
		"PlayResX: 1920\nPlayResY: 1080",
		"Style: Default,Arial,64,&H00FFFFFF,&H0000DCFF,&H00000000,&H80000000,0,0,0,0,100,100,0,0,1,3,1,2,60,60,50,1",
		`Dialogue: 0,0:00:01.00,0:00:03.00,Default,A  B,0,0,0,,{\k20}{\k40}Hello {\k40}brave\N{\k30}new {\k70}world`,
		`Dialogue: 0,0:00:04.00,0:00:05.00,Default,,0,0,0,,a ｛b｝ ＼c`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ASS lacks %q\n%s", want, out)
		}
	}
	b.Reset()
	_ = WriteASS(&b, cues[:1], ASSOptions{})
	if !strings.Contains(b.String(), `,,Hello brave\Nnew world`) {
		t.Errorf("plain ASS:\n%s", b.String())
	}
	if ASSColor(0x11, 0x22, 0x33, 0x44) != "&H44332211" {
		t.Error("ASSColor")
	}
}

func TestBreakLines(t *testing.T) {
	cases := []struct {
		in    string
		max   int
		lines int
		want  []string
	}{
		{"Short line", 42, 2, []string{"Short line"}},
		{"This is a much longer subtitle line, and it needs to be broken in two", 42, 2,
			[]string{"This is a much longer subtitle line,", "and it needs to be broken in two"}},
		{"I went to the store and bought some milk for the kids", 42, 2,
			[]string{"I went to the store", "and bought some milk for the kids"}},
		{"今天天气很好，我们一起去公园散步吧", 16, 2, []string{"今天天气很好，", "我们一起去公园散步吧"}},
		{"一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十一二三四", 16, 2, nil},
		{"supercalifragilisticexpialidocious", 10, 2, nil},
	}
	for _, c := range cases {
		got, ok := BreakLines(c.in, c.max, c.lines)
		if c.want == nil {
			if ok {
				t.Errorf("BreakLines(%q) = %q, want failure", c.in, got)
			}
			continue
		}
		if !ok || !slices.Equal(got, c.want) {
			t.Errorf("BreakLines(%q) = %q %v, want %q", c.in, got, ok, c.want)
		}
	}
}

func words(text string, start, per time.Duration) []Word {
	var out []Word
	at := start
	for _, f := range strings.Fields(text) {
		out = append(out, Word{f, at, at + per})
		at += per
	}
	return out
}

func TestSegment(t *testing.T) {
	en := "This is the first sentence of the talk. It is followed by a second one that is rather long and keeps going for a while, " +
		"with a clause here and more words after it until the end."
	segs := []Segment{
		{Start: 0, End: 30 * time.Second, Words: words(en, 0, 300*time.Millisecond), Speaker: "A"},
		{Start: 40 * time.Second, End: 41 * time.Second, Text: "Yes.", Speaker: "B"},
		{Start: 42 * time.Second, End: 46 * time.Second, Text: "我们今天讨论的是字幕的切分规则，以及如何让观众读得舒服，同时还要照顾到不同语言的阅读速度和习惯。"},
	}
	opt := Defaults("en")
	cues, _ := Split(segs, opt)
	if len(cues) < 4 {
		t.Fatalf("cues %+v", cues)
	}
	for i, c := range cues {
		lines := strings.Split(c.Text, "\n")
		max := opt.MaxCharsPerLine
		if len(lines) > 2 {
			t.Errorf("cue %d has %d lines: %q", i, len(lines), c.Text)
		}
		for _, l := range lines {
			if utf8.RuneCountInString(l) > max {
				t.Errorf("cue %d line too long (%d): %q", i, utf8.RuneCountInString(l), l)
			}
		}
		if c.End-c.Start > opt.MaxDuration {
			t.Errorf("cue %d lasts %v", i, c.End-c.Start)
		}
		if i > 0 && c.Start < cues[i-1].End {
			t.Errorf("cue %d overlaps the previous one", i)
		}
	}
	if !strings.HasSuffix(cues[0].Text, "talk.") {
		t.Errorf("first cue should end at the sentence: %q", cues[0].Text)
	}
	// "Yes." is extended to the minimum duration and keeps its speaker.
	var yes Cue
	for _, c := range cues {
		if c.Text == "Yes." {
			yes = c
		}
	}
	if yes.Speaker != "B" || yes.End-yes.Start < opt.MinDuration {
		t.Errorf("yes cue %+v", yes)
	}
	// The Chinese segment is split into CJK-sized cues with estimated words.
	zh, ds := Split(segs[2:], Defaults("zh-CN"))
	if len(zh) < 2 || !strings.HasSuffix(zh[0].Text, "，") {
		t.Errorf("zh cues should break after a comma: %q", zh[0].Text)
	}
	for _, c := range zh {
		if strings.Contains(c.Text, " ") {
			t.Errorf("CJK cue has spaces: %q", c.Text)
		}
		for _, l := range strings.Split(c.Text, "\n") {
			if utf8.RuneCountInString(l) > 16 {
				t.Errorf("zh line too long: %q", l)
			}
		}
	}
	if len(ds) == 0 || ds[0].Code != CodeCPS {
		t.Errorf("47 Chinese characters in 4 s exceed 9 CPS: %+v", ds)
	}
	// A long pause splits a cue.
	ws := []Word{{"Hello", 0, ms(400)}, {"there.", ms(500), ms(900)}, {"Again", ms(5000), ms(5400)}}
	if got, _ := Split([]Segment{{Words: ws}}, opt); len(got) != 2 {
		t.Errorf("pause split: %+v", got)
	}
}

func TestBilingualAndJoin(t *testing.T) {
	p := []Cue{{Start: 0, End: ms(2000), Text: "Hello"}, {Start: ms(2000), End: ms(4000), Text: "World"}}
	s := []Cue{{Start: ms(100), End: ms(1900), Text: "你好"}, {Start: ms(2100), End: ms(3900), Text: "世界"}}
	got := Bilingual(p, s)
	if got[0].Text != "Hello\n你好" || got[1].Text != "World\n世界" {
		t.Errorf("Bilingual %+v", got)
	}
	ws := []Word{{"Hello", 0, 0}, {",", 0, 0}, {"John", 0, 0}, {"'s", 0, 0}, {"你", 0, 0}, {"好", 0, 0}, {"(", 0, 0}, {"x", 0, 0}, {")", 0, 0}}
	if j := JoinWords(ws); j != "Hello, John's你好(x)" {
		t.Errorf("JoinWords = %q", j)
	}
}
