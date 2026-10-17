package services

// Lead-ins of story lines (experimental, Production.LeadIn;
// plan/story-emotion-context-plan.md §3 B): the models cannot read
// context without saying it, so a character's line is said with the end
// of their own previous line before it, in one synthesis, and the
// lead-in is cut off where the forced aligner says the line begins. The
// line then starts in the mood and pitch the previous one left instead of
// from scratch. Only text goes before the line: each synthesis starts
// from the character's voice as every other line does (no audio of
// earlier lines, which made voices drift, thread #40).
//
// The cut is checked: the aligner's words must be the text's, the line
// must come out a plausible length, and the speech recognizer must hear
// the line (character error rate within leadMaxCER). A line that fails is
// made as usual. Cut lines and failures are cached next to the chunks.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/text"
	"github.com/hehehai/AudioSmith/internal/text/plan"
)

// Lead-in limits: the lead-in is the last sentence (else clause) of the
// character's previous line within leadBack lines, at most leadMaxCJK
// unspaced characters or leadMaxWords words.
const (
	leadBack     = 4
	leadMaxCJK   = 24
	leadMaxWords = 14
	// A cut line is kept when the recognizer's error rate on it is at
	// most leadMaxCER, and its length within leadMinRatio–leadMaxRatio of
	// what the lead-in's pace predicts.
	leadMaxCER   = 0.25
	leadMinRatio = 0.4
	leadMaxRatio = 2.5
	// leadVersion changes when the cutting does, so cached cuts stop
	// matching.
	leadVersion = "lead1"
)

// leadStep is a line said after a lead-in: the chunk says prefix then
// the line (whole); path is where the cut line is cached; fallback makes the line
// as usual.
type leadStep struct {
	prefix, whole, line string // as said: the lead-in, both, the line
	lang                string
	path                string
	fallback            []chapterStep
}

// leadPrefix is the lead-in of line li: the end of the same character's
// previous line (within leadBack lines, in the same voice), or "" when
// the line takes none: not one plain span, a turn of the scene (its
// feeling should not carry over), or nobody said anything before it.
func leadPrefix(lines []LineSpec, li int) string {
	l := lines[li]
	plain := func(l LineSpec) bool {
		return len(l.Spans) == 1 && l.Character != "" && !strings.Contains(strings.ToLower(l.Spans[0].Text), "[pause") && hasSpeech(l.Spans[0].Text)
	}
	if !plain(l) || l.Turn {
		return ""
	}
	for j := li - 1; j >= max(li-leadBack, 0); j-- {
		p := lines[j]
		if p.Character != l.Character {
			continue
		}
		if !plain(p) || p.Spans[0].Voice != l.Spans[0].Voice {
			return ""
		}
		return leadTail(stripTags(p.Spans[0].Text))
	}
	return ""
}

// leadTail is the end of s that a lead-in says: all of it, or its last
// sentences, or its last clauses when that is too long, ending with its punctuation
// (a full stop added when it has none, so the model pauses before the
// line).
func leadTail(s string) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if s == "" {
		return ""
	}
	if r := []rune(s); !strings.ContainsRune("。！？.!?…；;", r[len(r)-1]) {
		if cjkShare(s) >= 0.3 {
			s += "。"
		} else {
			s += "."
		}
	}
	fits := func(t string) bool {
		w, c := text.Count(t)
		return c <= leadMaxCJK && w <= leadMaxWords
	}
	if fits(s) {
		return s
	}
	r := []rune(s)
	// Cut points: after sentence ends first, then after clauses.
	for _, marks := range []string{"。！？.!?…", "。！？.!?…，,；;：:"} {
		for i := 0; i < len(r)-1; i++ {
			if !strings.ContainsRune(marks, r[i]) {
				continue
			}
			if t := strings.TrimSpace(string(r[i+1:])); fits(t) && hasSpeech(t) {
				return t
			}
		}
	}
	return ""
}

