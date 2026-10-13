// Package lexicon is the pronunciation dictionary: user entries that say
// how a term is pronounced, applied to script text before synthesis.
//
// Matching is longest-match first and Unicode case-insensitive. In scripts
// written with spaces a term only matches whole words ("cat" not inside
// "concatenate"); in Han, kana, Thai and other unspaced scripts it matches
// anywhere ("重庆" inside "我在重庆工作").
//
// Only respelling entries change the text. IPA and CMU (ARPAbet) entries
// leave the text as is and come back as annotations, for engines with a
// phoneme input (MLXAudioG2P) to use. An inline "[[word|say]]" in the text
// is a one-off respelling that wins over the dictionary.
package lexicon

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/hehehai/AudioSmith/internal/text"
)

// Type is how an entry's replacement is written.
type Type string

const (
	Respelling Type = "respelling" // plain text the engine reads ("jif")
	IPA        Type = "ipa"        // International Phonetic Alphabet ("dʒɪf")
	CMU        Type = "cmu"        // ARPAbet as in the CMU dictionary ("JH IH1 F")
)

// Global is the scope of entries that apply to every language.
const Global = "global"

// Entry is one dictionary entry.
type Entry struct {
	Term        string `json:"term"`
	Replacement string `json:"replacement"`
	Type        Type   `json:"type"`
	Scope       string `json:"scope"` // Global or a BCP-47 language tag
	Enabled     bool   `json:"enabled"`
}

func (e Entry) typ() Type {
	if e.Type == "" {
		return Respelling
	}
	return e.Type
}

func (e Entry) scope() string {
	s := strings.TrimSpace(e.Scope)
	if s == "" || s == "*" || strings.EqualFold(s, Global) {
		return Global
	}
	return strings.ToLower(strings.ReplaceAll(s, "_", "-"))
}

// InScope reports whether the entry applies to language lang.
func (e Entry) InScope(lang string) bool {
	sc := e.scope()
	if sc == Global {
		return true
	}
	lang = strings.ToLower(strings.ReplaceAll(lang, "_", "-"))
	if strings.Contains(sc, "-") {
		return lang == sc || strings.HasPrefix(lang, sc+"-")
	}
	base, _, _ := strings.Cut(lang, "-")
	return base == sc
}

// Fold is the case-insensitive form of s used for matching: NFC, then
// every rune mapped to its simple case fold.
func Fold(s string) string {
	s = norm.NFC.String(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		b.WriteRune(foldRune(r))
	}
	return b.String()
}

func foldRune(r rune) rune {
	if r < utf8.RuneSelf {
		if 'A' <= r && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}
	return unicode.ToLower(unicode.ToUpper(r))
}

// Dictionary is a compiled set of entries for one language.
type Dictionary struct {
	root *node
	lang string
}

type node struct {
	next  map[rune]*node
	entry *Entry
	rank  int // 1 = global, 2 = language scope
}

// Compile builds the matcher for language lang from the enabled entries in
// scope. A language-scoped entry wins over a global one for the same term;
// among equals, the later entry wins.
func Compile(entries []Entry, lang string) *Dictionary {
	d := &Dictionary{root: &node{}, lang: lang}
	for i := range entries {
		e := entries[i]
		term := Fold(strings.TrimSpace(e.Term))
		if !e.Enabled || term == "" || !e.InScope(lang) {
			continue
		}
		if e.typ() == Respelling && strings.TrimSpace(e.Replacement) == "" {
			continue
		}
		rank := 1
		if e.scope() != Global {
			rank = 2
		}
		n := d.root
		for _, r := range term {
			if n.next == nil {
				n.next = map[rune]*node{}
			}
			c := n.next[r]
			if c == nil {
				c = &node{}
				n.next[r] = c
			}
			n = c
		}
		if n.entry == nil || rank >= n.rank {
			n.entry, n.rank = &e, rank
		}
	}
	return d
}

// Match is one replacement or annotation made by Apply.
type Match struct {
	Src    text.Span // in the input
	Out    text.Span // in Result.Text
	Term   string    // the input text matched
	Entry  Entry     // the entry used; for an inline override Type is Respelling, Scope ""
	Inline bool      // from "[[word|say]]"
}

// Result is the output of Apply.
type Result struct {
	Text    string
	Matches []Match
}

