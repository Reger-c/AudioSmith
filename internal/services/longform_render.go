package services

// A longform chapter: one "longform.chapter" job on the gpu lane renders
// every line of a chapter through the take pipeline's pieces (render.go):
// each span of a line is planned for the model (markup lowered,
// dictionary plus the book's lexicon, normalisation, chunks), each chunk
// comes from the chunk cache or one tts.synthesize call, and the chunks
// are joined with the production's gaps into the chapter. So a chapter
// whose text did not change costs no synthesis, and an edited line costs
// only its own chunks.
//
// The chapter is then brought to the book's loudness target (two passes:
// the join meters it, the second applies gain and a true-peak limiter) and
// written as a 16-bit WAV with a sidecar JSON (ChapterResult): where each
// line and span landed (stems, the read-along player) and the loudness
// report. A chapter whose sidecar says it was made from the same chunks,
// gaps and target is reused as it is (a resumed render).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp/edit"
	"github.com/hehehai/AudioSmith/internal/dsp/loudness"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/text"
	"github.com/hehehai/AudioSmith/internal/text/lexicon"
	"github.com/hehehai/AudioSmith/internal/text/plan"
)

// Job kinds and owner of longform renders.
const (
	KindChapter   = "longform.chapter"  // gpu lane: one chapter
	KindAssemble  = "longform.assemble" // cpu lane: the book, after its chapters
	OwnerLongform = "longform:"         // + the render's id
)

// Silence around a chapter (ACX asks for room tone at both ends) and
// between the spans of a line (a voice switch).
const (
	chapterHead = 0.5
	chapterTail = 1.5
	spanGap     = 0.15
)

// chapterKeyVersion changes when the chapter layout changes, so sidecars
// of older renders stop matching.
const chapterKeyVersion = "audiosmith.chapter.v1"

// ChapterParams are a longform.chapter job's params: everything the
// chapter needs, so it can run again after a restart.
type ChapterParams struct {
	Render     string               `json:"render"`
	Index      int                  `json:"index"`
	Title      string               `json:"title,omitempty"`
	Model      string               `json:"model"`
	Lang       string               `json:"lang,omitempty"`
	Seed       int64                `json:"seed,omitempty"`
	Voices     map[string]core.Take `json:"voices"`
	Lines      []LineSpec           `json:"lines"`
	Production Production           `json:"production"`
	Lexicon    []LexiconEntry       `json:"lexicon,omitempty"`
	Loudness   string               `json:"loudness,omitempty"`
	Scene      string               `json:"scene,omitempty"` // the story's (instructions)
	// Output is the chapter's WAV (absolute); its sidecar is Output+".json".
	Output string `json:"output"`
}

// ChapterResult is a chapter job's result and its sidecar.
type ChapterResult struct {
	Output       string  `json:"output"`
	Key          string  `json:"key"`
	SampleRate   int     `json:"sampleRate"`
	Channels     int     `json:"channels"`
	Frames       int64   `json:"frames"`
	AudioSeconds float64 `json:"audioSeconds"`
	Seconds      float64 `json:"seconds"` // wall time
	Chunks       int     `json:"chunks"`
	Cached       int     `json:"cached"` // chunks from the cache
	// Reused: the file of an earlier run of this render was kept.
	Reused bool `json:"reused,omitempty"`
	// Turns are the merged turns (lines of one character made as one)
	// split back into their lines; TurnsUnsplit the ones that could not
	// be, made line by line instead.
	Turns        int `json:"turns,omitempty"`
	TurnsUnsplit int `json:"turnsUnsplit,omitempty"`
	// Levelled counts the lines brought toward their character's loudness.
	Levelled int               `json:"levelled,omitempty"`
	Report   LoudnessReport    `json:"report"`
	Warnings []core.TakeNotice `json:"warnings,omitempty"`
	Lines    []LinePlace       `json:"lines"`
}

// LinePlace is where a line landed in its chapter (seconds).
type LinePlace struct {
	ID    string      `json:"id,omitempty"`
	Start float64     `json:"start"`
	End   float64     `json:"end"`
	Spans []SpanPlace `json:"spans"`
}

