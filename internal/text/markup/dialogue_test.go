package markup

import (
	"slices"
	"strings"
	"testing"
)

// lineView is a block line as a test reads it.
type lineView struct {
	kind  DialogueLineKind
	name  string
	dir   string
	text  string
	pause int
}

func viewLines(src string, b DialogueBlock) []lineView {
	var out []lineView
	for _, l := range b.Lines {
		out = append(out, lineView{l.Kind, l.Name, l.Direction, l.Text.Of(src), l.Millis})
	}
	return out
}

func TestDialogueBlockLines(t *testing.T) {
	src := "# 第三章 渡口\n\n雾还没有散。\n\n[对话]\n伊内丝：这是去哈罗角的船吗？\n托马斯: “唯一的一班。”\n他把缆绳抛上岸，指了指灯下的长椅。\n" +
		"托马斯（低声）：随便坐吧，要两个钟头。\n\n（远处传来汽笛声。）\n[pause 1s]\n旁白：雾更浓了。\n[voice:伊内丝]两个钟头？\n[voice:default]她坐下了。\n" +
		"妈妈：（小声）快睡吧。\n？：谁？\n伊内丝：\n[/对话]\n\n她选了灯下的长椅。"
	blocks, diags := DialogueBlocks(src)
	if len(diags) != 0 || len(blocks) != 1 {
		t.Fatalf("blocks %d, diags %v", len(blocks), diags)
	}
	b := blocks[0]
	if !b.Closed() || b.Open.Of(src) != "[对话]" || b.Close.Of(src) != "[/对话]" || !strings.HasPrefix(b.Span.Of(src), "[对话]") || !strings.HasSuffix(b.Span.Of(src), "[/对话]") {
		t.Fatalf("block %+v", b)
	}
	want := []lineView{
		{DialogueTurn, "伊内丝", "", "这是去哈罗角的船吗？", 0},
		{DialogueTurn, "托马斯", "", "唯一的一班。", 0},
		{DialogueNarration, "", "", "他把缆绳抛上岸，指了指灯下的长椅。", 0},
		{DialogueTurn, "托马斯", "低声", "随便坐吧，要两个钟头。", 0},
		{DialogueDirection, "", "", "", 0},
		{DialoguePause, "", "", "", 1000},
		{DialogueNarration, "", "", "雾更浓了。", 0},
		{DialogueTurn, "伊内丝", "", "两个钟头？", 0},
		{DialogueNarration, "", "", "她坐下了。", 0},
		{DialogueTurn, "妈妈", "小声", "快睡吧。", 0},
		{DialogueTurn, "", "", "谁？", 0},
		{DialogueTurn, "伊内丝", "", "", 0},
	}
	if got := viewLines(src, b); !slices.Equal(got, want) {
		t.Fatalf("lines\n got %+v\nwant %+v", got, want)
	}
	if !b.Lines[10].Unknown || b.Lines[10].NameSpan.Of(src) != "？" {
		t.Fatalf("unknown speaker: %+v %q", b.Lines[10], b.Lines[10].NameSpan.Of(src))
	}
	if b.Lines[3].NameSpan.Of(src) != "托马斯" || b.Lines[3].DirSpan.Of(src) != "（低声）" {
		t.Fatalf("name %q dir %q", b.Lines[3].NameSpan.Of(src), b.Lines[3].DirSpan.Of(src))
	}
	if b.Lines[9].DirSpan.Of(src) != "（小声）" {
		t.Fatalf("leading direction %q", b.Lines[9].DirSpan.Of(src))
	}
	if b.Lines[7].NameSpan.Of(src) != "[voice:伊内丝]" {
		t.Fatalf("voice name span %q", b.Lines[7].NameSpan.Of(src))
	}
	if got := b.Speakers(); !slices.Equal(got, []string{"伊内丝", "托马斯", "妈妈"}) {
		t.Fatalf("speakers %v", got)
	}
	if b.Turns() != 6 {
		t.Fatalf("turns %d", b.Turns())
	}
}

