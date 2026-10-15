package services

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
)

// Sentence cutting: a pause this long between words ends a segment, and
// no segment grows longer than maxSentence (Chinese ASR often has no
// punctuation at all).
const (
	sentencePause = 1.0  // seconds
	maxSentence   = 20.0 // seconds
)

// tok is a word of the text as it reads: its punctuation attached, one
// character for Chinese and Japanese.
type tok struct {
	text       string
	norm       string // lower-case letters and digits only
	space      bool   // a space came before it in the text
	start, end float64
	prob       *float64
	timed      bool
	speaker    string // its model segment's speaker label
}

// BuildSegments makes a transcript's segments from what the model said
// (segs) and the words an aligner or the model timed (words, in text
// order; their text may lack punctuation and case). With words, the text
// is cut into sentences timed by their words, each word keeping its
// punctuation; without, the model's segments are kept. Segments keep
// their model segment's speaker label: with words, a change of speaker
// ends a sentence (a speakers model's turns often end without a full
// stop). duration (seconds, 0 = unknown) bounds every time.
func BuildSegments(segs []proto.Segment, words []proto.Word, duration float64) []core.TranscriptSegment {
	clamp := func(v float64) float64 {
		v = max(v, 0)
		if duration > 0 {
			v = min(v, duration)
		}
		return v
	}
	if len(words) == 0 {
		var out []core.TranscriptSegment
		for _, s := range segs {
			txt := strings.TrimSpace(s.Text)
			if txt == "" {
				continue
			}
			out = append(out, core.TranscriptSegment{Start: clamp(s.Start), End: clamp(max(s.End, s.Start)), Text: txt, Speaker: s.Speaker})
		}
		return out
	}

	// The text's words, with each model segment's bounds as fallback times.
	var toks []tok
	var bounds [][2]float64 // per token: its model segment's start/end
	for _, s := range segs {
		ts := tokenize(s.Text)
		for i := range ts {
			bounds = append(bounds, [2]float64{s.Start, s.End})
			ts[i].speaker = s.Speaker
		}
		toks = append(toks, ts...)
	}
	if len(toks) == 0 {
		return nil
	}
	matchWords(toks, words)
	fillTimes(toks, bounds)
	for i := range toks {
		toks[i].start, toks[i].end = clamp(toks[i].start), clamp(toks[i].end)
	}

	// Sentences.
	var out []core.TranscriptSegment
	var cur []tok
	flush := func() {
		if len(cur) == 0 {
			return
		}
		seg := core.TranscriptSegment{Start: cur[0].start, End: cur[len(cur)-1].end, Speaker: cur[0].speaker}
		var b strings.Builder
		for i, t := range cur {
			if i > 0 && t.space && spaced(cur[i-1].text, t.text) {
				b.WriteByte(' ')
			}
			b.WriteString(t.text)
			seg.Words = append(seg.Words, core.TranscriptWord{Text: t.text, Start: t.start, End: t.end, Prob: t.prob})
		}
		seg.Text = b.String()
		seg.End = max(seg.End, seg.Start)
		out = append(out, seg)
		cur = nil
	}
	for i, t := range toks {
		cur = append(cur, t)
		last := i == len(toks)-1
		if last {
			break
		}
		next := toks[i+1]
		switch {
		case next.speaker != t.speaker:
			flush()
		case endsSentence(t.text):
			flush()
		case len(cur) >= 2 && next.start-t.end >= sentencePause:
			flush()
		case t.end-cur[0].start >= maxSentence && (endsClause(t.text) || next.start-t.end >= 0.3):
			flush()
		case t.end-cur[0].start >= 2*maxSentence:
			flush()
		}
	}
	flush()
	return out
}