// SpanPlace is where a span landed, with what it says (markup) and who.
type SpanPlace struct {
	Character string  `json:"character,omitempty"`
	Text      string  `json:"text"`
	Start     float64 `json:"start"`
	End       float64 `json:"end"`
}

// Sidecar is a chapter WAV's sidecar file.
func Sidecar(wavPath string) string { return wavPath + ".json" }

// ReadChapter reads a chapter's sidecar.
func ReadChapter(wavPath string) (ChapterResult, error) {
	var r ChapterResult
	data, err := os.ReadFile(Sidecar(wavPath))
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(data, &r)
	return r, err
}

// chapterStep is one step of a chapter's program: a silence, or a chunk,
// or a merged turn (a chunk saying several lines, split back into them).
type chapterStep struct {
	silence float64 // seconds; used when chunk is nil
	chunk   *renderChunk
	brk     plan.Break
	line    int
	span    int
	turn    *turnStep
}

// turnStep is a merged turn: its lines, their expected lengths, and the
// steps that make them one by one when the turn cannot be split.
type turnStep struct {
	lines    []int
	weights  []float64
	fallback []chapterStep
}

// chapterRenderer runs longform.chapter jobs.
type chapterRenderer struct{ r *renderer }

func (cr chapterRenderer) Run(ctx context.Context, job jobs.Job, report func(jobs.Progress), _ func(json.RawMessage)) (json.RawMessage, error) {
	var p ChapterParams
	if err := json.Unmarshal(job.Params, &p); err != nil {
		return nil, &jobs.Error{Code: proto.CodeBadRequest, Message: "params: " + err.Error()}
	}
	if p.Output == "" || !filepath.IsAbs(p.Output) {
		return nil, &jobs.Error{Code: proto.CodeBadRequest, Message: "output: an absolute path is required"}
	}
	r := cr.r
	modelPath, err := r.modelReady(ctx, p.Model)
	if err != nil {
		return nil, err
	}
	info := r.model(p.Model)
	for attempt := 0; ; attempt++ {
		res, err := cr.once(ctx, job, &p, info, modelPath, report)
		var je *jobs.Error
		if attempt < 2 && errors.As(err, &je) && je.Code == proto.CodeInputTooLong {
			// As a take's render: plan again with shorter chunks.
			info.MaxChars = max(info.MaxChars*6/10, 20)
			info.MaxCharsCJK = max(info.MaxCharsCJK*6/10, 8)
			r.logf("chapter %s: the engine wants shorter chunks (%s); planning again at %d characters", job.ID, je.Message, info.MaxChars)
			continue
		}
		return res, err
	}
}

// modelReady checks a model's licence and finds its local snapshot.
func (r *renderer) modelReady(ctx context.Context, model string) (string, error) {
	if r.licences != nil {
		if err := r.licences.Check(ctx, model); err != nil {
			if errors.Is(err, core.ErrLicenceRequired) {
				data, _ := json.Marshal(map[string]string{"model": model})
				return "", &jobs.Error{Code: proto.CodeLicenceRequired, Message: err.Error(), Data: data}
			}
			return "", &jobs.Error{Code: proto.CodeInternal, Message: err.Error()}
		}
	}
	if r.modelPath == nil {
		return "", nil
	}
	dir, err := r.modelPath(ctx, model)
	if err != nil {
		if errors.Is(err, core.ErrNotDownloaded) {
			data, _ := json.Marshal(map[string]string{"model": model})
			return "", &jobs.Error{Code: proto.CodeModelNotDownloaded, Message: err.Error(), Data: data}
		}
		return "", &jobs.Error{Code: proto.CodeInternal, Message: err.Error()}
	}
	return dir, nil
}

// longformChunkDir holds the chunks of longform renders: kept apart from
// the Studio's cache, whose size cap would evict a long book's chunks.
func (r *renderer) longformChunkDir() string { return filepath.Join(r.cacheDir, "longform", "chunks") }

