package services

// The steps of a workflow run: each job takes its step's items one after
// another, reads each item's input from the step before it (the branch a
// condition took), makes the result and saves it with its key.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/loudness"
	"github.com/hehehai/AudioSmith/internal/dsp/resample"
	"github.com/hehehai/AudioSmith/internal/dsp/stretch"
	"github.com/hehehai/AudioSmith/internal/dsp/wav"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/text"
	"github.com/hehehai/AudioSmith/internal/translate"
	"github.com/hehehai/AudioSmith/internal/workflow"
)

// wfStep is one step's job at work.
type wfStep struct {
	s      *Workflows
	job    jobs.Job
	report func(jobs.Progress)
	w      *workflow.Workflow
	n      *workflow.Node
	runID  string
	in     workflow.Type // the type its input receives
}

func (st *wfStep) dir() string { return st.s.Dir(st.w.ID) }

// out is a result file's name for an item, relative to the folder.
func (st *wfStep) out(item, ext string) string { return "out/" + item + "/" + st.n.ID + "." + ext }

func (st *wfStep) path(name string) string { return st.s.Path(st.w.ID, name) }

// step runs a workflow.step job.
func (s *Workflows) step(ctx context.Context, job jobs.Job, report func(jobs.Progress), _ func(json.RawMessage)) (json.RawMessage, error) {
	var p workflowStepParams
	if err := json.Unmarshal(job.Params, &p); err != nil || p.Workflow == "" || p.Node == "" {
		return nil, &jobs.Error{Code: proto.CodeBadRequest, Message: "workflow.step: bad params"}
	}
	w, run, err := s.Get(ctx, p.Workflow)
	if err != nil {
		return nil, err
	}
	if run == nil || run.ID != p.Run || run.State != workflow.RunRunning {
		return json.RawMessage(`{"stale":true}`), nil // a run since replaced or stopped
	}
	n := w.Node(p.Node)
	if n == nil {
		return nil, &jobs.Error{Code: proto.CodeBadRequest, Message: "workflow.step: no step " + p.Node}
	}
	rep := workflow.Validate(w, nil)
	st := &wfStep{s: s, job: job, report: report, w: w, n: n, runID: p.Run, in: rep.In[n.ID]}
	total := len(run.Items)
	for i := range run.Items {
		item := run.Items[i]
		cur := item.Steps[n.ID]
		if cur == nil || cur.Finished() || cur.Status == workflow.StepFailed {
			// Done, or failed in an earlier attempt of this job (a retry).
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		report(jobs.Progress{Stage: string(n.Kind), Fraction: float64(i) / float64(total), Message: fmt.Sprintf("%d/%d", i+1, total)})
		// The item's input, from the run as saved now.
		_, latest, err := s.Get(ctx, w.ID)
		if err != nil {
			return nil, err
		}
		if latest == nil || latest.ID != p.Run {
			return json.RawMessage(`{"stale":true}`), nil
		}
		ri := latest.Item(item.Key)
		if ri == nil {
			continue
		}
		in, skip := wfInput(w, ri, n.ID)
		if skip != "" {
			st.save(ctx, item.Key, func(x *workflow.Step) { x.Status, x.Code, x.Out = workflow.StepSkipped, skip, nil })
			continue
		}
		if n.Kind.IsInput() {
			in = &workflow.Value{Type: workflow.TypeAudio, File: ri.Path}
		}
		if in == nil {
			st.save(ctx, item.Key, func(x *workflow.Step) {
				x.Status, x.Code, x.Err = workflow.StepFailed, WorkflowCodeNoInput, "no input"
			})
			continue
		}
		st.save(ctx, item.Key, func(x *workflow.Step) { x.Status, x.Code, x.Err, x.Warnings = workflow.StepRunning, "", "", nil })
		started := time.Now()
		res, err := st.run(ctx, ri, i, in)
		if err != nil {
			if ctx.Err() != nil {
				st.save(context.WithoutCancel(ctx), item.Key, func(x *workflow.Step) { x.Status = workflow.StepPending })
				return nil, ctx.Err()
			}
			var je *jobs.Error
			if !errors.As(err, &je) {
				je = &jobs.Error{Code: WorkflowCodeFile, Message: err.Error()}
			}
			st.save(ctx, item.Key, func(x *workflow.Step) {
				x.Status, x.Code, x.Err, x.Seconds, x.At = workflow.StepFailed, je.Code, je.Message, time.Since(started).Seconds(), s.cfg.Now()
			})
			if wfFatal(je.Code) {
				// The step cannot work at all: the run stops (the steps
				// after this one fail with the job).
				s.update(context.WithoutCancel(ctx), w.ID, p.Run, func(r *workflow.Run) { r.Code, r.Err = je.Code, je.Message })
				return nil, je
			}
			continue
		}
		st.save(ctx, item.Key, func(x *workflow.Step) {
			x.Status, x.Out, x.Branch, x.Warnings = workflow.StepDone, res.out, res.branch, res.warnings
			x.Code, x.Err, x.Seconds, x.At = "", "", time.Since(started).Seconds(), s.cfg.Now()
		})
	}
	report(jobs.Progress{Stage: string(n.Kind), Fraction: 1})
	return json.RawMessage(`{}`), nil
}

// wfFatal reports whether an error stops the run: every other item would
// fail the same way.
func wfFatal(code string) bool {
	switch code {
	case proto.CodeModelNotDownloaded, proto.CodeLicenceRequired, CodeEngineUnavailable, proto.CodeOOM, proto.CodeModelLoadFailed,
		jobs.CodeInterrupted, jobs.CodeShutdown, proto.CodeCancelled, proto.CodeMethodNotFound, WorkflowCodeNoSTT, WorkflowCodeEncode,
		translate.CodeNoLLM, translate.CodeNoKey, translate.CodeAuth, translate.CodeQuota, translate.CodeConfig:
		return true
	}
	return false
}

// wfInput is an item's input at step id: the value of the first link in
// whose step is done (a condition's on the branch it took), or why the
// step is skipped.
func wfInput(w *workflow.Workflow, ri *workflow.RunItem, id string) (*workflow.Value, string) {
	in := w.Incoming(id)
	if len(in) == 0 {
		return nil, ""
	}
	skip := workflow.SkipBranch
	for _, e := range in {
		src := ri.Steps[e.From]
		if src == nil {
			continue
		}
		switch src.Status {
		case workflow.StepDone:
			if w.Node(e.From).Kind == workflow.KindCondition && src.Branch != e.FromPort {
				continue
			}
			return src.Out, ""
		case workflow.StepFailed:
			skip = workflow.SkipUpstream
		case workflow.StepSkipped:
			if src.Code == workflow.SkipUpstream {
				skip = workflow.SkipUpstream
			}
		default:
			return nil, "" // not made: the job before this one stopped
		}
	}
	return nil, skip
}

// save applies fn to the item's step in the saved run.
func (st *wfStep) save(ctx context.Context, item string, fn func(*workflow.Step)) {
	_, err := st.s.update(ctx, st.w.ID, st.runID, func(r *workflow.Run) {
		if ri := r.Item(item); ri != nil {
			if x := ri.Steps[st.n.ID]; x != nil {
				fn(x)
			}
		}
	})
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		st.s.cfg.Logf("workflow %s: saving step %s of %s: %v", st.w.ID, st.n.ID, item, err)
	}
}

