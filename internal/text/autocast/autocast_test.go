package autocast

import (
	"fmt"
	"slices"
	"testing"
)

func view(r Result) []string {
	var out []string
	for _, l := range r.Lines {
		who := l.Speaker
		if who == "" {
			who = "-"
		}
		out = append(out, fmt.Sprintf("%s|%s", who, l.Text))
	}
	return out
}

func TestDetect(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		want     []string
		speakers []string
	}{
		{"tagged", "[Mara] Hello there.\n[Cole] Hi, Mara!\n[pause 1s]\n[mara] Again.",
			[]string{"Mara|Hello there.", "Cole|Hi, Mara!\n[pause 1s]", "Mara|Again."}, []string{"Mara", "Cole"}},
		{"tag continuation", "[Mara]\nI have\nmany lines.\n\n[laughter] Ha.",
			[]string{"Mara|I have\nmany lines.", "-|[laughter] Ha."}, []string{"Mara"}},
		{"screenplay", "MARA: Where were you?\nCOLE (V.O.): Out.\nNARRATOR: They paused.\nNote: not a speaker.",
			[]string{"Mara|Where were you?", "Cole|Out.", "-|They paused.\nNote: not a speaker."}, []string{"Mara", "Cole"}},
		{"screenplay title case", "Old Tom: Evening.\n\nThe Captain: Aye.",
			[]string{"Old Tom|Evening.", "Captain|Aye."}, []string{"Old Tom", "Captain"}},
		{"screenplay zh", "小明：你好！\n小红：你好呀。\n旁白：两人笑了。",
			[]string{"小明|你好！", "小红|你好呀。", "-|两人笑了。"}, []string{"小明", "小红"}},
		{"not screenplay", "At 10:30 we met.\nhttp://example.com is a site.\nthe end: it was.",
			[]string{"-|At 10:30 we met.", "-|http://example.com is a site.", "-|the end: it was."}, nil},
		{"narration before speakers", "It was late.\nMARA: Go home.",
			[]string{"-|It was late.", "Mara|Go home."}, []string{"Mara"}},
		{"prose said X", `"Where were you?" asked Mara. "Out," said the captain.`,
			[]string{"Mara|Where were you?", "-|asked Mara.", "Captain|Out,", "-|said the captain."}, []string{"Mara", "Captain"}},
		{"prose X said", `"Stay," Cole whispered. He left.`,
			[]string{"Cole|Stay,", "-|Cole whispered. He left."}, []string{"Cole"}},
		{"prose before", `Mara said, "Come in." Then she sat.`,
			[]string{"-|Mara said,", "Mara|Come in.", "-|Then she sat."}, []string{"Mara"}},
		{"prose inherit in paragraph", `"Hello," said John. "How are you?"`,
			[]string{"John|Hello,", "-|said John.", "John|How are you?"}, []string{"John"}},
		{"prose pronoun", `“Fine,” she said.`,
			[]string{"-|Fine,", "-|she said."}, nil},
		{"prose unattributed", `"Nobody said who."`,
			[]string{"-|Nobody said who."}, nil},
		{"british single quotes", `‘It’s cold,’ said Ann.`,
			[]string{"Ann|It’s cold,", "-|said Ann."}, []string{"Ann"}},
		{"zh before", "小明说：“你好。”小红笑了。",
			[]string{"-|小明说：", "小明|你好。", "-|小红笑了。"}, []string{"小明"}},
		{"zh after with adverb", "“快走！”小红低声说道。",
			[]string{"小红|快走！", "-|小红低声说道。"}, []string{"小红"}},
		{"zh pronoun and 知道", "他说：“好。”\n\n“我知道，”他知道了。",
			[]string{"-|他说：", "-|好。", "-|我知道，", "-|他知道了。"}, nil},
		{"zh comma before name", "他看着窗外，老王问：“几点了？”",
			[]string{"-|他看着窗外，老王问：", "老王|几点了？"}, []string{"老王"}},
		{"ja after", "「行こう」と太郎は言った。",
			[]string{"太郎|行こう", "-|と太郎は言った。"}, []string{"太郎"}},
		{"heading kept", "# Chapter 1\n\nMARA: Hi.",
			[]string{"-|# Chapter 1", "Mara|Hi."}, []string{"Mara"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Detect(c.in, nil)
			if got := view(r); !slices.Equal(got, c.want) {
				t.Errorf("lines\n got  %q\n want %q", got, c.want)
			}
			if !slices.Equal(r.Speakers, c.speakers) {
				t.Errorf("speakers %q, want %q", r.Speakers, c.speakers)
			}
			for _, l := range r.Lines {
				if l.Span.Of(c.in) != l.Text {
					t.Errorf("span mismatch %q vs %q", l.Span.Of(c.in), l.Text)
				}
			}
		})
	}
}

func TestOptions(t *testing.T) {
	r := Detect(`[Robot] Beep. "Hi," said Ann.`, &Options{Reserved: []string{"robot"}, KeepQuotes: true})
	want := []string{`-|[Robot] Beep.`, `Ann|"Hi,"`, "-|said Ann."}
	if got := view(r); !slices.Equal(got, want) {
		t.Errorf("got %q", got)
	}
}