// tokenize splits text into words: on spaces, and one token per CJK
// character; punctuation sticks to the word before it (or, at the start,
// the word after it).
func tokenize(text string) []tok {
	var out []tok
	var cur strings.Builder
	space := true // a new model segment starts a word
	pendingSpace := false
	var lead strings.Builder // punctuation before any word
	emit := func() {
		if cur.Len() == 0 {
			return
		}
		s := cur.String()
		out = append(out, tok{text: s, norm: normWord(s), space: space})
		cur.Reset()
		space = false
	}
	for _, r := range text {
		switch {
		case unicode.IsSpace(r):
			emit()
			space = true
			pendingSpace = true
			continue
		case isPunctRune(r):
			if cur.Len() == 0 && len(out) > 0 && !pendingSpace {
				out[len(out)-1].text += string(r) // "你好，" "word."
				continue
			}
			if cur.Len() == 0 {
				// "word — next", "¿Qué": it sticks to what follows.
				lead.WriteRune(r)
				continue
			}
			cur.WriteRune(r)
		case unspacedRune(r):
			emit()
			if lead.Len() > 0 {
				cur.WriteString(lead.String())
				lead.Reset()
			}
			cur.WriteRune(r)
			emit()
		default:
			if cur.Len() == 0 && lead.Len() > 0 {
				cur.WriteString(lead.String())
				lead.Reset()
			}
			cur.WriteRune(r)
		}
		pendingSpace = false
	}
	emit()
	if lead.Len() > 0 {
		if len(out) > 0 {
			out[len(out)-1].text += lead.String()
		} else {
			out = append(out, tok{text: lead.String(), space: true})
		}
	}
	return out
}

func normWord(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

func isPunctRune(r rune) bool { return unicode.IsPunct(r) || unicode.IsSymbol(r) }

// unspacedRune: scripts written without spaces between words, one token
// per character (Han, kana, full-width forms).
func unspacedRune(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) ||
		r >= 0xFF00 && r <= 0xFFEF && !isPunctRune(r)
}

// spaced reports whether a space goes between two words: not next to a
// CJK character.
func spaced(before, after string) bool {
	b, _ := utf8.DecodeLastRuneInString(before)
	a, _ := utf8.DecodeRuneInString(after)
	return !(unspacedRune(b) || unspacedRune(a) || cjkPunct(b) || cjkPunct(a))
}

func cjkPunct(r rune) bool { return r >= 0x3000 && r <= 0x303F || r >= 0xFF00 && r <= 0xFFEF }

func endsSentence(s string) bool {
	r, _ := utf8.DecodeLastRuneInString(strings.TrimRight(s, `"'”’)」』】`))
	return strings.ContainsRune(".!?。！？…", r)
}

func endsClause(s string) bool {
	r, _ := utf8.DecodeLastRuneInString(s)
	return endsSentence(s) || strings.ContainsRune(",;:，、；：—–", r)
}

// matchWords gives tokens the times of the timed words that spell them,
// in order: one word per token, several words per token ("e-mail", "3.5"),
// skipping up to three words the text lacks.
func matchWords(toks []tok, words []proto.Word) {
	type w struct {
		norm       string
		start, end float64
		prob       *float64
	}
	var ws []w
	for _, x := range words {
		if n := normWord(x.Text); n != "" {
			ws = append(ws, w{n, x.Start, x.End, x.Prob})
		}
	}
	j := 0
	for i := range toks {
		t := &toks[i]
		if t.norm == "" || j >= len(ws) {
			continue
		}
		matched := false
		concat := ""
		for m := 1; m <= 4 && j+m <= len(ws); m++ {
			concat += ws[j+m-1].norm
			if concat == t.norm {
				t.start, t.end, t.timed = ws[j].start, ws[j+m-1].end, true
				if m == 1 {
					t.prob = ws[j].prob
				}
				j += m
				matched = true
				break
			}
			if !strings.HasPrefix(t.norm, concat) {
				break
			}
		}
		if matched {
			continue
		}
		for k := j + 1; k <= j+3 && k < len(ws); k++ {
			if ws[k].norm == t.norm {
				t.start, t.end, t.timed, t.prob = ws[k].start, ws[k].end, true, ws[k].prob
				j = k + 1
				matched = true
				break
			}
		}
	}
}

// fillTimes times the tokens no word matched, spread by length between
// their timed neighbours (or their model segment's bounds).
func fillTimes(toks []tok, bounds [][2]float64) {
	for i := 0; i < len(toks); {
		if toks[i].timed {
			i++
			continue
		}
		j := i
		for j < len(toks) && !toks[j].timed {
			j++
		}
		from := bounds[i][0]
		if i > 0 {
			from = toks[i-1].end
		}
		to := bounds[j-1][1]
		if j < len(toks) {
			to = toks[j].start
		}
		to = max(to, from)
		total := 0
		for k := i; k < j; k++ {
			total += max(utf8.RuneCountInString(toks[k].norm), 1)
		}
		acc := 0
		for k := i; k < j; k++ {
			n := max(utf8.RuneCountInString(toks[k].norm), 1)
			toks[k].start = from + (to-from)*float64(acc)/float64(total)
			acc += n
			toks[k].end = from + (to-from)*float64(acc)/float64(total)
		}
		i = j
	}
}