type wfResult struct {
	out      *workflow.Value
	branch   string
	warnings []string
}

// run makes one item's result.
func (st *wfStep) run(ctx context.Context, ri *workflow.RunItem, index int, in *workflow.Value) (wfResult, error) {
	switch st.n.Kind {
	case workflow.KindSource:
		return st.source(ctx, ri, in)
	case workflow.KindSpeak:
		return st.speak(ctx, ri, in)
	case workflow.KindTranscribe:
		return st.transcribe(ctx, ri, in)
	case workflow.KindTranslate:
		return st.translate(ctx, ri, in)
	case workflow.KindConvert:
		return st.convert(ctx, ri, in)
	case workflow.KindNormalize:
		return st.normalize(ctx, ri, in)
	case workflow.KindCondition:
		txt, err := st.readText(in)
		if err != nil {
			return wfResult{}, err
		}
		b := workflow.PortNo
		if workflow.Evaluate(st.n.P.Match, st.n.P.Text, txt) {
			b = workflow.PortYes
		}
		out := *in
		return wfResult{out: &out, branch: b}, nil
	case workflow.KindExport:
		return st.export(ctx, ri, index, in)
	}
	return wfResult{}, &jobs.Error{Code: proto.CodeBadRequest, Message: "unknown step " + string(st.n.Kind)}
}