// Annotations are the IPA and CMU matches: their text is unchanged and
// Out spans it in Result.Text.
func (r Result) Annotations() []Match {
	var out []Match
	for _, m := range r.Matches {
		if !m.Inline && m.Entry.typ() != Respelling {
			out = append(out, m)
		}
	}
	return out
}

// Apply rewrites s: respelling entries and "[[word|say]]" overrides are
// substituted, IPA/CMU entries are recorded as annotations, other bracket
// tags are kept verbatim and never matched inside. A nil dictionary
// applies only the inline overrides.
func (d *Dictionary) Apply(s string) Result {
	var res Result
	var b strings.Builder
	b.Grow(len(s))
	emit := func(src text.Span, out string, term string, e Entry, inline bool) {
		start := b.Len()
		b.WriteString(out)
		res.Matches = append(res.Matches, Match{Src: src, Out: text.Span{Start: start, End: b.Len()}, Term: term, Entry: e, Inline: inline})
	}
	prev := 0
	for _, t := range text.Tags(s) {
		d.applyRun(s, prev, t.Start, &b, emit)
		raw := t.Of(s)
		if word, say, ok := text.SplitOverride(raw); ok && word != "" && say != "" {
			emit(t, say, word, Entry{Term: word, Replacement: say, Type: Respelling, Enabled: true}, true)
		} else {
			b.WriteString(raw)
		}
		prev = t.End
	}
	d.applyRun(s, prev, len(s), &b, emit)
	res.Text = b.String()
	return res
}

func (d *Dictionary) applyRun(s string, from, to int, b *strings.Builder, emit func(text.Span, string, string, Entry, bool)) {
	if d == nil || d.root.next == nil {
		b.WriteString(s[from:to])
		return
	}
	i := from
	copied := from
	for i < to {
		_, n := utf8.DecodeRuneInString(s[i:to])
		if end, e := d.longest(s, i, to); e != nil {
			b.WriteString(s[copied:i])
			term := s[i:end]
			if e.typ() == Respelling {
				emit(text.Span{Start: i, End: end}, e.Replacement, term, *e, false)
			} else {
				emit(text.Span{Start: i, End: end}, term, term, *e, false)
			}
			i, copied = end, end
			continue
		}
		i += n
	}
	b.WriteString(s[copied:to])
}

// longest returns the end of the longest entry matching at i, honouring
// word boundaries in spaced scripts.
func (d *Dictionary) longest(s string, i, to int) (int, *Entry) {
	first, _ := utf8.DecodeRuneInString(s[i:to])
	if i > 0 {
		prev, _ := utf8.DecodeLastRuneInString(s[:i])
		if joined(prev, first) || isMark(first) {
			return 0, nil
		}
	}
	n := d.root
	bestEnd, best := 0, (*Entry)(nil)
	last := rune(0)
	for j := i; j < to; {
		r, sz := utf8.DecodeRuneInString(s[j:to])
		c := n.next[foldRune(r)]
		if c == nil {
			break
		}
		n, last = c, r
		j += sz
		if n.entry != nil {
			if j < to {
				next, _ := utf8.DecodeRuneInString(s[j:to])
				if joined(last, next) || isMark(next) {
					continue
				}
			}
			bestEnd, best = j, n.entry
		}
		if n.next == nil {
			break
		}
	}
	return bestEnd, best
}

// joined: a and b are both word characters of spaced scripts, so a match
// may not start or end between them.
func joined(a, b rune) bool {
	return text.IsWordRune(a) && text.IsWordRune(b) && !text.IsUnspaced(a) && !text.IsUnspaced(b)
}

func isMark(r rune) bool { return unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r) }

// ---- validation ----

// Issue codes; Params: "term", and "char" / "phoneme" / "type" where relevant.
const (
	CodeEmptyTerm        = "lexicon.empty_term"
	CodeEmptyReplacement = "lexicon.empty_replacement"
	CodeDuplicate        = "lexicon.duplicate" // also "first": index of the earlier entry
	CodeBadType          = "lexicon.bad_type"
	CodeBadIPA           = "lexicon.bad_ipa"
	CodeBadCMU           = "lexicon.bad_cmu"
)

// Issue is a problem with entry Index.
type Issue struct {
	Index  int
	Code   string
	Params map[string]string
}