// lead plans line li said after prefix: one chunk saying both, and the
// line's own steps as the fallback; nil when the two do not make exactly
// one chunk, or the plan says them differently together than apart.
func (cr chapterRenderer) lead(p *ChapterParams, li int, prefix string,
	take func(int, SpanSpec, string) (core.Take, *int64, map[string]string, error), planOf func(core.Take, string) plan.Plan,
	lineSteps func(int, io.Writer) ([]chapterStep, error), info ModelInfo, modelPath string) (*chapterStep, error) {
	sp := p.Lines[li].Spans[0]
	say := leadJoin(prefix, sp.Text)
	t, seed, extra, err := take(li, sp, say)
	if err != nil {
		return nil, err
	}
	whole, pre, line := planOf(t, say), planOf(t, prefix), planOf(t, sp.Text)
	one := func(pl plan.Plan) bool { return len(pl.Segments) == 1 && pl.Segments[0].Kind != plan.Silence }
	if !one(whole) || !one(pre) || !one(line) ||
		leadUnits(whole.Segments[0].Text) != leadUnits(pre.Segments[0].Text)+leadUnits(line.Segments[0].Text) {
		return nil, nil
	}
	chunks, err := cr.r.chunks(&RenderParams{Take: t, Workflow: p.Workflow}, &whole, info, modelPath, seed, cr.r.longformChunkDir(), extra)
	if err != nil {
		return nil, &jobs.Error{Code: CodeRenderFailed, Message: err.Error()}
	}
	fb, err := lineSteps(li, io.Discard)
	if err != nil {
		return nil, err
	}
	lang := p.Lang
	if lang == "" {
		lang = "en"
		if cjkShare(sp.Text) >= 0.3 {
			lang = "zh"
		}
	}
	c := chunks[0]
	return &chapterStep{chunk: &c, brk: whole.Segments[0].Break, line: li, lead: &leadStep{
		prefix: pre.Segments[0].Text, whole: whole.Segments[0].Text, line: line.Segments[0].Text, lang: lang,
		path: filepath.Join(cr.r.longformChunkDir(), c.key+"-"+leadVersion+".wav"), fallback: fb}}, nil
}

// leadJoin is what the lead-in synthesis says.
func leadJoin(prefix, line string) string {
	return joinTurn([]string{prefix, line})
}