func wfErr(code, format string, args ...any) error {
	return &jobs.Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// --- values ---------------------------------------------------------------------------

// readText reads a text value, or a transcript's text.
func (st *wfStep) readText(v *workflow.Value) (string, error) {
	data, err := os.ReadFile(st.path(v.File))
	if err != nil {
		return "", wfErr(WorkflowCodeFile, "%v", err)
	}
	if v.Type == workflow.TypeTranscript {
		var t core.Transcript
		if err := json.Unmarshal(data, &t); err != nil {
			return "", wfErr(WorkflowCodeFile, "transcript: %v", err)
		}
		return t.Preferred(), nil
	}
	return string(data), nil
}

func (st *wfStep) readTranscript(v *workflow.Value) (core.Transcript, error) {
	var t core.Transcript
	data, err := os.ReadFile(st.path(v.File))
	if err != nil {
		return t, wfErr(WorkflowCodeFile, "%v", err)
	}
	if err := json.Unmarshal(data, &t); err != nil {
		return t, wfErr(WorkflowCodeFile, "transcript: %v", err)
	}
	return t, nil
}

// textValue writes a text result.
func (st *wfStep) textValue(item, s, lang string) (*workflow.Value, error) {
	name := st.out(item, "txt")
	if err := wfWriteFile(st.path(name), []byte(s)); err != nil {
		return nil, err
	}
	return &workflow.Value{Type: workflow.TypeText, File: name, Text: workflow.Excerpt(s, 400), Chars: utf8.RuneCountInString(s), Lang: lang}, nil
}

// transcriptValue writes a transcript result.
func (st *wfStep) transcriptValue(item string, t core.Transcript) (*workflow.Value, error) {
	name := st.out(item, "json")
	data, err := json.Marshal(t)
	if err != nil {
		return nil, err
	}
	if err := wfWriteFile(st.path(name), data); err != nil {
		return nil, err
	}
	txt := t.Preferred()
	return &workflow.Value{Type: workflow.TypeTranscript, File: name, Text: workflow.Excerpt(txt, 400), Chars: utf8.RuneCountInString(txt),
		Seconds: round3(t.Duration), Lang: t.Language}, nil
}

// audioValue describes an audio result.
func (st *wfStep) audioValue(name string) (*workflow.Value, error) {
	f, err := os.Open(st.path(name))
	if err != nil {
		return nil, wfErr(WorkflowCodeFile, "%v", err)
	}
	defer f.Close()
	rd, err := wav.NewReader(f)
	if err != nil {
		return nil, wfErr(WorkflowCodeFile, "%v", err)
	}
	fm := rd.Format()
	secs := 0.0
	if fm.Rate > 0 {
		secs = float64(rd.Frames()) / float64(fm.Rate)
	}
	if secs <= 0 {
		return nil, wfErr(WorkflowCodeEmpty, "The audio is empty.")
	}
	return &workflow.Value{Type: workflow.TypeAudio, File: name, Seconds: round3(secs)}, nil
}

// --- engine calls -------------------------------------------------------------------------

// call runs one engine method inside the job (as Transcripts.call).
func (st *wfStep) call(ctx context.Context, method, model string, params any, modelPath *string, out any, report func(jobs.Progress)) error {
	s := st.s
	if model != "" && modelPath != nil && s.cfg.ModelPath != nil {
		dir, err := s.cfg.ModelPath(ctx, model)
		if err != nil {
			if errors.Is(err, core.ErrNotDownloaded) {
				data, _ := json.Marshal(map[string]string{"model": model})
				return &jobs.Error{Code: proto.CodeModelNotDownloaded, Message: err.Error(), Data: data}
			}
			return &jobs.Error{Code: proto.CodeInternal, Message: err.Error()}
		}
		*modelPath = dir
	}
	var ex jobs.Executor
	if s.cfg.Engine != nil {
		ex = s.cfg.Engine(method)
	}
	if ex == nil {
		return &jobs.Error{Code: CodeEngineUnavailable, Message: "The engine is not connected.", Retryable: true}
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	if report == nil {
		report = func(jobs.Progress) {}
	}
	role, m := proto.SplitKind(method)
	sub := jobs.Job{ID: st.job.ID + "." + m, Spec: jobs.Spec{Kind: method, Lane: jobs.Lane(role), Params: raw, Owner: st.job.Owner}}
	res, err := ex.Run(ctx, sub, report, nil)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return json.Unmarshal(res, out)
}

// decode writes an audio file as a WAV at rate and channels (0 keeps
// the source's): in Go for WAV files, else with the engine.
func (st *wfStep) decode(ctx context.Context, src, dst string, rate, ch int) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if strings.EqualFold(filepath.Ext(src), ".wav") {
		b, _, err := wav.ReadFile(src)
		if err == nil {
			if rate > 0 || ch > 0 {
				b = resample.Convert(b, dubFirstInt(rate, b.Rate), dubFirstInt(ch, b.Channels), resample.Good)
			}
			return writeWAVFloat(dst, b)
		}
		if os.IsNotExist(err) {
			return wfErr(proto.CodeFileNotFound, "%v", err)
		}
	}
	if _, err := os.Stat(src); err != nil {
		return wfErr(proto.CodeFileNotFound, "%v", err)
	}
	p := proto.DecodeParams{Input: src, Output: proto.Output{Path: dst, Format: "wav", Bits: 16}, SampleRate: rate, Channels: ch}
	var dr proto.DecodeResult
	return st.call(ctx, proto.MethodMediaDecode, "", &p, nil, &dr, nil)
}

func dubFirstInt(a, b int) int {
	if a > 0 {
		return a
	}
	return b
}

// --- the steps ------------------------------------------------------------------------------

// source reads a clip into the run's folder as a WAV.
func (st *wfStep) source(ctx context.Context, ri *workflow.RunItem, in *workflow.Value) (wfResult, error) {
	if in.File == "" {
		return wfResult{}, wfErr(WorkflowCodeNoInput, "no clip")
	}
	name := st.out(ri.Key, "wav")
	if err := st.decode(ctx, in.File, st.path(name), 0, 0); err != nil {
		return wfResult{}, err
	}
	v, err := st.audioValue(name)
	return wfResult{out: v}, err
}

// recipe is the take a Speak or Convert step renders for text.
func (st *wfStep) recipe(ctx context.Context, txt, lang string) (core.Take, error) {
	p := st.n.P
	caps := st.s.cfg.Caps(p.Model)
	t := core.Take{Text: txt, Model: p.Model, Render: &core.TakeRender{Bits: 32}}
	if lang != "" {
		t.Language = langValue(caps, lang)
	}
	if p.Voice != "" && st.s.cfg.Voice != nil {
		v, err := st.s.cfg.Voice(ctx, p.Voice, p.Model, lang)
		if err != nil {
			return t, wfErr(WorkflowCodeVoice, "%v", err)
		}
		t.Voice, t.RefAudio, t.RefText, t.VoicePrompt, t.Seed = v.Voice, v.RefAudio, v.RefText, v.VoicePrompt, v.Seed
		if v.Language != "" {
			t.Language = v.Language
		}
		if v.Instruct != "" && caps.Instruct && t.RefAudio == "" {
			t.Voice = strings.Trim(t.Voice+", "+v.Instruct, ", ")
		}
		if v.Seed != 0 {
			t.Render.SeedPinned = true
		}
	}
	if caps.Speed && p.Speed > 0 && p.Speed != 1 {
		t.Speed = p.Speed
	}
	return t, nil
}

// render speaks a take into name through the render pipeline.
func (st *wfStep) render(ctx context.Context, t core.Take, lang, name string) error {
	if st.s.cfg.Render == nil || st.s.cfg.Render.render == nil {
		return wfErr(CodeEngineUnavailable, "The render pipeline is not connected.")
	}
	out := st.path(name)
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	t.ID = core.NewID(st.s.cfg.Now())
	params, err := json.Marshal(RenderParams{Take: t, Lang: lang, Output: out})
	if err != nil {
		return err
	}
	sub := jobs.Job{ID: st.job.ID + "." + filepath.Base(filepath.Dir(name)), Spec: jobs.Spec{Kind: KindRender, Lane: jobs.LaneGPU, Params: params, Owner: st.job.Owner}}
	_, err = st.s.cfg.Render.render.Run(ctx, sub, func(pg jobs.Progress) {}, nil)
	return err
}

// speak renders the item's text.
func (st *wfStep) speak(ctx context.Context, ri *workflow.RunItem, in *workflow.Value) (wfResult, error) {
	txt, err := st.readText(in)
	if err != nil {
		return wfResult{}, err
	}
	if strings.TrimSpace(txt) == "" {
		return wfResult{}, wfErr(CodeNothingToSay, "There is nothing to say.")
	}
	lang := dubFirst(st.n.P.Language, in.Lang)
	t, err := st.recipe(ctx, txt, lang)
	if err != nil {
		return wfResult{}, err
	}
	name := st.out(ri.Key, "wav")
	if err := st.render(ctx, t, lang, name); err != nil {
		return wfResult{}, err
	}
	v, err := st.audioValue(name)
	if err != nil {
		return wfResult{}, err
	}
	v.Lang = lang
	return wfResult{out: v}, nil
}

// stt transcribes an audio value: a 16 kHz copy, speech to text, word
// times (the aligner when the model has none).
func (st *wfStep) stt(ctx context.Context, item string, in *workflow.Value, language string) (core.Transcript, []string, error) {
	m := st.s.cfg.STT()
	if m.Model == "" {
		return core.Transcript{}, nil, wfErr(WorkflowCodeNoSTT, "No speech-to-text model.")
	}
	speech := st.path("out/" + item + "/" + st.n.ID + ".16k.wav")
	defer os.Remove(speech)
	if err := st.decode(ctx, st.path(in.File), speech, TranscriptRate, 1); err != nil {
		return core.Transcript{}, nil, err
	}
	report := func(pg jobs.Progress) {}
	tp := proto.TranscribeParams{Model: m.Model, Audio: speech, Language: language, Words: m.Words}
	var tr proto.TranscribeResult
	if err := st.call(ctx, proto.MethodSTTTranscribe, m.Model, &tp, &tp.ModelPath, &tr, report); err != nil {
		return core.Transcript{}, nil, err
	}
	var warnings []string
	words := segmentWords(tr.Segments)
	if len(words) == 0 && m.Aligner != "" && strings.TrimSpace(tr.Text) != "" {
		ap := proto.AlignParams{Model: m.Aligner, Audio: speech, Text: strings.TrimSpace(tr.Text), Language: dubFirst(tr.Language, language)}
		var ar proto.AlignResult
		err := st.call(ctx, proto.MethodSTTAlign, m.Aligner, &ap, &ap.ModelPath, &ar, report)
		switch {
		case ctx.Err() != nil:
			return core.Transcript{}, nil, ctx.Err()
		case err != nil:
			warnings = append(warnings, WarnNoWordTimes)
		default:
			words = ar.Words
		}
	}
	lang := tr.Language
	if lang == "" || lang == "auto" {
		lang = language
	}
	secs := in.Seconds
	if tr.AudioSeconds > 0 {
		secs = tr.AudioSeconds
	}
	now := st.s.cfg.Now()
	t := core.Transcript{ID: item, CreatedAt: now, UpdatedAt: now, Source: core.SourceFile, Model: m.Model, Language: lang,
		Duration: secs, Raw: strings.TrimSpace(tr.Text), Seconds: tr.Seconds}
	t.Segments = BuildSegments(tr.Segments, words, secs)
	if strings.TrimSpace(t.Preferred()) == "" {
		return t, warnings, wfErr(WorkflowCodeNoSpeech, "No speech was found.")
	}
	return t, warnings, nil
}

// transcribe makes a transcript of the item's audio.
func (st *wfStep) transcribe(ctx context.Context, ri *workflow.RunItem, in *workflow.Value) (wfResult, error) {
	t, warnings, err := st.stt(ctx, ri.Key, in, st.n.P.From)
	if err != nil {
		return wfResult{}, err
	}
	t.Title = ri.Name
	v, err := st.transcriptValue(ri.Key, t)
	return wfResult{out: v, warnings: warnings}, err
}

// translate translates a text (by paragraphs) or a transcript (by
// segments, keeping their times).
func (st *wfStep) translate(ctx context.Context, ri *workflow.RunItem, in *workflow.Value) (wfResult, error) {
	p := st.n.P
	if st.s.cfg.Translator == nil {
		return wfResult{}, wfErr(translate.CodeNoLLM, "No translator is set up.")
	}
	tr, err := st.s.cfg.Translator(ctx, p.Translator)
	if err != nil {
		var te *translate.Error
		if errors.As(err, &te) {
			return wfResult{}, wfErr(te.Code, "%v", err)
		}
		return wfResult{}, wfErr(translate.CodeConfig, "%v", err)
	}
	src := dubFirst(p.From, in.Lang)
	ask := func(segs []translate.Segment) ([]string, error) {
		res, err := tr.Translate(ctx, translate.Request{Source: src, Target: p.To, Segments: segs, Quality: translate.Fast})
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			var te *translate.Error
			if errors.As(err, &te) {
				return nil, wfErr(te.Code, "%v", err)
			}
			return nil, wfErr(translate.CodeNetwork, "%v", err)
		}
		byID := map[string]string{}
		for _, t := range res.Segments {
			if t.Err != nil {
				return nil, wfErr(translate.CodeLineFailed, "%v", t.Err)
			}
			byID[t.ID] = t.Text
		}
		out := make([]string, len(segs))
		for i, s := range segs {
			t, ok := byID[s.ID]
			if !ok {
				return nil, wfErr(translate.CodeMissing, "a line came back missing")
			}
			out[i] = t
		}
		return out, nil
	}
	if in.Type == workflow.TypeTranscript {
		t, err := st.readTranscript(in)
		if err != nil {
			return wfResult{}, err
		}
		var segs []translate.Segment
		var idx []int
		for i, sg := range t.Segments {
			if strings.TrimSpace(sg.Text) != "" {
				segs = append(segs, translate.Segment{ID: fmt.Sprint(i), Text: sg.Text, Speaker: sg.Speaker, Slot: sg.End - sg.Start})
				idx = append(idx, i)
			}
		}
		if len(segs) == 0 {
			return wfResult{}, wfErr(WorkflowCodeEmpty, "Nothing to translate.")
		}
		got, err := ask(segs)
		if err != nil {
			return wfResult{}, err
		}
		for k, i := range idx {
			t.Segments[i].Text, t.Segments[i].Words, t.Segments[i].Edited = got[k], nil, true
		}
		t.Language, t.Clean, t.CleanBy = p.To, "", ""
		var parts []string
		for _, sg := range t.Segments {
			if s := strings.TrimSpace(sg.Text); s != "" {
				parts = append(parts, s)
			}
		}
		t.Raw = strings.Join(parts, " ")
		v, err := st.transcriptValue(ri.Key, t)
		return wfResult{out: v}, err
	}
	txt, err := st.readText(in)
	if err != nil {
		return wfResult{}, err
	}
	var segs []translate.Segment
	for i, para := range text.Paras(txt) {
		if s := strings.TrimSpace(txt[para.Start:para.End]); s != "" {
			segs = append(segs, translate.Segment{ID: fmt.Sprint(i), Text: s})
		}
	}
	if len(segs) == 0 {
		return wfResult{}, wfErr(WorkflowCodeEmpty, "Nothing to translate.")
	}
	got, err := ask(segs)
	if err != nil {
		return wfResult{}, err
	}
	v, err := st.textValue(ri.Key, strings.Join(got, "\n\n"), p.To)
	return wfResult{out: v}, err
}

