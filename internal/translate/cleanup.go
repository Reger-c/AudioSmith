package translate

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/hehehai/AudioSmith/internal/llm"
)

// DefaultCleanupBudget is how long dictation cleanup may take before the
// polished raw text is used instead.
const DefaultCleanupBudget = 4 * time.Second

// Cleanup fallback reasons (CleanupResult.Fallback).
const (
	FallbackNone     = ""
	FallbackDisabled = "cleanup.disabled"  // LLM cleanup off for this request
	FallbackNoLLM    = "cleanup.no_llm"    // no route for dictation_cleanup
	FallbackTimeout  = "cleanup.timeout"   // the budget ran out
	FallbackError    = "cleanup.error"     // the model call failed (Detail: its code)
	FallbackDiverged = "cleanup.diverged"  // the reply wasn't a cleanup of the input (answered it, rewrote it…)
	FallbackEmpty    = "cleanup.empty"     // the reply was empty
	FallbackTooShort = "cleanup.too_short" // too little text to be worth a model call
	FallbackBusy     = "cleanup.busy"      // the model was busy with other work (CleanupRequest.Busy)
)

// Cleanup warnings.
const (
	WarnTermsLost = "cleanup.terms_lost" // a vocabulary term in the input is missing from the output
)

// CleanupRequest is one dictated utterance.
type CleanupRequest struct {
	Text       string
	Lang       string        // BCP-47 of the dictation, "" if unknown (mixed zh/en is fine)
	Vocabulary []string      // names and jargon to keep (the dictation vocabulary hint)
	NoLLM      bool          // polish only
	Busy       bool          // the model is busy with other work (a render on the gpu lane): polish only, FallbackBusy
	Budget     time.Duration // 0: the Cleaner's
}

// CleanupResult is always usable: Text is the cleaned text, or the
// polished raw text when the model was skipped or failed (Fallback says why).
type CleanupResult struct {
	Text     string
	Raw      string
	Polished string // the deterministic polish of Raw
	LLM      bool   // Text came from the model
	Fallback string
	Detail   string // the failing code for FallbackError
	Warnings []string
	Usage    llm.Usage
	Elapsed  time.Duration
}

// Cleaner runs dictation cleanup.
type Cleaner struct {
	Router *llm.Router   // nil: polish only
	Budget time.Duration // 0: DefaultCleanupBudget
	Polish PolishOptions
	// MinChars skips the model for shorter input (0: 12 characters).
	MinChars int
}

var cleanupSchema = func() *llm.Schema {
	s := llm.Object(map[string]*llm.Schema{"text": llm.String()}, "text")
	s.Name = "cleanup"
	return s
}()

type cleanupData struct {
	Text       string
	Language   string
	Vocabulary []string
	Notes      string
	Zh, En     bool
}

