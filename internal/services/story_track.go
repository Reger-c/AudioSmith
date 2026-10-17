package services

// Character tracks (experimental, Production.Tracks;
// plan/story-emotion-context-plan.md §6 F): within a chapter, a
// character's lines are said in one synthesis per stretch, as one person
// reading their part, so the voice and the mood carry on from one line to
// the next; the forced aligner then cuts the stretch into its lines,
// which go back into the dialogue with the pacing's gaps. A stretch
// breaks at the character's turns (where the feeling may change) and at
// a length limit; it is said in the character's baseline feeling, or in
// the turn's that opens it, so a line's own feeling comes from its words
// (and its punctuation, story_steer.go).
//
// Each cut is checked as a lead-in's is (story_leadin.go): the aligner's
// words, each line's length at the stretch's pace, and the recognizer
// hearing each line. A stretch that fails is made line by line. The cut
// lines (and failures) are cached next to the chunks.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/text"
	"github.com/hehehai/AudioSmith/internal/text/plan"
)

// Track limits: a stretch has at most trackMaxLines lines and
// trackMaxCJK unspaced characters (about 35 s of Chinese) or
// trackMaxChars of other text, within 80 % of the model's chunk limit.
const (
	trackMaxLines = 8
	trackMaxCJK   = 160
	trackMaxChars = 400
	trackVersion  = "track1"
)

// trackSeg is one stretch of a character's lines: their indices, what
// each says, the chunk saying them all, and where their cut lines go.
type trackSeg struct {
	lines []int
	texts []string
	whole string
	lang  string
	chunk renderChunk
	brk   plan.Break
	paths []string
	// made: cut (or tried); err: why it could not be.
	made bool
	err  error
}

// trackStep is line k of a stretch, with its own steps as the fallback.
type trackStep struct {
	seg      *trackSeg
	k        int
	chunk    *renderChunk // the stretch's chunk; counted on its first line only
	fallback []chapterStep
}

// trackGroups are the stretches of a chapter's lines: per character (and
// voice), their plain lines in order, a new stretch at each turn, at a
// change of speed and at the limits. Only stretches of two or more lines.
func trackGroups(lines []LineSpec, info ModelInfo) [][]int {
	maxCJK := min(trackMaxCJK, max(info.MaxCharsCJK*8/10, 1))
	maxChars := min(trackMaxChars, max(info.MaxChars*8/10, 1))
	type open struct {
		lines     []int
		cjk, rest int
	}
	cur := map[string]*open{}
	var order []string
	var out [][]int
	flush := func(k string) {
		if o := cur[k]; o != nil && len(o.lines) > 1 {
			out = append(out, o.lines)
		}
		delete(cur, k)
	}
	for i, l := range lines {
		if len(l.Spans) != 1 || l.Character == "" || strings.Contains(strings.ToLower(l.Spans[0].Text), "[pause") || !hasSpeech(l.Spans[0].Text) {
			continue
		}
		k := l.Character + "\x00" + l.Spans[0].Voice
		w, c := text.Count(l.Spans[0].Text)
		o := cur[k]
		if o != nil && (l.Turn || len(o.lines) >= trackMaxLines || o.cjk+c > maxCJK || o.rest+w*6 > maxChars || lines[o.lines[0]].Speed != l.Speed) {
			flush(k)
			o = nil
		}
		if o == nil {
			o = &open{}
			cur[k] = o
			order = append(order, k)
		}
		o.lines = append(o.lines, i)
		o.cjk, o.rest = o.cjk+c, o.rest+w*6
	}
	for _, k := range order {
		flush(k)
	}
	slices.SortFunc(out, func(a, b []int) int { return a[0] - b[0] })
	return out
}

// trackFeeling is how a stretch is said: the turn's feeling that opens
// it, else the character's baseline, faintly (steady when none).
func trackFeeling(first LineSpec) (string, int) {
	switch {
	case first.Turn && first.Emotion != "":
		return first.Emotion, first.Intensity
	case first.Baseline != "":
		return first.Baseline, IntensityLight
	}
	return EmotionSteady, 0
}

