package dialogue

import (
	"fmt"
	"testing"
)

func turn(voice, text string) Item { return Item{Voice: voice, Text: text, Turn: true} }

func narration(text string) Item { return Item{Text: text, Aside: ShortAside(text)} }

// timed is a dub line: a turn at start, lasting secs.
func timed(voice string, start, secs float64) Item {
	return Item{Voice: voice, Text: "一句话。", Turn: true, Start: start, End: start + secs}
}

func TestLimitsFor(t *testing.T) {
	if l := LimitsFor("soulx_podcast"); l != (Limits{4, 16, 400, 1200}) {
		t.Errorf("SoulX %+v", l)
	}
	if l := LimitsFor(FamilyMossTTSD); l != (Limits{5, 24, 240, 720}) {
		t.Errorf("MOSS %+v", l)
	}
}

// A story: runs of character lines; a short aside keeps the run, a long
// narration line or a line that is no turn (pause markup) ends it; a
// fifth voice and the line limit start another; single-voice runs stay.
func TestGroupStory(t *testing.T) {
	items := []Item{
		narration("那天晚上，一家人吃完饭坐在客厅里，窗外下着小雨，屋里很安静。"),
		turn("kid", "妈妈，我想养猫。"), narration("她小声说。"), turn("mom", "你能照顾它吗？"),
		turn("kid", "我能！"),
		narration("第二天早上，太阳升起来了，孩子背着书包高高兴兴地去了学校，一路上都在想小猫。"),
		turn("a", "一。"), turn("b", "二。"), turn("c", "三。"), turn("d", "四。"), turn("e", "五。"),
		{Voice: "kid", Text: "[pause 500ms] 嗯。"}, // no turn, no aside: ends the run
	}
	if got := fmt.Sprint(Group(items, LimitsFor("soulx_podcast"), StoryRules)); got != "[[1 3 4] [6 7 8 9] [10]]" {
		t.Errorf("groups %s", got)
	}
	var many []Item
	for i := range 20 {
		many = append(many, turn("kid", fmt.Sprintf("第%d句。", i)))
	}
	if g := Group(many, LimitsFor("soulx_podcast"), StoryRules); len(g) != 2 || len(g[0]) != 16 {
		t.Errorf("turn limit: %v", g)
	}
	// Text limits: 60 s of Chinese for MOSS-TTSD, other text by words.
	long := []Item{turn("a", repeat("好", 200)), turn("b", repeat("好", 100))}
	if g := Group(long, LimitsFor(FamilyMossTTSD), StoryRules); len(g) != 2 {
		t.Errorf("CJK limit: %v", g)
	}
	words := []Item{turn("a", repeat("word ", 100)), turn("b", repeat("word ", 30))}
	if g := Group(words, LimitsFor(FamilyMossTTSD), StoryRules); len(g) != 2 {
		t.Errorf("word limit: %v", g)
	}
	if !ShortAside("他说。") || ShortAside(repeat("很长的叙述", 6)) || ShortAside(repeat("words ", 14)) {
		t.Error("asides")
	}
}

func repeat(s string, n int) string {
	out := ""
	for range n {
		out += s
	}
	return out
}

