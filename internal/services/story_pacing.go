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
//     (StoryInstruct; Traits.Instruct), Chatterbox's exaggeration for
//     Chatterbox, nothing for the others.
//
// A document without a pacing (stories made before it existed) renders
// exactly as before: one gap, a call per line.

import (
	"math"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/text"
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

// --- merged turns -------------------------------------------------------------

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

// --- delivery ------------------------------------------------------------------

// Instruction limits (runes): each part is cut to its limit, so the
// instruction stays short (the model reads it with every line).
const (
	instructPersona   = 32
	instructScene     = 48
	instructDirection = 32
	instructLatin     = 3 // Latin text: limits × this (in characters)
)

// StoryInstruct is a story line's delivery instruction for models that
// take one: the character's persona, the scene, and the line's direction
// with its intensity, in Chinese when the line is (else English). "" when
// all three are empty. prev is the direction of the line before, when
// another character said it ("" to leave it out).
func StoryInstruct(lineText, scene, persona, direction string, intensity int, prev string) string {
	zh := cjkShare(lineText) >= 0.3
	lim := func(s string, n int) string {
		s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
		s = strings.TrimRightFunc(s, func(r rune) bool { return strings.ContainsRune("。.，,；;：:！!", r) || unicode.IsSpace(r) })
		if !zh {
			n *= instructLatin
		}
		if utf8.RuneCountInString(s) > n {
			s = strings.TrimSpace(string([]rune(s)[:n])) + "…"
		}
		return s
	}
	persona, scene, direction, prev = lim(persona, instructPersona), lim(scene, instructScene), lim(direction, instructDirection), lim(prev, instructDirection)
	var parts []string
	if zh {
		if persona != "" {
			parts = append(parts, persona+"。")
		}
		if scene != "" {
			parts = append(parts, "场景："+scene+"。")
		}
		if prev != "" && direction != "" {
			parts = append(parts, "对方刚才"+prev+"。")
		}
		if direction != "" {
			d := "这一句：" + direction
			if turnLines(direction) {
				d = "这几句：" + direction
			}
			switch intensity {
			case IntensityLight:
				d += "，情绪克制"
			case IntensityStrong:
				d += "，情绪饱满强烈"
			}
			parts = append(parts, d+"。")
		}
		return strings.Join(parts, "")
	}
	if persona != "" {
		parts = append(parts, upperFirst(persona)+".")
	}
	if scene != "" {
		parts = append(parts, "Scene: "+scene+".")
	}
	if prev != "" && direction != "" {
		parts = append(parts, "The other speaker was just "+prev+".")
	}
	if direction != "" {
		d := "This line: " + direction
		if turnLines(direction) {
			d = "These lines: " + direction
		}
		switch intensity {
		case IntensityLight:
			d += ", restrained"
		case IntensityStrong:
			d += ", with strong emotion"
		}
		parts = append(parts, d+".")
	}
	return strings.Join(parts, " ")
}

// directionExaggeration is Chatterbox's exaggeration for a direction of
// intensity (0.5 is the model's neutral).
func directionExaggeration(intensity int) float64 {
	switch intensity {
	case IntensityLight:
		return 0.55
	case IntensityStrong:
		return 0.9
	}
	return 0.7
}

// withDelivery puts a line's delivery into its recipe: the instruction
// into the voice field of models that take one (after the speaker of
// Qwen3 CustomVoice, after the description of a designed voice: the
// engine's own instruct field is dropped when a voice is set), the
// exaggeration for Chatterbox. A clone (reference clip or voice prompt)
// takes no instruction.
func withDelivery(t *core.Take, info ModelInfo, instruct, direction string, intensity int) {
	switch {
	case info.Instruct && instruct != "" && t.RefAudio == "" && t.VoicePrompt == "":
		switch {
		case t.Voice == "":
			t.Voice = instruct
		case strings.ContainsAny(t.Voice, ",，"):
			// "Speaker, instruction" or a description already: goes on.
			t.Voice = strings.TrimRight(t.Voice, " ") + " " + instruct
		default:
			t.Voice += ", " + instruct
		}
	case info.Exaggeration && direction != "" && t.Exaggeration == 0:
		t.Exaggeration = directionExaggeration(intensity)
	}
}

// Continuity levels of a model for stories (ContinuityOf).
const (
	ContinuityInstruct  = "instruct"  // takes the scene, personas and directions
	ContinuityIntensity = "intensity" // Chatterbox: directions set its exaggeration
	ContinuityLines     = "lines"     // pacing and merged turns only
)

// ContinuityOf is what a model does with a story's scene, personas and
// directions.
func ContinuityOf(info Traits) string {
	switch {
	case info.Instruct:
		return ContinuityInstruct
	case info.Exaggeration:
		return ContinuityIntensity
	}
	return ContinuityLines
}

// TakesInstruct reports whether a model with capabilities caps takes a
// delivery instruction: "instruct" (Qwen3-TTS CustomVoice and VoiceDesign,
// as the engine reports them), or a free-form voice field on a model that
// does not clone (an entry that only says "voiceStyle"). OmniVoice's
// voice field is a closed list of design attributes, not a delivery.
func TakesInstruct(caps []string) bool {
	return slices.Contains(caps, "instruct") || slices.Contains(caps, "voiceStyle") && !slices.Contains(caps, "clone")
}

// cjkShare is the share of a text's letters in CJK scripts.
func cjkShare(s string) float64 {
	cjk, all := 0, 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			all++
			if text.IsCJK(r) {
				cjk++
			}
		}
	}
	if all == 0 {
		return 0
	}
	return float64(cjk) / float64(all)
}

func upperFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
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