// trackEnd ends a line of a stretch with a full stop when it ends no
// sentence, so the model pauses between lines.
func trackEnd(s string) string {
	if sentenceEnd(s) {
		return s
	}
	if cjkShare(s) >= 0.3 {
		return strings.TrimRight(s, " ") + "。"
	}
	return strings.TrimRight(s, " ") + "."
}

// tracks plans the chapter's stretches: by line, its step.
func (cr chapterRenderer) tracks(p *ChapterParams,
	take func(int, SpanSpec, string) (core.Take, *int64, map[string]string, error), planOf func(core.Take, string) plan.Plan,
	lineSteps func(int, io.Writer) ([]chapterStep, error), info ModelInfo, modelPath string) (map[int]*trackStep, error) {
	out := map[int]*trackStep{}
	for _, g := range trackGroups(p.Lines, info) {
		first := g[0]
		saved := p.Lines[first]
		e, in := trackFeeling(saved)
		// The stretch speaks as its first line, in the stretch's feeling.
		p.Lines[first].Emotion, p.Lines[first].Intensity, p.Lines[first].Direction, p.Lines[first].Turn = e, in, "", true
		var parts []string
		for _, li := range g {
			parts = append(parts, trackEnd(p.Lines[li].Spans[0].Text))
		}
		whole := joinTurn(parts)
		t, seed, extra, err := take(first, p.Lines[first].Spans[0], whole)
		p.Lines[first] = saved
		if err != nil {
			return nil, err
		}
		wp := planOf(t, whole)
		if len(wp.Segments) != 1 || wp.Segments[0].Kind == plan.Silence {
			continue
		}
		seg := &trackSeg{lines: g, whole: wp.Segments[0].Text, brk: wp.Segments[0].Break}
		units := ""
		for _, s := range parts {
			lp := planOf(t, s)
			if len(lp.Segments) != 1 || lp.Segments[0].Kind == plan.Silence {
				seg = nil
				break
			}
			seg.texts = append(seg.texts, lp.Segments[0].Text)
			units += leadUnits(lp.Segments[0].Text)
		}
		if seg == nil || units != leadUnits(seg.whole) {
			continue // said differently together than apart
		}
		chunks, err := cr.r.chunks(&RenderParams{Take: t, Workflow: p.Workflow}, &wp, info, modelPath, seed, cr.r.longformChunkDir(), extra)
		if err != nil {
			return nil, &jobs.Error{Code: CodeRenderFailed, Message: err.Error()}
		}
		seg.chunk = chunks[0]
		seg.lang = p.Lang
		if seg.lang == "" {
			seg.lang = "en"
			if cjkShare(whole) >= 0.3 {
				seg.lang = "zh"
			}
		}
		for k, li := range g {
			seg.paths = append(seg.paths, filepath.Join(cr.r.longformChunkDir(), fmt.Sprintf("%s-%s-%d.wav", seg.chunk.key, trackVersion, k)))
			fb, err := lineSteps(li, io.Discard)
			if err != nil {
				return nil, err
			}
			c := seg.chunk
			if k > 0 {
				c.chars = 0
			}
			out[li] = &trackStep{seg: seg, k: k, chunk: &c, fallback: fb}
		}
	}
	return out, nil
}

// trackRanges are where each part (a line, as said) of a stretch lies
// in its synthesis by the aligner's words, checked: the words are the
// text's, and each line lasts within leadMinRatio–leadMaxRatio of its
// share at the stretch's pace, with few words the aligner lost.
func trackRanges(words []proto.Word, parts []string) ([][2]float64, error) {
	var want strings.Builder
	ends := make([]int, len(parts)) // cumulative units at each part's end
	for i, s := range parts {
		want.WriteString(leadUnits(s))
		ends[i] = want.Len()
	}
	var got strings.Builder
	out := make([][2]float64, len(parts))
	k, first := 0, 0
	for i, w := range words {
		got.WriteString(leadUnits(w.Text))
		if k < len(parts) && got.Len() >= ends[k] {
			if got.Len() != ends[k] || i < first {
				return nil, errLeadWords // a word across two lines
			}
			lw := words[first : i+1]
			empty := 0
			for _, x := range lw {
				if x.End-x.Start < 0.01 {
					empty++
				}
			}
			if float64(empty) > 0.25*float64(len(lw)) {
				return nil, errLeadLength
			}
			out[k] = [2]float64{lw[0].Start, lw[len(lw)-1].End}
			k, first = k+1, i+1
		}
	}
	if got.String() != want.String() || k != len(parts) {
		return nil, errLeadWords
	}
	total := 0.0 // the voiced time, without the pauses between lines
	for _, r := range out {
		total += r[1] - r[0]
	}
	units := float64(len([]rune(want.String())))
	for i, r := range out {
		u := float64(len([]rune(leadUnits(parts[i]))))
		ratio := (r[1] - r[0]) / (u * total / units)
		if r[1] <= r[0] || ratio < leadMinRatio || ratio > leadMaxRatio {
			return nil, errLeadLength
		}
		if i > 0 && r[0] < out[i-1][1]-0.1 {
			return nil, errLeadOrder
		}
	}
	return out, nil
}

