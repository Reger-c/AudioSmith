package services

// Merged turns of a story (story_pacing.go): grouping consecutive lines
// of one character into one call (mergeGroups, joinTurn), tightening the
// pauses inside a turn, and splitting the turn back into its lines at
// the pauses between their sentences (splitMerged, quietRuns); split from
// story_pacing.go.

import (
	"math"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/text"
)

// Merging limits: a turn joins at most mergeMaxLines lines, and at most
// mergeMaxCJK characters of Han/kana/Hangul (mergeMaxChars of other
// scripts), well under every model's chunk limit, so a turn is one call.
const (
	mergeMaxLines = 4
	mergeMaxCJK   = 120
	mergeMaxChars = 360
)

// mergeGroups finds the turns to synthesize as one: runs of consecutive
// lines of one character in one voice, each one span without pauses,
// ending a sentence, with the same speed, within the limits. It returns
// the runs of two or more lines, as line indices. Their directions are
// said in order (turnDirection).
func mergeGroups(lines []LineSpec, info ModelInfo, varyRepeats bool) [][]int {
	maxCJK := min(mergeMaxCJK, max(info.MaxCharsCJK*8/10, 1))
	maxChars := min(mergeMaxChars, max(info.MaxChars*8/10, 1))
	mergeable := func(l LineSpec) bool {
		if len(l.Spans) != 1 || strings.Contains(strings.ToLower(l.Spans[0].Text), "[pause") || (varyRepeats && l.Repeat > 0) {
			return false
		}
		return hasSpeech(l.Spans[0].Text)
	}
	var out [][]int
	var cur []int
	cjk, other := 0, 0
	flush := func() {
		if len(cur) > 1 {
			out = append(out, cur)
		}
		cur, cjk, other = nil, 0, 0
	}
	for i, l := range lines {
		if !mergeable(l) {
			flush()
			continue
		}
		w, c := text.Count(l.Spans[0].Text)
		if len(cur) > 0 {
			first, prev := lines[cur[0]], lines[cur[len(cur)-1]]
			ok := len(cur) < mergeMaxLines && !l.Paragraph && prev.PauseAfter == 0 && sameSpeaker(prev, l) &&
				l.Speed == first.Speed && sentenceEnd(prev.Spans[0].Text) &&
				cjk+c <= maxCJK && other+w*6 <= maxChars
			if !ok {
				flush()
			}
		}
		cur = append(cur, i)
		cjk, other = cjk+c, other+w*6
	}
	flush()
	return out
}

// joinTurn joins the lines of a turn into one text: CJK lines run on,
// others with a space.
func joinTurn(texts []string) string {
	var b strings.Builder
	for i, t := range texts {
		t = strings.TrimSpace(t)
		if i > 0 && b.Len() > 0 {
			last, _ := utf8.DecodeLastRuneInString(b.String())
			first, _ := utf8.DecodeRuneInString(t)
			if !(text.IsCJK(last) && (text.IsCJK(first) || unicode.IsPunct(first))) {
				b.WriteByte(' ')
			}
		}
		b.WriteString(t)
	}
	return b.String()
}

// turnDirection is the direction of a merged turn: its lines' directions
// in order ("先认真恳求，再急切保证"), each once, and the strongest
// intensity.
func turnDirection(lines []LineSpec) (string, int) {
	var dirs []string
	intensity := 0
	zh := false
	for _, l := range lines {
		if l.Direction == "" {
			continue
		}
		if len(dirs) == 0 || dirs[len(dirs)-1] != l.Direction {
			dirs = append(dirs, l.Direction)
		}
		intensity = max(intensity, l.Intensity)
		if len(l.Spans) > 0 && cjkShare(l.Spans[0].Text) >= 0.3 {
			zh = true
		}
	}
	switch {
	case len(dirs) < 2:
		return strings.Join(dirs, ""), intensity
	case zh:
		return "先" + strings.Join(dirs, "，再"), intensity
	}
	return "first " + strings.Join(dirs, ", then "), intensity
}

// turnLines says whether a direction is a merged turn's (turnDirection):
// it is about several lines.
func turnLines(direction string) bool {
	return strings.HasPrefix(direction, "先") && strings.Contains(direction, "，再") ||
		strings.HasPrefix(direction, "first ") && strings.Contains(direction, ", then ")
}

// tightenTurn shortens the pauses between the lines of a split turn to
// at most maxPause seconds (the model's own sentence pauses run longer
// than a turn's gaps), cutting from their middle with short fades, and
// returns the audio with the cuts where they now are.
func tightenTurn(b dsp.Buffer, cuts []turnCut, maxPause float64) (dsp.Buffer, []turnCut) {
	keep := dsp.FramesAt(maxPause, b.Rate)
	long := false
	for _, c := range cuts {
		long = long || c.end-c.start > keep
	}
	if !long {
		return b, cuts
	}
	ch := b.Channels
	fade := dsp.FramesAt(0.01, b.Rate)
	out := dsp.Buffer{Rate: b.Rate, Channels: ch}
	moved := make([]turnCut, len(cuts))
	from := 0
	for i, c := range cuts {
		if c.end-c.start <= keep {
			base := out.Frames() - from
			out.Samples = append(out.Samples, b.Samples[from*ch:c.end*ch]...)
			moved[i] = turnCut{c.start + base, c.end + base}
			from = c.end
			continue
		}
		head := keep / 2
		out.Samples = append(out.Samples, b.Samples[from*ch:(c.start+head)*ch]...)
		start := out.Frames() - head
		resume := c.end - (keep - head)
		n := min(fade, head, keep-head)
		tail := out.Samples[(out.Frames()-n)*ch:]
		for f := range n {
			g := 1 - float32(f+1)/float32(n+1)
			for k := range ch {
				tail[f*ch+k] *= g
			}
		}
		for f := range n {
			g := float32(f+1) / float32(n+1)
			for k := range ch {
				out.Samples = append(out.Samples, b.Samples[(resume+f)*ch+k]*g)
			}
		}
		from = resume + n
		moved[i] = turnCut{start, start + keep}
	}
	out.Samples = append(out.Samples, b.Samples[from*ch:]...)
	return out, moved
}