// convert says the item's words again in the step's voice.
func (st *wfStep) convert(ctx context.Context, ri *workflow.RunItem, in *workflow.Value) (wfResult, error) {
	p := st.n.P
	t, warnings, err := st.stt(ctx, ri.Key, in, p.Language)
	if err != nil {
		return wfResult{}, err
	}
	lang := dubFirst(p.Language, t.Language)
	take, err := st.recipe(ctx, t.Preferred(), lang)
	if err != nil {
		return wfResult{}, err
	}
	caps := st.s.cfg.Caps(p.Model)
	if p.MatchDuration && caps.Duration && in.Seconds > 0.3 {
		take.Duration = math.Round(in.Seconds*100) / 100
	}
	name := st.out(ri.Key, "wav")
	if err := st.render(ctx, take, lang, name); err != nil {
		return wfResult{}, err
	}
	if p.MatchDuration && take.Duration == 0 && in.Seconds > 0.3 {
		// The model has no duration control: time-stretch to the source's
		// length (within the stretcher's limits).
		b, _, err := wav.ReadFile(st.path(name))
		if err != nil {
			return wfResult{}, wfErr(WorkflowCodeFile, "%v", err)
		}
		if b.Seconds() > 0 {
			b = stretch.Stretch(b, in.Seconds/b.Seconds(), stretch.Options{})
			if err := writeWAVFloat(st.path(name), b); err != nil {
				return wfResult{}, err
			}
		}
	}
	v, err := st.audioValue(name)
	if err != nil {
		return wfResult{}, err
	}
	v.Lang = lang
	return wfResult{out: v, warnings: warnings}, nil
}

