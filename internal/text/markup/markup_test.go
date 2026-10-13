package markup

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/hehehai/AudioSmith/internal/text"
)

// render is a compact view of tokens for table tests.
func render(toks []Token) []string {
	var out []string
	for _, t := range toks {
		switch t.Kind {
		case Text:
			out = append(out, fmt.Sprintf("text %q", t.Text))
		case Pause:
			out = append(out, fmt.Sprintf("pause %d", t.Millis))
		case StyleOn:
			out = append(out, "+"+t.Name)
		case StyleOff:
			out = append(out, "-"+t.Name)
		case Voice:
			out = append(out, "voice "+t.Name)
		case Chapter:
			out = append(out, "chapter "+t.Name)
		case NonVerbal:
			out = append(out, "nv "+t.Name)
		case Override:
			out = append(out, "override "+t.Text+"="+t.Say)
		case Paragraph:
			out = append(out, "para")
		case Unknown:
			out = append(out, "unknown "+t.Text)
		}
	}
	return out
}

func codes(ds []text.Diagnostic) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Code)
	}
	return out
}

func TestParse(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		want  []string
		diags []string
	}{
		{"plain", "Hello world.", []string{`text "Hello world."`}, nil},
		{"pauses", "a[pause]b[pause 500ms]c[pause 1.5s]d[PAUSE 2 s]e[pause 250]f[pause:3s]",
			[]string{`text "a"`, "pause 350", `text "b"`, "pause 500", `text "c"`, "pause 1500", `text "d"`, "pause 2000", `text "e"`, "pause 250", `text "f"`, "pause 3000"}, nil},
		{"pause clamp and bad", "x[pause 12s]y[pause soon]",
			[]string{`text "x"`, "pause 10000", `text "y"`, "pause 350"}, []string{CodePauseClamped, CodeBadPause}},
		{"styles", "[slow]slowly[/slow] [fast]quick[/fast] [Emphasis]big[/emphasis] [spell]USA[/spell]",
			[]string{"+slow", `text "slowly"`, "-slow", `text " "`, "+fast", `text "quick"`, "-fast", `text " "`, "+emphasis", `text "big"`, "-emphasis", `text " "`, "+spell", `text "USA"`, "-spell"}, nil},
		{"unclosed and stray", "[slow]never closed [/fast]",
			[]string{"+slow", `text "never closed "`, "-fast"}, []string{CodeUnclosedStyle, CodeStrayClose}},
		{"voice", "[voice:Mara] Hi. [voice: Cole Smith] Yo. [voice:default] N. [voice:]",
			[]string{"voice Mara", `text " Hi. "`, "voice Cole Smith", `text " Yo. "`, "voice ", `text " N. "`, "voice "}, nil},
		{"non-verbal", "Ha [laughter] oh [Sigh] [question-en]?",
			[]string{`text "Ha "`, "nv laughter", `text " oh "`, "nv sigh", `text " "`, "nv question-en", `text "?"`}, nil},
		{"unknown reported once", "[foo] and [foo] and [bar]",
			[]string{"unknown [foo]", `text " and "`, "unknown [foo]", `text " and "`, "unknown [bar]"}, []string{CodeUnknownTag, CodeUnknownTag}},
		{"override", "Say [[GIF|jif]] [[ 重庆 | chong2 qing4 ]].",
			[]string{`text "Say "`, "override GIF=jif", `text " "`, "override 重庆=chong2 qing4", `text "."`}, nil},
		{"bad override", "[[nopipe]] x",
			[]string{`text "[[nopipe]] x"`}, []string{CodeBadOverride}},
		{"chapters", "# One\nText.\n\n# Two\n\nMore.\n## not a chapter\n#nospace",
			[]string{"chapter One", `text "Text.\n"`, "chapter Two", `text "More.\n## not a chapter\n#nospace"`}, nil},
		{"paragraphs", "First line\nsame para.\n\n\nSecond para.",
			[]string{`text "First line\nsame para.\n"`, "para", `text "Second para."`}, nil},
		{"empty chapter", "# Empty\n\n[pause 1s]\n# Full\nhi",
			[]string{"chapter Empty", "pause 1000", `text "\n"`, "chapter Full", `text "hi"`}, []string{CodeEmptyChapter}},
		{"zh mixed", "# 第一章 开始\n他笑了[laughter]，说：[voice:小明]“你好[pause 0.5s]世界。”",
			[]string{"chapter 第一章 开始", `text "他笑了"`, "nv laughter", `text "，说："`, "voice 小明", `text "“你好"`, "pause 500", `text "世界。”"`}, nil},
		{"ja", "[slow]ゆっくり[/slow]話す。", []string{"+slow", `text "ゆっくり"`, "-slow", `text "話す。"`}, nil},
		{"brackets that are not tags", "a [ b\nc ] d", []string{`text "a [ b\nc ] d"`}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := Parse(c.in, nil)
			got := render(d.Tokens)
			if !slices.Equal(got, c.want) {
				t.Errorf("tokens\n got  %q\n want %q", got, c.want)
			}
			if gc := codes(d.Diags); !slices.Equal(gc, c.diags) {
				t.Errorf("diags %q, want %q", gc, c.diags)
			}
			for _, tk := range d.Tokens {
				if tk.Kind == Text && tk.Span.Of(c.in) != tk.Text {
					t.Errorf("text token span mismatch: %q vs %q", tk.Span.Of(c.in), tk.Text)
				}
				if tk.Kind != Paragraph && tk.Kind != Text && !strings.HasPrefix(tk.Span.Of(c.in), "[") && !strings.HasPrefix(tk.Span.Of(c.in), "#") {
					t.Errorf("%v token span %q", tk.Kind, tk.Span.Of(c.in))
				}
			}
		})
	}
}

