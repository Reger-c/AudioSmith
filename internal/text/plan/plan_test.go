package plan

import (
	"strings"
	"testing"

	"github.com/hehehai/AudioSmith/internal/text"
	"github.com/hehehai/AudioSmith/internal/text/lexicon"
)

func codes(p Plan) []string {
	var out []string
	for _, d := range p.Diags {
		out = append(out, d.Code)
	}
	return out
}

func hasCode(p Plan, code string) bool {
	for _, d := range p.Diags {
		if d.Code == code {
			return true
		}
	}
	return false
}

func TestPausesParagraphsAndChunks(t *testing.T) {
	src := "Hello there. It is 3:30 now.[pause 500ms]Second part.\n\nNew paragraph, with more words."
	p := Build(src, Options{Lang: "en", MaxChars: 24})
	want := "say 0 \"Hello there.\"\n" +
		"say 2 \"It is three thirty now.\"\n" +
		"pause 500\n" +
		"say 0 \"Second part.\"\n" +
		"say 4 \"New paragraph,\"\n" +
		"say 1 \"with more words.\"\n"
	if got := p.String(); got != want {
		t.Errorf("plan:\n%s\nwant:\n%s", got, want)
	}
	if len(p.Diags) != 0 {
		t.Errorf("diags %v", codes(p))
	}
	if p.PauseMillis() != 500 || p.Chars() == 0 || p.Seconds(1) <= 0.5 {
		t.Errorf("pause %d chars %d seconds %v", p.PauseMillis(), p.Chars(), p.Seconds(1))
	}
	if len(p.Speech()) != 5 {
		t.Errorf("speech %v", p.Speech())
	}
	// Sources point into the script.
	if s := p.Segments[0].Source.Of(src); !strings.HasPrefix(s, "Hello there.") {
		t.Errorf("source %q", s)
	}
}

func TestEngineLowering(t *testing.T) {
	src := "[voice:Ann]Ha [laughter] that was [slow]really[/slow] good [foo] [sigh]"
	plain := Build(src, Options{Lang: "en"})
	if got := plain.String(); got != "say 0 \"Ha that was really good\"\n" {
		t.Errorf("plain engine:\n%s", got)
	}
	for _, c := range []string{CodeVoiceIgnored, CodeNonVerbalRemoved, CodeStyleIgnored, CodeTagRemoved} {
		if !hasCode(plain, c) {
			t.Errorf("missing %s in %v", c, codes(plain))
		}
	}
	// Each removed tag is reported once.
	n := 0
	for _, d := range plain.Diags {
		if d.Code == CodeNonVerbalRemoved {
			n++
		}
	}
	if n != 2 {
		t.Errorf("non-verbal reported %d times", n)
	}

	omni := Build(src, Options{Lang: "en", NonVerbal: true, Speed: true})
	want := "say 0 \"Ha [laughter] that was\"\n" +
		"say 1 ×0.85 \"really\"\n" +
		"say 1 \"good [foo] [sigh]\"\n"
	if got := omni.String(); got != want {
		t.Errorf("omnivoice:\n%s\nwant:\n%s", got, want)
	}
	if hasCode(omni, CodeNonVerbalRemoved) || hasCode(omni, CodeStyleIgnored) || !hasCode(omni, CodeVoiceIgnored) {
		t.Errorf("omni diags %v", codes(omni))
	}
	if hasCode(Build("[voice:Bob] hi", Options{Voices: true}), CodeVoiceIgnored) {
		t.Error("voices allowed in Stories")
	}
}

func TestDictionaryOverridesAndNormalisation(t *testing.T) {
	dict := lexicon.Compile([]lexicon.Entry{
		{Term: "GIF", Replacement: "jif", Type: lexicon.Respelling, Scope: lexicon.Global, Enabled: true},
		{Term: "重庆", Replacement: "崇庆", Type: lexicon.Respelling, Scope: "zh", Enabled: true},
	}, "en")
	p := Build("A GIF costs $5. Say [[tomato|tomahto]].", Options{Lang: "en", Lexicon: dict})
	if got := p.Segments[0].Text; got != "A jif costs five dollars. Say tomahto." {
		t.Errorf("text %q", got)
	}
	// Scoped to Chinese: not applied to English.
	zh := lexicon.Compile([]lexicon.Entry{{Term: "重庆", Replacement: "崇庆", Type: lexicon.Respelling, Scope: "zh", Enabled: true}}, "zh")
	p = Build("我在重庆工作，\n今年2024年。", Options{Lang: "zh", Lexicon: zh})
	if got := p.Segments[0].Text; !strings.Contains(got, "崇庆") || strings.Contains(got, "2024") || strings.Contains(got, " ") {
		t.Errorf("zh text %q", got)
	}
	// Abbreviations before a dictionary term still read.
	nguyen := lexicon.Compile([]lexicon.Entry{{Term: "Nguyen", Replacement: "win", Type: lexicon.Respelling, Scope: lexicon.Global, Enabled: true}}, "en")
	if got := Build("Mr. Nguyen called.", Options{Lang: "en", Lexicon: nguyen}).Segments[0].Text; got != "Mister win called." {
		t.Errorf("abbreviation %q", got)
	}
	// Normalisation off.
	if got := Build("It is 3:30.", Options{Lang: "en", NoNormalize: true}).Segments[0].Text; got != "It is 3:30." {
		t.Errorf("unnormalised %q", got)
	}
}

func TestCJKChunkLimitAndSpell(t *testing.T) {
	zh := strings.Repeat("这是一个很长的句子，", 20) + "结束。"
	p := Build(zh, Options{Lang: "zh", MaxChars: 400, MaxCharsCJK: 60})
	for _, i := range p.Speech() {
		if n := text.RuneLen(p.Segments[i].Text); n > 60 {
			t.Errorf("chunk of %d runes", n)
		}
	}
	if len(p.Speech()) < 3 {
		t.Errorf("chunks %d", len(p.Speech()))
	}
	if got := Build("Call the [spell]API[/spell] now", Options{}).Segments[0].Text; got != "Call the A P I now" {
		t.Errorf("spell %q", got)
	}
}

func TestNothingToSay(t *testing.T) {
	for _, src := range []string{"", "   ", "[pause 1s]", "…!", "[laughter]"} {
		p := Build(src, Options{})
		if !hasCode(p, CodeNothingToSay) {
			t.Errorf("%q: %v / %s", src, codes(p), p.String())
		}
	}
}

func FuzzBuild(f *testing.F) {
	for _, s := range []string{"Hello [pause] world", "[slow]a[fast]b[/slow]c", "[[x|y]] 3:30 $5", "你好[laughter]世界\n\n第二段"} {
		f.Add(s, true)
	}
	f.Fuzz(func(t *testing.T, src string, nv bool) {
		p := Build(src, Options{NonVerbal: nv, Speed: nv, MaxChars: 50, MaxCharsCJK: 20})
		for _, s := range p.Segments {
			if s.Kind == Speech && (s.Text == "" || s.Speed <= 0) {
				t.Fatalf("bad segment %+v", s)
			}
			if s.Source.Start < 0 || s.Source.End > len(src) || s.Source.Start > s.Source.End {
				t.Fatalf("bad source %+v of %d", s.Source, len(src))
			}
		}
	})
}