// Limits of the longform chunk cache: chunks unused for longformKeep are
// deleted, and the oldest beyond longformCap.
const (
	longformKeep = 60 * 24 * time.Hour
	longformCap  = 24 << 30
)

// pruneLongformChunks keeps the longform chunk cache within its limits
// (after a book is assembled).
func (r *renderer) pruneLongformChunks() {
	type file struct {
		path string
		size int64
		at   time.Time
	}
	var files []file
	var total int64
	entries, _ := os.ReadDir(r.longformChunkDir())
	for _, e := range entries {
		fi, err := e.Info()
		if err != nil || e.IsDir() || !strings.HasSuffix(e.Name(), ".wav") {
			continue
		}
		path := filepath.Join(r.longformChunkDir(), e.Name())
		if time.Since(fi.ModTime()) > longformKeep {
			os.Remove(path)
			continue
		}
		files = append(files, file{path, fi.Size(), fi.ModTime()})
		total += fi.Size()
	}
	if total <= longformCap {
		return
	}
	slices.SortFunc(files, func(a, b file) int { return a.at.Compare(b.at) })
	for _, f := range files {
		if total <= longformCap*9/10 {
			break
		}
		if os.Remove(f.path) == nil {
			total -= f.size
		}
	}
}

// gapsOf are the joins of a production.
func gapsOf(p Production) edit.GapPolicy {
	g := edit.DefaultGaps
	g.Line = float64(p.LineGapMs) / 1000
	g.Paragraph = float64(p.ParagraphGapMs) / 1000
	return g
}

