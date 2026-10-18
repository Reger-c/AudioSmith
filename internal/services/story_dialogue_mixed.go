package services

// One-track whole dialogue (MOSS-TTSD, thread #82): the model speaks a
// segment's lines as one track (proto.DialogueResult.Mixed), which is cut
// back into the lines as character tracks are (story_track.go): the forced
// aligner times the segment's text, each line must be the text's words at
// a plausible share of the time, and the cut falls at the quietest point
// between two lines. A track the aligner cannot cut (a line skipped,
// repeated or garbled) is made once more with another seed, else the
// segment goes to the story's model. Each cut line then gets the per-line
// checks (its length for its text, the recognizer hears it); a line that
// fails is made again by continuation: the same speakers, the lines
// before it as context (their codec frames, cut from the track's), the
// line itself to speak, another seed. Failing twice, it is made on the
// story's model. The model's own gaps go, as SoulX-Podcast's do: each
// line's edge silence is trimmed and the pacing's gaps are put back.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
)

// mossFrameSamples is a MOSS-TTSD codec frame at 24 kHz (12.5 a second).
const mossFrameSamples = 1920

// dialogueMixed cuts a one-track result into the segment's lines, checks
// them and makes failing lines again (the first call's params and result).
func (cr chapterRenderer) dialogueMixed(ctx context.Context, st *renderState, seg *dialogueSeg, params proto.DialogueParams,
	res proto.DialogueResult, model string) error {
	r := cr.r
	files, cuts, err := cr.cutMixed(ctx, st, seg, res.Mixed.Output)
	if err != nil && ctx.Err() == nil && !isEngineGone(err) {
		// Once more with another seed.
		r.logf("chapter %s: dialogue track: %v; making it again", st.job.ID, err)
		redo := params
		redo.OutputDir = params.OutputDir + "-again"
		defer os.RemoveAll(redo.OutputDir)
		s2 := seg.seed + 7919*int64(len(seg.lines)+1)
		redo.Seed = &s2
		var res2 proto.DialogueResult
		if err = r.callProgress(ctx, st.job, proto.MethodTTSDialogue, model, &redo, &res2, nil); err == nil {
			if res2.Mixed == nil {
				err = fmt.Errorf("the dialogue model returned no track")
			} else {
				seg.redone += len(seg.lines)
				params, res = redo, res2
				files, cuts, err = cr.cutMixed(ctx, st, seg, res.Mixed.Output)
			}
		}
	}
	cleanup := slices.Clone(files)
	defer func() {
		for _, f := range cleanup {
			os.Remove(f)
		}
	}()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		if !isEngineGone(err) {
			os.WriteFile(seg.paths[0]+".segfail", []byte(err.Error()), 0o644)
		}
		return err
	}
	for k := range seg.lines {
		err := cr.dialogueCheck(ctx, st, seg, k, files[k])
		if err != nil && ctx.Err() == nil && res.Mixed.Codebooks > 0 && len(res.Mixed.Codes) > 0 {
			r.logf("chapter %s: dialogue line %d: %v; making it again after the lines before it", st.job.ID, k+1, err)
			seg.redone++
			var again string
			if again, err = cr.mixedRedo(ctx, st, seg, params, res.Mixed, cuts, k, model); err == nil {
				cleanup = append(cleanup, again)
				files[k] = again
				err = cr.dialogueCheck(ctx, st, seg, k, files[k])
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			r.logf("chapter %s: dialogue line %d: %v; making it on the story's model", st.job.ID, k+1, err)
			seg.failed[k] = true
			os.WriteFile(seg.paths[k]+".fail", []byte(err.Error()), 0o644)
			continue
		}
		if err := moveFile(files[k], seg.paths[k]); err != nil {
			return &jobs.Error{Code: CodeRenderFailed, Message: err.Error()}
		}
	}
	return nil
}

// isEngineGone reports errors a second try cannot help: no model, no
// licence, no engine.
func isEngineGone(err error) bool {
	var je *jobs.Error
	if !errors.As(err, &je) {
		return false
	}
	return je.Code == proto.CodeModelNotDownloaded || je.Code == proto.CodeLicenceRequired || je.Code == CodeEngineUnavailable
}

// cutMixed cuts the track at path into the segment's lines (temporary
// files next to their cache paths) and returns them with the cuts: the
// sample where each line starts, then the track's end.
func (cr chapterRenderer) cutMixed(ctx context.Context, st *renderState, seg *dialogueSeg, path string) ([]string, []int, error) {
	r := cr.r
	if r.aligner == "" {
		return nil, nil, fmt.Errorf("no forced aligner to cut the dialogue track")
	}
	var al proto.AlignResult
	if err := r.call(ctx, st.job, proto.MethodSTTAlign, r.aligner, &proto.AlignParams{Audio: path, Text: joinTurn(seg.texts), Language: seg.lang}, &al); err != nil {
		return nil, nil, err
	}
	rg, err := trackRanges(al.Words, seg.texts)
	if err != nil {
		return nil, nil, err
	}
	buf, _, err := wav.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	cuts := []int{0}
	for k := 1; k < len(rg); k++ {
		cuts = append(cuts, quietSplit(buf, rg[k-1][1], rg[k][0]))
	}
	cuts = append(cuts, buf.Frames())
	var files []string
	for k := range seg.lines {
		clip := buf.Slice(cuts[k], cuts[k+1]).Clone()
		fadeIn(clip)
		fadeOut(clip)
		f := seg.paths[k] + ".tmp.wav"
		if err := wav.WriteFile(f, clip, wav.Float32, wav.Options{}); err != nil {
			for _, g := range files {
				os.Remove(g)
			}
			return nil, nil, err
		}
		files = append(files, f)
	}
	return files, cuts, nil
}

// mixedFrames are line j's codec frames in a track's codes (cuts: each
// line's first sample): from the frame its cut falls in (the track's
// leading frame for the first line) to the next line's.
func mixedFrames(m *proto.DialogueMixed, cuts []int, j int) []int {
	n := m.Codebooks
	frames := len(m.Codes) / n
	at := func(k int) int {
		if k == 0 {
			return 0
		}
		return min(1+cuts[k]/mossFrameSamples, frames)
	}
	return m.Codes[at(j)*n : at(j+1)*n]
}

// mixedRedo makes line k again after the lines before it (their frames as
// context) with another seed; returns its file.
func (cr chapterRenderer) mixedRedo(ctx context.Context, st *renderState, seg *dialogueSeg, params proto.DialogueParams,
	m *proto.DialogueMixed, cuts []int, k int, model string) (string, error) {
	redo := params
	redo.Turns = slices.Clone(params.Turns[:k+1])
	for j := range k {
		redo.Turns[j].Codes = mixedFrames(m, cuts, j)
		if len(redo.Turns[j].Codes) == 0 {
			return "", fmt.Errorf("line %d has no frames to hear", j+1)
		}
	}
	redo.OutputDir = filepath.Join(params.OutputDir, fmt.Sprintf("line-%d", k))
	s2 := seg.seed + 7919*int64(k+1)
	redo.Seed = &s2
	var res proto.DialogueResult
	if err := cr.r.callProgress(ctx, st.job, proto.MethodTTSDialogue, model, &redo, &res, nil); err != nil {
		return "", err
	}
	if res.Mixed == nil {
		return "", fmt.Errorf("the dialogue model returned no track")
	}
	f := seg.paths[k] + ".again.wav"
	if err := moveFile(res.Mixed.Output, f); err != nil {
		return "", err
	}
	return f, nil
}
