package services

// Whole dialogue (Production.Dialogue, thread #80): a story's character
// lines are spoken by a dialogue model (catalogue capability "dialogue",
// SoulX-Podcast) a scene at a time, every line hearing the lines before it,
// instead of line by line or per character stretch; narration stays on
// the story's model. One tts.dialogue job speaks a segment: consecutive
// character lines of a chapter (a short narration line between them does
// not end it; a long one does), at most dialogueMaxSpeakers voices,
// dialogueMaxTurns lines and about 90 s of speech. The model writes each
// line to its own file, so nothing is cut: the files go back into the
// chapter as its lines, with the pacing's gaps (and the emotion's, E).
//
// Each speaker is cloned from a reference: a cloned voice's own clip and
// transcript, else the voice says the portable line (about 10 s) on the
// story's model first, evened out to −18 LUFS (a "portable sample", as
// voice_portable.go makes them). Each line is then checked: the recognizer
// must hear it (error rate ≤ cutMaxCER) and it must last a plausible time
// for its text (no runaway "singing", no cut-off end). A line that fails is
// made again by the dialogue model with the same context (the lines before
// it, from their codes) and another seed; failing twice, it is made on the
// story's model, as are all the segment's lines when the dialogue model
// fails (its character tracks when they apply, else line by line). The
// files and failures are cached by the segment's text and recipe: editing a
// line makes its segment again.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp/loudness"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/text"
	"github.com/hehehai/AudioSmith/internal/text/plan"
)

// Segment limits, and how long a narration line may be without ending the
// segment around it.
const (
	dialogueMaxSpeakers = 4
	dialogueMaxTurns    = 16
	dialogueMaxCJK      = 400 // ≈ 90 s of Chinese
	dialogueMaxChars    = 1200
	dialogueAsideCJK    = 24
	dialogueAsideChars  = 80
	dialogueVersion     = "dlg1"
	// A line lasts within dialogueMinRatio–dialogueMaxRatio of text.Estimate.
	dialogueMinRatio = 0.3
	dialogueMaxRatio = 3.0
	// dialogueRefLUFS is the references' loudness.
	dialogueRefLUFS = -18
)

// PortableLine is what a voice says to become a reference clip (about
// 10 s), in its language: varied sounds, calm, without pauses.
func PortableLine(lang string) string {
	if strings.HasPrefix(lang, "zh") {
		return "这是一段用来克隆音色的参考录音。我会用平稳自然的语气，把这几句话清楚地读完，让每个字的声音都能被听见。"
	}
	return "This is a reference recording for cloning a voice. I will read these few sentences in a calm and natural way, so that every sound comes through clearly."
}

// dialogueRef is a speaker's reference: a clip and its exact transcript,
// or a chunk of the story's model to render first.
type dialogueRef struct {
	audio, text string
	chunk       *renderChunk // the portable line on the story's model; nil: audio as it is
	key         string       // identifies the clip for the segment's key
}

// dialogueSeg is one segment: its lines (chapter indices), each line's
// speaker and text, the references, and where the lines' files go.
type dialogueSeg struct {
	lines    []int
	speakers []int    // per line: index in refs
	texts    []string // per line: what it says (planned: lexicon, numbers)
	tagged   []string // per line: what the model is given (DialogueTags)
	refs     []dialogueRef
	voices   []string // the refs' voice keys
	key      string
	paths    []string
	seed     int64
	lang     string
	// made: done (or tried); err: the segment could not be made; failed:
	// lines made on the story's model instead; redone: lines made twice.
	made   bool
	err    error
	failed map[int]bool
	redone int
}

// dialogueStep is line k of a segment, with the steps that make it on the
// story's model.
type dialogueStep struct {
	seg      *dialogueSeg
	k        int
	chunk    *renderChunk // a stand-in for the progress: the line's text
	fallback []chapterStep
}

// dialogueLine reports whether line l can be a turn: one span of a
// character, with speech and no pause markup.
func dialogueLine(l LineSpec) bool {
	return len(l.Spans) == 1 && l.Character != "" && hasSpeech(l.Spans[0].Text) &&
		!strings.Contains(strings.ToLower(l.Spans[0].Text), "[pause")
}

// dialogueAside reports whether line l may sit inside a segment without
// ending it: a short narration line (or one without speech).
func dialogueAside(l LineSpec) bool {
	if l.Character != "" {
		return false
	}
	w, c := 0, 0
	for _, s := range l.Spans {
		sw, sc := text.Count(s.Text)
		w, c = w+sw, c+sc
	}
	return c <= dialogueAsideCJK && w*6 <= dialogueAsideChars
}