// program plans every line of the chapter into steps, with its key.
// Under a pacing preset (story_pacing.go) the gaps follow the turns, the
// lines carry their delivery, and turns of one character are merged.
func (cr chapterRenderer) program(ctx context.Context, p *ChapterParams, info ModelInfo, modelPath string) ([]chapterStep, string, []core.TakeNotice, error) {
	r := cr.r
	dict := cr.dictionary(ctx, p)
	gaps := gapsOf(p.Production)
	turns, paced := GapsOf(p.Production.Pacing)
	var steps []chapterStep
	var warnings []core.TakeNotice
	reported := map[string]bool{}
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%g\x00%g\x00%g\x00%g\x00%v\x00%s\x00%s\x00", chapterKeyVersion, p.Loudness, gaps.Sentence, gaps.Line,
		gaps.Paragraph, gaps.Crossfade, p.Production.KeepEdges, p.Model, info.Revision)
	if paced {
		fmt.Fprintf(h, "pacing %s level %v\x00", p.Production.Pacing, !p.Production.NoLevel)
	}
	// take is the recipe of span sp of line li, saying text.
	take := func(li int, sp SpanSpec, say string) (core.Take, *int64, map[string]string, error) {
		l := p.Lines[li]
		v, ok := p.Voices[sp.Voice]
		if !ok {
			return core.Take{}, nil, nil, &jobs.Error{Code: proto.CodeBadRequest, Message: fmt.Sprintf("line %d: no voice %q", li+1, sp.Voice)}
		}
		t := v
		t.Model, t.Text = p.Model, say
		if l.Speed > 0 && l.Speed != 1 {
			t.Speed = l.Speed
		}
		if p.Production.Steps > 0 {
			t.Steps = p.Production.Steps
		}
		if p.Production.Guidance > 0 {
			t.Guidance = p.Production.Guidance
		}
		if l.Direction != "" || l.Persona != "" || p.Scene != "" {
			prev := ""
			if li > 0 && paced && !sameSpeaker(p.Lines[li-1], l) && p.Lines[li-1].Character != "" && StoryPrevDirection {
				prev = p.Lines[li-1].Direction
			}
			withDelivery(&t, info, StoryInstruct(say, p.Scene, l.Persona, l.Direction, l.Intensity, prev), l.Direction, l.Intensity)
		}
		seed := p.Seed
		if v.Seed != 0 {
			seed = v.Seed // a designed or locked voice's identity
		}
		var extra map[string]string
		if p.Production.VaryRepeats && l.Repeat > 0 {
			seed += int64(l.Repeat)
			extra = map[string]string{"repeat": strconv.Itoa(l.Repeat)}
		}
		return t, &seed, extra, nil
	}
	// planOf plans what t says.
	planOf := func(t core.Take, say string) plan.Plan {
		lang := p.Lang
		if lang == "" {
			lang = info.Lang(t.Language, t.Voice)
		}
		pl := plan.Build(say, plan.Options{
			Lang: lang, NonVerbal: info.NonVerbal, NonVerbalTags: info.NonVerbalTags, Speed: info.Speed, Voices: true,
			MaxChars: info.MaxChars, MaxCharsCJK: info.MaxCharsCJK, Lexicon: dict,
		})
		for _, d := range pl.Diags {
			if d.Severity >= text.Warning && d.Code != plan.CodeNothingToSay {
				k := d.Code
				for _, v := range d.Params {
					k += "\x00" + v
				}
				if !reported[k] {
					reported[k] = true
					warnings = append(warnings, core.TakeNotice{Code: d.Code, Severity: "warning", Params: d.Params})
				}
			}
		}
		return pl
	}
	// lineSteps are the steps of line li's spans, each step's key part
	// written to key.
	lineSteps := func(li int, key io.Writer) ([]chapterStep, error) {
		l := p.Lines[li]
		var out []chapterStep
		for si, sp := range l.Spans {
			if si > 0 && !strings.HasPrefix(strings.ToLower(sp.Text), "[pause") {
				out = append(out, chapterStep{silence: spanGap, line: li, span: si})
				fmt.Fprintf(key, "span\x00")
			}
			t, seed, extra, err := take(li, sp, sp.Text)
			if err != nil {
				return nil, err
			}
			pl := planOf(t, sp.Text)
			chunks, err := r.chunks(&RenderParams{Take: t}, &pl, info, modelPath, seed, r.longformChunkDir(), extra)
			if err != nil {
				return nil, &jobs.Error{Code: CodeRenderFailed, Message: err.Error()}
			}
			k := 0
			for _, seg := range pl.Segments {
				if seg.Kind == plan.Silence {
					out = append(out, chapterStep{silence: float64(seg.Millis) / 1000, line: li, span: si})
					fmt.Fprintf(key, "pause %d\x00", seg.Millis)
					continue
				}
				c := chunks[k]
				k++
				out = append(out, chapterStep{chunk: &c, brk: seg.Break, line: li, span: si})
				fmt.Fprintf(key, "chunk %s %d\x00", c.key, seg.Break)
			}
		}
		return out, nil
	}
	// gapBefore is the silence before line li.
	gapBefore := func(li int) float64 {
		l, prev := p.Lines[li], p.Lines[li-1]
		if paced {
			return TurnGap(turns, prev, l)
		}
		gap := gaps.Line
		if prev.PauseAfter > 0 {
			gap = float64(prev.PauseAfter) / 1000
		} else if l.Paragraph {
			gap = gaps.Paragraph
		}
		return gap
	}
	// The merged turns, by their first line.
	merged := map[int][]int{}
	if paced && !p.Production.NoMerge {
		for _, g := range mergeGroups(p.Lines, info, p.Production.VaryRepeats) {
			merged[g[0]] = g
		}
	}
	for li := 0; li < len(p.Lines); li++ {
		if li > 0 {
			gap := gapBefore(li)
			steps = append(steps, chapterStep{silence: gap, line: li})
			fmt.Fprintf(h, "gap %g\x00", gap)
		}
		if g, ok := merged[li]; ok {
			st, err := cr.turn(p, g, take, planOf, lineSteps, gapBefore, info, modelPath)
			if err != nil {
				return nil, "", nil, err
			}
			if st != nil {
				steps = append(steps, *st)
				fmt.Fprintf(h, "turn %s %d\x00", st.chunk.key, len(g))
				li = g[len(g)-1]
				continue
			}
		}
		ls, err := lineSteps(li, h)
		if err != nil {
			return nil, "", nil, err
		}
		steps = append(steps, ls...)
	}
	return steps, hex.EncodeToString(h.Sum(nil)), warnings, nil
}

