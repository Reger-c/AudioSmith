package markup

import (
	"testing"

	"github.com/hehehai/AudioSmith/internal/text"
)

func FuzzParse(f *testing.F) {
	for _, s := range []string{"# A\n[voice:x][slow]hi[/slow][pause 2s]", "[[a|b]] [laughter] [/fast]", "[pause 99999999999999999999s]", "#", "[a【对话】]x【/对话】"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		d := Parse(s, nil)
		end := 0
		for _, tk := range d.Tokens {
			if tk.Span.Start < 0 || tk.Span.End > len(s) || tk.Span.Start > tk.Span.End || tk.Span.Start < end {
				t.Fatalf("bad span %+v", tk)
			}
			end = tk.Span.End
			if tk.Kind == Pause && (tk.Millis < 0 || tk.Millis > 10000) {
				t.Fatalf("pause %d", tk.Millis)
			}
		}
		d.Runs()
		d.Stats()
		d.Plain()
		d.Validate(func(string) bool { return false })
	})
}

func FuzzDialogueBlocks(f *testing.F) {
	for _, s := range []string{"[对话]\n甲：你好\n乙（低声）：“嗯”\n（静）\n[pause 1s]\n[/对话]", "【对话】[对话]\n# A\n[/dialogue]x", "[dialogue]?: hi\n[voice:A]x\nNarrator: y", "[对话]甲："} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		in := func(sp text.Span) bool { return sp.Start >= 0 && sp.Start <= sp.End && sp.End <= len(s) }
		blocks, diags := DialogueBlocks(s)
		prev := 0
		for _, b := range blocks {
			if !in(b.Span) || !in(b.Open) || b.Span.Start < prev || b.Open.Start != b.Span.Start {
				t.Fatalf("block %+v", b)
			}
			prev = b.Span.End
			for _, l := range b.Lines {
				for _, sp := range []text.Span{l.Line, l.Text, l.NameSpan, l.DirSpan} {
					if !in(sp) {
						t.Fatalf("line %+v", l)
					}
				}
				if l.Line.Start < b.Open.End || l.Line.End > b.Span.End {
					t.Fatalf("line %+v outside %+v", l, b)
				}
			}
			b.Speakers()
			b.Turns()
		}
		for _, d := range diags {
			if !in(d.Span) {
				t.Fatalf("diag %+v", d)
			}
		}
	})
}
