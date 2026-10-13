package text

import (
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func texts(cs []Chunk) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Text
	}
	return out
}

func TestSentences(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"en", "Hello world. How are you? Fine!", []string{"Hello world.", "How are you?", "Fine!"}},
		{"abbrev", "Dr. Smith met Mr. Jones. They talked.", []string{"Dr. Smith met Mr. Jones.", "They talked."}},
		{"initials", "J. K. Rowling wrote it. Then she rested.", []string{"J. K. Rowling wrote it.", "Then she rested."}},
		{"decimal and url", "Pi is 3.14 today. See example.com now.", []string{"Pi is 3.14 today.", "See example.com now."}},
		{"lowercase after", "Use e.g. apples. Or pears.", []string{"Use e.g. apples.", "Or pears."}},
		{"quotes", `He said "Stop." Then he left.`, []string{`He said "Stop."`, "Then he left."}},
		{"ellipsis run", "Wait... What?! Really.", []string{"Wait...", "What?!", "Really."}},
		{"zh", "今天天气很好。我们去公园吧！你去吗？", []string{"今天天气很好。", "我们去公园吧！", "你去吗？"}},
		{"zh quotes", "他说：“你好。”她笑了。", []string{"他说：“你好。”", "她笑了。"}},
		{"ja", "こんにちは。元気ですか？はい、元気です。", []string{"こんにちは。", "元気ですか？", "はい、元気です。"}},
		{"ja brackets", "「行こう。」と彼は言った。", []string{"「行こう。」", "と彼は言った。"}},
		{"mixed", "Hello.你好。OK? 好的", []string{"Hello.", "你好。", "OK?", "好的"}},
		{"tag with period", "Wait [pause 1.5s] and go. Next.", []string{"Wait [pause 1.5s] and go.", "Next."}},
		{"override", "Say [[Dr.|doctor]] now. Done.", []string{"Say [[Dr.|doctor]] now.", "Done."}},
		{"tag after terminator", "Stop.[laughter] Next one.", []string{"Stop.[laughter] Next one."}},
		{"hindi danda", "नमस्ते। आप कैसे हैं?", []string{"नमस्ते।", "आप कैसे हैं?"}},
		{"arabic", "مرحبا. كيف حالك؟ بخير", []string{"مرحبا.", "كيف حالك؟", "بخير"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			for _, s := range SentenceSpans(c.in) {
				got = append(got, s.Of(c.in))
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestParas(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
		head []bool
	}{
		{"blank lines", "One.\nstill one.\n\nTwo.\n\n\n  Three.  ", []string{"One.\nstill one.", "Two.", "Three."}, []bool{false, false, false}},
		{"no blank lines", "Line one\nLine two\r\nLine three", []string{"Line one", "Line two", "Line three"}, nil},
		{"headings", "# One\nText a\nText b\n\n## Sub\nmore\n# Two\n\nbody", []string{"# One", "Text a\nText b", "## Sub\nmore", "# Two", "body"}, []bool{true, false, false, true, false}},
		{"crlf blank", "a\r\n\r\nb", []string{"a", "b"}, nil},
		{"fullwidth space blank", "甲\n　\n乙", []string{"甲", "乙"}, nil},
		{"empty", "  \n\n ", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ps := Paras(c.in)
			var got []string
			var head []bool
			for _, p := range ps {
				got = append(got, p.Of(c.in))
				head = append(head, p.Heading)
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("got  %q\nwant %q", got, c.want)
			}
			if c.head != nil && !slices.Equal(head, c.head) {
				t.Errorf("headings %v, want %v", head, c.head)
			}
		})
	}
}

func TestChunksPack(t *testing.T) {
	in := "One two. Three four. Five six seven.\n\nEight."
	cases := []struct {
		name string
		opt  Options
		want []string
	}{
		{"default fits per paragraph", Options{}, []string{"One two. Three four. Five six seven.", "Eight."}},
		{"cross paragraphs", Options{CrossParagraphs: true}, []string{"One two. Three four. Five six seven.\n\nEight."}},
		{"max 20", Options{MaxChars: 20}, []string{"One two. Three four.", "Five six seven.", "Eight."}},
		{"sentences", Options{Mode: Sentences}, []string{"One two.", "Three four.", "Five six seven.", "Eight."}},
		{"paragraphs", Options{Mode: Paragraphs}, []string{"One two. Three four. Five six seven.", "Eight."}},
		{"chapters", Options{Mode: Chapters, MaxChars: -1}, []string{"One two. Three four. Five six seven.\n\nEight."}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := texts(Chunks(in, c.opt))
			if !slices.Equal(got, c.want) {
				t.Errorf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestChunksHeadings(t *testing.T) {
	in := "# Chapter One\n\nIt was dark. It rained.\n\n# Chapter Two\nThe sun rose."
	got := Chunks(in, Options{Mode: Chapters, MaxChars: -1})
	want := []string{"# Chapter One", "It was dark. It rained.", "# Chapter Two", "The sun rose."}
	if !slices.Equal(texts(got), want) {
		t.Fatalf("got %q", texts(got))
	}
	if !got[0].Heading || got[1].Heading || !got[2].Heading {
		t.Errorf("heading flags wrong: %+v", got)
	}
	if got[3].Paragraph != 3 {
		t.Errorf("paragraph index %d, want 3", got[3].Paragraph)
	}
	// Pack never crosses a heading, even with CrossParagraphs.
	got = Chunks(in, Options{CrossParagraphs: true})
	if !slices.Equal(texts(got), want) {
		t.Errorf("pack across heading: %q", texts(got))
	}
}

func TestChunksLongSentence(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want []string
		cuts []Cut
	}{
		{"clause en", "alpha beta gamma, delta epsilon zeta eta theta", 20,
			[]string{"alpha beta gamma,", "delta epsilon zeta", "eta theta"}, []Cut{CutClause, CutSpace, CutNatural}},
		{"clause zh", "我们今天去公园散步，然后去超市买东西，最后回家做饭吃。", 12,
			[]string{"我们今天去公园散步，", "然后去超市买东西，", "最后回家做饭吃。"}, []Cut{CutClause, CutClause, CutNatural}},
		{"hard zh", "一二三四五六七八九十一二三四五", 6,
			[]string{"一二三四五六", "七八九十一二", "三四五"}, []Cut{CutHard, CutHard, CutNatural}},
		{"never inside tag", "aaaa[pause 500ms]bbbb", 8,
			[]string{"aaaa", "[pause 500ms]", "bbbb"}, nil},
		{"never inside override", "xx [[supercalifragilistic|super]] yy", 10,
			[]string{"xx", "[[supercalifragilistic|super]]", "yy"}, nil},
		{"line break", "first line without stop\nsecond line also long", 30,
			[]string{"first line without stop", "second line also long"}, []Cut{CutLine, CutNatural}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Use a blank line elsewhere so the single newline stays inside
			// one paragraph.
			src := c.in
			if strings.Contains(src, "\n") {
				src += "\n\nEnd."
			}
			cs := Chunks(src, Options{MaxChars: c.max})
			if strings.Contains(c.in, "\n") {
				cs = cs[:len(cs)-1]
			}
			if !slices.Equal(texts(cs), c.want) {
				t.Fatalf("got  %q\nwant %q", texts(cs), c.want)
			}
			for i, ch := range cs {
				if ch.Text != src[ch.Start:ch.End] {
					t.Errorf("chunk %d offsets do not match text", i)
				}
				if c.cuts != nil && ch.Cut != c.cuts[i] {
					t.Errorf("chunk %d cut %d, want %d", i, ch.Cut, c.cuts[i])
				}
			}
		})
	}
}

func TestChunksInvariants(t *testing.T) {
	src := strings.Repeat("这是一个很长的句子，里面有逗号，还有更多的文字[laughter]以及英文 words mixed in. ", 40) +
		"\n\n" + strings.Repeat("Short one. ", 200) + "[[GIF|jif]] end"
	tags := Tags(src)
	for _, max := range []int{10, 50, 120, 800} {
		cs := Chunks(src, Options{MaxChars: max})
		prevEnd := 0
		for i, c := range cs {
			if c.Start < prevEnd {
				t.Fatalf("max %d: chunk %d overlaps", max, i)
			}
			prevEnd = c.End
			if n := utf8.RuneCountInString(c.Text); n > max {
				// Only a single over-long tag may exceed the cap.
				if len(Tags(c.Text)) != 1 || Tags(c.Text)[0].Len() != len(c.Text) {
					t.Fatalf("max %d: chunk %d has %d runes: %q", max, i, n, c.Text)
				}
			}
			for _, tg := range tags {
				if (c.Start > tg.Start && c.Start < tg.End) || (c.End > tg.Start && c.End < tg.End) {
					t.Fatalf("max %d: chunk %d cuts tag %q", max, i, tg.Of(src))
				}
			}
		}
		// Nothing but white space is lost between chunks.
		var b strings.Builder
		for _, c := range cs {
			b.WriteString(c.Text)
		}
		if strings.Join(strings.Fields(b.String()), "") != strings.Join(strings.Fields(src), "") {
			t.Fatalf("max %d: content lost", max)
		}
	}
}

func TestTags(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"a [pause] b [voice:Mara] c", []string{"[pause]", "[voice:Mara]"}},
		{"[[GIF|jif]] and [x]", []string{"[[GIF|jif]]", "[x]"}},
		{"unclosed [ here\n] no", nil},
		{"[a [b] c", []string{"[b]"}},
		{"[[bad] ok", []string{"[bad]"}},
		{"[]", []string{"[]"}},
		{"中文[笑声]中文", []string{"[笑声]"}},
	}
	for _, c := range cases {
		var got []string
		for _, s := range Tags(c.in) {
			got = append(got, s.Of(c.in))
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("Tags(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := Outside("a1 [b1] c1", strings.ToUpper); got != "A1 [b1] C1" {
		t.Errorf("Outside = %q", got)
	}
}

func TestCount(t *testing.T) {
	cases := []struct {
		in           string
		words, chars int
	}{
		{"Hello, world! It's fine.", 4, 0},
		{"你好世界", 0, 4},
		{"我用 Python 写代码。", 1, 5},
		{"안녕하세요 세계", 2, 0},
		{"Wait [pause 2s] now [[GIF|jif]]", 3, 0},
		{"こんにちは、世界", 0, 7},
		{"state-of-the-art", 1, 0},
	}
	for _, c := range cases {
		w, ch := Count(c.in)
		if w != c.words || ch != c.chars {
			t.Errorf("Count(%q) = %d,%d want %d,%d", c.in, w, ch, c.words, c.chars)
		}
	}
	if d := Estimate(155, 0); d != time.Minute {
		t.Errorf("Estimate(155,0) = %v", d)
	}
	if d := Estimate(0, 9); d != 2*time.Second {
		t.Errorf("Estimate(0,9) = %v", d)
	}
}

func TestKey(t *testing.T) {
	p := map[string]string{"model": "kokoro", "voice": "af_heart", "speed": "1"}
	k := Key("Hello  world.\n", p)
	if len(k) != 64 {
		t.Fatalf("key length %d", len(k))
	}
	if Key(" Hello world. ", map[string]string{"speed": "1", "voice": "af_heart", "model": "kokoro"}) != k {
		t.Error("white space or param order changed the key")
	}
	if Key("Café", nil) != Key("Café", nil) {
		t.Error("NFC not applied")
	}
	if Key("hello world.", p) == k {
		t.Error("case should matter")
	}
	if Key("Hello world.", map[string]string{"model": "kokoro", "voice": "af_heart", "speed": "1.1"}) == k {
		t.Error("params ignored")
	}
	if Key("a", map[string]string{"b": "c"}) == Key("a", map[string]string{"bc": ""}) {
		t.Error("field boundaries ambiguous")
	}
}
