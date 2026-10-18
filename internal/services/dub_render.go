package services

// The dub stages after the transcript: translate here; synthesize in
// dub_lines.go; mix and qc in dub_mix.go.

import (
	"context"
	"slices"
	"strings"

	"github.com/hehehai/AudioSmith/internal/dub"
	"github.com/hehehai/AudioSmith/internal/jobs"
	"github.com/hehehai/AudioSmith/internal/lang"
	"github.com/hehehai/AudioSmith/internal/translate"
)

// More codes of the dub stages.
const (
	DubWarnTranslateFailed = "dub.translate_failed" // some lines failed (Line.Err): retry them
	DubWarnLinesFailed     = "dub.lines_failed"     // some lines could not be rendered (Render.Err)
	DubCodeNoModel         = "dub.no_model"         // no TTS model chosen
	// LineLoudness is the level every dubbed line is brought to (LUFS).
	LineLoudness = -20.0
)

// translateBatch is how many lines a translation saves at a time (a run
// stopped halfway keeps what was translated).
const translateBatch = 60

// translateSet lists the segments the translate stage for lang does:
// lines whose language is lang, with source text, without a translation
// (all of them with force, but not those written by hand).
func translateSet(p *dub.Project, l string, force bool) []int {
	var out []int
	src := p.SourceLanguage()
	for i, s := range p.Segments {
		if (s.Lang != "" && s.Lang != l) || strings.TrimSpace(s.Text) == "" {
			continue
		}
		if src != "" && dub.LangFor(s, l) != "" && strings.EqualFold(lang.Canonical(src), lang.Canonical(l)) && !strings.Contains(l, "-") {
			continue // the source language: nothing to translate
		}
		line := s.Lines[l]
		if line.Skipped && !force {
			continue
		}
		if strings.TrimSpace(line.Text) == "" || (force && !line.Edited) {
			out = append(out, i)
		}
	}
	return out
}

// translate translates the lines of one language, a batch at a time.
func (s *Dubs) translate(ctx context.Context, sc *stageCtx) error {
	p := sc.p
	l := sc.params.Lang
	idx := translateSet(p, l, sc.params.Force)
	if len(idx) == 0 {
		return nil
	}
	if s.cfg.Translator == nil {
		return stageErr(DubCodeNoTranslator, "No translation engine is set up.")
	}
	tr, err := s.cfg.Translator(ctx, p.Settings)
	if err != nil {
		return translatorErr(err)
	}
	// Auto glossary: terms the LLM finds, once per project.
	glossary := p.Glossary
	if p.Settings.AutoGlossary && s.cfg.Extractor != nil && !slices.ContainsFunc(glossary, func(t translate.Term) bool { return t.Auto }) {
		if x := s.cfg.Extractor(); x != nil {
			sc.progress("glossary", -1, "")
			var segs []translate.Segment
			for _, i := range idx {
				segs = append(segs, translate.Segment{ID: p.Segments[i].ID, Text: p.Segments[i].Text})
			}
			cands, _, err := x.Extract(ctx, p.SourceLanguage(), l, segs, glossary, p.Settings.Style)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err == nil && len(cands) > 0 {
				var auto translate.Glossary
				for _, c := range cands {
					t := c.Term
					t.Auto = true
					auto = append(auto, t)
				}
				glossary = glossary.Merge(auto)
				if err := sc.save(ctx, func(p *dub.Project) { p.Glossary = p.Glossary.Merge(auto) }); err != nil {
					return err
				}
			}
		}
	}
	failed := 0
	for start := 0; start < len(idx); start += translateBatch {
		part := idx[start:min(start+translateBatch, len(idx))]
		req := translate.Request{
			Source: p.SourceLanguage(), Target: l, Quality: p.Settings.Quality, Glossary: glossary,
			Style: p.Settings.Style, Formality: p.Settings.Formality, Context: p.Title,
			SuggestShorter: p.Settings.SuggestShorter, Speed: s.dialogueSpeed(p, l),
		}
		done := start
		total := len(idx)
		req.Progress = func(pr translate.Progress) {
			if pr.Total > 0 {
				sc.progress("translating", (float64(done)+float64(pr.Done)/float64(pr.Total)*float64(len(part)))/float64(total), pr.Stage)
			}
		}
		for _, i := range part {
			seg := p.Segments[i]
			req.Segments = append(req.Segments, translate.Segment{ID: seg.ID, Text: seg.Text, Speaker: p.SpeakerName(seg.Speaker),
				Slot: seg.Duration(), Note: seg.Direction})
		}
		res, err := tr.Translate(ctx, req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return translatorErr(err)
		}
		failed += res.Failed
		err = sc.save(ctx, func(p *dub.Project) {
			for _, t := range res.Segments {
				seg, ok := p.Segment(t.ID)
				if !ok {
					continue
				}
				old := seg.Lines[l]
				if old.Edited && !sc.params.Force {
					continue // written by hand meanwhile
				}
				line := dub.Line{Text: t.Text, Draft: t.Draft, Shorter: t.Shorter, Fit: t.Fit, Warnings: t.Warnings}
				if t.Err != nil {
					line.Err = t.Err.Code
				}
				if seg.Lines == nil {
					seg.Lines = map[string]dub.Line{}
				}
				seg.Lines[l] = line
			}
		})
		if err != nil {
			return err
		}
	}
	if failed > 0 {
		sc.warn(DubWarnTranslateFailed)
	}
	return nil
}

// translatorErr explains a translator that cannot run.
func translatorErr(err error) error {
	c := translate.Code(err)
	switch c {
	case translate.CodeNoLLM, translate.CodeNoKey, translate.CodeUnsupported:
		return &jobs.Error{Code: DubCodeNoTranslator, Message: err.Error()}
	case "":
		c = DubCodeFailed
	}
	return &jobs.Error{Code: c, Message: err.Error(), Retryable: c == translate.CodeQuota || c == translate.CodeNetwork}
}
