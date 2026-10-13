package services

// The render pipeline: a take of any length, as one "take.render" job.
//
//	script ─▶ text/plan (markup lowered for the model, dictionary,
//	          normalisation, chunks the model can take)
//	       ─▶ for each chunk: the chunk cache (text.Key) or one
//	          tts.synthesize call through the engine executor
//	       ─▶ edges trimmed, joined (dsp/edit.Joiner: 50 ms crossfades
//	          inside sentences, gaps between sentences and paragraphs,
//	          [pause] silences) into a float WAV, streamed to the UI
//	          while it grows (the job's chunks, pcm16le)
//	       ─▶ "Even out volume" (dsp/master preset, two passes) ─▶ the
//	          take's WAV in its bit depth (TPDF dither at 16 bits)
//	       ─▶ Check audio (dsp/qc) and a dropped-chunk check
//
// The job runs on the gpu lane, so the chunks of one take run back to
// back on a warm model, and the engine sees one call at a time. It is
// idempotent: run again after a crash or a retryable failure, the chunks
// already made come from the cache, so it goes on where it stopped.
// Cancelling the job cancels the chunk being made.
//
// Each engine call uses the job's id with the chunk's number as request
// id ("<job>.3"), so the engine log and its cancellation follow it.

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp/edit"
	"github.com/hehehai/AudioSmith/internal/dsp/loudness"
	"github.com/hehehai/AudioSmith/internal/dsp/master"
	"github.com/hehehai/AudioSmith/internal/dsp/qc"
	"github.com/hehehai/AudioSmith/internal/dsp/resample"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/text"
	"github.com/hehehai/AudioSmith/internal/text/lexicon"
	"github.com/hehehai/AudioSmith/internal/text/plan"
)

// KindRender is the job kind of a take's render.
const KindRender = "take.render"

// Job error codes of renders, besides the engine's (proto.Code*), which a
// failed chunk passes on.
const (
	CodeNothingToSay      = "nothing_to_say"     // the script has no speech
	CodeRenderFailed      = "render_failed"      // joining, mastering or writing failed
	CodeEngineUnavailable = "engine_unavailable" // as engine.CodeEngineUnavailable
)

// Notice codes of TakeRender.Warnings (besides plan's and markup's
// diagnostic codes, which keep theirs) and of TakeRender.Issues (dsp/qc's
// codes).
const (
	NoticeChunkShort  = "render.chunk_short"  // much shorter than its text: words may be missing (value: seconds, params expected)
	NoticeChunkSilent = "render.chunk_silent" // no speech at all
	NoticeChunkLong   = "render.chunk_long"   // much longer than its text: the model may have rambled
	NoticePostSkipped = "render.post_skipped" // the engine's file could not be read: kept as it is
)

// MasterDefault is the mastering preset of "Even out volume".
const MasterDefault = "broadcast"

// RenderParams are a take.render job's params.
type RenderParams struct {
	// Take is the recipe: its ID and File are set; Render holds the
	// output options.
	Take core.Take `json:"take"`
	// Lang is the script's language for normalisation and the
	// dictionary (BCP-47); "" guesses it from the text.
	Lang string `json:"lang,omitempty"`
	// Output is the absolute path of the WAV to write.
	Output string `json:"output"`
	// Stream sends the audio joined so far as the job's chunks
	// (proto.ChunkParams, pcm16le) for the streaming preview.
	Stream bool `json:"stream,omitempty"`
}

// RenderResult is a take.render job's result.
type RenderResult struct {
	Output       string          `json:"output"`
	SampleRate   int             `json:"sampleRate"`
	Channels     int             `json:"channels"`
	Frames       int64           `json:"frames"`
	AudioSeconds float64         `json:"audioSeconds"`
	Seconds      float64         `json:"seconds"` // the render's wall time
	Seed         int64           `json:"seed,omitempty"`
	Render       core.TakeRender `json:"render"`
}

// RenderProgress is published when a render starts a chunk, so the UI can
// say which ("chunk 3 of 12"). Topic "render.progress".
type RenderProgress struct {
	Job    string // the render job
	Owner  string // its owner ("take:<id>")
	Chunk  int    // 1-based chunk starting; 0 when finishing (mastering, writing)
	Chunks int
	Cached int // chunks taken from the cache so far
}

