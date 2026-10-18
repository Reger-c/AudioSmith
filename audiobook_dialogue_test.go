package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui/editor"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/services"
)

const ferryManuscript = "# 渡口\n\n雾还没有散。\n\n[voice:伊内丝]我先上船。\n\n“这是去哈罗角的船吗？”伊内丝问。\n\n托马斯说：“唯一的一班。”\n\n后面的叙述。"

// Insert → Dialogue passage: an empty passage with the latest name
// selected; the selection made a passage with Undo; whole dialogue turns
// on; the inspector lists it; its speakers without a voice get their own.
func TestAudiobookDialogueInsert(t *testing.T) {
	stubMachine(t, 16, true)
	a, eng, tt := newTestApp(t)
	tt.SetSize(1100, 1400)
	a.sys = &sysInfo{Memory: 16 << 30}
	a.navigate("/audiobook")
	p := a.lfBook
	a.setManuscript(p, ferryManuscript)
	p.doc.Voice = "speaker:af_heart"
	tt.Frame()

	// The template after the voice paragraph: its name first.
	at := strings.Index(p.doc.Script, "我先上船。") + len("我先上船。")
	p.ed.Select(at, at)
	tt.Click("Insert")
	if err := tt.ChooseMenuItem("Dialogue passage"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !strings.Contains(p.doc.Script, "我先上船。\n\n[dialogue]\n伊内丝: \nSpeaker B: \n[/dialogue]\n\n“这是") || p.ed.SelectedText() != "伊内丝" {
		t.Fatalf("template %q, selected %q", p.doc.Script, p.ed.SelectedText())
	}
	if !p.doc.Production.Dialogue || p.doc.Production.DialogueModel == "" {
		t.Errorf("dialogue not on: %+v", p.doc.Production)
	}
	p.ed.Buffer.Undo()
	a.syncManuscript(p)
	if p.doc.Script != ferryManuscript {
		t.Fatalf("undo %q", p.doc.Script)
	}

	// The two quote paragraphs (part of the first selected) become a passage.
	start := strings.Index(p.doc.Script, "这是去")
	end := strings.Index(p.doc.Script, "一班。”") + 3
	p.ed.Select(start, end)
	tt.Click("Insert")
	if err := tt.ChooseMenuItem("Dialogue passage"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	want := "[voice:伊内丝]我先上船。\n\n[dialogue]\n伊内丝: 这是去哈罗角的船吗？\n伊内丝问。\n托马斯说：\n托马斯: 唯一的一班。\n[/dialogue]\n\n后面的叙述。"
	if !strings.Contains(p.doc.Script, want) {
		t.Fatalf("wrapped %q", p.doc.Script)
	}
	if !hasAnyText(tt.Texts(), "Made a dialogue passage: 2 speakers · 2 lines") {
		t.Errorf("toast %q", tt.Texts())
	}
	tt.Frame()
	if len(p.blocks) != 1 || !slices.Equal(p.blocks[0].Speakers, []string{"伊内丝", "托马斯"}) {
		t.Fatalf("blocks %+v", p.blocks)
	}
	// The cast lists the passage's speakers; the inspector the passage.
	if got := bookCast(p); !slices.Equal(got, []string{"伊内丝", "托马斯"}) {
		t.Errorf("cast %q", got)
	}
	for _, s := range []string{"Dialogue passages", "渡口 · 伊内丝, 托马斯", "伊内丝, 托马斯 have no voices yet", "Assign voices", "1 dialogue passage"} {
		if !hasAnyText(tt.Texts(), s) {
			t.Errorf("no %q in %q", s, tt.Texts())
		}
	}
	// The editor: the passage tinted, a dot per line, its summary.
	if bg := p.ed.LineBackgrounds; len(bg) != 1 || bg[0].First != 6 || bg[0].Last != 11 || len(p.ed.GutterMarks) != 6 {
		t.Errorf("styling %+v %d marks", bg, len(p.ed.GutterMarks))
	}
	if len(p.deco.inlays) != 1 || !strings.Contains(p.deco.inlays[0].Text, "2 speakers · 2 lines · ") {
		t.Errorf("inlays %+v", p.deco.inlays)
	}
	// 自动分配: each gets a voice of their own, unlike the narrator's.
	tt.Click("Assign voices")
	tt.Frame()
	ines, tomas := p.doc.VoiceCast["伊内丝"], p.doc.VoiceCast["托马斯"]
	if ines == "" || tomas == "" || ines == tomas || ines == p.doc.Voice || tomas == p.doc.Voice || len(uncastNames(p.blocks)) != 0 {
		t.Errorf("assigned %+v (narrator %s)", p.doc.VoiceCast, p.doc.Voice)
	}

	// The passage's preview renders its lines alone.
	a.startLf(p, blockWhat(p.blocks[0]))
	tt.Frame()
	jl := lfJobs(eng)
	if len(jl) != 1 {
		t.Fatalf("preview jobs %d", len(jl))
	}
	cp := chapterParams(t, jl[0])
	if len(cp.Lines) != 4 || cp.Lines[0].Paragraph || slices.ContainsFunc(cp.Lines, func(l services.LineSpec) bool { return l.Block != 1 }) {
		t.Errorf("preview lines %+v", cp.Lines)
	}

	// Turn back into paragraphs: the voice around it goes on after it.
	a.unwrapDialogue(p, strings.Index(p.doc.Script, "[dialogue]")+2)
	tt.Frame()
	if want := "[voice:伊内丝]我先上船。\n\n[voice:伊内丝]这是去哈罗角的船吗？\n\n[voice:default]伊内丝问。\n\n托马斯说：\n\n[voice:托马斯]唯一的一班。[voice:伊内丝]\n\n后面的叙述。"; !strings.Contains(p.doc.Script, want) {
		t.Errorf("unwrapped %q", p.doc.Script)
	}
}

// Without the dialogue model the passages are read line by line: not
// downloaded, or too little memory.
func TestBookDialogueReadiness(t *testing.T) {
	stubMachine(t, 16, true)
	a, _, _ := newTestApp(t)
	a.sys = &sysInfo{Memory: 16 << 30}
	p := a.lfBook
	a.setManuscript(p, "[对话]\n甲：你好。\n乙：嗯。\n[/对话]")
	p.doc.Voice = "speaker:af_heart"
	a.compileLf(p)
	a.bookDialogueDefault(p)
	if !p.doc.Production.Dialogue {
		t.Fatal("a hand-written passage turns dialogue on")
	}
	e, _, _ := dialogueChoice(16, &p.doc)
	m := a.model(e)
	m.known, m.local.State = true, hub.Missing
	spec := p.spec
	a.bookDialogueSpec(p, &spec)
	if spec.Production.Dialogue {
		t.Error("not downloaded, still dialogue")
	}
	m.local.State = hub.Downloaded
	spec = p.spec
	a.bookDialogueSpec(p, &spec)
	if !spec.Production.Dialogue || spec.Production.DialogueModel != e.Repo {
		t.Errorf("downloaded: %+v", spec.Production)
	}
	a.sys = &sysInfo{Memory: 8 << 30}
	spec = p.spec
	a.bookDialogueSpec(p, &spec)
	if spec.Production.Dialogue {
		t.Error("8 GB, still dialogue")
	}
	// The user's own choice stays.
	p.doc.Production.Dialogue = false
	a.sys = &sysInfo{Memory: 16 << 30}
	a.bookDialogueDefault(p)
	if p.doc.Production.Dialogue {
		t.Error("turned on again")
	}
}

func TestManuscriptTokens(t *testing.T) {
	kinds := func(line string, state uint64) ([]string, uint64) {
		toks, st := tokenizeManuscript(line, state, nil)
		var out []string
		prev := 0
		for _, tk := range toks {
			if tk.End > prev {
				out = append(out, line[prev:tk.End]+"="+tokName(tk.Kind))
			}
			prev = tk.End
		}
		return out, st
	}
	got, st := kinds("[对话]", 0)
	if st != 1 || !slices.Equal(got, []string{"[对话]=marker"}) {
		t.Errorf("open %q %d", got, st)
	}
	got, st = kinds("伊内丝（低声）：[slow]你好[/slow]", 1)
	if st != 1 || !slices.Equal(got, []string{"伊内丝=speaker", "（低声）=direction", "：=text", "[slow]=type", "你好=text", "[/slow]=type"}) {
		t.Errorf("turn %q", got)
	}
	got, _ = kinds("（汽笛声。）", 1)
	if !slices.Equal(got, []string{"（汽笛声。）=direction"}) {
		t.Errorf("direction %q", got)
	}
	got, _ = kinds("？：谁？", 1)
	if !slices.Equal(got, []string{"？=invalid", "：谁？=text"}) {
		t.Errorf("unknown %q", got)
	}
	if _, st = kinds("# 第二章", 1); st != 0 {
		t.Error("a heading ends a passage")
	}
	got, st = kinds("嗯。[/对话] 后来", 1)
	if st != 0 || !slices.Equal(got, []string{"嗯。=text", "[/对话]=marker", " 后来=text"}) {
		t.Errorf("close %q %d", got, st)
	}
	// Outside a passage, as before.
	got, st = kinds("伊内丝：你好 [voice:Mara]", 0)
	if st != 0 || !slices.Equal(got, []string{"伊内丝：你好 =text", "[voice:Mara]=variable"}) {
		t.Errorf("outside %q", got)
	}
	if speakerKind("Ines") != speakerKind(" INES ") {
		t.Error("a name's colour")
	}
}

func tokName(k editor.TokenKind) string {
	switch {
	case k == tokenMarker:
		return "marker"
	case k == tokenDirection:
		return "direction"
	case slices.Contains(speakerKinds, k):
		return "speaker"
	case k == editor.TokenInvalid:
		return "invalid"
	case k == editor.TokenType:
		return "type"
	case k == editor.TokenVariable:
		return "variable"
	case k == editor.TokenText:
		return "text"
	}
	return "other"
}

func TestRecentNamesAndBlank(t *testing.T) {
	s := "[voice:A]x\n\n[对话]\nB：y\nC：z\n[/对话]\n\n[voice:A]w"
	if got := recentNames(s, len(s)); got != [2]string{"A", "C"} {
		t.Errorf("names %q", got)
	}
	if got := recentNames("", 0); got != [2]string{"Speaker A", "Speaker B"} {
		t.Errorf("none %q", got)
	}
	for _, c := range []struct{ before, after, pre, post string }{
		{"", "", "", ""}, {"a", "b", "\n\n", "\n\n"}, {"a\n", "\nb", "\n", "\n"}, {"a\n\n", "\n\nb", "", ""},
	} {
		if pre, post := blankAround(c.before, c.after); pre != c.pre || post != c.post {
			t.Errorf("%q|%q: %q %q", c.before, c.after, pre, post)
		}
	}
	if a, b := lineRange("ab\ncd\nef", 4, 5); a != 3 || b != 5 {
		t.Errorf("range %d %d", a, b)
	}
}