// turn plans the lines g of one character as one chunk; nil when their
// text does not make exactly one chunk (the lines are made one by one).
func (cr chapterRenderer) turn(p *ChapterParams, g []int,
	take func(int, SpanSpec, string) (core.Take, *int64, map[string]string, error), planOf func(core.Take, string) plan.Plan,
	lineSteps func(int, io.Writer) ([]chapterStep, error), gapBefore func(int) float64, info ModelInfo, modelPath string) (*chapterStep, error) {
	texts := make([]string, len(g))
	for i, li := range g {
		texts[i] = p.Lines[li].Spans[0].Text
	}
	joined := joinTurn(texts)
	// The turn speaks as its first line, with the directions of all.
	saved := p.Lines[g[0]]
	lines := make([]LineSpec, len(g))
	for i, li := range g {
		lines[i] = p.Lines[li]
	}
	p.Lines[g[0]].Direction, p.Lines[g[0]].Intensity = turnDirection(lines)
	t, seed, extra, err := take(g[0], p.Lines[g[0]].Spans[0], joined)
	p.Lines[g[0]] = saved
	if err != nil {
		return nil, err
	}
	pl := planOf(t, joined)
	if len(pl.Segments) != 1 || pl.Segments[0].Kind == plan.Silence {
		return nil, nil
	}
	chunks, err := cr.r.chunks(&RenderParams{Take: t}, &pl, info, modelPath, seed, cr.r.longformChunkDir(), extra)
	if err != nil {
		return nil, &jobs.Error{Code: CodeRenderFailed, Message: err.Error()}
	}
	c := chunks[0]
	ts := &turnStep{lines: g, weights: turnWeights(texts)}
	for i, li := range g {
		if i > 0 {
			ts.fallback = append(ts.fallback, chapterStep{silence: gapBefore(li), line: li})
		}
		ls, err := lineSteps(li, io.Discard)
		if err != nil {
			return nil, err
		}
		ts.fallback = append(ts.fallback, ls...)
	}
	return &chapterStep{chunk: &c, brk: pl.Segments[0].Break, line: g[0], turn: ts}, nil
}

// StoryPrevDirection adds the other character's last direction to a
// line's instruction ("对方刚才…"): an experiment of the A/B listening
// (e2e_story_ab_test.go), off in the app.
var StoryPrevDirection = false

// dictionary is the pronunciation dictionary with the book's lexicon on
// top (its words win).
func (cr chapterRenderer) dictionary(ctx context.Context, p *ChapterParams) *lexicon.Dictionary {
	var entries []lexicon.Entry
	if cr.r.pron != nil {
		e, err := cr.r.pron.Entries(ctx)
		if err != nil {
			cr.r.logf("chapter: pronunciation dictionary: %v", err)
		}
		entries = e
	}
	scope := lexicon.Global
	if p.Lang != "" {
		scope = p.Lang
	}
	for _, l := range p.Lexicon {
		entries = append(entries, lexicon.Entry{Term: l.Word, Replacement: l.Say, Type: lexicon.Respelling, Scope: scope, Enabled: true})
	}
	return lexicon.Compile(entries, p.Lang)
}