func (RenderProgress) Topic() string { return "render.progress" }

// ModelInfo is what the services need to know of a model (from the
// catalogue, which they do not import).
type ModelInfo struct {
	Traits
	Revision string
	// Languages maps the values the language parameter takes to BCP-47
	// tags ("Chinese" → "zh"); VoiceLanguages built-in voices to theirs.
	Languages      map[string]string
	VoiceLanguages map[string]string
}

// Lang is the BCP-47 tag of a recipe's language for the text pipeline:
// its language parameter, else its voice's language, else "" (guess).
func (m ModelInfo) Lang(language, voice string) string {
	if l, ok := m.Languages[language]; ok {
		return l
	}
	if l, ok := m.VoiceLanguages[voice]; ok {
		return l
	}
	if language != "" && !strings.ContainsAny(language, " ") && len(language) <= 12 {
		return language // a code already ("en-US")
	}
	return ""
}

// renderer runs take.render jobs.
type renderer struct {
	synth     func() jobs.Executor
	modelPath func(ctx context.Context, model string) (string, error)
	licences  core.LicenceService
	models    func(repo string) ModelInfo
	pron      *Pronunciation
	cacheDir  string
	bus       *core.Bus
	logf      func(string, ...any)
	// cacheMax caps the chunk cache, in bytes.
	cacheMax int64
}

func (r *renderer) model(repo string) ModelInfo {
	if r.models != nil {
		return r.models(repo)
	}
	return ModelInfo{Traits: TraitsFor("", nil)}
}

func (r *renderer) chunkDir() string { return filepath.Join(r.cacheDir, "chunks") }
func (r *renderer) tmpDir() string   { return filepath.Join(r.cacheDir, "tmp") }

// renderChunk is one speech segment's call.
type renderChunk struct {
	seg    plan.Segment
	params proto.SynthesizeParams
	key    string
	path   string
	chars  int
}

