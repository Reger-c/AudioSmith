package translate

import (
	"context"
	"strings"
	"unicode"

	"github.com/hehehai/AudioSmith/internal/llm"
	"github.com/hehehai/AudioSmith/internal/text"
)

// promptData is what the prompt templates see.
type promptData struct {
	Source      Lang
	SourceKnown bool
	Target      Lang
	Autofit     bool
	Style       string
	Context     string
	Formality   Formality
	Glossary    Glossary
	TargetNotes string
	Lines       any
	Before      []ctxLine
	After       []ctxLine
	Max         int    // glossary: terms wanted
	Transcript  string // glossary
}

type lineIn struct {
	ID         string  `json:"id"`
	Text       string  `json:"text"`
	Speaker    string  `json:"speaker,omitempty"`
	Note       string  `json:"note,omitempty"`
	MaxSeconds float64 `json:"maxSeconds,omitempty"`
	MaxChars   int     `json:"maxChars,omitempty"`
}

type ctxLine struct {
	Speaker     string `json:"speaker,omitempty"`
	Text        string `json:"text"`
	Translation string `json:"translation,omitempty"`
}

type reviewIn struct {
	ID         string  `json:"id"`
	Source     string  `json:"source"`
	Draft      string  `json:"draft"`
	Notes      string  `json:"notes,omitempty"`
	Speaker    string  `json:"speaker,omitempty"`
	MaxSeconds float64 `json:"maxSeconds,omitempty"`
	MaxChars   int     `json:"maxChars,omitempty"`
}

var linesSchema = func() *llm.Schema {
	s := llm.Object(map[string]*llm.Schema{
		"lines": llm.Array(llm.Object(map[string]*llm.Schema{"id": llm.String(), "text": llm.String()}, "id", "text")),
	}, "lines")
	s.Name = "lines"
	return s
}()

var issuesSchema = func() *llm.Schema {
	s := llm.Object(map[string]*llm.Schema{
		"lines": llm.Array(llm.Object(map[string]*llm.Schema{"id": llm.String(), "issues": llm.String()}, "id", "issues")),
	}, "lines")
	s.Name = "review"
	return s
}()

// LLM translates with the router's translate / translate_cinematic /
// fit_rewrite skills.
type LLM struct {
	Router    *llm.Router
	Predictor *Predictor // nil: DefaultPredictor
	// BatchLines and BatchChars bound one request (0: 20 lines, 2500 characters).
	BatchLines int
	BatchChars int
	Repairs    int // JSON repair turns per request (0: llm.DefaultRepairs)
}

func (t *LLM) predictor() *Predictor {
	if t.Predictor != nil {
		return t.Predictor
	}
	return DefaultPredictor
}

// Info reports the translator; Network/ThirdParty reflect the current routes.
func (t *LLM) Info() Info {
	in := Info{ID: "llm", Kind: "llm", Qualities: []Quality{Fast, Cinematic, Autofit}, Glossary: true, Detects: true}
	if t.Router == nil || t.Router.Registry == nil {
		return in
	}
	for _, s := range []llm.Skill{llm.SkillTranslate, llm.SkillTranslateCinematic, llm.SkillFitRewrite} {
		for _, r := range t.Router.Routes(s) {
			if b, ok := t.Router.Registry.Backend(r.Provider); ok && !b.Info().Local {
				in.Network, in.ThirdParty = true, true
			}
		}
	}
	return in
}

// session is one Translate call's state.
type session struct {
	t     *LLM
	req   Request
	src   Lang
	srcOK bool
	tgt   Lang
	notes string
	masks []masked
	out   []Translated
	index map[string]int
	usage llm.Usage
	ctxN  int
	// budgets: the first pass sees each line's time budget (Autofit on a
	// cloud route). Local models shown a budget over-shorten into broken,
	// mixed-script fragments (thread #44), so with them Autofit's first
	// pass is a plain translation and only overflowing lines are
	// rewritten, and checked (fitRejected).
	budgets bool
}