// once plans and renders the chapter with info's chunk limits.
func (cr chapterRenderer) once(ctx context.Context, job jobs.Job, p *ChapterParams, info ModelInfo, modelPath string, report func(jobs.Progress)) (json.RawMessage, error) {
	started := time.Now()
	r := cr.r
	steps, key, warnings, err := cr.program(ctx, p, info, modelPath)
	if err != nil {
		return nil, err
	}
	// A resumed render keeps a chapter made from the same program.
	if old, err := ReadChapter(p.Output); err == nil && old.Key == key {
		if fi, err := os.Stat(p.Output); err == nil && fi.Size() > 44 {
			old.Reused, old.Seconds = true, time.Since(started).Seconds()
			report(jobs.Progress{Stage: "cached", Fraction: 1, AudioSeconds: old.AudioSeconds})
			return json.Marshal(old)
		}
	}
	for _, d := range []string{r.longformChunkDir(), r.tmpDir(), filepath.Dir(p.Output)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, &jobs.Error{Code: CodeRenderFailed, Message: err.Error()}
		}
	}
	gaps := gapsOf(p.Production)
	st := &renderState{r: r, job: job, p: &RenderParams{Output: p.Output}, report: report, gaps: &gaps, keepEdge: p.Production.KeepEdges}
	n := 0
	for _, s := range steps {
		if s.chunk != nil {
			st.total += s.chunk.chars
			n++
		}
	}
	defer st.cleanup()
	res := ChapterResult{Output: p.Output, Key: key, Chunks: n, Warnings: warnings}
	res.Lines = make([]LinePlace, len(p.Lines))
	for i, l := range p.Lines {
		res.Lines[i] = LinePlace{ID: l.ID, Start: -1, Spans: make([]SpanPlace, len(l.Spans))}
		for j, sp := range l.Spans {
			res.Lines[i].Spans[j] = SpanPlace{Character: sp.Character, Text: sp.Text, Start: -1}
		}
	}
	place := func(s chapterStep, start, end float64) {
		lp := &res.Lines[s.line]
		sp := &lp.Spans[s.span]
		if sp.Start < 0 {
			sp.Start = start
		}
		sp.End = end
		if lp.Start < 0 {
			lp.Start = start
		}
		lp.End = end
	}
	if err := st.silence(chapterHead); err != nil {
		return nil, &jobs.Error{Code: CodeRenderFailed, Message: err.Error()}
	}
	i := 0
	var run func(s chapterStep) error
	run = func(s chapterStep) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.chunk == nil {
			if err := st.silence(s.silence); err != nil {
				return &jobs.Error{Code: CodeRenderFailed, Message: err.Error()}
			}
			return nil
		}
		i++
		c := *s.chunk
		_, hit, err := st.synthesize(ctx, i, n, c)
		if err != nil {
			return err
		}
		if hit {
			res.Cached++
		}
		buf, trimmed, lead, err := st.loadChunk(c.path)
		if err != nil {
			os.Remove(c.path) // unreadable: made again next time
			return &jobs.Error{Code: CodeRenderFailed, Message: fmt.Sprintf("chunk %d of %d: %v", i, n, err), Retryable: true}
		}
		if s.turn != nil {
			cuts, ok := splitMerged(trimmed, s.turn.weights)
			if !ok {
				// Lines the turn cannot be split into are made one by one.
				r.logf("chapter %s: lines %v could not be split from their merged turn; making them one by one", job.ID, s.turn.lines)
				res.TurnsUnsplit++
				for _, f := range s.turn.fallback {
					if f.chunk != nil {
						n++
						st.total += f.chunk.chars
					}
				}
				for _, f := range s.turn.fallback {
					if err := run(f); err != nil {
						return err
					}
				}
				return nil
			}
			res.Turns++
			if turns, ok := GapsOf(p.Production.Pacing); ok && !st.keepEdge {
				trimmed, cuts = tightenTurn(trimmed, cuts, float64(turns.Same)/1000+turnPauseSlack)
			}
			start, end, err := st.appendLoaded(buf, trimmed, s.brk)
			if err != nil {
				return &jobs.Error{Code: CodeRenderFailed, Message: err.Error()}
			}
			off := 0
			if st.keepEdge {
				off = lead
			}
			at := func(frame int) float64 { return start + float64(frame+off)/float64(st.rate) }
			for k, li := range s.turn.lines {
				a, b := 0, trimmed.Frames()
				if k > 0 {
					a = cuts[k-1].end
				}
				if k < len(cuts) {
					b = cuts[k].start
				}
				ls, le := at(a), at(b)
				if k == 0 {
					ls = start
				}
				if k == len(s.turn.lines)-1 {
					le = end
				}
				place(chapterStep{line: li}, ls, le)
			}
			for _, w := range dropCheck(i, c, p.Lines[s.line].Speed, start, end, trimmed.Seconds()) {
				w.Params["line"] = strconv.Itoa(s.line + 1)
				warnings = append(warnings, w)
			}
			return nil
		}
		start, end, err := st.appendLoaded(buf, trimmed, s.brk)
		if err != nil {
			return &jobs.Error{Code: CodeRenderFailed, Message: fmt.Sprintf("chunk %d of %d: %v", i, n, err), Retryable: true}
		}
		place(s, start, end)
		for _, w := range dropCheck(i, c, p.Lines[s.line].Speed, start, end, trimmed.Seconds()) {
			w.Params["line"] = strconv.Itoa(s.line + 1)
			warnings = append(warnings, w)
		}
		return nil
	}
	for _, s := range steps {
		if err := run(s); err != nil {
			return nil, err
		}
	}
	if st.joiner == nil {
		return nil, &jobs.Error{Code: CodeNothingToSay, Message: "The chapter has nothing to say."}
	}
	if _, err := st.joiner.Silence(chapterTail); err != nil {
		return nil, &jobs.Error{Code: CodeRenderFailed, Message: err.Error()}
	}
	report(jobs.Progress{Stage: "writing", Fraction: 1, AudioSeconds: st.seconds()})
	var level func([]LinePlace) ([]float64, error)
	if _, paced := GapsOf(p.Production.Pacing); paced && !p.Production.NoLevel {
		level = func(places []LinePlace) ([]float64, error) { return st.lineLoudness(ctx, places) }
	}
	out, rep, err := st.finishChapter(ctx, loudnessTarget(p.Loudness), func() []lineGain {
		if level == nil {
			return nil
		}
		lufs, err := level(res.Lines)
		if err != nil {
			r.logf("chapter %s: levelling: %v", job.ID, err)
			return nil
		}
		who := make([]string, len(p.Lines))
		for i, l := range p.Lines {
			who[i] = l.Character
			if len(l.Spans) > 0 {
				who[i] += "\x00" + l.Spans[0].Voice
			}
		}
		g := levelGains(res.Lines, who, lufs)
		res.Levelled = len(g)
		return g
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &jobs.Error{Code: CodeRenderFailed, Message: err.Error()}
	}
	if _, notice := r.watermarkOutput(ctx, p.Output, 16); notice != nil { // watermark.go
		warnings = append(warnings, *notice)
	}
	res.SampleRate, res.Channels, res.Frames = out.rate, out.channels, out.frames
	res.AudioSeconds = out.seconds()
	res.Report = rep
	res.Warnings = warnings
	res.Seconds = time.Since(started).Seconds()
	for i := range res.Lines {
		if res.Lines[i].Start < 0 {
			res.Lines[i].Start = 0
		}
		for j := range res.Lines[i].Spans {
			if res.Lines[i].Spans[j].Start < 0 {
				res.Lines[i].Spans[j].Start = 0
			}
		}
	}
	data, err := json.Marshal(res)
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(Sidecar(p.Output), data); err != nil {
		return nil, &jobs.Error{Code: CodeRenderFailed, Message: err.Error()}
	}
	return data, nil
}

