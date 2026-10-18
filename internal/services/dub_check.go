package services

// The synthesize stage's language check (thread #90): a line cloned from
// a clip in another language can come out in the clip's language, or
// garbled. Such lines are heard again (speech to text in the line's
// language, dub.CheckSpoken); one that fails is made once more another
// way (from the clip alone when its transcript was given, else another
// seed), the better take is kept, and a line still failing is flagged as
// a conversation's fallback is (Render.Flag and Heard, the stage warning
// DubWarnLinesFlagged), never kept silently.

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
)

// errNoRecognizer: no speech-to-text model to check a line with.
var errNoRecognizer = errors.New("dub: no speech-to-text model for the check")

// hearLine transcribes a made line (path) in its language and measures it
// against text (dub.CheckSpoken). Shared by the dub's checks: the
// per-segment lines here, dialogue lines and their fallbacks.
func (s *Dubs) hearLine(ctx context.Context, sc *stageCtx, path, text, lang string) (dub.Spoken, error) {
	l := baseLang(lang)
	stt, _ := s.cfg.Models().STTFor(l)
	if stt == "" {
		return dub.Spoken{}, errNoRecognizer
	}
	tp := proto.TranscribeParams{Model: stt, Audio: path, Language: l}
	var tr proto.TranscribeResult
	if err := sc.call(ctx, proto.MethodSTTTranscribe, stt, &tp, &tp.ModelPath, &tr, nil); err != nil {
		return dub.Spoken{}, err
	}
	return dub.CheckSpoken(lang, text, tr.Text), nil
}

// checkedLine is a line the synthesize stage made this run and checks.
type checkedLine struct {
	i    int
	key  string
	take core.Take
}

// needsCheck reports whether a line made with take t is checked: a clone
// speaking another language than its speaker's clip.
func needsCheck(p *dub.Project, seg dub.Segment, l string, t core.Take) bool {
	return (t.RefAudio != "" || t.VoicePrompt != "") && p.CrossLingual(seg, l)
}

// retryTake is how a line that failed the check is made once more: from
// the clip alone when its transcript was given (and the model can),
// else with another seed.
func retryTake(t core.Take, caps DubModelCaps) core.Take {
	if t.RefAudio != "" && t.RefText != "" && caps.CloneAudioOnly {
		t.RefText = ""
		return t
	}
	t.Seed = t.Seed*31 + 7
	if t.Render != nil {
		r := *t.Render
		r.SeedPinned = true
		t.Render = &r
	}
	return t
}

// checkLines checks the lines made this run (rendered, by segment id),
// makes the failing ones once more and keeps the better take. It stops
// checking, keeping what was made, when the recognizer cannot run.
func (s *Dubs) checkLines(ctx context.Context, sc *stageCtx, l, dir string, caps DubModelCaps, lines []checkedLine, rendered map[string]dub.Render) error {
	p := sc.p
	heard := map[string]dub.Spoken{}
	var again []checkedLine
	for n, c := range lines {
		seg := p.Segments[c.i]
		r := rendered[seg.ID]
		if r.Err != "" || r.File == "" {
			continue
		}
		sc.progress("transcribing", float64(n)/float64(len(lines)), fmt.Sprintf("%d/%d", n+1, len(lines)))
		sp, err := s.hearLine(ctx, sc, sc.path(r.File), c.take.Text, dub.LangFor(seg, l))
		if err != nil {
			return s.uncheckable(ctx, err)
		}
		heard[seg.ID] = sp
		if !sp.OK() {
			again = append(again, c)
		}
	}
	for n, c := range again {
		seg := p.Segments[c.i]
		first := rendered[seg.ID]
		sc.progress("generating", float64(n)/float64(len(again)), fmt.Sprintf("%d/%d", n+1, len(again)))
		aside := sc.path(first.File) + ".first"
		if err := os.Rename(sc.path(first.File), aside); err != nil {
			return err
		}
		keepFirst := func() error { return os.Rename(aside, sc.path(first.File)) }
		r, err := s.renderLine(ctx, sc, p, c.i, l, c.key, retryTake(c.take, caps), dir)
		if err != nil {
			if err := keepFirst(); err != nil {
				return err
			}
			if ctx.Err() != nil || fatal(err) {
				return err
			}
			continue
		}
		sp, err := s.hearLine(ctx, sc, sc.path(r.File), c.take.Text, dub.LangFor(seg, l))
		if err != nil {
			if err := keepFirst(); err != nil {
				return err
			}
			return s.uncheckable(ctx, err)
		}
		if sp.Worse(heard[seg.ID]) {
			if r.File != first.File {
				os.Remove(sc.path(r.File))
				os.Remove(sc.path(r.File) + ".txt")
			}
			if err := keepFirst(); err != nil {
				return err
			}
			continue
		}
		os.Remove(aside)
		heard[seg.ID] = sp
		rendered[seg.ID] = r
	}
	for id, sp := range heard {
		if f := sp.Flag(); f != "" {
			r := rendered[id]
			r.Flag, r.Heard = f, sp.Heard
			rendered[id] = r
			sc.warn(DubWarnLinesFlagged)
		}
	}
	return nil
}

// uncheckable ends the check when the recognizer cannot run (none, not
// downloaded, no engine): the lines stay as made, unchecked. A cancelled
// stage stops.
func (s *Dubs) uncheckable(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	s.cfg.Logf("dub: line check skipped: %v", err)
	return nil
}

// has reports whether line id, made on the dub's model, is checked as a
// conversation's fallback (dubDialogue.mark): it is in one of the
// track's conversations and the dialogue model was there to compare with
// (nil: no dialogue model).
func (dd *dubDialogue) has(id string) bool {
	if dd == nil {
		return false
	}
	_, ok := dd.byID[id]
	return ok && dd.reasons[id] != DialogueUnavailable
}