// Validate checks entries: empty fields, unknown types, invalid IPA or
// ARPAbet, and enabled duplicates (same folded term and scope).
func Validate(entries []Entry) []Issue {
	var out []Issue
	add := func(i int, code string, kv ...string) {
		p := map[string]string{"term": entries[i].Term}
		for k := 0; k+1 < len(kv); k += 2 {
			p[kv[k]] = kv[k+1]
		}
		out = append(out, Issue{Index: i, Code: code, Params: p})
	}
	seen := map[[2]string]int{}
	for i, e := range entries {
		term := strings.TrimSpace(e.Term)
		if term == "" {
			add(i, CodeEmptyTerm)
			continue
		}
		rep := strings.TrimSpace(e.Replacement)
		if rep == "" {
			add(i, CodeEmptyReplacement)
		}
		switch e.typ() {
		case Respelling:
		case IPA:
			if c, ok := checkIPA(rep); !ok && rep != "" {
				add(i, CodeBadIPA, "char", c)
			}
		case CMU:
			if p, ok := checkCMU(rep); !ok && rep != "" {
				add(i, CodeBadCMU, "phoneme", p)
			}
		default:
			add(i, CodeBadType, "type", string(e.Type))
		}
		if e.Enabled {
			k := [2]string{Fold(term), e.scope()}
			if j, dup := seen[k]; dup {
				add(i, CodeDuplicate, "first", strconv.Itoa(j))
			} else {
				seen[k] = i
			}
		}
	}
	return out
}

// checkIPA accepts IPA letters, diacritics, suprasegmentals, tone letters
// and separators, optionally wrapped in /…/ or […]. It returns the first
// offending character.
func checkIPA(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && ((s[0] == '/' && s[len(s)-1] == '/') || (s[0] == '[' && s[len(s)-1] == ']')) {
		s = s[1 : len(s)-1]
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r == ' ' || r == '.' || r == '-' || r == '|' || r == '‖' || r == '‿' || r == '↗' || r == '↘':
		case r >= 0x00E0 && r <= 0x024F && unicode.IsLower(r): // æ ç ð ø œ ŋ ħ …
		case r >= 0x0250 && r <= 0x036F: // IPA extensions, modifier letters, combining marks
		case r >= 0x03B0 && r <= 0x03C9: // β θ χ
		case r >= 0x1D00 && r <= 0x1DFF: // phonetic extensions
		case r == 0x2C71 || r == 0xA71B || r == 0xA71C: // ⱱ, ꜛ ꜜ
		default:
			return string(r), false
		}
	}
	return "", true
}

var arpabet = map[string]bool{
	"AA": true, "AE": true, "AH": true, "AO": true, "AW": true, "AY": true, "EH": true, "ER": true, "EY": true,
	"IH": true, "IY": true, "OW": true, "OY": true, "UH": true, "UW": true, // vowels (take a stress digit)
	"B": false, "CH": false, "D": false, "DH": false, "F": false, "G": false, "HH": false, "JH": false, "K": false,
	"L": false, "M": false, "N": false, "NG": false, "P": false, "R": false, "S": false, "SH": false, "T": false,
	"TH": false, "V": false, "W": false, "Y": false, "Z": false, "ZH": false,
}

// checkCMU accepts space-separated ARPAbet phonemes; vowels may carry a
// stress digit 0–2. It returns the first offending phoneme.
func checkCMU(s string) (string, bool) {
	for _, p := range strings.Fields(s) {
		u := strings.ToUpper(p)
		base := u
		if n := len(u); n > 1 && u[n-1] >= '0' && u[n-1] <= '2' {
			base = u[:n-1]
			if vowel, ok := arpabet[base]; !ok || !vowel {
				return p, false
			}
			continue
		}
		if _, ok := arpabet[base]; !ok {
			return p, false
		}
	}
	return "", true
}

// ---- JSON import/export ----

// FileVersion is the version Export writes.
const FileVersion = 1

