package markup

import "testing"

func FuzzParse(f *testing.F) {
	for _, s := range []string{"# A\n[voice:x][slow]hi[/slow][pause 2s]", "[[a|b]] [laughter] [/fast]", "[pause 99999999999999999999s]", "#"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		d := Parse(s, nil)
		for _, tk := range d.Tokens {
			if tk.Span.Start < 0 || tk.Span.End > len(s) || tk.Span.Start > tk.Span.End {
				t.Fatalf("bad span %+v", tk)
			}
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
