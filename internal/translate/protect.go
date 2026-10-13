package translate

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/hehehai/AudioSmith/internal/text"
)

// masked is a segment's text with its [tags] and [[overrides]] replaced by
// numbered markers ⟦1⟧, ⟦2⟧… that the model copies through.
type masked struct {
	Text string    // with markers
	Tags []string  // Tags[i] is marker i+1's original text
	Pos  []float64 // relative position (0…1) of each tag in the source
}

func mask(s string) masked {
	spans := text.Tags(s)
	if len(spans) == 0 {
		return masked{Text: s}
	}
	var b strings.Builder
	m := masked{}
	last := 0
	for i, sp := range spans {
		b.WriteString(s[last:sp.Start])
		b.WriteString(marker(i + 1))
		m.Tags = append(m.Tags, sp.Of(s))
		m.Pos = append(m.Pos, float64(sp.Start)/float64(max(len(s), 1)))
		last = sp.End
	}
	b.WriteString(s[last:])
	m.Text = b.String()
	return m
}

func marker(n int) string { return "⟦" + strconv.Itoa(n) + "⟧" }

var markerRE = regexp.MustCompile(`⟦\s*(\d+)\s*⟧`)

// unmask restores the tags in out. ok is false when a marker is missing,
// repeated or unknown (strict use: retry the line); repaired is the best
// effort text with missing tags put back at the edge nearest their source
// position and stray markers dropped.
func (m masked) unmask(out string) (restored string, ok bool) {
	if len(m.Tags) == 0 {
		if markerRE.MatchString(out) {
			return strings.TrimSpace(markerRE.ReplaceAllString(out, "")), false
		}
		return out, true
	}
	ok = true
	used := make([]bool, len(m.Tags))
	restored = markerRE.ReplaceAllStringFunc(out, func(x string) string {
		n, _ := strconv.Atoi(markerRE.FindStringSubmatch(x)[1])
		if n < 1 || n > len(m.Tags) || used[n-1] {
			ok = false
			return ""
		}
		used[n-1] = true
		return m.Tags[n-1]
	})
	var pre, post []string
	for i, u := range used {
		if u {
			continue
		}
		ok = false
		if m.Pos[i] < 0.5 {
			pre = append(pre, m.Tags[i])
		} else {
			post = append(post, m.Tags[i])
		}
	}
	if len(pre) > 0 {
		restored = strings.Join(pre, " ") + " " + strings.TrimLeft(restored, " ")
	}
	if len(post) > 0 {
		restored = strings.TrimRight(restored, " ") + " " + strings.Join(post, " ")
	}
	return strings.TrimSpace(collapseSpaces(restored)), ok
}

var multiSpace = regexp.MustCompile(`[ \t]{2,}`)

func collapseSpaces(s string) string { return multiSpace.ReplaceAllString(s, " ") }