// Warnings of steps.
const (
	WorkflowWarnSilent   = "workflow.silent"    // normalize: the audio is silent, kept as it is
	WorkflowWarnNoFolder = "workflow.no_folder" // export: no folder, nothing written
)

// normalize brings the audio to the step's loudness or peak.
func (st *wfStep) normalize(ctx context.Context, ri *workflow.RunItem, in *workflow.Value) (wfResult, error) {
	b, _, err := wav.ReadFile(st.path(in.File))
	if err != nil {
		return wfResult{}, wfErr(WorkflowCodeFile, "%v", err)
	}
	if err := ctx.Err(); err != nil {
		return wfResult{}, err
	}
	p := st.n.P
	var warnings []string
	peak := float64(dsp.Peak(b.Samples))
	switch {
	case peak <= 1e-6:
		warnings = append(warnings, WorkflowWarnSilent)
	case p.Level == workflow.LevelPeak:
		dsp.Gain(b.Samples, float32(dsp.Amp(p.Target-dsp.DB(peak))))
	default:
		b, _ = loudness.Normalize(b, loudness.Target{ID: "workflow", Integrated: p.Target, MinIntegrated: p.Target - 1,
			MaxIntegrated: p.Target + 1, TruePeak: -1}, loudness.NormalizeOptions{})
	}
	name := st.out(ri.Key, "wav")
	if err := os.MkdirAll(filepath.Dir(st.path(name)), 0o755); err != nil {
		return wfResult{}, err
	}
	if err := writeWAVFloat(st.path(name), b); err != nil {
		return wfResult{}, err
	}
	v, err := st.audioValue(name)
	if err != nil {
		return wfResult{}, err
	}
	v.Lang = in.Lang
	return wfResult{out: v, warnings: warnings}, nil
}

