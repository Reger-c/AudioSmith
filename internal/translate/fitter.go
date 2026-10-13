package translate

import (
	"context"

	"github.com/hehehai/AudioSmith/internal/llm"
)

// Fitter predicts whether translated lines fit their slots and, with an
// LLM route for fit_rewrite, shortens the ones that overflow.
type Fitter struct {
	Router     *llm.Router // nil: predict only
	Predictor  *Predictor  // nil: DefaultPredictor
	Repairs    int
	BatchLines int // lines per rewrite request (0: 20)
}

func (f *Fitter) predictor() *Predictor {
	if f.Predictor != nil {
		return f.Predictor
	}
	return DefaultPredictor
}

type fitIn struct {
	ID         string  `json:"id"`
	Source     string  `json:"source,omitempty"`
	Text       string  `json:"text"`
	MaxSeconds float64 `json:"maxSeconds"`
	MaxChars   int     `json:"maxChars"`
}

// apply fills Fit for every line with a slot and, when rewrite is set,
// shortens overflowing lines: Autofit replaces Text (keeping Draft),
// otherwise the rewrite goes to Shorter.
func (f *Fitter) apply(ctx context.Context, req Request, masks []masked, out []Translated, src Lang, srcOK bool, tgt Lang, notes string, rewrite bool, progress func(string, int)) (llm.Usage, error) {
	p := f.predictor()
	var over []int
	for j, seg := range req.Segments {
		if seg.Slot <= 0 || out[j].Err != nil || out[j].Text == "" {
			continue
		}
		fit := p.Fit(out[j].Text, tgt.Tag, seg.Slot, req.Speed)
		out[j].Fit = &fit
		if fit.Verdict == FitOverflow {
			over = append(over, j)
		}
	}
	if !rewrite || len(over) == 0 {
		return llm.Usage{}, nil
	}
	if !f.Router.Available(llm.SkillFitRewrite) {
		for _, j := range over {
			out[j].Warnings = append(out[j].Warnings, WarnFitFailed)
		}
		return llm.Usage{}, nil
	}
	s := &session{
		t:   &LLM{Router: f.Router, Predictor: p, Repairs: f.Repairs},
		req: req, src: src, srcOK: srcOK, tgt: tgt, notes: notes, masks: masks, out: out,
	}
	batch := f.BatchLines
	if batch <= 0 {
		batch = 20
	}
	done := 0
	for start := 0; start < len(over); start += batch {
		idx := over[start:min(start+batch, len(over))]
		lines := make([]fitIn, len(idx))
		texts := make([]string, len(idx))
		for i, j := range idx {
			seg := req.Segments[j]
			_, pause := Units(out[j].Text, tgt.Tag)
			_, chars := p.Budget(tgt.Tag, seg.Slot, req.Speed, pause)
			lines[i] = fitIn{ID: seg.ID, Source: masks[j].Text, Text: s.remask(j, out[j].Text), MaxSeconds: round2(seg.Slot), MaxChars: chars}
			texts[i] = seg.Text
		}
		d := promptData{Source: src, SourceKnown: srcOK, Target: tgt, TargetNotes: notes, Glossary: req.Glossary.Relevant(texts...), Lines: lines}
		got, _, err := s.call(ctx, llm.SkillFitRewrite, "fit", d, idx)
		if err != nil {
			return s.usage, err
		}
		for _, j := range idx {
			text, ok := got[j]
			if !ok {
				out[j].Warnings = append(out[j].Warnings, WarnFitFailed)
				continue
			}
			nf := p.Fit(text, tgt.Tag, req.Segments[j].Slot, req.Speed)
			if nf.Predicted >= out[j].Fit.Predicted {
				out[j].Warnings = append(out[j].Warnings, WarnFitFailed)
				continue
			}
			if req.Quality == Autofit {
				if out[j].Draft == "" {
					out[j].Draft = out[j].Text
				}
				out[j].Text, out[j].Fit = text, &nf
			} else {
				out[j].Shorter = text
			}
		}
		done += len(idx)
		if progress != nil {
			progress("fit", done)
		}
	}
	return s.usage, nil
}