func (t *LLM) Translate(ctx context.Context, req Request) (Result, error) {
	if err := validate(&req); err != nil {
		return Result{}, err
	}
	skill := llm.SkillTranslate
	if req.Quality == Cinematic {
		skill = llm.SkillTranslateCinematic
	}
	if !t.Router.Available(skill) {
		return Result{}, newErr(CodeNoLLM, "skill", string(skill))
	}
	s := &session{t: t, req: req, index: map[string]int{}}
	s.tgt, _ = parseLang(req.Target)
	s.src, s.srcOK = parseLang(req.Source)
	s.notes = langNotes("notes:", s.tgt)
	s.budgets = req.Quality == Autofit && !localRoute(t.Router, skill)
	s.ctxN = req.ContextLines
	if s.ctxN == 0 {
		s.ctxN = DefaultContextLines
	}
	if s.ctxN < 0 {
		s.ctxN = 0
	}
	s.out = make([]Translated, len(req.Segments))
	s.masks = make([]masked, len(req.Segments))
	for i, seg := range req.Segments {
		s.out[i].ID = seg.ID
		s.masks[i] = mask(seg.Text)
		s.index[seg.ID] = i
	}
	res := Result{Translator: "llm"}
	finish := func(err error) (Result, error) {
		res.Segments, res.Usage = s.out, s.usage
		countFailed(&res)
		return res, err
	}

	batches := t.batches(req.Segments)
	done := 0
	for _, b := range batches {
		if err := s.translateBatch(ctx, skill, b); err != nil {
			return finish(err)
		}
		done += len(b)
		s.progress("translate", done)
		if req.Quality == Cinematic {
			if err := s.cinematic(ctx, b); err != nil {
				return finish(err)
			}
			s.progress("adapt", done)
		}
	}
	if err := s.fit(ctx); err != nil {
		return finish(err)
	}
	return finish(nil)
}

func (s *session) progress(stage string, done int) {
	if s.req.Progress != nil {
		s.req.Progress(Progress{Stage: stage, Done: done, Total: len(s.req.Segments)})
	}
}

