package autocast

import "testing"

func FuzzDetect(f *testing.F) {
	for _, s := range []string{`"Hi," said Ann.`, "MARA: x\n[Cole] y", "小明说：“你好”", "「行こう」と太郎は言った。", "“unclosed"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		r := Detect(s, nil)
		for _, l := range r.Lines {
			if l.Span.Start < 0 || l.Span.End > len(s) || l.Span.Of(s) != l.Text {
				t.Fatalf("bad line %+v", l)
			}
		}
	})
}