// File is the exported JSON document.
type File struct {
	Format  string  `json:"format"` // "audiosmith.pronunciation"
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

// Export encodes entries as an indented JSON File.
func Export(entries []Entry) ([]byte, error) {
	if entries == nil {
		entries = []Entry{}
	}
	return json.MarshalIndent(File{Format: "audiosmith.pronunciation", Version: FileVersion, Entries: entries}, "", "  ")
}

// Mode is how Import combines imported entries with existing ones.
type Mode uint8

const (
	// Merge keeps existing entries; an imported entry with the same folded
	// term and scope replaces the existing one in place.
	Merge Mode = iota
	// Replace discards the existing entries.
	Replace
)

// ImportReport counts what Import did.
type ImportReport struct {
	Added, Updated, Skipped int
}

// ErrFormat is returned for JSON that is none of the accepted shapes.
var ErrFormat = errors.New("lexicon: unrecognised pronunciation file")

// Import decodes data and combines it with existing according to mode.
// Accepted shapes: a File object ({"entries": […]}), a bare array of
// entries, or a plain object of term → respelling ({"GIF": "jif"}, the
// audiobook lexicon). Missing fields default to type respelling, scope
// global and enabled true. Entries without a term are skipped.
func Import(data []byte, existing []Entry, mode Mode) ([]Entry, ImportReport, error) {
	var rep ImportReport
	in, err := decode(data)
	if err != nil {
		return existing, rep, err
	}
	var out []Entry
	if mode == Merge {
		out = slices.Clone(existing)
	}
	index := map[[2]string]int{}
	for i, e := range out {
		index[[2]string{Fold(strings.TrimSpace(e.Term)), e.scope()}] = i
	}
	for _, e := range in {
		e.Term = strings.TrimSpace(e.Term)
		if e.Term == "" {
			rep.Skipped++
			continue
		}
		k := [2]string{Fold(e.Term), e.scope()}
		if i, ok := index[k]; ok {
			if out[i] != e {
				out[i] = e
				rep.Updated++
			} else {
				rep.Skipped++
			}
			continue
		}
		index[k] = len(out)
		out = append(out, e)
		rep.Added++
	}
	return out, rep, nil
}

type wireEntry struct {
	Term        string `json:"term"`
	Word        string `json:"word"` // alias seen in other tools
	Replacement string `json:"replacement"`
	Say         string `json:"say"` // alias
	Type        Type   `json:"type"`
	Scope       string `json:"scope"`
	Language    string `json:"language"` // alias for scope
	Enabled     *bool  `json:"enabled"`
}

func (w wireEntry) entry() Entry {
	e := Entry{Term: w.Term, Replacement: w.Replacement, Type: w.Type, Scope: w.Scope, Enabled: true}
	if e.Term == "" {
		e.Term = w.Word
	}
	if e.Replacement == "" {
		e.Replacement = w.Say
	}
	if e.Scope == "" {
		e.Scope = w.Language
	}
	if e.Scope == "" {
		e.Scope = Global
	}
	if e.Type == "" {
		e.Type = Respelling
	}
	e.Type = Type(strings.ToLower(string(e.Type)))
	if w.Enabled != nil {
		e.Enabled = *w.Enabled
	}
	return e
}

func decode(data []byte) ([]Entry, error) {
	data = bytes.TrimPrefix(bytes.TrimSpace(data), []byte("\xef\xbb\xbf"))
	if len(data) == 0 {
		return nil, ErrFormat
	}
	conv := func(ws []wireEntry) []Entry {
		out := make([]Entry, len(ws))
		for i, w := range ws {
			out[i] = w.entry()
		}
		return out
	}
	switch data[0] {
	case '[':
		var ws []wireEntry
		if err := json.Unmarshal(data, &ws); err != nil {
			return nil, errors.Join(ErrFormat, err)
		}
		return conv(ws), nil
	case '{':
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(data, &probe); err != nil {
			return nil, errors.Join(ErrFormat, err)
		}
		if raw, ok := probe["entries"]; ok {
			var ws []wireEntry
			if err := json.Unmarshal(raw, &ws); err != nil {
				return nil, errors.Join(ErrFormat, err)
			}
			return conv(ws), nil
		}
		var m map[string]string
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, errors.Join(ErrFormat, err)
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		out := make([]Entry, 0, len(m))
		for _, k := range keys {
			out = append(out, Entry{Term: k, Replacement: m[k], Type: Respelling, Scope: Global, Enabled: true})
		}
		return out, nil
	}
	return nil, ErrFormat
}