// batches splits indices into requests.
func (t *LLM) batches(segs []Segment) [][]int {
	maxLines, maxChars := t.BatchLines, t.BatchChars
	if maxLines <= 0 {
		maxLines = 20
	}
	if maxChars <= 0 {
		maxChars = 2500
	}
	var out [][]int
	var cur []int
	chars := 0
	for i, seg := range segs {
		n := len([]rune(seg.Text))
		if len(cur) > 0 && (len(cur) >= maxLines || chars+n > maxChars) {
			out = append(out, cur)
			cur, chars = nil, 0
		}
		cur = append(cur, i)
		chars += n
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

func (s *session) data(idx []int) promptData {
	texts := make([]string, len(idx))
	for i, j := range idx {
		texts[i] = s.req.Segments[j].Text
	}
	d := promptData{
		Source: s.src, SourceKnown: s.srcOK, Target: s.tgt, Autofit: s.budgets,
		Style: s.req.Style, Context: s.req.Context, Formality: s.req.Formality,
		Glossary: s.req.Glossary.Relevant(texts...), TargetNotes: s.notes,
	}
	if s.ctxN > 0 && len(idx) > 0 {
		first, last := idx[0], idx[len(idx)-1]
		for j := max(0, first-s.ctxN); j < first; j++ {
			d.Before = append(d.Before, ctxLine{Speaker: s.req.Segments[j].Speaker, Text: s.masks[j].Text, Translation: s.out[j].Text})
		}
		for j := last + 1; j < min(len(s.req.Segments), last+1+s.ctxN); j++ {
			d.After = append(d.After, ctxLine{Speaker: s.req.Segments[j].Speaker, Text: s.masks[j].Text})
		}
	}
	return d
}

func (s *session) lineIn(j int) lineIn {
	seg := s.req.Segments[j]
	l := lineIn{ID: seg.ID, Text: s.masks[j].Text, Speaker: seg.Speaker, Note: seg.Note}
	if s.budgets && seg.Slot > 0 {
		l.MaxSeconds = round2(seg.Slot)
		_, pause := Units(seg.Text, s.src.Tag)
		_, l.MaxChars = s.t.predictor().Budget(s.tgt.Tag, seg.Slot, s.req.Speed, pause)
	}
	return l
}

// call runs one lines request and returns the accepted texts by index
// (tags restored) and the per-index failures. A whole-request failure
// marks every index failed with the cause; cancellation is returned.
func (s *session) call(ctx context.Context, skill llm.Skill, name string, data promptData, idx []int) (map[int]string, map[int]error, error) {
	got := map[int]string{}
	failed := map[int]error{}
	msgs, err := messages(name, data)
	if err != nil {
		return nil, nil, err
	}
	var res struct {
		Lines []struct{ ID, Text string } `json:"lines"`
	}
	resp, err := s.t.Router.JSON(ctx, skill, llm.Request{Messages: msgs, Schema: linesSchema}, &res, s.t.Repairs)
	s.usage = s.usage.Add(resp.Usage)
	if err != nil {
		if isCancel(ctx, err) {
			return nil, nil, cancelled(err)
		}
		if fatal(err) {
			return nil, nil, err
		}
		for _, j := range idx {
			failed[j] = err
		}
		return got, failed, nil
	}
	byID := map[string]string{}
	dup := map[string]bool{}
	for _, l := range res.Lines {
		if _, ok := byID[l.ID]; ok {
			dup[l.ID] = true
		}
		byID[l.ID] = l.Text
	}
	for _, j := range idx {
		id := s.req.Segments[j].ID
		text, ok := byID[id]
		text = strings.TrimSpace(text)
		switch {
		case !ok || dup[id] || (text == "" && strings.TrimSpace(s.req.Segments[j].Text) != ""):
			failed[j] = newErr(CodeMissing, "id", id)
		default:
			restored, ok := s.masks[j].unmask(text)
			if !ok {
				failed[j] = &tagsLost{text: restored, err: newErr(CodeTagsLost, "id", id)}
				continue
			}
			got[j] = restored
		}
	}
	return got, failed, nil
}

// fatal reports whether a whole-request failure would fail every other
// request too (every route already failed for auth, network, quota,
// configuration…), so the translation stops instead of retrying line by
// line. Content failures (bad JSON, filtered, too long) are retried per line.
func fatal(err error) bool {
	switch llm.Code(err) {
	case llm.CodeInvalidJSON, llm.CodeSchema, llm.CodeFiltered, llm.CodeEmpty, llm.CodeContextLength, llm.CodeBadRequest, llm.CodeBadResponse:
		return false
	case "":
		return false
	}
	return true
}

// tagsLost is a failure that still has a usable (repaired) text.
type tagsLost struct {
	text string
	err  *Error
}

func (e *tagsLost) Error() string { return e.err.Error() }
func (e *tagsLost) Unwrap() error { return e.err }

func cancelled(err error) error {
	e := newErr(CodeCancelled)
	e.Err = err
	return e
}

func (s *session) translateBatch(ctx context.Context, skill llm.Skill, idx []int) error {
	lines := make([]lineIn, len(idx))
	for i, j := range idx {
		lines[i] = s.lineIn(j)
	}
	d := s.data(idx)
	d.Lines = lines
	got, failed, err := s.call(ctx, skill, "translate", d, idx)
	if err != nil {
		return err
	}
	for j, text := range got {
		s.accept(j, text)
	}
	// Retry failed lines one by one, with their neighbours as context.
	retried := 0
	for _, j := range idx {
		ferr, bad := failed[j]
		if !bad {
			continue
		}
		if len(idx) > 1 {
			d := s.data([]int{j})
			d.Lines = []lineIn{s.lineIn(j)}
			g, f, err := s.call(ctx, skill, "translate", d, []int{j})
			if err != nil {
				return err
			}
			if text, ok := g[j]; ok {
				s.accept(j, text)
				retried++
				continue
			}
			ferr = f[j]
		}
		var tl *tagsLost
		if asTagsLost(ferr, &tl) {
			s.accept(j, tl.text)
			s.out[j].Warnings = append(s.out[j].Warnings, WarnTagsRepaired)
			continue
		}
		s.out[j].Err = lineErr(s.req.Segments[j].ID, ferr)
	}
	if retried > 0 {
		s.progress("retry", retried)
	}
	return nil
}

func asTagsLost(err error, out **tagsLost) bool {
	tl, ok := err.(*tagsLost)
	if ok {
		*out = tl
	}
	return ok
}

func (s *session) accept(j int, text string) {
	s.out[j].Text = text
	s.out[j].Err = nil
	src := strings.TrimSpace(s.req.Segments[j].Text)
	if text == src && letters(src) >= 3 && (!s.srcOK || s.src.Code != s.tgt.Code) {
		s.out[j].Warnings = append(s.out[j].Warnings, WarnUnchanged)
	}
}

func letters(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			n++
		}
	}
	return n
}

