package translate

import (
	"context"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hehehai/AudioSmith/internal/llm"
)

// Term is one glossary entry: Source is always rendered as Target.
type Term struct {
	Source        string `json:"source"`
	Target        string `json:"target"`
	Note          string `json:"note,omitempty"`
	Auto          bool   `json:"auto,omitempty"`          // proposed by Extract, not entered by the user
	CaseSensitive bool   `json:"caseSensitive,omitempty"` // match Source's case exactly ("Apple" the company vs "apple")
}

// Glossary is a project's terms. Manual terms win over automatic ones.
type Glossary []Term

func foldKey(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// Normalize trims every term, drops empty ones and resolves duplicate
// sources (same text ignoring case and spacing): a manual term beats an
// automatic one, otherwise the first wins. Order is kept.
func (g Glossary) Normalize() Glossary {
	out := make(Glossary, 0, len(g))
	idx := map[string]int{}
	for _, t := range g {
		t.Source, t.Target, t.Note = strings.TrimSpace(t.Source), strings.TrimSpace(t.Target), strings.TrimSpace(t.Note)
		if t.Source == "" || t.Target == "" {
			continue
		}
		k := foldKey(t.Source)
		if i, ok := idx[k]; ok {
			if out[i].Auto && !t.Auto {
				out[i] = t
			}
			continue
		}
		idx[k] = len(out)
		out = append(out, t)
	}
	return out
}

// Merge returns g plus the terms of auto (marked Auto) whose sources g
// lacks: manual wins.
func (g Glossary) Merge(auto Glossary) Glossary {
	all := slices.Clone(g)
	for _, t := range auto {
		t.Auto = true
		all = append(all, t)
	}
	return all.Normalize()
}

// Relevant returns the terms occurring in any of texts, longest source
// first (so "New York Times" is matched before "New York").
func (g Glossary) Relevant(texts ...string) Glossary {
	var out Glossary
	for _, t := range g.Normalize() {
		for _, s := range texts {
			if len(t.match(s)) > 0 {
				out = append(out, t)
				break
			}
		}
	}
	slices.SortStableFunc(out, func(a, b Term) int { return utf8.RuneCountInString(b.Source) - utf8.RuneCountInString(a.Source) })
	return out
}

// Count returns how many times t occurs in s.
func (t Term) Count(s string) int { return len(t.match(s)) }

// match returns the byte ranges of t.Source in s: case-insensitive unless
// CaseSensitive, on word boundaries for spaced scripts.
func (t Term) match(s string) [][2]int {
	src := t.Source
	if src == "" {
		return nil
	}
	hay, needle := s, src
	if !t.CaseSensitive {
		hay, needle = lowerSameLen(s), lowerSameLen(src)
	}
	var out [][2]int
	for off := 0; off < len(hay); {
		i := strings.Index(hay[off:], needle)
		if i < 0 {
			break
		}
		start, end := off+i, off+i+len(needle)
		if boundary(s, start, end) {
			out = append(out, [2]int{start, end})
			off = end
		} else {
			_, w := utf8.DecodeRuneInString(hay[start:])
			off = start + w
		}
	}
	return out
}

// lowerSameLen lower-cases s rune by rune where that keeps the byte
// length (so offsets stay valid), leaving other runes alone.
func lowerSameLen(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		l := unicode.ToLower(r)
		if utf8.RuneLen(l) != utf8.RuneLen(r) {
			l = r
		}
		b.WriteRune(l)
	}
	return b.String()
}

// boundary reports whether s[start:end] is a whole word: the runes outside
// it are not letters or digits, unless the term's edge is unspaced script
// (CJK terms match anywhere).
func boundary(s string, start, end int) bool {
	first, _ := utf8.DecodeRuneInString(s[start:end])
	last, _ := utf8.DecodeLastRuneInString(s[start:end])
	if start > 0 && !isUnspacedRune(first) {
		prev, _ := utf8.DecodeLastRuneInString(s[:start])
		if isWordRune(prev) && !isUnspacedRune(prev) {
			return false
		}
	}
	if end < len(s) && !isUnspacedRune(last) {
		next, _ := utf8.DecodeRuneInString(s[end:])
		if isWordRune(next) && !isUnspacedRune(next) {
			return false
		}
	}
	return true
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r)
}