// dialogueGroups are a chapter's segments: runs of character lines,
// within the limits.
func dialogueGroups(lines []LineSpec) [][]int {
	var out [][]int
	var cur []int
	voices := map[string]bool{}
	cjk, rest := 0, 0
	flush := func() {
		if len(cur) > 0 {
			out = append(out, cur)
		}
		cur, voices, cjk, rest = nil, map[string]bool{}, 0, 0
	}
	for i, l := range lines {
		if !dialogueLine(l) {
			if !dialogueAside(l) {
				flush()
			}
			continue
		}
		v := l.Spans[0].Voice
		w, c := text.Count(l.Spans[0].Text)
		if len(cur) >= dialogueMaxTurns || cjk+c > dialogueMaxCJK || rest+w*6 > dialogueMaxChars ||
			!voices[v] && len(voices) >= dialogueMaxSpeakers {
			flush()
		}
		cur = append(cur, i)
		voices[v] = true
		cjk, rest = cjk+c, rest+w*6
	}
	flush()
	return out
}

// dialogueTag is the non-verbal tag a line's emotion becomes when
// Production.DialogueTags puts it into the dialogue model's text (an
// experiment; the words never change): laughter for clear joy, a sigh for
// clear sadness.
func dialogueTag(l LineSpec) string {
	if l.Intensity < IntensityMedium {
		return ""
	}
	switch l.Emotion {
	case EmotionHappy, EmotionExcited:
		return "<|laughter|>"
	case EmotionSad, EmotionHurt, EmotionApologetic:
		return "<|sigh|>"
	}
	return ""
}

// dialogues plans the chapter's segments: by line, its step. lineSteps
// gives each line's own steps (its fallback) unless fallback has a step
// for it (its character track).
func (cr chapterRenderer) dialogues(p *ChapterParams,
	take func(int, SpanSpec, string) (core.Take, *int64, map[string]string, error), planOf func(core.Take, string) plan.Plan,
	lineSteps func(int, io.Writer) ([]chapterStep, error), fallback map[int]*trackStep, info ModelInfo, modelPath string,
	dialogueInfo ModelInfo) (map[int]*dialogueStep, error) {
	out := map[int]*dialogueStep{}
	r := cr.r
	refs := map[string]int{} // voice key → index in a segment's refs
	for gi, g := range dialogueGroups(p.Lines) {
		seg := &dialogueSeg{lines: g, failed: map[int]bool{}, seed: p.Seed + int64(1000*p.Index+gi)}
		seg.lang = p.Lang
		clear(refs)
		h := sha256.New()
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%d\x00%v\x00", dialogueVersion, p.Production.DialogueModel, dialogueInfo.Revision, seg.seed, p.Production.DialogueTags)
		for _, li := range g {
			l := p.Lines[li]
			sp := l.Spans[0]
			k, ok := refs[sp.Voice]
			if !ok {
				ref, err := cr.dialogueRef(p, li, take, planOf, info, modelPath)
				if err != nil {
					return nil, err
				}
				k = len(seg.refs)
				refs[sp.Voice] = k
				seg.refs = append(seg.refs, ref)
				seg.voices = append(seg.voices, sp.Voice)
				fmt.Fprintf(h, "ref %s\x00%s\x00", ref.key, ref.text)
			}
			t, _, _, err := take(li, sp, sp.Text)
			if err != nil {
				return nil, err
			}
			say := spokenText(planOf(t, sp.Text))
			if say == "" {
				say = stripTags(sp.Text)
			}
			if seg.lang == "" {
				seg.lang = "en"
				if cjkShare(say) >= 0.3 {
					seg.lang = "zh"
				}
			}
			tagged := say
			if p.Production.DialogueTags {
				tagged = dialogueTag(l) + say
			}
			seg.speakers = append(seg.speakers, k)
			seg.texts = append(seg.texts, say)
			seg.tagged = append(seg.tagged, tagged)
			fmt.Fprintf(h, "turn %d\x00%s\x00", k, tagged)
		}
		seg.key = hex.EncodeToString(h.Sum(nil))[:32]
		for k, li := range g {
			seg.paths = append(seg.paths, filepath.Join(r.longformChunkDir(), fmt.Sprintf("%s-%s-%d.wav", seg.key, dialogueVersion, k)))
			var fb []chapterStep
			if ts := fallback[li]; ts != nil {
				fb = []chapterStep{{chunk: ts.chunk, brk: ts.seg.brk, line: li, track: ts}}
			} else {
				steps, err := lineSteps(li, io.Discard)
				if err != nil {
					return nil, err
				}
				fb = steps
			}
			w, c := text.Count(seg.texts[k])
			stand := renderChunk{key: seg.key, path: seg.paths[k], chars: max(c+w*5, 1), seg: plan.Segment{Text: seg.texts[k]}}
			out[li] = &dialogueStep{seg: seg, k: k, chunk: &stand, fallback: fb}
		}
	}
	return out, nil
}