// cinematic reviews the batch's drafts and adapts the lines with issues.
func (s *session) cinematic(ctx context.Context, idx []int) error {
	var ok []int
	for _, j := range idx {
		if s.out[j].Err == nil && s.out[j].Text != "" {
			ok = append(ok, j)
		}
	}
	if len(ok) == 0 {
		return nil
	}
	review := func(j int) reviewIn {
		seg := s.req.Segments[j]
		r := reviewIn{ID: seg.ID, Source: s.masks[j].Text, Draft: s.remask(j, s.out[j].Text), Speaker: seg.Speaker}
		l := s.lineIn(j) // timing budget in Autofit only
		r.MaxSeconds, r.MaxChars = l.MaxSeconds, l.MaxChars
		return r
	}
	d := s.data(ok)
	in := make([]reviewIn, len(ok))
	for i, j := range ok {
		in[i] = review(j)
	}
	d.Lines = in
	msgs, err := messages("reflect", d)
	if err != nil {
		return err
	}
	var rev struct {
		Lines []struct{ ID, Issues string } `json:"lines"`
	}
	resp, err := s.t.Router.JSON(ctx, llm.SkillTranslateCinematic, llm.Request{Messages: msgs, Schema: issuesSchema}, &rev, s.t.Repairs)
	s.usage = s.usage.Add(resp.Usage)
	if err != nil {
		if isCancel(ctx, err) {
			return cancelled(err)
		}
		for _, j := range ok {
			s.out[j].Warnings = append(s.out[j].Warnings, WarnAdaptFailed)
		}
		return nil
	}
	issues := map[string]string{}
	for _, l := range rev.Lines {
		if v := strings.TrimSpace(l.Issues); v != "" && !isNoIssue(v) {
			issues[l.ID] = v
		}
	}
	var todo []int
	var adaptIn []reviewIn
	for _, j := range ok {
		if v, has := issues[s.req.Segments[j].ID]; has {
			r := review(j)
			r.Notes = v
			adaptIn = append(adaptIn, r)
			todo = append(todo, j)
		}
	}
	if len(todo) == 0 {
		return nil
	}
	d = s.data(todo)
	d.Lines = adaptIn
	d.Before, d.After = nil, nil
	got, failed, err := s.call(ctx, llm.SkillTranslateCinematic, "adapt", d, todo)
	if err != nil {
		return err
	}
	for _, j := range todo {
		if text, ok := got[j]; ok {
			if text != s.out[j].Text {
				s.out[j].Draft = s.out[j].Text
				s.out[j].Text = text
			}
			continue
		}
		_ = failed[j]
		s.out[j].Warnings = append(s.out[j].Warnings, WarnAdaptFailed)
	}
	return nil
}

func isNoIssue(v string) bool {
	switch strings.ToLower(strings.Trim(v, " .!")) {
	case "none", "no issues", "ok", "good", "n/a", "-", "无", "没有":
		return true
	}
	return false
}

// remask turns a restored line back into marker form for a later pass.
func (s *session) remask(j int, line string) string {
	m := s.masks[j]
	if len(m.Tags) == 0 {
		return line
	}
	var b strings.Builder
	last := 0
	used := make([]bool, len(m.Tags))
	for _, sp := range text.Tags(line) {
		tag := sp.Of(line)
		k := -1
		for i, t := range m.Tags {
			if !used[i] && t == tag {
				k = i
				break
			}
		}
		if k < 0 {
			continue
		}
		used[k] = true
		b.WriteString(line[last:sp.Start])
		b.WriteString(marker(k + 1))
		last = sp.End
	}
	b.WriteString(line[last:])
	return b.String()
}

// fit predicts every line with a slot and shortens overflowing ones
// (Autofit replaces the text; SuggestShorter offers it).
func (s *session) fit(ctx context.Context) error {
	f := &Fitter{Router: s.t.Router, Predictor: s.t.predictor(), Repairs: s.t.Repairs}
	rewrite := s.req.Quality == Autofit || s.req.SuggestShorter
	u, err := f.apply(ctx, s.req, s.masks, s.out, s.src, s.srcOK, s.tgt, s.notes, rewrite, s.progress)
	s.usage = s.usage.Add(u)
	return err
}