// A dub: a silence over 4 s ends a conversation; one voice alone, or two
// voices with one change, is no conversation; a monologue (4 lines, or
// 30 s) is cut out and the sides stand alone; short replies stay; a line
// that cannot be a turn (another language, "no dialogue") ends it.
func TestGroupDub(t *testing.T) {
	lim := LimitsFor("soulx_podcast")
	items := []Item{
		timed("A", 0, 2), timed("B", 2.5, 0.3), timed("A", 3, 2), // a short "嗯" stays
		timed("B", 10, 2), timed("A", 12.2, 1), // 5 s of silence: a new run, A B: one change only
		timed("A", 20, 3),                                                          // alone
		timed("A", 30, 2), timed("B", 32, 2), timed("A", 34, 2), timed("B", 36, 2), // kept
	}
	if got := fmt.Sprint(Group(items, lim, DubRules)); got != "[[0 1 2] [6 7 8 9]]" {
		t.Errorf("dub groups %s", got)
	}
	// A line that is no turn ends the run.
	no := []Item{timed("A", 0, 1), timed("B", 1, 1), {Voice: "A", Text: "song", Start: 2, End: 3}, timed("A", 3, 1), timed("B", 4, 1), timed("A", 5, 1)}
	if got := fmt.Sprint(Group(no, lim, DubRules)); got != "[[3 4 5]]" {
		t.Errorf("excluded line: %s", got)
	}
	// A monologue of four lines goes; each side is judged on its own.
	mono := []Item{timed("A", 0, 1), timed("B", 1, 1), timed("A", 2, 1),
		timed("B", 3, 1), timed("B", 4, 1), timed("B", 5, 1), timed("B", 6, 1),
		timed("A", 7, 1), timed("C", 8, 1), timed("A", 9, 1)}
	if got := fmt.Sprint(Group(mono, lim, DubRules)); got != "[[0 1 2] [7 8 9]]" {
		t.Errorf("monologue by lines: %s", got)
	}
	// A monologue of 30 s in two lines goes too.
	long := []Item{timed("A", 0, 1), timed("B", 1, 1), timed("A", 2, 15), timed("A", 17.5, 15), timed("B", 33, 1), timed("A", 34, 1), timed("B", 35, 1)}
	if got := fmt.Sprint(Group(long, lim, DubRules)); got != "[[4 5 6]]" {
		t.Errorf("monologue by time: %s", got)
	}
	// Untimed lines count their text's time.
	untimed := Rules{MonologueSeconds: 2}
	if got := fmt.Sprint(Group([]Item{turn("A", "你好你好你好"), turn("A", "你好你好你好"), turn("B", "好")}, lim, untimed)); got != "[[2]]" {
		t.Errorf("untimed monologue: %s", got)
	}
	// Five voices: SoulX takes four, the fifth's lines start another
	// segment, which alone is no conversation.
	five := []Item{timed("A", 0, 1), timed("B", 1, 1), timed("C", 2, 1), timed("D", 3, 1), timed("E", 4, 1), timed("D", 5, 1), timed("E", 6, 1)}
	if got := fmt.Sprint(Group(five, lim, DubRules)); got != "[[0 1 2 3] [4 5 6]]" {
		t.Errorf("five voices: %s", got)
	}
	if got := fmt.Sprint(Group(five[:5], lim, DubRules)); got != "[[0 1 2 3]]" {
		t.Errorf("five voices, the fifth alone: %s", got)
	}
}

// An audiobook: lines outside blocks never group; narration inside a
// block, however long, does not end it; two blocks never join; a block of
// one voice is read line by line.
func TestGroupBook(t *testing.T) {
	in := func(b int, it Item) Item { it.Block = b; return it }
	items := []Item{
		turn("A", "外面的台词。"), turn("B", "也在外面。"),
		in(1, turn("A", "这是去哈罗角的船吗？")), in(1, turn("B", "唯一的一班。")),
		in(1, Item{Text: repeat("他把缆绳抛上岸，指了指灯下的长椅。", 4), Aside: true}),
		in(1, turn("B", "随便坐吧。")),
		in(2, turn("A", "两个钟头？")), in(2, turn("B", "地图总是骗人的。")),
		turn("A", "又在外面。"),
		in(3, turn("A", "一个人的块。")),
	}
	if got := fmt.Sprint(Group(items, LimitsFor("soulx_podcast"), BookRules)); got != "[[2 3 5] [6 7]]" {
		t.Errorf("book groups %s", got)
	}
}

func TestSeconds(t *testing.T) {
	if s := Seconds(Item{Start: 1, End: 3.5}); s != 2.5 {
		t.Errorf("timed %v", s)
	}
	if s := Seconds(Item{Text: "你好你好你好你好你"}); s != 2 {
		t.Errorf("estimate %v", s)
	}
}
