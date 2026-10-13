package subtitle

import (
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hehehai/AudioSmith/internal/text"
)

// Segment is a transcribed or translated stretch of speech: one speaker,
// with word timings when the aligner produced them.
type Segment struct {
	Start, End time.Duration
	Text       string
	Words      []Word // optional; estimated from Text when empty
	Speaker    string
}

// SegmentOptions are the readability limits Split enforces.
type SegmentOptions struct {
	MaxCharsPerLine int           // 42 (CJK 13–16)
	MaxLines        int           // 2
	MaxCPS          float64       // characters per second: 17 (zh 9, ja 4, ko 12)
	MinDuration     time.Duration // 5/6 s
	MaxDuration     time.Duration // 7 s
	MinGap          time.Duration // between cues: 2 frames at 24 fps
	MaxPause        time.Duration // a silence this long between words ends a cue: 1.5 s
}

// Defaults returns Netflix-style limits for a language (BCP-47).
func Defaults(lang string) SegmentOptions {
	o := SegmentOptions{
		MaxCharsPerLine: 42, MaxLines: 2, MaxCPS: 17,
		MinDuration: 833 * time.Millisecond, MaxDuration: 7 * time.Second,
		MinGap: 83 * time.Millisecond, MaxPause: 1500 * time.Millisecond,
	}
	base, _, _ := strings.Cut(strings.ToLower(strings.ReplaceAll(lang, "_", "-")), "-")
	switch base {
	case "zh", "yue", "cmn", "wuu":
		o.MaxCharsPerLine, o.MaxCPS = 16, 9
	case "ja":
		o.MaxCharsPerLine, o.MaxCPS = 13, 4
	case "ko":
		o.MaxCharsPerLine, o.MaxCPS = 16, 12
	}
	return o
}

func (o SegmentOptions) withDefaults() SegmentOptions {
	d := Defaults("")
	if o.MaxCharsPerLine <= 0 {
		o.MaxCharsPerLine = d.MaxCharsPerLine
	}
	if o.MaxLines <= 0 {
		o.MaxLines = d.MaxLines
	}
	if o.MaxCPS <= 0 {
		o.MaxCPS = d.MaxCPS
	}
	if o.MinDuration <= 0 {
		o.MinDuration = d.MinDuration
	}
	if o.MaxDuration <= 0 {
		o.MaxDuration = d.MaxDuration
	}
	if o.MinGap < 0 {
		o.MinGap = 0
	}
	if o.MaxPause <= 0 {
		o.MaxPause = d.MaxPause
	}
	return o
}

// CodeCPS flags a cue read faster than MaxCPS even after extending it into
// the following silence. Params: index (0-based), cps (one decimal).
const CodeCPS = "subtitle.reading_speed"

// Split turns timed segments into cues that respect opt: at most
// MaxLines lines of MaxCharsPerLine, at most MaxDuration on screen,
// breaking preferably at sentence ends, then clause marks; never across
// segments (speaker turns) or long pauses. Short cues are extended into
// the silence that follows (up to MinGap before the next cue) to reach
// MinDuration and MaxCPS; cues still too fast are reported.
func Split(segs []Segment, opt SegmentOptions) ([]Cue, []text.Diagnostic) {
	o := opt.withDefaults()
	var cues []Cue
	for _, s := range segs {
		words := s.Words
		if len(words) == 0 {
			words = estimateWords(s)
		}
		cues = append(cues, segmentWords(words, s.Speaker, o)...)
	}
	return cues, fixTiming(cues, o)
}