func TestDialogueBlockMarkers(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		blocks    int
		diags     []string
		first     string // the first line's text
	}{
		{"zh", "[对话]\nA：x\n[/对话]", 1, nil, "x"},
		{"en", "[dialogue]\nMARA: Hello.\n[/dialogue]", 1, nil, "Hello."},
		{"case and spaces", "[ Dialogue ]\nMara: Hi.\n[ / DIALOGUE ]", 1, nil, "Hi."},
		{"full-width", "【对话】\n甲：好。\n【/对话】", 1, nil, "好。"},
		{"mixed", "【对话】\n甲：好。\n[/dialogue]", 1, nil, "好。"},
		{"same line", "[对话]甲：你好。\n乙：嗯。[/对话] 后面", 1, nil, "你好。"},
		{"unclosed", "[对话]\n甲：你好。\n乙：嗯。", 1, []string{CodeUnclosedDialogue}, "你好。"},
		{"nested", "[对话]\n甲：你好。\n[对话]\n乙：嗯。\n[/对话]", 1, []string{CodeNestedDialogue}, "你好。"},
		{"stray", "甲：你好。\n[/对话]", 0, []string{CodeStrayDialogueClose}, ""},
		{"empty", "[对话]\n\n（沉默。）\n[/对话]", 1, []string{CodeEmptyDialogue}, ""},
		{"heading", "[对话]\n甲：你好。\n# 第二章\n乙：嗯。\n[/对话]", 1, []string{CodeDialogueHeading, CodeStrayDialogueClose}, "你好。"},
		{"system tag is text", "【系统提示】任务完成。\n[pause]", 0, nil, ""},
		{"other words", "[对白]\n甲：你好。", 0, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blocks, diags := DialogueBlocks(tc.src)
			if len(blocks) != tc.blocks || !slices.Equal(codes(diags), tc.diags) {
				t.Fatalf("blocks %d diags %v", len(blocks), codes(diags))
			}
			if tc.first != "" {
				if got := blocks[0].Lines[0].Text.Of(tc.src); got != tc.first {
					t.Fatalf("first line %q", got)
				}
			}
		})
	}
	// A heading ends the block at its last line.
	src := "[对话]\n甲：你好。\n\n# 第二章\n乙：嗯。"
	blocks, _ := DialogueBlocks(src)
	if got := blocks[0].Span.Of(src); got != "[对话]\n甲：你好。" {
		t.Fatalf("span %q", got)
	}
	// Words before a closer on its line are the last line; after it, text.
	src = "[对话]\n甲：你好。\n乙：嗯。[/对话]然后"
	blocks, _ = DialogueBlocks(src)
	if n := len(blocks[0].Lines); n != 2 || blocks[0].Lines[1].Text.Of(src) != "嗯。" || !strings.HasSuffix(blocks[0].Span.Of(src), "[/对话]") {
		t.Fatalf("lines %+v", viewLines(src, blocks[0]))
	}
}

func TestDialogueEnglishLines(t *testing.T) {
	src := "[dialogue]\nINES: Is this the boat to Harrow Point?\nTomas (quietly): Sit anywhere. Two hours.\nHe threw the rope ashore.\n(A horn sounds.)\nNarrator: The fog thickened.\nNote: not a speaker.\n[/dialogue]"
	blocks, _ := DialogueBlocks(src)
	want := []lineView{
		{DialogueTurn, "Ines", "", "Is this the boat to Harrow Point?", 0},
		{DialogueTurn, "Tomas", "quietly", "Sit anywhere. Two hours.", 0},
		{DialogueNarration, "", "", "He threw the rope ashore.", 0},
		{DialogueDirection, "", "", "", 0},
		{DialogueNarration, "", "", "The fog thickened.", 0},
		{DialogueNarration, "", "", "Note: not a speaker.", 0},
	}
	if got := viewLines(src, blocks[0]); !slices.Equal(got, want) {
		t.Fatalf("lines\n got %+v\nwant %+v", got, want)
	}
	if got := blocks[0].Lines[0].NameSpan.Of(src); got != "INES" {
		t.Fatalf("name span %q", got)
	}
}

func TestDialogueTokens(t *testing.T) {
	src := "开头。\n[对话]\n甲：[slow]你好[/slow]。\n【/对话】\n[dialogue] [/Dialogue]"
	d := Parse(src, nil)
	var marks []string
	for _, tk := range d.Tokens {
		if tk.Kind == Dialogue {
			marks = append(marks, tk.Name+" "+tk.Span.Of(src))
		}
	}
	if !slices.Equal(marks, []string{"open [对话]", "close 【/对话】", "open [dialogue]", "close [/Dialogue]"}) {
		t.Fatalf("marks %v", marks)
	}
	if len(d.Diags) != 0 {
		t.Fatalf("diags %v", d.Diags)
	}
	if strings.Contains(d.Plain(), "对话") || strings.Contains(d.Plain(), "dialogue") {
		t.Fatalf("plain %q", d.Plain())
	}
	for _, r := range d.Runs() {
		if r.Kind == Dialogue {
			t.Fatal("a marker in the runs")
		}
	}
	if Dialogue.String() != "dialogue" {
		t.Fatal(Dialogue.String())
	}
}

func TestNameKey(t *testing.T) {
	for _, s := range []string{"Ines", " INES ", "ｉｎｅｓ", "ines　"} {
		if NameKey(s) != "ines" {
			t.Fatalf("%q → %q", s, NameKey(s))
		}
	}
	if NameKey("Old  Tom") != "old tom" {
		t.Fatal(NameKey("Old  Tom"))
	}
}