// spokenText is what a plan says, its speech segments joined.
func spokenText(pl plan.Plan) string {
	var parts []string
	for _, s := range pl.Segments {
		if s.Kind != plan.Silence && strings.TrimSpace(s.Text) != "" {
			parts = append(parts, strings.TrimSpace(s.Text))
		}
	}
	return joinTurn(parts)
}

// dialogueRef is the reference of line li's voice: a cloned voice's clip
// and transcript, else the portable line on the story's model.
func (cr chapterRenderer) dialogueRef(p *ChapterParams, li int,
	take func(int, SpanSpec, string) (core.Take, *int64, map[string]string, error), planOf func(core.Take, string) plan.Plan,
	info ModelInfo, modelPath string) (dialogueRef, error) {
	sp := p.Lines[li].Spans[0]
	v := p.Voices[sp.Voice]
	if v.RefAudio != "" && strings.TrimSpace(v.RefText) != "" {
		key := v.RefAudio
		if fi, err := os.Stat(v.RefAudio); err == nil {
			key = fmt.Sprintf("%s|%d|%d", v.RefAudio, fi.Size(), fi.ModTime().Unix())
		}
		return dialogueRef{audio: v.RefAudio, text: strings.TrimSpace(v.RefText), key: key}, nil
	}
	lang := p.Lang
	if lang == "" {
		lang = info.Lang(v.Language, v.Voice)
	}
	line := PortableLine(lang)
	// The voice in its plain delivery: no emotion, scene or instruction.
	saved, scene := p.Lines[li], p.Scene
	p.Lines[li].Emotion, p.Lines[li].Intensity, p.Lines[li].Direction, p.Lines[li].Persona, p.Lines[li].Speed = "", 0, "", "", 0
	p.Lines[li].Repeat, p.Scene = 0, ""
	t, seed, _, err := take(li, sp, line)
	p.Lines[li], p.Scene = saved, scene
	if err != nil {
		return dialogueRef{}, err
	}
	pl := planOf(t, line)
	chunks, err := cr.r.chunks(&RenderParams{Take: t, Workflow: p.Workflow}, &pl, info, modelPath, seed, cr.r.longformChunkDir(), nil)
	if err != nil {
		return dialogueRef{}, &jobs.Error{Code: CodeRenderFailed, Message: err.Error()}
	}
	if len(chunks) != 1 {
		return dialogueRef{}, &jobs.Error{Code: CodeRenderFailed, Message: "the reference line does not fit one chunk"}
	}
	c := chunks[0]
	return dialogueRef{audio: strings.TrimSuffix(c.path, ".wav") + "-ref.wav", text: line, chunk: &c, key: c.key}, nil
}

// errDialogueLength: a line far shorter or longer than its text.
var errDialogueLength = errors.New("the line is far shorter or longer than its text")