func isUnspacedRune(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Thai, r) || unicode.Is(unicode.Lao, r) || unicode.Is(unicode.Khmer, r) || unicode.Is(unicode.Myanmar, r)
}

// Candidate is a term proposed by Extract.
type Candidate struct {
	Term
	Count int `json:"count"` // occurrences in the transcript (counted locally)
}

// Extractor proposes glossary terms with the glossary_extract skill.
type Extractor struct {
	Router *llm.Router
	Max    int // terms returned (0: 40)
	// ChunkChars splits long transcripts (0: 12000 characters per call).
	ChunkChars int
}

var termsSchema = func() *llm.Schema {
	s := llm.Object(map[string]*llm.Schema{
		"terms": llm.Array(llm.Object(map[string]*llm.Schema{
			"source": llm.String(),
			"target": llm.String(),
			"note":   llm.String(),
			"count":  llm.Integer(),
		}, "source", "target")),
	}, "terms")
	s.Name = "glossary"
	return s
}()

// Extract asks the model for candidate terms in segments (source → target),
// skipping those already in existing. Candidates are checked against the
// transcript (a term that doesn't occur is dropped), counted locally and
// sorted by count. Merge them into a Glossary with Glossary.Merge after
// the user reviews them.
func (x *Extractor) Extract(ctx context.Context, source, target string, segments []Segment, existing Glossary, style string) ([]Candidate, llm.Usage, error) {
	var usage llm.Usage
	if !x.Router.Available(llm.SkillGlossaryExtract) {
		return nil, usage, newErr(CodeNoLLM, "skill", string(llm.SkillGlossaryExtract))
	}
	tgt, ok := parseLang(target)
	if !ok {
		return nil, usage, newErr(CodeLanguage, "lang", target)
	}
	src, srcKnown := parseLang(source)
	maxTerms := x.Max
	if maxTerms <= 0 {
		maxTerms = 40
	}
	chunk := x.ChunkChars
	if chunk <= 0 {
		chunk = 12000
	}
	var chunks []string
	var b strings.Builder
	var all strings.Builder
	for _, s := range segments {
		line := strings.TrimSpace(s.Text)
		if line == "" {
			continue
		}
		if b.Len() > 0 && b.Len()+len(line) > chunk {
			chunks = append(chunks, b.String())
			b.Reset()
		}
		b.WriteString(line + "\n")
		all.WriteString(line + "\n")
	}
	if b.Len() > 0 {
		chunks = append(chunks, b.String())
	}
	if len(chunks) == 0 {
		return nil, usage, newErr(CodeNoSegments)
	}
	transcript := all.String()
	known := existing.Normalize()
	seen := map[string]int{}
	var out []Candidate
	for _, c := range chunks {
		data := promptData{Source: src, SourceKnown: srcKnown, Target: tgt, Glossary: known, Style: clipRunes(style, MaxStyle), Max: maxTerms, Transcript: c}
		msgs, err := messages("glossary", data)
		if err != nil {
			return nil, usage, err
		}
		var res struct {
			Terms []struct {
				Source, Target, Note string
			} `json:"terms"`
		}
		resp, err := x.Router.JSON(ctx, llm.SkillGlossaryExtract, llm.Request{Messages: msgs, Schema: termsSchema}, &res, 0)
		usage = usage.Add(resp.Usage)
		if err != nil {
			if len(out) > 0 && !isCancel(ctx, err) {
				break // keep what earlier chunks found
			}
			return nil, usage, err
		}
		for _, t := range res.Terms {
			term := Term{Source: strings.TrimSpace(t.Source), Target: strings.TrimSpace(t.Target), Note: strings.TrimSpace(t.Note), Auto: true}
			if term.Source == "" || term.Target == "" {
				continue
			}
			k := foldKey(term.Source)
			if _, dup := seen[k]; dup {
				continue
			}
			if slices.ContainsFunc(known, func(e Term) bool { return foldKey(e.Source) == k }) {
				continue
			}
			n := term.Count(transcript)
			if n == 0 {
				continue // not in the text: hallucinated or reformatted
			}
			seen[k] = len(out)
			out = append(out, Candidate{Term: term, Count: n})
		}
	}
	slices.SortStableFunc(out, func(a, b Candidate) int { return b.Count - a.Count })
	if len(out) > maxTerms {
		out = out[:maxTerms]
	}
	return out, usage, nil
}