// turnWeights are the expected seconds of each line of a turn.
func turnWeights(texts []string) []float64 {
	out := make([]float64, len(texts))
	for i, t := range texts {
		w, c := text.Count(t)
		out[i] = max(text.Estimate(w, c).Seconds(), 0.3)
	}
	return out
}

// turnPauseSlack is how much longer than the pacing's same-speaker gap
// a pause inside a merged turn may stay (tightenTurn).
const turnPauseSlack = 0.12

// turnCut is where one line of a merged turn gives way to the next: the
// pause between them, in frames of the turn's audio.
type turnCut struct{ start, end int }

// Split limits: a pause must last splitMinPause; a line must come out
// between splitMinRatio and splitMaxRatio of its expected share of the
// turn.
const (
	splitMinPause = 0.06
	splitMinRatio = 0.35
	splitMaxRatio = 2.8
)

// splitMerged finds the pauses that split a merged turn's audio (edges
// trimmed) into its lines, whose expected lengths are weights: it picks,
// in order, the pauses that best combine length and closeness to where
// each line should end. ok is false when the turn cannot be split with
// confidence (too few pauses, a line far too short or long): the caller
// makes the lines one by one instead.
func splitMerged(b dsp.Buffer, weights []float64) ([]turnCut, bool) {
	n := len(weights)
	if n < 2 {
		return nil, n == 1
	}
	frames := b.Frames()
	if frames == 0 || b.Rate <= 0 {
		return nil, false
	}
	pauses := quietRuns(b, splitMinPause)
	if len(pauses) < n-1 {
		return nil, false
	}
	total := 0.0
	for _, w := range weights {
		total += w
	}
	// Where each boundary is expected, in frames.
	expect := make([]float64, n-1)
	acc := 0.0
	for k := 0; k < n-1; k++ {
		acc += weights[k]
		expect[k] = acc / total * float64(frames)
	}
	score := func(p turnCut, k int) float64 {
		dur := float64(p.end-p.start) / float64(b.Rate)
		mid := float64(p.start+p.end) / 2
		off := math.Abs(mid-expect[k]) / float64(frames)
		return math.Min(dur, 0.6)/0.6 - 3*off
	}
	// Dynamic programming over (boundary k, pause j): best[k][j] is the
	// best total with boundary k at pause j.
	m := len(pauses)
	best := make([][]float64, n-1)
	from := make([][]int, n-1)
	for k := range best {
		best[k] = make([]float64, m)
		from[k] = make([]int, m)
		for j := range best[k] {
			best[k][j] = math.Inf(-1)
			from[k][j] = -1
			if j < k || m-j < n-1-k {
				continue
			}
			s := score(pauses[j], k)
			if k == 0 {
				best[k][j] = s
				continue
			}
			for i := k - 1; i < j; i++ {
				if v := best[k-1][i] + s; v > best[k][j] {
					best[k][j], from[k][j] = v, i
				}
			}
		}
	}
	j := -1
	top := math.Inf(-1)
	for x := range m {
		if best[n-2][x] > top {
			top, j = best[n-2][x], x
		}
	}
	if j < 0 || math.IsInf(top, -1) {
		return nil, false
	}
	cuts := make([]turnCut, n-1)
	for k := n - 2; k >= 0; k-- {
		cuts[k] = pauses[j]
		j = from[k][j]
	}
	// Each line within reason of its expected length.
	prev := 0
	for k := range n {
		end := frames
		if k < n-1 {
			end = cuts[k].start
		}
		got := float64(end-prev) / float64(b.Rate)
		want := weights[k] / total * float64(frames) / float64(b.Rate)
		if got < splitMinRatio*want || got > splitMaxRatio*want {
			return nil, false
		}
		if k < n-1 {
			prev = cuts[k].end
		}
	}
	return cuts, true
}

// quietRuns are the internal stretches of b quieter than its speech by
// 30 dB (at least −60, at most −35 dBFS), of at least minPause seconds,
// in 10 ms windows.
func quietRuns(b dsp.Buffer, minPause float64) []turnCut {
	w := max(b.Rate/100, 1)
	frames := b.Frames()
	nw := (frames + w - 1) / w
	db := make([]float64, nw)
	for k := range nw {
		lo, hi := k*w, min((k+1)*w, frames)
		var e float64
		for _, v := range b.Samples[lo*b.Channels : hi*b.Channels] {
			e += float64(v) * float64(v)
		}
		db[k] = dsp.PowerDB(e/float64((hi-lo)*b.Channels) + 1e-12)
	}
	sorted := slices.Clone(db)
	slices.Sort(sorted)
	ref := sorted[min(len(sorted)-1, len(sorted)*95/100)]
	thr := min(max(ref-30, -60), -35)
	minW := int(math.Ceil(minPause * 100))
	var out []turnCut
	run, seen := -1, false
	for k, v := range db {
		quiet := v < thr
		switch {
		case !quiet:
			if seen && run >= 0 && k-run >= minW {
				out = append(out, turnCut{run * w, k * w})
			}
			seen, run = true, -1
		case run < 0:
			run = k
		}
	}
	return out
}