// FitRequest is the Speech-rate fit tool's input.
type FitRequest struct {
	Text   string  // the (translated) line, markup allowed
	Lang   string  // its language
	Slot   float64 // seconds available
	Speed  float64 // TTS speed (0: 1)
	Source string  // optional: the original line, given to the rewrite as context
	// SourceLang is Source's language (optional).
	SourceLang string
	Rewrite    bool // ask fit_rewrite for a shorter version when it overflows
	Glossary   Glossary
}

// FitResult is the tool's answer.
type FitResult struct {
	Fit        Fit
	Rewrite    string    // "" when not asked, not needed or not shorter
	RewriteFit *Fit      // the rewrite's prediction
	Usage      llm.Usage // of the rewrite
}

// Check predicts req.Text against its slot and, if asked and it
// overflows, proposes a shorter version.
func (f *Fitter) Check(ctx context.Context, req FitRequest) (FitResult, error) {
	tgt, ok := parseLang(req.Lang)
	if !ok {
		return FitResult{}, newErr(CodeLanguage, "lang", req.Lang)
	}
	if req.Speed <= 0 {
		req.Speed = 1
	}
	res := FitResult{Fit: f.predictor().Fit(req.Text, tgt.Tag, req.Slot, req.Speed)}
	if !req.Rewrite || res.Fit.Verdict != FitOverflow {
		return res, nil
	}
	if !f.Router.Available(llm.SkillFitRewrite) {
		return res, newErr(CodeNoLLM, "skill", string(llm.SkillFitRewrite))
	}
	src, srcOK := parseLang(req.SourceLang)
	treq := Request{Target: req.Lang, Speed: req.Speed, SuggestShorter: true, Glossary: req.Glossary,
		Segments: []Segment{{ID: "1", Text: req.Source, Slot: req.Slot}}}
	out := []Translated{{ID: "1", Text: req.Text}}
	masks := []masked{mask(req.Text)}
	if req.Source != "" {
		masks[0] = mask(req.Source)
	}
	// The rewrite keeps the line's own tags: mask the line, not the source.
	lineMask := mask(req.Text)
	masks[0].Tags, masks[0].Pos = lineMask.Tags, lineMask.Pos
	if req.Source == "" {
		masks[0].Text = ""
	}
	u, err := f.apply(ctx, treq, masks, out, src, srcOK, tgt, langNotes("notes:", tgt), true, nil)
	res.Usage = u
	if err != nil {
		return res, err
	}
	if out[0].Shorter != "" {
		res.Rewrite = out[0].Shorter
		nf := f.predictor().Fit(res.Rewrite, tgt.Tag, req.Slot, req.Speed)
		res.RewriteFit = &nf
	}
	return res, nil
}

// WithFit wraps a translator (normally an MT service) so its results get
// fit predictions, and, for Autofit or SuggestShorter requests, LLM
// rewrites of overflowing lines.
func WithFit(t Translator, f *Fitter) Translator { return &fitWrapper{t: t, f: f} }

type fitWrapper struct {
	t Translator
	f *Fitter
}

func (w *fitWrapper) Info() Info {
	in := w.t.Info()
	if w.f.Router.Available(llm.SkillFitRewrite) && !containsQ(in.Qualities, Autofit) {
		in.Qualities = append(append([]Quality{}, in.Qualities...), Autofit)
	}
	return in
}

func containsQ(qs []Quality, q Quality) bool {
	for _, x := range qs {
		if x == q {
			return true
		}
	}
	return false
}

func (w *fitWrapper) Translate(ctx context.Context, req Request) (Result, error) {
	inner := req
	if inner.Quality == Autofit {
		inner.Quality = Fast
	}
	res, err := w.t.Translate(ctx, inner)
	if err != nil {
		return res, err
	}
	if err := validate(&req); err != nil {
		return res, err
	}
	tgt, _ := parseLang(req.Target)
	src, srcOK := parseLang(req.Source)
	masks := make([]masked, len(req.Segments))
	for i, s := range req.Segments {
		masks[i] = mask(s.Text)
	}
	rewrite := req.Quality == Autofit || req.SuggestShorter
	progress := func(stage string, done int) {
		if req.Progress != nil {
			req.Progress(Progress{Stage: stage, Done: done, Total: len(req.Segments)})
		}
	}
	u, err := w.f.apply(ctx, req, masks, res.Segments, src, srcOK, tgt, langNotes("notes:", tgt), rewrite, progress)
	res.Usage = res.Usage.Add(u)
	countFailed(&res)
	return res, err
}
