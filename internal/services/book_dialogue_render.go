package services

// Rendering an audiobook's dialogue blocks (book_dialogue.go): the gaps
// inside a block, its segments for the dialogue model, and the levelling
// of the lines it made to the chapter's narration.

import (
	"math"
	"slices"
	"strings"

	"github.com/hehehai/AudioSmith/internal/dialogue"
)

// NoticeDialogueUnavailable: a chapter's dialogue blocks were read line
// by line, the dialogue model not being ready (not downloaded, no
// licence) [model].
const NoticeDialogueUnavailable = "longform.dialogue_unavailable"

// blockGap is the silence before line cur when it and prev are lines of
// one dialogue block: the dialogue pacing's turn gaps (an answer to a
// question comes quicker than one to a statement, narration breathes
// more), a [pause] line's PauseAfter winning.
func blockGap(prev, cur LineSpec) (float64, bool) {
	if cur.Block == 0 || prev.Block != cur.Block {
		return 0, false
	}
	g, _ := GapsOf(PacingDialogue)
	return TurnGap(g, prev, cur), true
}

// hasBlocks reports whether lines have a dialogue passage's.
func hasBlocks(lines []LineSpec) bool {
	for _, l := range lines {
		if l.Block > 0 {
			return true
		}
	}
	return false
}

// bookDialogueGroups are the segments of a chapter's dialogue blocks,
// with each one's seed offset: the block and the segment's place in it,
// so a block's preview (a chapter of that block alone) and the book's
// render make the same segments. Blocks in a language the dialogue
// models do not speak have none (they are read line by line), and so do
// segments of one voice (dialogue.BookRules).
func bookDialogueGroups(lines []LineSpec, lim dialogue.Limits, lang string) ([][]int, []int64) {
	items := make([]dialogue.Item, len(lines))
	for i, l := range lines {
		if l.Block == 0 {
			continue
		}
		items[i].Block = l.Block
		if dialogueLine(l) {
			items[i].Voice, items[i].Text, items[i].Turn = l.Spans[0].Voice, l.Spans[0].Text, true
			continue
		}
		items[i].Aside = true // narration inside a block never ends its segment
	}
	var groups [][]int
	var seeds []int64
	k, block := 0, 0
	for _, g := range dialogue.Group(items, lim, dialogue.BookRules) {
		var words strings.Builder
		for _, li := range g {
			words.WriteString(stripTags(lines[li].Spans[0].Text))
		}
		if _, ok := blockLanguage(lang, words.String()); !ok {
			continue
		}
		b := lines[g[0]].Block
		if b != block {
			block, k = b, 0
		}
		groups = append(groups, g)
		seeds = append(seeds, int64(1000*b+k))
		k++
	}
	return groups, seeds
}

// bookLevelMaxDB bounds the gain that brings the dialogue model's lines
// to the narration.
const bookLevelMaxDB = 10.0

// bookLevelGains bring the lines the dialogue model made (made) to the
// chapter's other lines (the narration and the [voice:] paragraphs, on
// the book's model): each voice's made lines move together by the
// difference of their median loudness to the others' median, so the
// dialogue sits at the narration's level and keeps its own rise and fall.
func bookLevelGains(places []LinePlace, lines []LineSpec, made map[int]bool, lufs []float64) []lineGain {
	ok := func(v float64) bool { return !math.IsInf(v, 0) && !math.IsNaN(v) }
	var ref []float64
	by := map[string][]float64{}
	for i, l := range lufs {
		if !ok(l) {
			continue
		}
		if made[i] {
			by[voiceOf(lines[i])] = append(by[voiceOf(lines[i])], l)
		} else {
			ref = append(ref, l)
		}
	}
	if len(ref) == 0 {
		return nil
	}
	target := median(ref)
	gain := map[string]float64{}
	for v, ls := range by {
		gain[v] = min(max(target-median(ls), -bookLevelMaxDB), bookLevelMaxDB)
	}
	var out []lineGain
	for i := range lufs {
		if !made[i] {
			continue
		}
		db, has := gain[voiceOf(lines[i])]
		if has && math.Abs(db) >= 0.1 {
			out = append(out, lineGain{places[i].Start, places[i].End, math.Round(db*100) / 100})
		}
	}
	return out
}

func voiceOf(l LineSpec) string {
	if len(l.Spans) == 0 {
		return ""
	}
	return l.Spans[0].Voice
}

func median(v []float64) float64 {
	s := slices.Clone(v)
	slices.Sort(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}