// leadUnits are a text's words as the aligner counts them, folded:
// unspaced characters one by one, other words whole, no punctuation.
func leadUnits(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Reasons a cut fails (logged, and counted by the evaluation).
var (
	errLeadWords  = errors.New("the aligner's words are not the text's")
	errLeadLength = errors.New("the line is far shorter or longer than expected")
	errLeadOrder  = errors.New("the line starts before the lead-in ends")
	errLeadHeard  = errors.New("the recognizer does not hear the line")
)

// leadCut finds where the line starts in the lead-in synthesis from the
// aligner's words: the window between the lead-in's last word and the
// line's first (seconds), checked.
func leadCut(words []proto.Word, prefix, whole string) (from, to float64, err error) {
	want, pre := leadUnits(whole), leadUnits(prefix)
	if !strings.HasPrefix(want, pre) || len(want) == len(pre) {
		return 0, 0, errLeadWords
	}
	var got strings.Builder
	k := -1
	for i, w := range words {
		got.WriteString(leadUnits(w.Text))
		if k < 0 && got.Len() >= len(pre) {
			if got.String() != pre {
				return 0, 0, errLeadWords // a word across the join
			}
			k = i + 1
		}
	}
	if got.String() != want || k <= 0 || k >= len(words) {
		return 0, 0, errLeadWords
	}
	pw, lw := words[:k], words[k:]
	from, to = pw[len(pw)-1].End, lw[0].Start
	if to < from-0.1 {
		return 0, 0, errLeadOrder
	}
	// The line's length at the lead-in's pace.
	pre1, line1 := pw[0].Start, lw[len(lw)-1].End
	pd, ld := from-pre1, line1-to
	pu, lu := float64(len([]rune(pre))), float64(len([]rune(want))-len([]rune(pre)))
	if pd <= 0 || ld <= 0 || pu == 0 {
		return 0, 0, errLeadLength
	}
	ratio := ld / (lu * pd / pu)
	if ratio < leadMinRatio || ratio > leadMaxRatio {
		return 0, 0, errLeadLength
	}
	// Words the aligner gave no time: it lost the line.
	empty := 0
	for _, w := range lw {
		if w.End-w.Start < 0.01 {
			empty++
		}
	}
	if float64(empty) > 0.25*float64(len(lw)) {
		return 0, 0, errLeadLength
	}
	return min(from, to), max(from, to), nil
}

// leadSplit is the frame where the line's audio begins: the quietest
// 10 ms between the lead-in's end and the line's start (widened by
// 30 ms on the lead-in's side, where the aligner's ends run late).
func leadSplit(b dsp.Buffer, from, to float64) int {
	w := max(b.Rate/100, 1)
	lo := max(dsp.FramesAt(from-0.03, b.Rate), 0)
	hi := min(dsp.FramesAt(to, b.Rate), b.Frames())
	if hi-lo < w {
		return min(max(dsp.FramesAt((from+to)/2, b.Rate), 0), b.Frames())
	}
	best, at := math.Inf(1), lo
	for s := lo; s+w <= hi; s += w / 2 {
		var e float64
		for _, v := range b.Samples[s*b.Channels : (s+w)*b.Channels] {
			e += float64(v) * float64(v)
		}
		if e < best {
			best, at = e, s+w/2
		}
	}
	return at
}

// leadFade fades the cut line in over 10 ms (in place).
func leadFade(b dsp.Buffer) {
	n := min(max(b.Rate/100, 1), b.Frames())
	for f := range n {
		g := float32(f+1) / float32(n+1)
		for c := range b.Channels {
			b.Samples[f*b.Channels+c] *= g
		}
	}
}

// leadIn makes the cut line of step s (its chunk the lead-in synthesis)
// at s.lead.path, or reports why it could not: the caller makes the line
// as usual then. A cut made before is used again; so is a failure.
func (cr chapterRenderer) leadIn(ctx context.Context, st *renderState, i, n int, s chapterStep) (cached bool, err error) {
	ls := s.lead
	if fi, err := os.Stat(ls.path); err == nil && fi.Size() > 44 {
		return true, nil
	}
	if b, err := os.ReadFile(ls.path + ".fail"); err == nil {
		return true, errors.New(strings.TrimSpace(string(b)))
	}
	r := cr.r
	if r.engine == nil || r.aligner == "" {
		return false, errors.New("no forced aligner")
	}
	if _, _, err := st.synthesize(ctx, i, n, *s.chunk); err != nil {
		return false, err
	}
	var al proto.AlignResult
	if err := r.call(ctx, st.job, proto.MethodSTTAlign, r.aligner, &proto.AlignParams{Audio: s.chunk.path, Text: ls.whole, Language: ls.lang}, &al); err != nil {
		return false, err
	}
	fail := func(e error) (bool, error) {
		os.WriteFile(ls.path+".fail", []byte(e.Error()), 0o644)
		return false, e
	}
	from, to, err := leadCut(al.Words, ls.prefix, ls.whole)
	if err != nil {
		return fail(err)
	}
	buf, _, err := wav.ReadFile(s.chunk.path)
	if err != nil {
		return false, err
	}
	cut := buf.Slice(leadSplit(buf, from, to), buf.Frames()).Clone()
	leadFade(cut)
	tmp := ls.path + ".tmp.wav"
	defer os.Remove(tmp)
	if err := wav.WriteFile(tmp, cut, wav.Float32, wav.Options{}); err != nil {
		return false, err
	}
	if r.recognizer != "" {
		var tr proto.TranscribeResult
		if err := r.call(ctx, st.job, proto.MethodSTTTranscribe, r.recognizer, &proto.TranscribeParams{Audio: tmp, Language: ls.lang}, &tr); err != nil {
			return false, err
		}
		if dub.Drift(leadFold(ls.line), leadFold(tr.Text)) > leadMaxCER {
			return fail(errLeadHeard)
		}
	}
	return false, os.Rename(tmp, ls.path)
}

// leadFold folds the third-person pronouns, which sound the same.
func leadFold(s string) string {
	return strings.NewReplacer("她", "他", "它", "他").Replace(stripTags(s))
}

// call runs one engine request other than tts.synthesize for a render
// (the lead-ins' alignment and check), with the model's local snapshot.
func (r *renderer) call(ctx context.Context, job jobs.Job, method, model string, params any, out any) error {
	ex := r.engine(method)
	if ex == nil {
		return &jobs.Error{Code: CodeEngineUnavailable, Message: "The engine is not connected.", Retryable: true}
	}
	dir := ""
	if r.modelPath != nil {
		d, err := r.modelPath(ctx, model)
		if err != nil {
			return err
		}
		dir = d
	}
	switch p := params.(type) {
	case *proto.AlignParams:
		p.Model, p.ModelPath = model, dir
	case *proto.TranscribeParams:
		p.Model, p.ModelPath = model, dir
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	sub := jobs.Job{ID: job.ID + "." + filepath.Base(method), Spec: jobs.Spec{Kind: method, Lane: jobs.LaneGPU, Params: raw, Owner: job.Owner}}
	res, err := ex.Run(ctx, sub, func(jobs.Progress) {}, nil)
	if err != nil {
		return err
	}
	return json.Unmarshal(res, out)
}