// Run implements jobs.Executor.
func (r *renderer) Run(ctx context.Context, job jobs.Job, report func(jobs.Progress), chunkOut func(json.RawMessage)) (json.RawMessage, error) {
	started := time.Now()
	var p RenderParams
	if err := json.Unmarshal(job.Params, &p); err != nil {
		return nil, &jobs.Error{Code: proto.CodeBadRequest, Message: "params: " + err.Error()}
	}
	t := p.Take
	opts := core.TakeRender{}
	if t.Render != nil {
		opts = *t.Render
	}
	if p.Output == "" || !filepath.IsAbs(p.Output) {
		return nil, &jobs.Error{Code: proto.CodeBadRequest, Message: "output: an absolute path is required"}
	}
	if r.licences != nil {
		if err := r.licences.Check(ctx, t.Model); err != nil {
			if errors.Is(err, core.ErrLicenceRequired) {
				data, _ := json.Marshal(map[string]string{"model": t.Model})
				return nil, &jobs.Error{Code: proto.CodeLicenceRequired, Message: err.Error(), Data: data}
			}
			return nil, &jobs.Error{Code: proto.CodeInternal, Message: err.Error()}
		}
	}
	modelPath := ""
	if r.modelPath != nil {
		dir, err := r.modelPath(ctx, t.Model)
		if err != nil {
			if errors.Is(err, core.ErrNotDownloaded) {
				data, _ := json.Marshal(map[string]string{"model": t.Model})
				return nil, &jobs.Error{Code: proto.CodeModelNotDownloaded, Message: err.Error(), Data: data}
			}
			return nil, &jobs.Error{Code: proto.CodeInternal, Message: err.Error()}
		}
		modelPath = dir
	}
	info := r.model(t.Model)

	// The plan.
	dict := r.dictionary(ctx, p.Lang)
	pl := plan.Build(t.Text, plan.Options{
		Lang: p.Lang, NonVerbal: info.NonVerbal, Speed: info.Speed,
		MaxChars: info.MaxChars, MaxCharsCJK: info.MaxCharsCJK,
		NoNormalize: opts.NoNormalize, Lexicon: dict,
	})
	var warnings []core.TakeNotice
	for _, d := range pl.Diags {
		if d.Code == plan.CodeNothingToSay {
			return nil, &jobs.Error{Code: CodeNothingToSay, Message: "The script has nothing to say."}
		}
		if d.Severity >= text.Warning {
			warnings = append(warnings, core.TakeNotice{Code: d.Code, Severity: "warning", Params: d.Params})
		}
	}

	// One seed for every chunk, so the take can be made again.
	var seed *int64
	switch {
	case opts.SeedPinned && t.Seed != 0:
		seed = &t.Seed
	case len(pl.Speech()) > 1:
		n, _ := rand.Int(rand.Reader, big.NewInt(math.MaxInt32))
		s := n.Int64() + 1
		seed = &s
	}

	chunks, err := r.chunks(&p, &pl, info, modelPath, seed)
	if err != nil {
		return nil, &jobs.Error{Code: CodeRenderFailed, Message: err.Error()}
	}
	if err := os.MkdirAll(r.chunkDir(), 0o755); err != nil {
		return nil, &jobs.Error{Code: CodeRenderFailed, Message: err.Error()}
	}
	if err := os.MkdirAll(r.tmpDir(), 0o755); err != nil {
		return nil, &jobs.Error{Code: CodeRenderFailed, Message: err.Error()}
	}

	st := &renderState{r: r, job: job, p: &p, report: report, chunkOut: chunkOut, total: 0}
	for _, c := range chunks {
		st.total += c.chars
	}
	defer st.cleanup()

	// Speech and silences, in order.
	n := len(chunks)
	ci := 0
	cached := 0
	var takeChunks []core.TakeChunk
	single := n == 1 && len(pl.Segments) == 1
	var rawSingle string // the one chunk's file, when post-processing failed
	var rawRes proto.SynthesizeResult
	for _, seg := range pl.Segments {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if seg.Kind == plan.Silence {
			if err := st.silence(float64(seg.Millis) / 1000); err != nil {
				return nil, &jobs.Error{Code: CodeRenderFailed, Message: err.Error()}
			}
			continue
		}
		c := chunks[ci]
		ci++
		r.publish(RenderProgress{Job: job.ID, Owner: job.Owner, Chunk: ci, Chunks: n, Cached: cached})
		tc := core.TakeChunk{Text: c.seg.Text}
		res, hit, err := st.synthesize(ctx, ci, n, c)
		if err != nil {
			return nil, err
		}
		if hit {
			cached++
			tc.Cached = true
		} else {
			tc.GenSeconds = res.Seconds
			if res.Seed != nil {
				tc.Seed = *res.Seed
			}
		}
		if seed != nil && tc.Seed == 0 {
			tc.Seed = *seed
		}
		start, end, voiced, err := st.appendChunk(c.path, c.seg.Break)
		if err != nil {
			if single && !errors.Is(err, context.Canceled) {
				// The one file the engine wrote cannot be read: keep it as
				// it is rather than fail the take.
				rawSingle, rawRes = c.path, res
				warnings = append(warnings, core.TakeNotice{Code: NoticePostSkipped, Severity: "info", Params: map[string]string{"error": err.Error()}})
				takeChunks = append(takeChunks, tc)
				break
			}
			os.Remove(c.path) // unreadable: made again next time
			return nil, &jobs.Error{Code: CodeRenderFailed, Message: fmt.Sprintf("chunk %d of %d: %v", ci, n, err), Retryable: true}
		}
		tc.Start, tc.End = start, end
		takeChunks = append(takeChunks, tc)
		warnings = append(warnings, dropCheck(ci, c, t.Speed, start, end, voiced)...)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	r.publish(RenderProgress{Job: job.ID, Owner: job.Owner, Chunk: 0, Chunks: n, Cached: cached})
	report(jobs.Progress{Stage: "writing", Fraction: 1, AudioSeconds: st.seconds()})
	var out finalAudio
	if rawSingle != "" {
		out, err = copyRaw(ctx, rawSingle, p.Output)
		out.rate, out.channels, out.frames = rawRes.SampleRate, max(rawRes.Channels, 1), rawRes.Frames
		if out.frames == 0 && rawRes.SampleRate > 0 {
			out.frames = int64(rawRes.AudioSeconds * float64(rawRes.SampleRate))
		}
	} else {
		out, err = st.finish(ctx, opts)
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &jobs.Error{Code: CodeRenderFailed, Message: err.Error()}
	}
	r.pruneCache()

	res := RenderResult{
		Output: p.Output, SampleRate: out.rate, Channels: out.channels, Frames: out.frames,
		AudioSeconds: out.seconds(), Seconds: time.Since(started).Seconds(),
	}
	if seed != nil {
		res.Seed = *seed
	} else if len(takeChunks) > 0 {
		res.Seed = takeChunks[0].Seed
	}
	res.Render = opts
	res.Render.Channels, res.Render.Bytes = out.channels, out.bytes
	res.Render.Chunks, res.Render.Warnings = takeChunks, warnings
	res.Render.Issues, res.Render.QCDone = out.issues, out.qcDone
	return json.Marshal(res)
}

func (r *renderer) dictionary(ctx context.Context, lang string) *lexicon.Dictionary {
	if r.pron == nil {
		return nil
	}
	d, err := r.pron.Dictionary(ctx, lang)
	if err != nil {
		r.logf("render: pronunciation dictionary: %v", err)
		return nil
	}
	return d
}

func (r *renderer) publish(ev RenderProgress) {
	if r.bus != nil {
		r.bus.Publish(ev)
	}
}

// chunks makes the engine calls of the plan's speech, with their cache keys.
func (r *renderer) chunks(p *RenderParams, pl *plan.Plan, info ModelInfo, modelPath string, seed *int64) ([]renderChunk, error) {
	t := p.Take
	speech := pl.Speech()
	totalChars := 0
	for _, i := range speech {
		totalChars += text.RuneLen(pl.Segments[i].Text)
	}
	// A target duration is shared by the chunks by their length, after
	// the pauses.
	speechSeconds := 0.0
	if t.Duration > 0 {
		speechSeconds = max(t.Duration-float64(pl.PauseMillis())/1000, 0.5*float64(len(speech)))
	}
	ref := ""
	if t.RefAudio != "" {
		fi, err := os.Stat(t.RefAudio)
		if err != nil {
			return nil, fmt.Errorf("reference clip: %w", err)
		}
		ref = t.RefAudio + "|" + strconv.FormatInt(fi.Size(), 10) + "|" + strconv.FormatInt(fi.ModTime().UnixNano(), 10)
	}
	out := make([]renderChunk, 0, len(speech))
	for _, i := range speech {
		seg := pl.Segments[i]
		chars := text.RuneLen(seg.Text)
		sp := proto.SynthesizeParams{
			Model: t.Model, ModelPath: modelPath, Text: seg.Text, Voice: t.Voice, Language: t.Language,
			MaxTokens: t.MaxTokens, RefAudio: t.RefAudio, RefText: t.RefText, Steps: t.Steps, Seed: seed,
		}
		speed := t.Speed
		if seg.Speed != 1 && seg.Speed > 0 {
			if speed == 0 {
				speed = 1
			}
			speed *= seg.Speed
		}
		if speed != 0 {
			sp.Speed = &speed
		}
		if t.Temperature != 0 {
			sp.Temperature = &t.Temperature
		}
		if t.TopP != 0 {
			sp.TopP = &t.TopP
		}
		if speechSeconds > 0 && totalChars > 0 {
			d := math.Round(speechSeconds*float64(chars)/float64(totalChars)*100) / 100
			sp.Duration = &d
		}
		f := func(v *float64) string {
			if v == nil {
				return ""
			}
			return strconv.FormatFloat(*v, 'g', -1, 64)
		}
		keyParams := map[string]string{
			"model": t.Model, "revision": info.Revision, "voice": t.Voice, "language": t.Language,
			"speed": f(sp.Speed), "temperature": f(sp.Temperature), "topP": f(sp.TopP),
			"maxTokens": strconv.Itoa(t.MaxTokens), "steps": strconv.Itoa(t.Steps), "duration": f(sp.Duration),
			"ref": ref, "refText": t.RefText, "bits": "32",
		}
		if seed != nil && !info.Deterministic {
			keyParams["seed"] = strconv.FormatInt(*seed, 10)
		}
		key := text.Key(seg.Text, keyParams)
		path := filepath.Join(r.chunkDir(), key+".wav")
		sp.Output = proto.Output{Path: path, Format: "wav", Bits: 32}
		out = append(out, renderChunk{seg: seg, params: sp, key: key, path: path, chars: chars})
	}
	return out, nil
}

// renderState is one render in progress: the joined audio streams to a
// float WAV in the temporary directory, metered for mastering.
type renderState struct {
	r        *renderer
	job      jobs.Job
	p        *RenderParams
	report   func(jobs.Progress)
	chunkOut func(json.RawMessage)

	total, done int // chars of all chunks, of chunks finished

	rate, channels int
	joiner         *edit.Joiner
	tmp            *wav.FileWriter
	tmpPath        string
	meter          *loudness.Meter // integrated loudness of the joined audio
	frames         int64
	pending        []byte // pcm16 not yet streamed
	seq            int
	files          []string
	leading        float64 // seconds of [pause] before the first speech
}

func (st *renderState) seconds() float64 {
	if st.rate == 0 {
		return 0
	}
	return float64(st.frames) / float64(st.rate)
}

func (st *renderState) cleanup() {
	if st.tmp != nil {
		st.tmp.Abort()
	}
	for _, f := range st.files {
		os.Remove(f)
	}
}

// synthesize makes chunk i (1-based) of n, or finds it in the cache.
func (st *renderState) synthesize(ctx context.Context, i, n int, c renderChunk) (proto.SynthesizeResult, bool, error) {
	var res proto.SynthesizeResult
	if fi, err := os.Stat(c.path); err == nil && fi.Size() > 44 {
		now := time.Now()
		os.Chtimes(c.path, now, now) // most recently used
		st.done += c.chars
		return res, true, nil
	}
	synth := st.r.synth
	var ex jobs.Executor
	if synth != nil {
		ex = synth()
	}
	if ex == nil {
		return res, false, &jobs.Error{Code: CodeEngineUnavailable, Message: "The engine is not connected.", Retryable: true}
	}
	params, err := json.Marshal(c.params)
	if err != nil {
		return res, false, err
	}
	sub := jobs.Job{ID: st.job.ID + "." + strconv.Itoa(i), Spec: jobs.Spec{Kind: proto.MethodTTSSynthesize, Lane: jobs.LaneGPU, Params: params, Owner: st.job.Owner}}
	base := st.seconds()
	report := func(pr jobs.Progress) {
		frac := 0.0
		if pr.Fraction > 0 {
			frac = pr.Fraction
		}
		stage := "generating"
		if pr.Stage == "loading" {
			stage = "loading"
		}
		fraction := -1.0
		if st.total > 0 && (pr.Fraction >= 0 || n > 1) {
			fraction = (float64(st.done) + frac*float64(c.chars)) / float64(st.total)
		}
		st.report(jobs.Progress{Stage: stage, Fraction: min(fraction, 1), AudioSeconds: base + pr.AudioSeconds,
			Message: fmt.Sprintf("chunk %d/%d", i, n)})
	}
	report(jobs.Progress{Stage: "generating", Fraction: -1})
	raw, err := ex.Run(ctx, sub, report, nil)
	if err != nil {
		if ctx.Err() != nil {
			return res, false, ctx.Err()
		}
		var je *jobs.Error
		if errors.As(err, &je) {
			data, _ := json.Marshal(map[string]int{"chunk": i, "chunks": n})
			msg := je.Message
			if n > 1 {
				msg = fmt.Sprintf("Chunk %d of %d: %s", i, n, je.Message)
			}
			return res, false, &jobs.Error{Code: je.Code, Message: msg, Data: data, Retryable: je.Retryable}
		}
		return res, false, err
	}
	json.Unmarshal(raw, &res)
	st.done += c.chars
	return res, false, nil
}

// joinFor is the join before a segment.
func joinFor(b plan.Break) edit.Join {
	g := edit.DefaultGaps
	switch b {
	case plan.BreakChunk:
		return g.Join(edit.Chunk)
	case plan.BreakSentence:
		return g.Join(edit.Sentence)
	case plan.BreakLine:
		return g.Join(edit.Line)
	case plan.BreakParagraph:
		return g.Join(edit.Paragraph)
	}
	return edit.Join{}
}

// open starts the joined stream at the first chunk's format.
func (st *renderState) open(rate, channels int) error {
	st.rate, st.channels = rate, channels
	st.tmpPath = filepath.Join(st.r.tmpDir(), st.job.ID+"-joined.wav")
	w, err := wav.Create(st.tmpPath, wav.Format{Rate: rate, Channels: channels, Encoding: wav.Float32}, wav.Options{Large: true})
	if err != nil {
		return err
	}
	st.tmp = w
	st.meter = loudness.NewMeter(rate, channels, loudness.Options{SkipTruePeak: true})
	st.joiner = edit.NewJoiner(rate, channels, edit.DefaultCrossfade, st.sink)
	return nil
}

// sink receives finished joined audio.
func (st *renderState) sink(s []float32) error {
	if err := st.tmp.Write(s); err != nil {
		return err
	}
	st.meter.Write(s)
	st.frames += int64(len(s) / st.channels)
	if st.p.Stream && st.chunkOut != nil {
		for _, v := range s {
			v = min(max(v, -1), 1)
			st.pending = binary.LittleEndian.AppendUint16(st.pending, uint16(int16(v*32767)))
		}
	}
	return nil
}

// flushStream sends the audio joined so far to the preview, in pieces of
// at most about 5 s.
func (st *renderState) flushStream(final bool) {
	if !st.p.Stream || st.chunkOut == nil {
		return
	}
	piece := max(st.rate*st.channels*2*5, 2)
	for len(st.pending) > 0 || final {
		n := min(len(st.pending), piece)
		c := proto.ChunkParams{Job: st.job.ID, Seq: st.seq, SampleRate: st.rate, Channels: st.channels,
			Encoding: "pcm16le", Data: st.pending[:n], Final: final && n == len(st.pending)}
		raw, err := json.Marshal(c)
		if err == nil {
			st.chunkOut(raw)
		}
		st.seq++
		st.pending = st.pending[n:]
		if c.Final {
			break
		}
	}
	st.pending = st.pending[:0:0]
}

// silence appends a [pause].
func (st *renderState) silence(seconds float64) error {
	if st.joiner == nil {
		// A pause before any speech: the format is not known yet; keep it.
		st.leading += seconds
		return nil
	}
	_, err := st.joiner.Silence(seconds)
	st.flushStream(false)
	return err
}

// appendChunk reads a chunk's audio, trims its edges and joins it. It
// returns where the chunk landed (seconds) and how much of it is sound.
func (st *renderState) appendChunk(path string, b plan.Break) (start, end, voiced float64, err error) {
	buf, _, err := wav.ReadFile(path)
	if err != nil {
		return 0, 0, 0, err
	}
	if buf.Frames() == 0 || buf.Rate <= 0 {
		return 0, 0, 0, fmt.Errorf("%s: no audio", filepath.Base(path))
	}
	if st.joiner == nil {
		if err := st.open(buf.Rate, buf.Channels); err != nil {
			return 0, 0, 0, err
		}
		if st.leading > 0 {
			if _, err := st.joiner.Silence(st.leading); err != nil {
				return 0, 0, 0, err
			}
		}
	}
	if buf.Rate != st.rate || buf.Channels != st.channels {
		buf = resample.Convert(buf, st.rate, st.channels, resample.Good)
	}
	trimmed := edit.Trim(buf, edit.TrimOptions{})
	voiced = trimmed.Seconds()
	join := joinFor(b)
	if st.joiner.Frames() == 0 {
		join = edit.Join{}
	}
	span, err := st.joiner.Append(trimmed.Samples, join)
	if err != nil {
		return 0, 0, 0, err
	}
	st.flushStream(false)
	rg := span.Range(st.rate)
	return rg.Start, rg.End, voiced, nil
}

// dropCheck compares a chunk's length with what its text should take.
func dropCheck(i int, c renderChunk, speed, start, end, voiced float64) []core.TakeNotice {
	if speed <= 0 {
		speed = 1
	}
	if c.seg.Speed > 0 {
		speed *= c.seg.Speed
	}
	w, n := text.Count(c.seg.Text)
	expected := text.Estimate(w, n).Seconds() / speed
	if c.params.Duration != nil {
		expected = *c.params.Duration
	}
	notice := func(code string) core.TakeNotice {
		return core.TakeNotice{Code: code, Severity: "warning", Chunk: i, Start: start, End: end, Value: math.Round(voiced*10) / 10,
			Params: map[string]string{"expected": strconv.FormatFloat(math.Round(expected*10)/10, 'f', -1, 64)}}
	}
	switch {
	case voiced < 0.05:
		return []core.TakeNotice{notice(NoticeChunkSilent)}
	case expected >= 2 && voiced < 0.4*expected:
		return []core.TakeNotice{notice(NoticeChunkShort)}
	case expected >= 2 && voiced > 3*expected && voiced > 8:
		return []core.TakeNotice{notice(NoticeChunkLong)}
	}
	return nil
}

// finalAudio describes the take's file.
type finalAudio struct {
	rate, channels int
	frames         int64
	bytes          int64
	issues         []core.TakeNotice
	qcDone         bool
}

func (f finalAudio) seconds() float64 {
	if f.rate == 0 {
		return 0
	}
	return float64(f.frames) / float64(f.rate)
}

// encodingOf maps a take's bits to a WAV encoding.
func encodingOf(bits int) wav.Encoding {
	switch bits {
	case 24:
		return wav.PCM24
	case 32:
		return wav.Float32
	}
	return wav.PCM16
}

// finish closes the joined audio, masters it if asked, and writes the
// take's file with Check audio's findings.
func (st *renderState) finish(ctx context.Context, opts core.TakeRender) (finalAudio, error) {
	if err := st.joiner.Close(); err != nil {
		return finalAudio{}, err
	}
	st.flushStream(true)
	integrated := st.meter.Integrated()
	if err := st.tmp.Close(); err != nil {
		return finalAudio{}, err
	}
	st.tmp = nil
	st.files = append(st.files, st.tmpPath)
	src := st.tmpPath

	var gainStage *loudness.Stream
	if opts.Master != "" && opts.Master != master.Raw.ID {
		preset, err := master.Lookup(opts.Master)
		if err != nil {
			return finalAudio{}, err
		}
		pre := 0.0
		if preset.Compressor != nil && !math.IsInf(integrated, 0) {
			pre = master.Reference - integrated
		}
		pr, err := master.NewProcessor(preset, st.rate, st.channels, pre)
		if err != nil {
			return finalAudio{}, err
		}
		// Pass 2: filters and compressor, metered with true peak.
		mid := filepath.Join(st.r.tmpDir(), st.job.ID+"-mastered.wav")
		st.files = append(st.files, mid)
		w, err := wav.Create(mid, wav.Format{Rate: st.rate, Channels: st.channels, Encoding: wav.Float32}, wav.Options{Large: true})
		if err != nil {
			return finalAudio{}, err
		}
		m := loudness.NewMeter(st.rate, st.channels, loudness.Options{})
		err = eachBlock(ctx, src, func(s []float32) error {
			pr.Process(s)
			m.Write(s)
			return w.Write(s)
		})
		if err != nil {
			w.Abort()
			return finalAudio{}, err
		}
		if err := w.Close(); err != nil {
			return finalAudio{}, err
		}
		src = mid
		if preset.Loudness != 0 {
			target := loudness.Target{ID: preset.ID, Integrated: preset.Loudness, MinIntegrated: preset.Loudness - 1,
				MaxIntegrated: preset.Loudness + 1, TruePeak: preset.TruePeak}
			gain, limit := loudness.Plan(m.Stats(), target)
			gainStage = loudness.NewStream(st.rate, st.channels, gain, preset.TruePeak, limit)
		}
	}

	// Last pass: gain and ceiling, the take's bit depth, Check audio.
	enc := encodingOf(opts.Bits)
	out, err := wav.Create(st.p.Output, wav.Format{Rate: st.rate, Channels: st.channels, Encoding: enc}, wav.Options{Dither: enc == wav.PCM16})
	if err != nil {
		return finalAudio{}, err
	}
	check := qc.NewChecker(st.rate, st.channels, qc.Options{MaxIssues: 20})
	write := func(s []float32) error {
		check.Write(s)
		return out.Write(s)
	}
	var buf []float32
	err = eachBlock(ctx, src, func(s []float32) error {
		if gainStage == nil {
			return write(s)
		}
		buf = gainStage.Process(buf[:0], s)
		return write(buf)
	})
	if err == nil && gainStage != nil {
		err = write(gainStage.Flush(buf[:0]))
	}
	if err != nil {
		out.Abort()
		return finalAudio{}, err
	}
	frames := out.Frames()
	if err := out.Close(); err != nil {
		return finalAudio{}, err
	}
	fa := finalAudio{rate: st.rate, channels: st.channels, frames: frames, qcDone: true}
	if fi, err := os.Stat(st.p.Output); err == nil {
		fa.bytes = fi.Size()
	}
	fa.issues = Issues(check.Report())
	return fa, nil
}

// Issues converts Check audio's report to take notices.
func Issues(rep qc.Report) []core.TakeNotice {
	var out []core.TakeNotice
	for _, is := range rep.Issues {
		out = append(out, core.TakeNotice{Code: "qc." + is.Code, Severity: string(is.Severity),
			Start: math.Round(is.Start*1000) / 1000, End: math.Round(is.End*1000) / 1000, Value: math.Round(is.Value*100) / 100})
	}
	return out
}

// CheckAudio runs Check audio over a WAV file.
func CheckAudio(ctx context.Context, path string) ([]core.TakeNotice, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rd, err := wav.NewReader(f)
	if err != nil {
		return nil, err
	}
	format := rd.Format()
	check := qc.NewChecker(format.Rate, format.Channels, qc.Options{MaxIssues: 20})
	buf := make([]float32, 8192*format.Channels)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := rd.Read(buf)
		check.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	return Issues(check.Report()), nil
}

// eachBlock reads a WAV file in blocks.
func eachBlock(ctx context.Context, path string, fn func([]float32) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	rd, err := wav.NewReader(f)
	if err != nil {
		return err
	}
	buf := make([]float32, 16384*rd.Format().Channels)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := rd.Read(buf)
		if n > 0 {
			if err := fn(buf[:n]); err != nil {
				return err
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// copyRaw copies an engine file the pipeline could not read to the take.
func copyRaw(ctx context.Context, src, dst string) (finalAudio, error) {
	if err := copyFile(ctx, src, dst); err != nil {
		return finalAudio{}, err
	}
	fa := finalAudio{}
	if fi, err := os.Stat(dst); err == nil {
		fa.bytes = fi.Size()
	}
	return fa, nil
}

// pruneCache keeps the chunk cache under its cap, dropping the least
// recently used chunks.
func (r *renderer) pruneCache() {
	limit := r.cacheMax
	if limit <= 0 {
		limit = 2 << 30
	}
	type file struct {
		path string
		size int64
		at   time.Time
	}
	var files []file
	var total int64
	filepath.WalkDir(r.chunkDir(), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".wav") {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			files = append(files, file{path, fi.Size(), fi.ModTime()})
			total += fi.Size()
		}
		return nil
	})
	if total <= limit {
		return
	}
	slices.SortFunc(files, func(a, b file) int { return a.at.Compare(b.at) })
	for _, f := range files {
		if total <= limit*9/10 {
			break
		}
		if os.Remove(f.path) == nil {
			total -= f.size
		}
	}
}

// convertWAV writes src to dst (atomically) as a WAV of bits.
func convertWAV(ctx context.Context, src, dst string, bits int) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	rd, err := wav.NewReader(f)
	if err != nil {
		f.Close()
		return err
	}
	format := rd.Format()
	f.Close()
	enc := encodingOf(bits)
	out, err := wav.Create(dst, wav.Format{Rate: format.Rate, Channels: format.Channels, Encoding: enc}, wav.Options{Dither: enc == wav.PCM16 && format.Encoding != wav.PCM16})
	if err != nil {
		return err
	}
	if err := eachBlock(ctx, src, out.Write); err != nil {
		out.Abort()
		return err
	}
	return out.Close()
}
