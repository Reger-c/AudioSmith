package lexicon

import "testing"

func FuzzApply(f *testing.F) {
	d := Compile([]Entry{resp("GIF", "jif", Global), resp("重庆", "崇庆", Global), resp("a", "b", Global)}, "zh")
	for _, s := range []string{"GIF 重庆", "[[a|b]] [x] gif", "aaa", "ǅ"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		r := d.Apply(s)
		for _, m := range r.Matches {
			if m.Out.End > len(r.Text) || m.Src.End > len(s) {
				t.Fatalf("bad match %+v", m)
			}
		}
	})
}
