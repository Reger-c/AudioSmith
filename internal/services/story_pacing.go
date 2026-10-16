package services

// Dialogue continuity of stories (plan/story-continuity-plan.md, P0):
//
//   - turn-aware gaps: the silence between two lines follows the turn
//     (the same character going on, an answer to a question, a statement,
//     a sentence left hanging with … or —, narration around dialogue)
//     instead of one fixed gap, in three presets (Production.Pacing);
//   - merged turns: consecutive lines of one character are synthesized as
//     one call, so the delivery flows from one sentence into the next,
//     and split back into lines at the pauses between their sentences
//     (splitMerged), so the read-along, stems and the sidecar still see
//     lines; a turn that cannot be split is made line by line instead;
//   - delivery: a line's direction, its character's persona and the
//     story's scene become an instruction for models that take one
//     (StoryInstruct; Traits.Instruct), nothing for the others.
//
// A document without a pacing (stories made before it existed) renders
// exactly as before: one gap, a call per line.
//
// Files: story_pacing.go (gaps by turn, levelling), story_merge.go
// (merged turns), story_delivery.go (instructions).

import (
	"math"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Pacing presets (Production.Pacing).
const (
	PacingFixed    = ""         // one gap between all lines (LineGapMs)
	PacingDialogue = "dialogue" // tight turns: a conversation
	PacingReading  = "reading"  // a reading pace
	PacingRelaxed  = "relaxed"  // unhurried
)

// Pacings lists the presets in the order the UI shows them.
var Pacings = []string{PacingDialogue, PacingReading, PacingRelaxed, PacingFixed}

// TurnGaps are the silences (ms) of a pacing preset, by turn.
type TurnGaps struct {
	Same      int // the same character goes on
	Question  int // a character answers a question
	Statement int // a character answers a statement
	Trailing  int // after a line left hanging (… —): an interruption
	Narration int // between narration and dialogue
	Paragraph int // a paragraph break
}

// Turn gaps of the presets. Measured conversation leaves about 200 ms
// between turns (Stivers et al. 2009: turn transitions cluster at
// 0–200 ms); an answer to a question comes quicker than a reply to a
// statement, an interrupted line is cut into, and narration breathes more.
// The engine's own edge silence is trimmed before these are added.
var turnGaps = map[string]TurnGaps{
	PacingDialogue: {Same: 180, Question: 220, Statement: 320, Trailing: 100, Narration: 450, Paragraph: 700},
	PacingReading:  {Same: 280, Question: 350, Statement: 450, Trailing: 180, Narration: 550, Paragraph: 800},
	PacingRelaxed:  {Same: 400, Question: 500, Statement: 650, Trailing: 280, Narration: 750, Paragraph: 1100},
}

// GapsOf are the turn gaps of a pacing preset; ok is false for
// PacingFixed (and unknown presets).
func GapsOf(pacing string) (TurnGaps, bool) {
	g, ok := turnGaps[pacing]
	return g, ok
}

// Turn kinds between two lines (TurnOf).
const (
	TurnSame      = "same"
	TurnQuestion  = "question"
	TurnStatement = "statement"
	TurnTrailing  = "trailing"
	TurnNarration = "narration"
	TurnParagraph = "paragraph"
)

// TurnOf says what kind of turn goes from line prev to line cur.
func TurnOf(prev, cur LineSpec) string {
	end := lineEnding(prev)
	switch {
	case cur.Paragraph:
		return TurnParagraph
	case end == endTrailing:
		return TurnTrailing
	case sameSpeaker(prev, cur):
		return TurnSame
	case prev.Character == "" || cur.Character == "":
		return TurnNarration
	case end == endQuestion:
		return TurnQuestion
	}
	return TurnStatement
}

// TurnGap is the silence (seconds) before line cur under pacing g; an
// explicit PauseAfter of prev wins.
func TurnGap(g TurnGaps, prev, cur LineSpec) float64 {
	if prev.PauseAfter > 0 {
		return float64(prev.PauseAfter) / 1000
	}
	ms := g.Statement
	switch TurnOf(prev, cur) {
	case TurnParagraph:
		ms = g.Paragraph
	case TurnTrailing:
		ms = g.Trailing
	case TurnSame:
		ms = g.Same
	case TurnNarration:
		ms = g.Narration
	case TurnQuestion:
		ms = g.Question
	}
	return float64(ms) / 1000
}

// sameSpeaker: the same character in the same voice (the last span of
// prev, the first of cur).
func sameSpeaker(prev, cur LineSpec) bool {
	if prev.Character != cur.Character || len(prev.Spans) == 0 || len(cur.Spans) == 0 {
		return false
	}
	return prev.Spans[len(prev.Spans)-1].Voice == cur.Spans[0].Voice
}

// Line endings.
const (
	endNone = iota
	endStatement
	endQuestion
	endTrailing
)

// lineEnding reads how a line ends: its last spoken character (markup
// tags, closing quotes and brackets skipped).
func lineEnding(l LineSpec) int {
	if len(l.Spans) == 0 {
		return endNone
	}
	s := stripTags(l.Spans[len(l.Spans)-1].Text)
	s = strings.TrimRightFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("\"'”’」』）)]》>", r)
	})
	switch {
	case s == "":
		return endNone
	case strings.HasSuffix(s, "…") || strings.HasSuffix(s, "...") || strings.HasSuffix(s, "—") ||
		strings.HasSuffix(s, "--") || strings.HasSuffix(s, "～") || strings.HasSuffix(s, "~"):
		return endTrailing
	case strings.HasSuffix(s, "?") || strings.HasSuffix(s, "？"):
		return endQuestion
	}
	return endStatement
}