func segmentWords(words []Word, speaker string, o SegmentOptions) []Cue {
	var out []Cue
	var cur []Word
	emit := func(ws []Word) {
		if len(ws) == 0 {
			return
		}
		t := JoinWords(ws)
		lines, ok := BreakLines(t, o.MaxCharsPerLine, o.MaxLines)
		if !ok {
			lines = hardWrap(t, o.MaxCharsPerLine)
		}
		out = append(out, Cue{Start: ws[0].Start, End: ws[len(ws)-1].End, Text: strings.Join(lines, "\n"),
			Words: append([]Word(nil), ws...), Speaker: speaker})
	}
	fits := func(ws []Word) bool {
		_, ok := BreakLines(JoinWords(ws), o.MaxCharsPerLine, o.MaxLines)
		return ok && ws[len(ws)-1].End-ws[0].Start <= o.MaxDuration
	}
	for _, w := range words {
		if strings.TrimSpace(w.Text) == "" {
			continue
		}
		if len(cur) > 0 {
			pause := w.Start - cur[len(cur)-1].End
			if pause >= o.MaxPause {
				emit(cur)
				cur = nil
			} else if !fits(append(cur[:len(cur):len(cur)], w)) {
				k := splitPoint(cur)
				emit(cur[:k])
				cur = append([]Word(nil), cur[k:]...)
				if len(cur) > 0 && !fits(append(cur[:len(cur):len(cur)], w)) {
					emit(cur)
					cur = nil
				}
			}
		}
		cur = append(cur, w)
		// End the cue at a sentence end once it holds a line's worth.
		if endsSentence(w.Text) && utf8.RuneCountInString(JoinWords(cur)) >= o.MaxCharsPerLine/2 {
			emit(cur)
			cur = nil
		}
	}
	emit(cur)
	return out
}

// splitPoint picks where a full cue should end: after the last clause or
// sentence mark in its second half, else after its last word.
func splitPoint(ws []Word) int {
	for k := len(ws) - 1; k >= (len(ws)+1)/2 && k > 0; k-- {
		if endsClause(ws[k-1].Text) {
			return k
		}
	}
	return len(ws)
}

func lastRune(s string) rune {
	s = strings.TrimRightFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(`"'”’」』)）»`, r)
	})
	r, _ := utf8.DecodeLastRuneInString(s)
	return r
}

func endsSentence(s string) bool { return strings.ContainsRune(".!?。！？…‼⁇", lastRune(s)) }

func endsClause(s string) bool {
	return endsSentence(s) || strings.ContainsRune(",;:，、；：—–", lastRune(s))
}

// estimateWords splits a segment's text into words (characters in CJK)
// and spreads its duration over them by length.
func estimateWords(s Segment) []Word {
	var toks []string
	for _, f := range strings.Fields(s.Text) {
		var cur strings.Builder
		for _, r := range f {
			if text.IsUnspaced(r) && !isPunct(r) {
				if cur.Len() > 0 {
					toks = append(toks, cur.String())
					cur.Reset()
				}
				cur.WriteRune(r)
				continue
			}
			if isPunct(r) && cur.Len() > 0 {
				lr, _ := utf8.DecodeLastRuneInString(cur.String())
				if text.IsUnspaced(lr) {
					cur.WriteRune(r) // attach CJK punctuation to its character
					continue
				}
			}
			if cur.Len() > 0 {
				lr, _ := utf8.DecodeLastRuneInString(cur.String())
				if text.IsUnspaced(lr) && !isPunct(lr) {
					toks = append(toks, cur.String())
					cur.Reset()
				}
			}
			cur.WriteRune(r)
		}
		if cur.Len() > 0 {
			toks = append(toks, cur.String())
		}
	}
	total := 0
	for _, t := range toks {
		total += utf8.RuneCountInString(t)
	}
	if total == 0 {
		return nil
	}
	span := s.End - s.Start
	out := make([]Word, len(toks))
	acc := 0
	for i, t := range toks {
		a := s.Start + time.Duration(int64(span)*int64(acc)/int64(total))
		acc += utf8.RuneCountInString(t)
		z := s.Start + time.Duration(int64(span)*int64(acc)/int64(total))
		out[i] = Word{Text: t, Start: a, End: z}
	}
	return out
}

func isPunct(r rune) bool { return unicode.IsPunct(r) || unicode.IsSymbol(r) }

// fixTiming extends short or fast cues into the following silence and
// trims overlaps, then reports cues that are still too fast.
func fixTiming(cues []Cue, o SegmentOptions) []text.Diagnostic {
	var ds []text.Diagnostic
	for i := range cues {
		c := &cues[i]
		limit := time.Duration(1<<62 - 1)
		if i+1 < len(cues) {
			limit = cues[i+1].Start - o.MinGap
		}
		chars := utf8.RuneCountInString(strings.ReplaceAll(c.Text, "\n", ""))
		need := max(o.MinDuration, time.Duration(float64(chars)/o.MaxCPS*float64(time.Second)))
		need = min(need, o.MaxDuration)
		if c.End-c.Start < need {
			c.End = max(c.End, min(c.Start+need, limit))
		}
		if c.End > limit && limit > c.Start {
			c.End = limit
		}
		if d := c.End - c.Start; d > 0 {
			if cps := float64(chars) / d.Seconds(); cps > o.MaxCPS+0.05 {
				ds = append(ds, text.Diagnostic{Code: CodeCPS, Severity: text.Info,
					Params: map[string]string{"index": strconv.Itoa(i), "cps": strconv.FormatFloat(cps, 'f', 1, 64)}})
			}
		}
	}
	return ds
}