// dialogueRun makes a segment's lines (or finds them in the cache): the
// references, one tts.dialogue, each line checked and redone once.
func (cr chapterRenderer) dialogueRun(ctx context.Context, st *renderState, i, n int, seg *dialogueSeg, model, modelPath string) error {
	r := cr.r
	cached := true
	for k, p := range seg.paths {
		if _, err := os.ReadFile(p + ".fail"); err == nil {
			seg.failed[k] = true
			continue
		}
		if fi, err := os.Stat(p); err != nil || fi.Size() <= 44 {
			cached = false
		}
	}
	if cached {
		return nil
	}
	if b, err := os.ReadFile(seg.paths[0] + ".segfail"); err == nil {
		return errors.New(strings.TrimSpace(string(b)))
	}
	// The references.
	speakers := make([]proto.DialogueSpeaker, len(seg.refs))
	for k, ref := range seg.refs {
		if ref.chunk != nil {
			if _, _, err := st.synthesize(ctx, i, n, *ref.chunk); err != nil {
				return err
			}
			if err := levelReference(ref.chunk.path, ref.audio); err != nil {
				return &jobs.Error{Code: CodeRenderFailed, Message: err.Error()}
			}
		}
		speakers[k] = proto.DialogueSpeaker{ID: fmt.Sprintf("s%d", k), RefAudio: ref.audio, RefText: ref.text}
	}
	turns := make([]proto.DialogueTurn, len(seg.lines))
	for k := range seg.lines {
		turns[k] = proto.DialogueTurn{Speaker: speakers[seg.speakers[k]].ID, Text: seg.tagged[k]}
	}
	dir := filepath.Join(r.tmpDir(), st.job.ID+"-"+seg.key)
	defer os.RemoveAll(dir)
	seed := seg.seed
	params := proto.DialogueParams{Speakers: speakers, Turns: turns, OutputDir: dir, Bits: 32, Seed: &seed, ModelPath: modelPath}
	var res proto.DialogueResult
	chars := 0
	for k := range seg.texts {
		w, c := text.Count(seg.texts[k])
		chars += max(c+w*5, 1)
	}
	base := st.seconds()
	progress := func(pr jobs.Progress) {
		stage, fraction := "generating", -1.0
		if pr.Stage == "loading" {
			stage = "loading"
		}
		if st.total > 0 {
			fraction = min((float64(st.done)+max(pr.Fraction, 0)*float64(chars))/float64(st.total), 1)
		}
		st.report(jobs.Progress{Stage: stage, Fraction: fraction, AudioSeconds: base + pr.AudioSeconds,
			Message: fmt.Sprintf("dialogue, %d lines", len(seg.lines))})
	}
	defer func() { st.done += chars }()
	if err := r.callProgress(ctx, st.job, proto.MethodTTSDialogue, model, &params, &res, progress); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var je *jobs.Error
		if errors.As(err, &je) && (je.Code == proto.CodeModelNotDownloaded || je.Code == proto.CodeLicenceRequired) {
			return err
		}
		os.WriteFile(seg.paths[0]+".segfail", []byte(err.Error()), 0o644)
		return err
	}
	codes := make([][]int, len(seg.lines))
	files := make([]string, len(seg.lines))
	for _, t := range res.Turns {
		if t.Index >= 0 && t.Index < len(seg.lines) {
			codes[t.Index], files[t.Index] = t.Codes, t.Output
		}
	}
	for k := range seg.lines {
		if files[k] == "" {
			return &jobs.Error{Code: CodeRenderFailed, Message: fmt.Sprintf("the dialogue model returned no line %d", k+1)}
		}
		err := cr.dialogueCheck(ctx, st, seg, k, files[k])
		if err != nil && ctx.Err() == nil {
			// Once more with the same context and another seed.
			r.logf("chapter %s: dialogue line %d: %v; making it again", st.job.ID, k+1, err)
			seg.redone++
			redo := params
			redo.Turns = slices.Clone(turns[:k+1])
			for j := range k {
				redo.Turns[j].Codes = codes[j]
			}
			s2 := seed + 7919*int64(k+1)
			redo.Seed = &s2
			var res2 proto.DialogueResult
			if err2 := r.callProgress(ctx, st.job, proto.MethodTTSDialogue, model, &redo, &res2, nil); err2 == nil && len(res2.Turns) == 1 {
				files[k], codes[k] = res2.Turns[0].Output, res2.Turns[0].Codes
				err = cr.dialogueCheck(ctx, st, seg, k, files[k])
			} else if err2 != nil {
				err = err2
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

// dialogueCheck checks line k's file: a plausible length for its text, and
// the recognizer hears it.
func (cr chapterRenderer) dialogueCheck(ctx context.Context, st *renderState, seg *dialogueSeg, k int, path string) error {
	r := cr.r
	buf, _, err := wav.ReadFile(path)
	if err != nil {
		return err
	}
	w, c := text.Count(seg.texts[k])
	want := text.Estimate(w, c).Seconds()
	if got := buf.Seconds(); want > 0 && (got < dialogueMinRatio*want || got > dialogueMaxRatio*want) {
		return fmt.Errorf("%.1f s for %.1f s of text: %w", got, want, errDialogueLength)
	}
	if r.recognizer == "" {
		return nil
	}
	var tr proto.TranscribeResult
	if err := r.call(ctx, st.job, proto.MethodSTTTranscribe, r.recognizer, &proto.TranscribeParams{Audio: path, Language: seg.lang}, &tr); err != nil {
		return err
	}
	if d := dub.Drift(foldPronouns(seg.texts[k]), foldPronouns(tr.Text)); d > cutMaxCER {
		return fmt.Errorf("heard %q (error rate %.2f): %w", tr.Text, d, errCutHeard)
	}
	return nil
}

// levelReference writes src evened out to dialogueRefLUFS as dst (once).
func levelReference(src, dst string) error {
	if fi, err := os.Stat(dst); err == nil && fi.Size() > 44 {
		return nil
	}
	b, _, err := wav.ReadFile(src)
	if err != nil {
		return err
	}
	out, _ := loudness.Normalize(b, loudness.Target{ID: "reference", Integrated: dialogueRefLUFS, TruePeak: -1.5}, loudness.NormalizeOptions{})
	tmp := dst + ".tmp.wav"
	if err := wav.WriteFile(tmp, out, wav.Float32, wav.Options{}); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// moveFile renames src to dst, copying across file systems.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(dst, data); err != nil {
		return err
	}
	return os.Remove(src)
}
