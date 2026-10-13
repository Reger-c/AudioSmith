package text

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func FuzzChunks(f *testing.F) {
	for _, s := range []string{"Hello. World!", "你好。[pause 1s]世界", "# T\n\na [[b|c]] d", "[[x", "...", "\n\n#\n"} {
		f.Add(s, 10)
	}
	f.Fuzz(func(t *testing.T, s string, max int) {
		if !utf8.ValidString(s) {
			return
		}
		max = max%200 + 1
		if max <= 0 {
			max = -max + 1
		}
		prev := 0
		for _, c := range Chunks(s, Options{MaxChars: max}) {
			if c.Start < prev || c.End > len(s) || c.Text != s[c.Start:c.End] || strings.TrimSpace(c.Text) == "" {
				t.Fatalf("bad chunk %+v (prev %d)", c, prev)
			}
			prev = c.End
		}
		for _, m := range []Mode{Sentences, Paragraphs, Chapters} {
			Chunks(s, Options{Mode: m, MaxChars: max})
		}
	})
}