// BreakLines splits s into at most maxLines lines of at most maxChars
// runes, at spaces or between CJK characters. With two lines it prefers a
// break after punctuation, balanced lengths and a longer bottom line, and
// avoids leaving a function word at a line end. ok is false when s cannot
// fit.
func BreakLines(s string, maxChars, maxLines int) ([]string, bool) {
	s = strings.Join(strings.Fields(s), " ")
	n := utf8.RuneCountInString(s)
	if n <= maxChars {
		return []string{s}, true
	}
	if maxLines < 2 {
		return nil, false
	}
	best, bestScore := -1, 1<<30
	runes := []rune(s)
	for p := 1; p < len(runes); p++ {
		if !canBreak(runes, p) {
			continue
		}
		l1 := strings.TrimSpace(string(runes[:p]))
		l2 := strings.TrimSpace(string(runes[p:]))
		n1, n2 := utf8.RuneCountInString(l1), utf8.RuneCountInString(l2)
		if n1 == 0 || n2 == 0 || n1 > maxChars {
			continue
		}
		if n2 > maxChars {
			if maxLines == 2 {
				continue
			}
			if _, ok := BreakLines(l2, maxChars, maxLines-1); !ok {
				continue
			}
		}
		score := abs(n1 - n2)
		if n1 > n2 {
			score += 2 // prefer a bottom-heavy pair
		}
		switch {
		case endsSentence(l1):
			score -= 12
		case endsClause(l1):
			score -= 8
		}
		if weakEnd(l1) {
			score += 10
		}
		if f := strings.Fields(l2); len(f) > 0 && conjunctions[strings.ToLower(f[0])] {
			score -= 6 // "…store / and bought…"
		}
		if n2 > maxChars {
			score += 20 // needs a third line
		}
		if score < bestScore {
			best, bestScore = p, score
		}
	}
	if best < 0 {
		return nil, false
	}
	first := strings.TrimSpace(string(runes[:best]))
	rest, _ := BreakLines(strings.TrimSpace(string(runes[best:])), maxChars, maxLines-1)
	return append([]string{first}, rest...), true
}

// canBreak: at a space, or between two characters of an unspaced script
// (not before closing punctuation, not after opening punctuation).
func canBreak(r []rune, p int) bool {
	a, b := r[p-1], r[p]
	if b == ' ' {
		return true
	}
	if a == ' ' {
		return false
	}
	if !(text.IsUnspaced(a) || text.IsUnspaced(b)) {
		return false
	}
	if strings.ContainsRune("，。、；：！？）」』》〉】”’…,.;:!?)", b) || strings.ContainsRune("（「『《〈【“‘(", a) {
		return false
	}
	return true
}

var weakWords = map[string]bool{
	"a": true, "an": true, "the": true, "of": true, "to": true, "and": true, "in": true, "on": true, "at": true,
	"for": true, "with": true, "but": true, "or": true, "my": true, "your": true, "his": true, "her": true,
	"de": true, "la": true, "le": true, "les": true, "el": true, "los": true, "las": true, "un": true, "une": true,
	"der": true, "die": true, "das": true, "und": true, "y": true, "et": true,
}

var conjunctions = map[string]bool{
	"and": true, "but": true, "or": true, "so": true, "because": true, "that": true, "which": true, "who": true,
	"when": true, "where": true, "if": true, "while": true, "than": true, "y": true, "pero": true, "que": true,
	"et": true, "mais": true, "qui": true, "und": true, "aber": true, "dass": true, "weil": true,
}

func weakEnd(line string) bool {
	f := strings.Fields(line)
	return len(f) > 1 && weakWords[strings.ToLower(f[len(f)-1])]
}

func hardWrap(s string, maxChars int) []string {
	var out []string
	r := []rune(s)
	for len(r) > maxChars {
		out = append(out, strings.TrimSpace(string(r[:maxChars])))
		r = r[maxChars:]
	}
	if t := strings.TrimSpace(string(r)); t != "" {
		out = append(out, t)
	}
	return out
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
