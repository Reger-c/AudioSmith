package services

// Cutting a synthesis of several lines back into its lines (character
// tracks, story_track.go): the aligner's units of a text, the quietest
// point between two lines, short fades, the checks' reasons, and the
// engine requests other than tts.synthesize a render makes (stt.align,
// stt.transcribe).

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
)

// A cut line is kept when the recognizer's error rate on it is at most
// cutMaxCER, and its length within cutMinRatio–cutMaxRatio of what the
// synthesis's pace predicts.
const (
	cutMaxCER   = 0.25
	cutMinRatio = 0.4
	cutMaxRatio = 2.5
)

// alignUnits are a text's words as the aligner counts them, folded:
// unspaced characters one by one, other words whole, no punctuation.
func alignUnits(s string) string {
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
	errCutWords  = errors.New("the aligner's words are not the text's")
	errCutLength = errors.New("the line is far shorter or longer than expected")
	errCutOrder  = errors.New("a line starts before the one before it ends")
	errCutHeard  = errors.New("the recognizer does not hear the line")
)

// quietSplit is the frame where a line's audio begins: the quietest
// 10 ms between the end of the line before and the line's start (widened
// by 30 ms on the side of the line before, where the aligner's ends run
// late).
func quietSplit(b dsp.Buffer, from, to float64) int {
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

// fadeIn fades a cut line in over 10 ms (in place).
func fadeIn(b dsp.Buffer) {
	n := min(max(b.Rate/100, 1), b.Frames())
	for f := range n {
		g := float32(f+1) / float32(n+1)
		for c := range b.Channels {
			b.Samples[f*b.Channels+c] *= g
		}
	}
}

// foldPronouns folds the third-person pronouns, which sound the same.
func foldPronouns(s string) string {
	return strings.NewReplacer("她", "他", "它", "他").Replace(stripTags(s))
}

// call runs one engine request other than tts.synthesize for a render
// (the tracks' alignment and check), with the model's local snapshot.
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