// Clean cleans req.Text within the budget. It never fails: on any problem
// it returns the polished raw text. The budget does not count a wait for
// the model's turn (llm.WithBudget); a caller that must not wait longer
// in all (dictation's paste) bounds ctx itself.
func (c *Cleaner) Clean(ctx context.Context, req CleanupRequest) CleanupResult {
	start := time.Now()
	res := CleanupResult{Raw: req.Text}
	res.Polished = Polish(req.Text, req.Lang, c.Polish)
	res.Text = res.Polished
	done := func() CleanupResult { res.Elapsed = time.Since(start); return res }

	minChars := c.MinChars
	if minChars <= 0 {
		minChars = 12
	}
	switch {
	case req.NoLLM:
		res.Fallback = FallbackDisabled
		return done()
	case req.Busy:
		res.Fallback = FallbackBusy
		return done()
	case !c.Router.Available(llm.SkillDictationCleanup):
		res.Fallback = FallbackNoLLM
		return done()
	case len([]rune(strings.TrimSpace(req.Text))) < minChars:
		res.Fallback = FallbackTooShort
		return done()
	}
	budget := req.Budget
	if budget <= 0 {
		budget = c.Budget
	}
	if budget <= 0 {
		budget = DefaultCleanupBudget
	}
	bctx, cancel := llm.WithBudget(ctx, budget) // from when the model's turn comes (the gpu lane may be busy)
	defer cancel()

	l, known := parseLang(req.Lang)
	share := cjkShare(req.Text)
	data := cleanupData{
		Text:       req.Text,
		Vocabulary: cleanVocab(req.Vocabulary),
		Zh:         l.Code == "zh" || l.Code == "yue" || share > 0,
		En:         l.Code == "en" || !known || share < 1,
	}
	if known {
		data.Language = l.Describe()
		data.Notes = langNotes("cleanup:", l)
	} else if share > 0.3 {
		data.Notes = langNotes("cleanup:", Lang{Code: "zh"})
	}
	msgs, err := messages("cleanup", data)
	if err != nil {
		res.Fallback, res.Detail = FallbackError, "template"
		return done()
	}
	var out struct {
		Text string `json:"text"`
	}
	resp, err := c.Router.JSON(bctx, llm.SkillDictationCleanup, llm.Request{
		Messages: msgs, Schema: cleanupSchema, Timeout: budget,
		MaxTokens: max(256, llm.EstimateTokens(req.Text)*2+64),
	}, &out, -1)
	res.Usage = resp.Usage
	if err != nil {
		switch {
		case bctx.Err() != nil && (ctx.Err() == nil || errors.Is(ctx.Err(), context.DeadlineExceeded)), llm.Code(err) == llm.CodeTimeout:
			res.Fallback = FallbackTimeout
		default:
			res.Fallback, res.Detail = FallbackError, Code(err)
		}
		return done()
	}
	cleaned := strings.TrimSpace(out.Text)
	if cleaned == "" {
		res.Fallback = FallbackEmpty
		return done()
	}
	if !plausibleCleanup(req.Text, cleaned) {
		res.Fallback = FallbackDiverged
		return done()
	}
	// The model's punctuation is kept; Polish only normalises spacing etc.
	res.Text = Polish(cleaned, req.Lang, PolishOptions{KeepFillers: true, CJKLatinSpace: c.Polish.CJKLatinSpace, NoTerminal: c.Polish.NoTerminal})
	res.LLM = true
	for _, v := range data.Vocabulary {
		if containsFold(req.Text, v) && !containsFold(res.Text, v) {
			res.Warnings = append(res.Warnings, WarnTermsLost)
			break
		}
	}
	return done()
}

func cleanVocab(v []string) []string {
	var out []string
	seen := map[string]bool{}
	total := 0
	for _, s := range v {
		s = strings.TrimSpace(s)
		if s == "" || seen[strings.ToLower(s)] {
			continue
		}
		total += len(s)
		if total > 1000 {
			break
		}
		seen[strings.ToLower(s)] = true
		out = append(out, s)
	}
	return out
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

// cleanTokens splits text into comparable tokens: lower-cased words for
// spaced scripts, single characters for CJK.
func cleanTokens(s string) []string {
	var out []string
	var w []rune
	flush := func() {
		if len(w) > 0 {
			out = append(out, strings.ToLower(string(w)))
			w = w[:0]
		}
	}
	for _, r := range s {
		switch {
		case isCJKRune(r) || unicode.Is(unicode.Hangul, r):
			flush()
			out = append(out, string(r))
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'':
			w = append(w, r)
		default:
			flush()
		}
	}
	flush()
	return out
}

// plausibleCleanup guards against a model that answered or rewrote the
// dictation instead of cleaning it: most output tokens must come from the
// input, and the output may not be much longer (or, for long input, far
// shorter) than the input.
func plausibleCleanup(raw, out string) bool {
	in, got := cleanTokens(raw), cleanTokens(out)
	if len(got) == 0 {
		return false
	}
	bag := map[string]int{}
	for _, t := range in {
		bag[t]++
	}
	hit := 0
	for _, t := range got {
		if bag[t] > 0 {
			bag[t]--
			hit++
		}
	}
	precision := float64(hit) / float64(len(got))
	ratio := float64(len(got)) / float64(max(len(in), 1))
	if precision < 0.7 {
		return false
	}
	if len(got) > len(in)+3 && ratio > 1.3 {
		return false
	}
	if len(in) >= 20 && ratio < 0.25 {
		return false
	}
	return true
}