// export writes the item's value into the step's folder.
func (st *wfStep) export(ctx context.Context, ri *workflow.RunItem, index int, in *workflow.Value) (wfResult, error) {
	p := st.n.P
	if strings.TrimSpace(p.Folder) == "" {
		out := *in
		return wfResult{out: &out, warnings: []string{WorkflowWarnNoFolder}}, nil
	}
	format := workflow.ExportFormat(p.Format, in.Type)
	name := workflow.FileName(p.Template, workflow.NameVars{Workflow: st.w.Name, N: index + 1, Item: ri.Name,
		Step: st.n.StepName(st.n.Kind.Label()), Date: st.s.cfg.Now(), Lang: in.Lang})
	dst := filepath.Join(p.Folder, name+"."+format)
	if err := st.s.write(ctx, st.w.ID, in, format, dst, st.call); err != nil {
		return wfResult{}, err
	}
	out := *in
	out.File = dst
	return wfResult{out: &out}, nil
}

// write saves a value as format at dst (absolute), atomically.
func (s *Workflows) write(ctx context.Context, id string, v *workflow.Value, format, dst string,
	call func(ctx context.Context, method, model string, params any, modelPath *string, out any, report func(jobs.Progress)) error) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return wfErr(WorkflowCodeFile, "%v", err)
	}
	src := s.Path(id, v.File)
	switch v.Type {
	case workflow.TypeAudio:
		switch format {
		case workflow.FormatM4A:
			if call == nil {
				return wfErr(WorkflowCodeEncode, "M4A needs the engine.")
			}
			tmp := dst + ".tmp.m4a"
			ep := proto.EncodeParams{Input: src, Output: proto.Output{Path: tmp, Format: "m4a"}}
			var er json.RawMessage
			if err := call(ctx, proto.MethodMediaEncode, "", &ep, nil, &er, nil); err != nil {
				os.Remove(tmp)
				if c := code(err); c == proto.CodeMethodNotFound || c == proto.CodeUnsupported {
					return wfErr(WorkflowCodeEncode, "This engine can't write M4A: %v", err)
				}
				return err
			}
			return os.Rename(tmp, dst)
		default:
			return ConvertWAV(ctx, src, dst, 24)
		}
	case workflow.TypeTranscript:
		data, err := os.ReadFile(src)
		if err != nil {
			return wfErr(WorkflowCodeFile, "%v", err)
		}
		var t core.Transcript
		if err := json.Unmarshal(data, &t); err != nil {
			return wfErr(WorkflowCodeFile, "%v", err)
		}
		out, err := Export(t, format)
		if err != nil {
			return err
		}
		return wfWriteFile(dst, out)
	default:
		data, err := os.ReadFile(src)
		if err != nil {
			return wfErr(WorkflowCodeFile, "%v", err)
		}
		if !bytes.HasSuffix(data, []byte("\n")) {
			data = append(data, '\n')
		}
		return wfWriteFile(dst, data)
	}
}