// trackCut makes a stretch's cut lines, or says why it cannot.
func (cr chapterRenderer) trackCut(ctx context.Context, st *renderState, i, n int, seg *trackSeg) error {
	cached := true
	for _, p := range seg.paths {
		if fi, err := os.Stat(p); err != nil || fi.Size() <= 44 {
			cached = false
		}
	}
	if cached {
		return nil
	}
	if b, err := os.ReadFile(seg.paths[0] + ".fail"); err == nil {
		return errors.New(strings.TrimSpace(string(b)))
	}
	r := cr.r
	if _, _, err := st.synthesize(ctx, i, n, seg.chunk); err != nil {
		return err
	}
	var al proto.AlignResult
	if err := r.call(ctx, st.job, proto.MethodSTTAlign, r.aligner, &proto.AlignParams{Audio: seg.chunk.path, Text: seg.whole, Language: seg.lang}, &al); err != nil {
		return err
	}
	fail := func(e error) error {
		os.WriteFile(seg.paths[0]+".fail", []byte(e.Error()), 0o644)
		return e
	}
	rg, err := trackRanges(al.Words, seg.texts)
	if err != nil {
		return fail(err)
	}
	buf, _, err := wav.ReadFile(seg.chunk.path)
	if err != nil {
		return err
	}
	cuts := []int{0}
	for k := 1; k < len(rg); k++ {
		cuts = append(cuts, leadSplit(buf, rg[k-1][1], rg[k][0]))
	}
	cuts = append(cuts, buf.Frames())
	tmps := make([]string, len(seg.paths))
	defer func() {
		for _, t := range tmps {
			os.Remove(t)
		}
	}()
	for k := range seg.paths {
		clip := buf.Slice(cuts[k], cuts[k+1]).Clone()
		leadFade(clip)
		trackFadeOut(clip)
		tmps[k] = seg.paths[k] + ".tmp.wav"
		if err := wav.WriteFile(tmps[k], clip, wav.Float32, wav.Options{}); err != nil {
			return err
		}
		if r.recognizer != "" {
			var tr proto.TranscribeResult
			if err := r.call(ctx, st.job, proto.MethodSTTTranscribe, r.recognizer, &proto.TranscribeParams{Audio: tmps[k], Language: seg.lang}, &tr); err != nil {
				return err
			}
			if dub.Drift(leadFold(seg.texts[k]), leadFold(tr.Text)) > leadMaxCER {
				return fail(fmt.Errorf("line %d: %w", k+1, errLeadHeard))
			}
		}
	}
	for k := range seg.paths {
		if err := os.Rename(tmps[k], seg.paths[k]); err != nil {
			return err
		}
	}
	return nil
}

// trackFadeOut fades a cut line out over its last 10 ms (in place).
func trackFadeOut(b dsp.Buffer) {
	n := min(max(b.Rate/100, 1), b.Frames())
	for f := range n {
		g := float32(f+1) / float32(n+1)
		at := b.Frames() - 1 - f
		for c := range b.Channels {
			b.Samples[at*b.Channels+c] *= g
		}
	}
}

// characterSeed is a character's own seed in a story whose seed is seed
// (Production.CharacterSeeds): the same for every line and every render.
func characterSeed(seed int64, character string) int64 {
	h := uint64(14695981039346656037)
	for _, b := range []byte(character) {
		h = (h ^ uint64(b)) * 1099511628211
	}
	return seed ^ int64(h>>34) // within the engine's positive seeds
}