// stripTags drops [tags] from markup.
func stripTags(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '[':
			depth++
		case r == ']' && depth > 0:
			depth--
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// sentenceEnd reports whether markup s ends a sentence (。！？.!?… with
// closing quotes after it): lines that may be merged must, so the turn
// pauses where one line gives way to the next.
func sentenceEnd(s string) bool {
	s = strings.TrimRightFunc(stripTags(s), func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("\"'”’」』）)", r)
	})
	r, _ := utf8.DecodeLastRuneInString(s)
	return strings.ContainsRune("。！？.!?…；;", r)
}

// --- levelling -----------------------------------------------------------------

// Levelling of paced stories: each line is brought toward its
// character's median loudness by levelShare of the difference, at most
// levelMaxDB either way, so one voice does not jump in level from line to
// line (measured: 4–7 LU within one character on Qwen3-TTS) while a
// direction's louder or softer line keeps some of its difference.
const (
	levelShare = 0.75
	levelMaxDB = 5.0
	levelMinS  = 0.4 // shorter lines are not measured
	levelRamp  = 0.01
)

// lineGain is a gain (dB) over a line's place in the chapter (seconds).
type lineGain struct {
	start, end float64
	db         float64
}

// levelGains are the levelling gains of the lines placed at places
// (their characters' keys who), from their measured loudness (LUFS;
// −Inf: not measured).
func levelGains(places []LinePlace, who []string, lufs []float64) []lineGain {
	by := map[string][]float64{}
	for i, l := range lufs {
		if !math.IsInf(l, 0) && !math.IsNaN(l) {
			by[who[i]] = append(by[who[i]], l)
		}
	}
	median := map[string]float64{}
	for k, v := range by {
		if len(v) < 2 {
			continue
		}
		s := slices.Clone(v)
		slices.Sort(s)
		m := s[len(s)/2]
		if len(s)%2 == 0 {
			m = (s[len(s)/2-1] + s[len(s)/2]) / 2
		}
		median[k] = m
	}
	var out []lineGain
	for i, l := range lufs {
		m, ok := median[who[i]]
		if !ok || math.IsInf(l, 0) || math.IsNaN(l) {
			continue
		}
		db := min(max(levelShare*(m-l), -levelMaxDB), levelMaxDB)
		if math.Abs(db) >= 0.1 {
			out = append(out, lineGain{places[i].Start, places[i].End, math.Round(db*100) / 100})
		}
	}
	return out
}

// gainAt is the levelling gain (linear) at time x: the line's, ramped over
// levelRamp at its edges (which lie in silence), 1 between lines.
func gainAt(gains []lineGain, x float64) float64 {
	for _, g := range gains {
		if x < g.start-levelRamp || x > g.end+levelRamp {
			continue
		}
		f := 1.0
		switch {
		case x < g.start:
			f = (x - (g.start - levelRamp)) / levelRamp
		case x > g.end:
			f = ((g.end + levelRamp) - x) / levelRamp
		}
		return math.Pow(10, g.db*f/20)
	}
	return 1
}