// finishChapter closes the joined audio and writes the chapter: lines
// levelled by the gains level returns (nil: none), then gain and a
// true-peak limiter to target (nil: as joined), 16 bits with dither,
// measured for the report.
func (st *renderState) finishChapter(ctx context.Context, target *loudness.Target, level func() []lineGain) (finalAudio, LoudnessReport, error) {
	if err := st.joiner.Close(); err != nil {
		return finalAudio{}, LoudnessReport{}, err
	}
	integrated := st.meter.Integrated()
	if err := st.tmp.Close(); err != nil {
		return finalAudio{}, LoudnessReport{}, err
	}
	st.tmp = nil
	st.files = append(st.files, st.tmpPath)
	if level != nil {
		if gains := level(); len(gains) > 0 {
			lv, err := st.applyLevel(ctx, gains)
			if err != nil {
				return finalAudio{}, LoudnessReport{}, err
			}
			integrated = lv
		}
	}
	var stage *loudness.Stream
	gain := 0.0
	if target != nil && integrated > loudness.Floor {
		gain = target.Integrated - integrated
		stage = loudness.NewStream(st.rate, st.channels, gain, target.TruePeak, true)
	}
	out, err := wav.Create(st.p.Output, wav.Format{Rate: st.rate, Channels: st.channels, Encoding: wav.PCM16}, wav.Options{Dither: true})
	if err != nil {
		return finalAudio{}, LoudnessReport{}, err
	}
	v := newVerifier(st.rate, st.channels)
	write := func(s []float32) error {
		v.Write(s)
		return out.Write(s)
	}
	var buf []float32
	err = eachBlock(ctx, st.tmpPath, func(s []float32) error {
		if stage == nil {
			return write(s)
		}
		buf = stage.Process(buf[:0], s)
		return write(buf)
	})
	if err == nil && stage != nil {
		err = write(stage.Flush(buf[:0]))
	}
	if err != nil {
		out.Abort()
		return finalAudio{}, LoudnessReport{}, err
	}
	frames := out.Frames()
	if err := out.Close(); err != nil {
		return finalAudio{}, LoudnessReport{}, err
	}
	rep := v.Report(target)
	if stage != nil {
		rep.GainDB = round2(gain)
		rep.LimitDB = round2(stage.MaxReductionDB())
	}
	fa := finalAudio{rate: st.rate, channels: st.channels, frames: frames}
	if fi, err := os.Stat(st.p.Output); err == nil {
		fa.bytes = fi.Size()
	}
	return fa, rep, nil
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// lineLoudness measures each placed line of the joined audio (LUFS;
// −Inf for lines too short to measure).
func (st *renderState) lineLoudness(ctx context.Context, places []LinePlace) ([]float64, error) {
	meters := make([]*loudness.Meter, len(places))
	for i, pl := range places {
		if pl.End-pl.Start >= levelMinS {
			meters[i] = loudness.NewMeter(st.rate, st.channels, loudness.Options{SkipTruePeak: true})
		}
	}
	var at int64
	err := eachBlock(ctx, st.tmpPath, func(s []float32) error {
		n := int64(len(s) / st.channels)
		t0, t1 := float64(at)/float64(st.rate), float64(at+n)/float64(st.rate)
		for i, pl := range places {
			if meters[i] == nil || pl.End <= t0 || pl.Start >= t1 {
				continue
			}
			a := max(int64(math.Round(pl.Start*float64(st.rate)))-at, 0)
			b := min(int64(math.Round(pl.End*float64(st.rate)))-at, n)
			if b > a {
				meters[i].Write(s[a*int64(st.channels) : b*int64(st.channels)])
			}
		}
		at += n
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]float64, len(places))
	for i, m := range meters {
		out[i] = math.Inf(-1)
		if m != nil {
			out[i] = m.Integrated()
		}
	}
	return out, nil
}

// applyLevel rewrites the joined audio with the levelling gains and
// returns its new integrated loudness.
func (st *renderState) applyLevel(ctx context.Context, gains []lineGain) (float64, error) {
	path := filepath.Join(st.r.tmpDir(), st.job.ID+"-levelled.wav")
	st.files = append(st.files, path)
	w, err := wav.Create(path, wav.Format{Rate: st.rate, Channels: st.channels, Encoding: wav.Float32}, wav.Options{Large: true})
	if err != nil {
		return 0, err
	}
	m := loudness.NewMeter(st.rate, st.channels, loudness.Options{SkipTruePeak: true})
	var at int64
	err = eachBlock(ctx, st.tmpPath, func(s []float32) error {
		for f := 0; f < len(s)/st.channels; f++ {
			g := float32(gainAt(gains, float64(at+int64(f))/float64(st.rate)))
			if g != 1 {
				for c := range st.channels {
					s[f*st.channels+c] *= g
				}
			}
		}
		at += int64(len(s) / st.channels)
		m.Write(s)
		return w.Write(s)
	})
	if err != nil {
		w.Abort()
		return 0, err
	}
	if err := w.Close(); err != nil {
		return 0, err
	}
	st.tmpPath = path
	return m.Integrated(), nil
}