func TestOptions(t *testing.T) {
	d := Parse("[breath] [whisper] [pause] # x", &Options{NonVerbal: []string{"Whisper"}, DefaultPauseMillis: 200})
	want := []string{"unknown [breath]", `text " "`, "nv whisper", `text " "`, "pause 200", `text " # x"`}
	if got := render(d.Tokens); !slices.Equal(got, want) {
		t.Errorf("got %q", got)
	}
	d = Parse("# Title\nbody", &Options{NoChapters: true})
	if got := render(d.Tokens); !slices.Equal(got, []string{`text "# Title\nbody"`}) {
		t.Errorf("NoChapters: %q", got)
	}
}

func TestChaptersAndStats(t *testing.T) {
	src := "Intro words here.\n# One\nHello world [laughter].\n# \n你好世界。\n# Empty\n"
	d := Parse(src, nil)
	chs := d.Chapters()
	if len(chs) != 4 {
		t.Fatalf("chapters %+v", chs)
	}
	titles := []string{chs[0].Title, chs[1].Title, chs[2].Title, chs[3].Title}
	if !slices.Equal(titles, []string{"", "One", "", "Empty"}) || chs[0].Heading || !chs[1].Heading {
		t.Errorf("chapters %+v", chs)
	}
	if !chs[0].Spoken || !chs[2].Spoken || chs[3].Spoken {
		t.Errorf("spoken flags %+v", chs)
	}
	if gc := codes(d.Diags); !slices.Equal(gc, []string{CodeEmptyChapter}) || d.Diags[0].Params["title"] != "Empty" || d.Diags[0].Params["index"] != "3" {
		t.Errorf("diags %+v", d.Diags)
	}
	st := d.Stats()
	if st.Chapters != 4 || st.Words != 5 || st.Chars != 4 {
		t.Errorf("stats %+v", st)
	}
	if st := Parse("", nil).Stats(); st.Chapters != 0 {
		t.Errorf("empty stats %+v", st)
	}
	if st := Parse("just text [pause 2s]", nil).Stats(); st.Chapters != 1 || st.PauseMillis != 2000 {
		t.Errorf("stats %+v", st)
	}
}

func TestValidateVoices(t *testing.T) {
	d := Parse("[voice:Mara] a [voice:Ghost] b [voice:Ghost] c [voice:default] [zzz]", nil)
	if got := d.VoiceNames(); !slices.Equal(got, []string{"Mara", "Ghost"}) {
		t.Errorf("VoiceNames %q", got)
	}
	ds := d.Validate(func(n string) bool { return n == "Mara" })
	if gc := codes(ds); !slices.Equal(gc, []string{CodeUnknownVoice, CodeUnknownTag}) || ds[0].Params["voice"] != "Ghost" {
		t.Errorf("Validate %+v", ds)
	}
}

func TestRuns(t *testing.T) {
	d := Parse("[voice:A][slow]one [emphasis]two[/emphasis][/slow] three[pause]\n# Next\nfour [fast]five", nil)
	type r struct {
		what, voice string
		styles      string
		chapter     int
	}
	var got []r
	for _, x := range d.Runs() {
		w := x.Text
		if x.Kind == Pause {
			w = "pause"
		}
		got = append(got, r{strings.TrimSpace(w), x.Voice, strings.Join(x.Styles, "+"), x.Chapter})
	}
	want := []r{
		{"one", "A", "slow", 0}, {"two", "A", "slow+emphasis", 0}, {"three", "A", "", 0}, {"pause", "A", "", 0},
		{"", "A", "", 0}, // the line break before the heading
		{"four", "", "", 1}, {"five", "", "fast", 1},
	}
	if !slices.Equal(got, want) {
		t.Errorf("runs\n got  %+v\n want %+v", got, want)
	}
	if !d.Runs()[1].Has(Emphasis) {
		t.Error("Has")
	}
}

func TestPlain(t *testing.T) {
	d := Parse("# Title\nSay [[GIF|jif]] [laughter] now[pause].\n\nBye [foo].", nil)
	if got := d.Plain(); got != "Title\nSay GIF  now.\n\nBye ." {
		t.Errorf("Plain = %q", got)
	}
}
