package doc

import (
	"fmt"
	"strings"
	"time"
)

// Mark is a chapter start in rendered audio.
type Mark struct {
	Title string
	Start time.Duration
}

// RenderedChapter is a chapter's outcome in a render: OK is false for a
// failed chapter, which adds no time and no cue.
type RenderedChapter struct {
	Title    string
	Duration time.Duration
	OK       bool
}

// MarksFromDurations turns rendered chapter durations into start marks,
// summing in whole milliseconds and skipping failed chapters.
func MarksFromDurations(chs []RenderedChapter) []Mark {
	var out []Mark
	var at time.Duration
	for _, c := range chs {
		if !c.OK {
			continue
		}
		out = append(out, Mark{Title: c.Title, Start: at})
		at += c.Duration.Round(time.Millisecond)
	}
	return out
}

// CueSheet writes the chapter cue sheet: one "HH:MM:SS<TAB>Title" line per
// mark, no trailing newline. Tabs and line breaks in titles collapse to
// spaces; an untitled chapter n (1-based) is named by untitled, or
// "Chapter n" when untitled is nil.
func CueSheet(marks []Mark, untitled func(n int) string) string {
	lines := make([]string, len(marks))
	for i, m := range marks {
		title := oneLine(m.Title)
		if title == "" {
			if untitled != nil {
				title = oneLine(untitled(i + 1))
			} else {
				title = fmt.Sprintf("Chapter %d", i+1)
			}
		}
		s := int64(max(m.Start, 0) / time.Second)
		lines[i] = fmt.Sprintf("%02d:%02d:%02d\t%s", s/3600, s/60%60, s%60, title)
	}
	return strings.Join(lines, "\n")
}
