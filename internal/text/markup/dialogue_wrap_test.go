package markup

import (
	"slices"
	"strings"
	"testing"
	"unicode"
)

var zhWrap = WrapOptions{Open: "[对话]", Close: "[/对话]", Colon: "："}

func TestWrapDialogue(t *testing.T) {
	for _, c := range []struct {
		name, in string
		known    []string
		want     string
		speakers []string
		unknown  int
	}{
		{"verbs and narration kept", "小明说：“你好。”\n\n“你来了。”妈妈放下碗，轻声说，“饭在锅里。”", []string{"妈妈"},
			"[对话]\n小明说：\n小明：你好。\n妈妈：你来了。\n妈妈放下碗，轻声说，\n妈妈：饭在锅里。\n[/对话]", []string{"小明", "妈妈"}, 0},
		{"voice paragraphs", "[voice:伊内丝]这是去哈罗角的船吗？\n\n[voice:Tomas][slow]唯一的一班。[/slow]\n\n[voice:default]她坐下了。", nil,
			"[对话]\n伊内丝：这是去哈罗角的船吗？\nTomas：[slow]唯一的一班。[/slow]\n她坐下了。\n[/对话]", []string{"伊内丝", "Tomas"}, 0},
		{"screenplay and directions", "妈妈（温柔地）：宝贝，过来。\n孩子：我可以养它吗？\n旁白：雨停了。", nil,
			"[对话]\n妈妈（温柔地）：宝贝，过来。\n孩子：我可以养它吗？\n雨停了。\n[/对话]", []string{"妈妈", "孩子"}, 0},
		{"two speakers take turns", "小明说：“走吧。”\n\n小红说：“好。”\n\n“现在？”\n\n“现在。”", nil,
			"[对话]\n小明说：\n小明：走吧。\n小红说：\n小红：好。\n小明：现在？\n小红：现在。\n[/对话]", []string{"小明", "小红"}, 0},
		{"unknown with three", "甲：一。\n乙：二。\n丙：三。\n\n“谁？”", nil,
			"[对话]\n甲：一。\n乙：二。\n丙：三。\n？：谁？\n[/对话]", []string{"甲", "乙", "丙"}, 1},
		{"the cast's spelling", "INES: Hello.\nTomas: Hi.", []string{"ines"},
			"[对话]\nines：Hello.\nTomas：Hi.\n[/对话]", []string{"ines", "Tomas"}, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := zhWrap
			o.Known = c.known
			w := WrapDialogue(c.in, o)
			if w.Text != c.want || !slices.Equal(w.Speakers, c.speakers) || w.Unknown != c.unknown {
				t.Fatalf("got\n%s\n%q unknown %d", w.Text, w.Speakers, w.Unknown)
			}
			// What it wrote parses back as a block of those turns.
			blocks, diags := DialogueBlocks(w.Text)
			if len(blocks) != 1 || len(diags) != 0 || blocks[0].Turns() != w.Turns {
				t.Fatalf("parsed %+v %v", blocks, diags)
			}
		})
	}
	en := WrapDialogue(`"Hello," said Mara. "Sit." He sat.`, WrapOptions{Open: "[dialogue]", Close: "[/dialogue]", Colon: ": "})
	if en.Text != "[dialogue]\nMara: Hello,\nsaid Mara.\nMara: Sit.\nHe sat.\n[/dialogue]" {
		t.Errorf("en %q", en.Text)
	}
	if w := WrapDialogue("“谁？”\n\n“我。”\n\n“你？”", WrapOptions{Open: "[dialogue]", Close: "[/dialogue]", Colon: ": "}); !strings.Contains(w.Text, "\n?: 谁？") || w.Unknown != 3 {
		t.Errorf("en unknown %q", w.Text)
	}
}

// Every word of the selection is in the block: its turns' words and its
// narration, in order (the names, markers, quotes and spaces aside).
func TestWrapKeepsEveryWord(t *testing.T) {
	letters := func(s string) string {
		var b strings.Builder
		for _, r := range s {
			if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("。，！？.,!?…", r) {
				b.WriteRune(r)
			}
		}
		return b.String()
	}
	for _, in := range []string{
		"“你来了。”妈妈放下碗，轻声说，“饭在锅里。”他没有回答。",
		"雾还没有散，渡口只有一盏灯。\n\n“这是去哈罗角的船吗？”伊内丝问。\n\n托马斯说：“唯一的一班。”他把缆绳抛上岸。\n\n“要多久？”\n\n“两个钟头。”",
		`"Is this the boat?" Ines asked. The ferryman nodded. "The only one," said Tomas. "Sit anywhere."`,
		"她看了他一眼。\n“你确定？”\n“确定。”",
	} {
		w := WrapDialogue(in, zhWrap)
		blocks, _ := DialogueBlocks(w.Text)
		var said strings.Builder
		for _, l := range blocks[0].Lines {
			said.WriteString(l.Text.Of(w.Text))
		}
		if letters(said.String()) != letters(in) {
			t.Errorf("%q\n became %q\n says  %q", in, w.Text, said.String())
		}
	}
}

func TestUnwrapDialogue(t *testing.T) {
	src := "[voice:旁白甲]前面。\n\n[对话]\n伊内丝：你好。\n托马斯（低声）：嗯。\n他走了。\n（汽笛声。）\n[pause 1s]\n？：谁？\n[/对话]\n\n后面。"
	blocks, _ := DialogueBlocks(src)
	b := blocks[0]
	outer := VoiceAt(src, b.Span.Start)
	if outer != "旁白甲" {
		t.Fatalf("outer %q", outer)
	}
	got := UnwrapDialogue(src, b, outer)
	want := "[voice:伊内丝]你好。\n\n[voice:托马斯]（低声）嗯。\n\n[voice:旁白甲]他走了。\n\n（汽笛声。）\n\n[pause 1s]\n\n谁？"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	// Ending on a turn: the voice goes back after it.
	src = "[对话]\nA：一。\nB：二。\n[/对话]"
	blocks, _ = DialogueBlocks(src)
	if got := UnwrapDialogue(src, blocks[0], ""); got != "[voice:A]一。\n\n[voice:B]二。[voice:default]" {
		t.Errorf("got %q", got)
	}
	// A switch inside an earlier block is not the voice around a later one.
	src = "[voice:X]a\n\n[对话]\n[voice:Y]b\nZ：c\n[/对话]\n\nd\n\n[对话]\nA：e\n[/对话]\n\n# 二\n\n"
	if got := VoiceAt(src, strings.LastIndex(src, "[对话]")); got != "X" {
		t.Errorf("voice at the second block %q", got)
	}
	if got := VoiceAt(src, len(src)); got != "" {
		t.Errorf("after a heading %q", got)
	}
}
