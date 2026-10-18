package services

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/hehehai/AudioSmith/internal/core"
)

// bookOf is an audiobook of script with the cast Ines and Tomas.
func bookOf(script string) LongformDoc {
	d := NewLongformDoc(core.ProjectAudiobook, "m/kokoro")
	d.Seed, d.Voice = 42, "speaker:zm_yunxi"
	d.VoiceCast = map[string]string{"伊内丝": "speaker:zf_xiaobei", "Tomas": "speaker:zm_yunjian"}
	d.Script = script
	return d
}

// lineText is a compiled line as a test reads it.
func lineText(l LineSpec) string {
	var parts []string
	for _, s := range l.Spans {
		parts = append(parts, s.Voice+"="+s.Text)
	}
	s := fmt.Sprintf("%d %s %s", l.Block, l.Character, strings.Join(parts, " + "))
	if l.Paragraph {
		s = "¶ " + s
	}
	if l.PauseAfter > 0 {
		s += fmt.Sprintf(" (%d ms)", l.PauseAfter)
	}
	if l.Direction != "" {
		s += " {" + l.Direction + "}"
	}
	return s
}

func TestCompileBookBlocks(t *testing.T) {
	src := "# 第一章\n\n[voice:伊内丝]雾还没有散。\n\n[对话]\n伊内丝：这是去哈罗角的船吗？\n托马斯：“唯一的一班。”\n" +
		"他把缆绳抛上岸。\n（远处传来汽笛声。）\n[pause 1s]\nTomas（低声）：随便坐吧。\n船长：[slow]开船了[/slow]。\n？：谁？\n[/对话]\n" +
		"她坐下了。\n\n# 空\n\n# 第二章\n\n[对话]\n伊内丝：我一个人说话。\n[/对话]\n\n" +
		"【对话】\nアンナ：こんにちは。\n伊内丝：你好。\n【/对话】\n\n[/对话]"
	d := bookOf(src)
	spec, probs, blocks := CompileBook(&d, testVoices())
	if len(spec.Chapters) != 2 {
		t.Fatalf("chapters %d", len(spec.Chapters))
	}
	var got []string
	for _, l := range spec.Chapters[0].Lines {
		got = append(got, lineText(l))
	}
	want := []string{
		"0  speaker:zf_xiaobei=雾还没有散。",
		"¶ 1 伊内丝 speaker:zf_xiaobei=这是去哈罗角的船吗？",
		"1 托马斯 speaker:zm_yunxi=唯一的一班。",
		"1  speaker:zm_yunxi=他把缆绳抛上岸。 (1450 ms)",
		"1 Tomas speaker:zm_yunjian=随便坐吧。 {低声}",
		"1 船长 speaker:zm_yunxi=[slow]开船了[/slow]。",
		"1  speaker:zm_yunxi=谁？",
		// The voice before the block goes on after it.
		"¶ 0  speaker:zf_xiaobei=她坐下了。",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("chapter 1:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// Blocks of chapter 2: one speaker; another language.
	if ls := spec.Chapters[1].Lines; len(ls) != 3 || ls[0].Block != 1 || ls[1].Block != 2 || ls[2].Block != 2 || !ls[1].Paragraph {
		t.Fatalf("chapter 2: %+v", ls)
	}
	type bv struct {
		ch, idx   int
		speakers  string
		uncast    string
		turns     int
		lines     int
		unknown   int
		lang      string
		ok        bool
		tags      string
		spanStart string
	}
	var gotB []bv
	for _, b := range blocks {
		gotB = append(gotB, bv{b.Chapter, b.Index, strings.Join(b.Speakers, ","), strings.Join(b.Uncast, ","), b.Turns, b.Lines, b.Unknown, b.Lang, b.Language,
			strings.Join(b.Tags, " "), string([]rune(src[b.Span.Start:])[:3])})
	}
	wantB := []bv{
		{0, 1, "伊内丝,托马斯,Tomas,船长", "托马斯,船长", 5, 6, 1, "zh", true, "[slow]", "[对话"},
		{1, 1, "伊内丝", "", 1, 1, 0, "zh", true, "", "[对话"},
		{1, 2, "アンナ,伊内丝", "アンナ", 2, 2, 0, "ja", false, "", "【对话"},
	}
	if !slices.Equal(gotB, wantB) {
		t.Fatalf("blocks\n%+v\nwant\n%+v", gotB, wantB)
	}
	if got := strings.Join(codesOf(probs), " "); got != "longform.dialogue_stray_close longform.empty_chapter longform.dialogue_uncast longform.dialogue_unknown "+
		"longform.dialogue_tags longform.dialogue_one_speaker longform.dialogue_uncast longform.dialogue_language" {
		t.Errorf("problems %s", got)
	}
	for _, p := range probs {
		if p.Code == ProblemDialogueUncast && p.Chapter == 0 && p.Params["names"] != "托马斯, 船长" {
			t.Errorf("uncast %v", p.Params)
		}
		if p.Blocking {
			t.Errorf("blocking %+v", p)
		}
	}
	// A heading inside a block ends it; Compile is CompileBook without blocks.
	d = bookOf("[对话]\n伊内丝：你好。\n# 二\nTomas: Hi.\n[/对话]")
	spec2, probs2 := Compile(&d, testVoices())
	if len(spec2.Chapters) != 2 || spec2.Chapters[1].Lines[0].Block != 0 || !strings.Contains(strings.Join(codesOf(probs2), " "), ProblemDialogueUnclosed) {
		t.Errorf("heading: %+v %v", spec2.Chapters, codesOf(probs2))
	}
}

// A pause before a block's first line, or after its last, lengthens the
// paragraph gap around the block.
func TestBlockPausesAtEdges(t *testing.T) {
	d := bookOf("前面。\n\n[对话]\n[pause 2s]\n伊内丝：一。\nTomas: 二。\n[pause 500ms]\n[/对话]\n\n后面。")
	spec, _ := Compile(&d, testVoices())
	ls := spec.Chapters[0].Lines
	if len(ls) != 4 || ls[0].PauseAfter != 2800 || ls[2].PauseAfter != 1300 || ls[1].PauseAfter != 0 {
		var s []string
		for _, l := range ls {
			s = append(s, lineText(l))
		}
		t.Fatalf("lines %s", strings.Join(s, " | "))
	}
}

// Inside a block the gaps are the dialogue pacing's; around it the
// book's paragraph gaps.
func TestBookBlockGaps(t *testing.T) {
	d := bookOf("前面。\n\n[对话]\n伊内丝：你好吗？\nTomas: 很好。\nTomas: 走吧。\n他们走了。\n[/对话]\n后面。")
	spec, _ := Compile(&d, testVoices())
	ch := spec.Chapters[0]
	p := ChapterParams{Render: "r", Model: spec.Model, Seed: spec.Seed, Voices: spec.Voices, Lines: ch.Lines, Production: spec.Production,
		Output: "/tmp/ch.wav", Workflow: string(core.ProjectAudiobook)}
	_, steps := chapterKeyOf(t, p, kokoroInfo)
	var gaps []string
	for _, s := range steps {
		if s.chunk == nil && s.line > 0 {
			gaps = append(gaps, fmt.Sprint(s.silence))
		}
	}
	// paragraph, question → answer, same speaker, narration, paragraph.
	if got := strings.Join(gaps, " "); got != "0.8 0.22 0.18 0.45 0.8" {
		t.Errorf("gaps %s", got)
	}
}

func TestBlockLanguage(t *testing.T) {
	for _, c := range []struct {
		book, words, lang string
		ok                bool
	}{
		{"", "你好", "zh", true}, {"", "Hello", "en", true}, {"", "こんにちは", "ja", false}, {"", "안녕", "ko", false},
		{"en-GB", "你好", "en-gb", true}, {"zh-Hant", "x", "zh-hant", true}, {"fr", "Bonjour", "fr", false},
	} {
		if lang, ok := blockLanguage(c.book, c.words); lang != c.lang || ok != c.ok {
			t.Errorf("%q %q: %s %v", c.book, c.words, lang, ok)
		}
	}
}