// --- results ------------------------------------------------------------------------------------

// WorkflowOutput is a result the Results list shows: an item's value at
// a step with nothing after it (an export, or the end of a branch).
type WorkflowOutput struct {
	Item  string // the item's key
	Index int    // its place, 0-based
	Name  string // the item's name
	Node  string
	Value workflow.Value
}

// Outputs are a run's final results, item by item, in the graph's order.
func Outputs(w *workflow.Workflow, run *workflow.Run) []WorkflowOutput {
	if run == nil {
		return nil
	}
	rep := workflow.Validate(w, nil)
	var out []WorkflowOutput
	for i, it := range run.Items {
		for _, id := range rep.Order {
			n := w.Node(id)
			if n == nil || n.Kind == workflow.KindStart || len(w.Outgoing(id)) > 0 && n.Kind != workflow.KindExport {
				continue
			}
			s := it.Steps[id]
			if s == nil || s.Status != workflow.StepDone || s.Out == nil {
				continue
			}
			out = append(out, WorkflowOutput{Item: it.Key, Index: i, Name: it.Name, Node: id, Value: *s.Out})
		}
	}
	return out
}

// ExportAll copies every final result of the last run into dir, named by
// template (workflow.DefaultTemplate when ""), audio as WAV, and returns
// the files written.
func (s *Workflows) ExportAll(ctx context.Context, id, dir, template string) ([]string, error) {
	w, run, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if template == "" {
		template = workflow.DefaultTemplate
	}
	var written []string
	used := map[string]int{}
	for _, o := range Outputs(w, run) {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		n := w.Node(o.Node)
		format := workflow.ExportFormat("", o.Value.Type)
		if n.Kind == workflow.KindExport {
			format = workflow.ExportFormat(n.P.Format, o.Value.Type)
			if format == workflow.FormatM4A {
				format = workflow.FormatWAV
			}
		}
		base := workflow.FileName(template, workflow.NameVars{Workflow: w.Name, N: o.Index + 1, Item: o.Name,
			Step: n.StepName(n.Kind.Label()), Date: s.cfg.Now(), Lang: o.Value.Lang})
		if used[base]++; used[base] > 1 {
			base = fmt.Sprintf("%s-%d", base, used[base])
		}
		dst := filepath.Join(dir, base+"."+format)
		v := o.Value
		if filepath.IsAbs(v.File) {
			// An export's copy: from the step before it.
			if in, _ := wfInput(w, &run.Items[o.Index], o.Node); in != nil {
				v = *in
			}
		}
		if err := s.write(ctx, id, &v, format, dst, nil); err != nil {
			return written, err
		}
		written = append(written, dst)
	}
	return written, nil
}
