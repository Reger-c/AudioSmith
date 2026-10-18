package services

// Rendering a longform chapter: each chunk from the cache or the engine,
// joined with the production's gaps (chapterRenderer.once), then brought
// to the book's loudness target with per-character levelling and written
// with its sidecar (renderState.finishChapter); split from
// longform_render.go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/hehehai/AudioSmith/internal/dsp/loudness"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
)

// once plans and renders the chapter with info's chunk limits.
func (cr chapterRenderer) once(ctx context.Context, job jobs.Job, p *ChapterParams, info ModelInfo, modelPath string, report func(jobs.Progress)) (json.RawMessage, error) {
	started := time.Now()
	r := cr.r
	dialoguePath := ""
	if p.Production.Dialogue && p.Production.DialogueModel != "" && r.engine != nil {
		var err error
		if dialoguePath, err = r.modelReady(ctx, p.Production.DialogueModel); err != nil {
			return nil, err
		}
	}
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
		if s.dialogue != nil {
			// A line of a whole-dialogue segment: its file, else as usual.
			seg := s.dialogue.seg
			if !seg.made {
				seg.made = true
				seg.err = cr.dialogueRun(ctx, st, i, n, seg, p.Production.DialogueModel, dialoguePath)
				var je *jobs.Error
				switch {
				case ctx.Err() != nil:
					return ctx.Err()
				case errors.As(seg.err, &je) && (je.Code == proto.CodeModelNotDownloaded || je.Code == proto.CodeLicenceRequired):
					return seg.err
				case seg.err != nil:
					r.logf("chapter %s: lines %v: no dialogue (%v); making them on the story's model", job.ID, seg.lines, seg.err)
					res.DialogueFailed += len(seg.lines)
				default:
					res.Dialogues++
					res.DialogueLines += len(seg.lines) - len(seg.out.failed)
					res.DialogueFailed += len(seg.out.failed)
					res.DialogueRedone += seg.out.redone
				}
			}
			if _, failed := seg.out.failed[s.dialogue.k]; seg.err != nil || failed {
				for _, f := range s.dialogue.fallback {
					if f.chunk != nil {
						n++
						st.total += f.chunk.chars
					}
				}
				for _, f := range s.dialogue.fallback {
					if err := run(f); err != nil {
						return err
					}
				}
				return nil
			}
			c.path, c.seg.Text = seg.job.paths[s.dialogue.k], seg.job.texts[s.dialogue.k]
		} else if s.track != nil {
			// A line of a character's track: cut from it, else as usual.
			seg := s.track.seg
			if !seg.made {
				seg.made = true
				seg.err = cr.trackCut(ctx, st, i, n, seg)
				if seg.err != nil && ctx.Err() == nil {
					r.logf("chapter %s: lines %v: no track (%v); making them one by one", job.ID, seg.lines, seg.err)
					res.TracksFailed++
				} else if seg.err == nil {
					res.Tracks++
					res.TrackLines += len(seg.lines)
				}
			}
			if seg.err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				for _, f := range s.track.fallback {
					if f.chunk != nil {
						n++
						st.total += f.chunk.chars
					}
				}
				for _, f := range s.track.fallback {
					if err := run(f); err != nil {
						return err
					}
				}
				return nil
			}
			c.path, c.seg.Text = seg.paths[s.track.k], seg.texts[s.track.k]
		} else {
			_, hit, err := st.synthesize(ctx, i, n, c)
			if err != nil {
				return err
			}
			if hit {
				res.Cached++
			}
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
