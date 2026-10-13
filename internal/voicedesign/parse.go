package voicedesign

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// MaxDescription is the longest description, in runes, that Parse reads
// and Lower passes on. The UI caps the text box at this length.
const MaxDescription = 2000

// Match is one phrase of the description that maps to a value.
type Match struct {
	Category Category `json:"category"`
	Value    string   `json:"value"`
	Phrase   string   `json:"phrase"` // the text as written
	Start    int      `json:"start"`  // byte offsets into the description
	End      int      `json:"end"`
	// Applied is false when the phrase was negated ("not deep"), later
	// overridden by another phrase of the same category, or a weak default
	// ("boy" → child) that a stronger phrase made moot.
	Applied bool   `json:"applied"`
	Note    string `json:"note,omitempty"` // "negated", "overridden", "default"
}

// Term is a word or phrase that the parser could not map.
type Term struct {
	Text  string `json:"text"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

// Result is what Parse found in a description.
type Result struct {
	Attrs     Attributes `json:"attrs"`
	Matched   []Match    `json:"matched"`
	Unmatched []Term     `json:"unmatched"`
	Truncated bool       `json:"truncated,omitempty"` // text beyond MaxDescription was ignored
}

// UnmatchedText joins the unmapped terms for "Couldn't map: …".
func (r Result) UnmatchedText() string {
	parts := make([]string, len(r.Unmatched))
	for i, t := range r.Unmatched {
		parts[i] = t.Text
	}
	return strings.Join(parts, ", ")
}

type ruleKind uint8

const (
	kindMap ruleKind = iota
	kindStop
	kindNeg
	kindInt
)

type assign struct {
	cat  Category
	val  string
	weak bool
}

type rule struct {
	words   []string // English: words; Chinese: one element, the phrase
	prefix  bool     // last word matches as a prefix
	kind    ruleKind
	assigns []assign
}

type lexicon struct {
	en       map[string][]*rule // by first word, longest first
	enPrefix []*rule            // single-word prefix rules
	zh       map[rune][]*rule   // by first rune, longest first
}

var lex = sync.OnceValue(func() *lexicon {
	l, err := buildLexicon(lexiconEN, lexiconZH)
	if err != nil {
		panic(err) // caught by TestLexicon
	}
	return l
})

func buildLexicon(en, zh string) (*lexicon, error) {
	l := &lexicon{en: map[string][]*rule{}, zh: map[rune][]*rule{}}
	seen := map[string]bool{}
	parse := func(src string, chinese bool) error {
		for n, line := range strings.Split(src, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			left, right, ok := strings.Cut(line, ":")
			if !ok {
				return errf("lexicon line %d: no ':'", n)
			}
			proto := rule{}
			switch left {
			case "!stop":
				proto.kind = kindStop
			case "!neg":
				proto.kind = kindNeg
			case "!int":
				proto.kind = kindInt
			default:
				for _, a := range strings.Split(left, ",") {
					weak := strings.Contains(a, "~")
					c, v, ok := strings.Cut(a, "=")
					if weak {
						c, v, ok = strings.Cut(a, "~")
					}
					if !ok || !Valid(Category(c), v) || v == "" {
						return errf("lexicon line %d: bad assignment %q", n, a)
					}
					proto.assigns = append(proto.assigns, assign{Category(c), v, weak})
				}
			}
			for _, ph := range strings.Split(right, "|") {
				ph = strings.TrimSpace(ph)
				if ph == "" {
					continue
				}
				key := map[bool]string{false: "en:", true: "zh:"}[chinese] + ph
				if seen[key] {
					return errf("lexicon: duplicate phrase %q", ph)
				}
				seen[key] = true
				r := proto
				if chinese {
					r.words = []string{ph}
					first, _ := utf8.DecodeRuneInString(ph)
					l.zh[first] = append(l.zh[first], &r)
					continue
				}
				if strings.HasSuffix(ph, "*") {
					r.prefix = true
					ph = strings.TrimSuffix(ph, "*")
				}
				r.words = strings.Fields(ph)
				if r.prefix && len(r.words) == 1 {
					l.enPrefix = append(l.enPrefix, &r)
				} else {
					l.en[r.words[0]] = append(l.en[r.words[0]], &r)
				}
			}
		}
		return nil
	}
	if err := parse(en, false); err != nil {
		return nil, err
	}
	if err := parse(zh, true); err != nil {
		return nil, err
	}
	longest := func(a, b *rule) int {
		if c := cmp.Compare(len(b.words), len(a.words)); c != 0 {
			return c
		}
		return cmp.Compare(len(b.words[0]), len(a.words[0]))
	}
	for _, rs := range l.en {
		slices.SortStableFunc(rs, longest)
	}
	for _, rs := range l.zh {
		slices.SortStableFunc(rs, longest)
	}
	slices.SortStableFunc(l.enPrefix, longest)
	return l, nil
}

func errf(format string, args ...any) error {
	return fmt.Errorf("voicedesign: "+format, args...)
}

// token is a unit of the input: an English word, one CJK rune, a number,
// or a separator.
type token struct {
	text       string // normalised: lower case, ’ → '
	start, end int
	cjk        bool
	punct      bool // hard separator: , . ; ! ? and their CJK forms
}

func isCJK(r rune) bool {
	return r >= 0x2E80 && (unicode.IsLetter(r) || unicode.Is(unicode.Han, r))
}

func isWordRune(r rune) bool {
	return !isCJK(r) && (unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r))
}

func tokenize(s string) []token {
	var toks []token
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case isCJK(r):
			toks = append(toks, token{text: string(r), start: i, end: i + n, cjk: true})
			i += n
			continue
		case isWordRune(r):
			j := i
			var b strings.Builder
			for j < len(s) {
				r2, n2 := utf8.DecodeRuneInString(s[j:])
				if r2 == '\'' || r2 == '’' {
					// apostrophe inside a word ("smoker's", "xi'an")
					if j > i && j+n2 < len(s) {
						r3, _ := utf8.DecodeRuneInString(s[j+n2:])
						if isWordRune(r3) {
							b.WriteByte('\'')
							j += n2
							continue
						}
					}
					break
				}
				if !isWordRune(r2) {
					break
				}
				b.WriteRune(unicode.ToLower(r2))
				j += n2
			}
			toks = append(toks, token{text: b.String(), start: i, end: j})
			i = j
			continue
		}
		if strings.ContainsRune(",.;:!?()[]{}\"“”「」『』《》，。；：！？（）【】、…\n", r) {
			toks = append(toks, token{text: string(r), start: i, end: i + n, punct: true})
		}
		// spaces, hyphens, slashes and other symbols are soft separators
		i += n
	}
	return toks
}

// piece is a run of tokens the matcher consumed together.
type piece struct {
	from, to int // token indexes [from, to)
	kind     ruleKind
	assigns  []assign
	unmapped bool
	punct    bool
}

// Parse maps a free-text description (English, Chinese or mixed) to
// attributes. It is deterministic and fast (no allocation-heavy steps;
// microseconds for typical input), so the UI may call it on every
// debounced keystroke.
func Parse(description string) Result {
	res := Result{Matched: []Match{}, Unmatched: []Term{}}
	if n := utf8.RuneCountInString(description); n > MaxDescription {
		cut := 0
		for i := range description {
			if cut == MaxDescription {
				description = description[:i]
				break
			}
			cut++
		}
		res.Truncated = true
	}
	toks := tokenize(description)
	pieces := matchPieces(toks)

	// Negation, intensifiers, and the matches in text order.
	type pending struct {
		m      Match
		assign assign
	}
	var found []pending
	negAt := -1 // piece index of the last negator, -1 = none
	mapsSinceNeg := 0
	for pi := 0; pi < len(pieces); pi++ {
		p := pieces[pi]
		switch {
		case p.punct:
			negAt = -1
			continue
		case p.kind == kindNeg:
			negAt, mapsSinceNeg = pi, 0
			continue
		case p.unmapped, p.kind == kindStop, p.kind == kindInt:
			continue
		}
		// a mapped phrase
		start, end := toks[p.from].start, toks[p.to-1].end
		assigns := p.assigns
		// intensifier directly before (possibly repeated: "very very deep")
		k := pi - 1
		for k >= 0 && pieces[k].kind == kindInt && !pieces[k].unmapped && !pieces[k].punct {
			k--
		}
		if k < pi-1 {
			if up, ok := intensify(assigns); ok {
				assigns = up
				start = toks[pieces[k+1].from].start
			}
		}
		negated := negAt >= 0 && pi-negAt <= 4 && mapsSinceNeg == 0
		if negAt >= 0 {
			mapsSinceNeg++
		}
		for _, a := range assigns {
			m := Match{Category: a.cat, Value: a.val, Phrase: description[start:end], Start: start, End: end}
			if negated {
				m.Note = "negated"
			}
			found = append(found, pending{m, a})
		}
	}

	// Apply strong assignments in order, then weak ones where nothing
	// strong decided.
	var attrs Attributes
	setBy := map[Category]int{} // category → index in found of the value in force
	for i := range found {
		f := &found[i]
		if f.m.Note != "" || f.assign.weak {
			continue
		}
		next, cleared, _ := attrs.With(f.assign.cat, f.assign.val)
		if f.assign.cat != Style {
			if j, ok := setBy[f.assign.cat]; ok && found[j].m.Value != f.assign.val {
				found[j].m.Applied, found[j].m.Note = false, "overridden"
			}
			setBy[f.assign.cat] = i
		}
		for _, c := range cleared {
			for j := range found {
				if j != i && found[j].m.Applied && found[j].m.Category == c.Category && found[j].m.Value == c.ID {
					found[j].m.Applied, found[j].m.Note = false, "overridden"
				}
			}
			delete(setBy, c.Category)
		}
		attrs = next
		f.m.Applied = true
	}
	strong := attrs
	for i := range found {
		f := &found[i]
		if f.m.Note != "" || !f.assign.weak {
			continue
		}
		decided := len(strong.Get(f.assign.cat)) > 0 ||
			(f.assign.cat == Accent && strong.Dialect != "") ||
			(f.assign.cat == Dialect && strong.Accent != "")
		if decided {
			f.m.Note = "default"
			continue
		}
		next, cleared, _ := attrs.With(f.assign.cat, f.assign.val)
		if len(cleared) > 0 && !(len(cleared) == 1 && cleared[0].Category == f.assign.cat) {
			f.m.Note = "default" // would contradict a stronger pick
			continue
		}
		if j, ok := setBy[f.assign.cat]; ok && found[j].m.Value != f.assign.val {
			found[j].m.Applied, found[j].m.Note = false, "overridden"
		}
		setBy[f.assign.cat] = i
		attrs = next
		f.m.Applied = true
	}
	for _, f := range found {
		if f.m.Note == "" && !f.m.Applied {
			f.m.Note = "overridden"
		}
		res.Matched = append(res.Matched, f.m)
	}
	res.Attrs = attrs.Canonical()

	// Unmapped terms: adjacent unmapped words (English) or runes (CJK)
	// merge into one term; punctuation, mapped and filler words split them.
	for pi := 0; pi < len(pieces); {
		if !pieces[pi].unmapped {
			pi++
			continue
		}
		cjk := toks[pieces[pi].from].cjk
		pj := pi + 1
		for pj < len(pieces) && pieces[pj].unmapped && toks[pieces[pj].from].cjk == cjk &&
			(!cjk || toks[pieces[pj].from].start == toks[pieces[pj-1].to-1].end) {
			pj++
		}
		start, end := toks[pieces[pi].from].start, toks[pieces[pj-1].to-1].end
		res.Unmatched = append(res.Unmatched, Term{Text: description[start:end], Start: start, End: end})
		pi = pj
	}
	return res
}

// intensify upgrades pitch and pace one step outwards.
func intensify(as []assign) ([]assign, bool) {
	up := map[string]string{"low": "very_low", "high": "very_high", "slow": "very_slow", "fast": "very_fast"}
	out := slices.Clone(as)
	changed := false
	for i, a := range out {
		if (a.cat == Pitch || a.cat == Pace) && up[a.val] != "" {
			out[i].val = up[a.val]
			changed = true
		}
	}
	return out, changed
}

func matchPieces(toks []token) []piece {
	l := lex()
	var out []piece
	for i := 0; i < len(toks); {
		t := toks[i]
		if t.punct {
			out = append(out, piece{from: i, to: i + 1, punct: true})
			i++
			continue
		}
		if p, ok := matchAge(toks, i); ok {
			out = append(out, p)
			i = p.to
			continue
		}
		if t.cjk {
			if r, n := matchZH(l, toks, i); r != nil {
				out = append(out, piece{from: i, to: i + n, kind: r.kind, assigns: r.assigns})
				i += n
				continue
			}
		} else if r, n := matchEN(l, toks, i); r != nil {
			out = append(out, piece{from: i, to: i + n, kind: r.kind, assigns: r.assigns})
			i += n
			continue
		}
		out = append(out, piece{from: i, to: i + 1, unmapped: true})
		i++
	}
	return out
}

func matchEN(l *lexicon, toks []token, i int) (*rule, int) {
	try := func(r *rule) int {
		if i+len(r.words) > len(toks) {
			return 0
		}
		for k, w := range r.words {
			t := toks[i+k]
			if t.cjk || t.punct {
				return 0
			}
			last := k == len(r.words)-1
			if last && r.prefix {
				if !strings.HasPrefix(t.text, w) {
					return 0
				}
			} else if t.text != w {
				return 0
			}
		}
		return len(r.words)
	}
	var best *rule
	bestN := 0
	for _, r := range l.en[toks[i].text] {
		if n := try(r); n > bestN || (n == bestN && n > 0 && best.prefix && !r.prefix) {
			best, bestN = r, n
		}
	}
	if bestN == 0 {
		// whole-word prefix rules ("whisper*"); exact words win above
		for _, r := range l.enPrefix {
			if try(r) == 1 {
				return r, 1
			}
		}
	}
	return best, bestN
}

func matchZH(l *lexicon, toks []token, i int) (*rule, int) {
	first := []rune(toks[i].text)[0]
	for _, r := range l.zh[first] {
		ph := r.words[0]
		n := 0
		ok := true
		for _, want := range ph {
			if i+n >= len(toks) || !toks[i+n].cjk || []rune(toks[i+n].text)[0] != want ||
				(n > 0 && toks[i+n].start != toks[i+n-1].end) {
				ok = false
				break
			}
			n++
		}
		if ok {
			return r, n
		}
	}
	return nil, 0
}

// matchAge reads ages written as numbers: "40-year-old", "in her 40s",
// "aged 70", "35岁", "四十多岁".
func matchAge(toks []token, i int) (piece, bool) {
	num, rest, n := -1, "", 0
	if t := toks[i]; !t.cjk && !t.punct {
		num, rest = leadingInt(t.text)
		n = 1
	} else if t.cjk {
		num, n = zhNumber(toks, i)
	}
	if num < 0 || n == 0 {
		return piece{}, false
	}
	j := i + n // next token
	// Chinese suffix: [多|来|几]? 岁 (directly attached)
	if rest == "" {
		k := j
		if k < len(toks) && toks[k].cjk && toks[k].start == toks[k-1].end && strings.Contains("多来几", toks[k].text) {
			k++
		}
		if k < len(toks) && toks[k].cjk && toks[k].text == "岁" && toks[k].start == toks[k-1].end {
			return agePiece(i, k+1, num), true
		}
	}
	if toks[i].cjk {
		return piece{}, false
	}
	switch {
	case rest == "s" && num >= 10 && num%10 == 0 && num < 100: // "40s"
		return agePiece(i, j, num), true
	case rest == "yo":
		return agePiece(i, j, num), true
	case rest != "":
		return piece{}, false
	}
	if j+1 < len(toks) && (toks[j].text == "year" || toks[j].text == "years" || toks[j].text == "yr") && toks[j+1].text == "old" {
		return agePiece(i, j+2, num), true
	}
	if j < len(toks) && toks[j].text == "yo" {
		return agePiece(i, j+1, num), true
	}
	if i > 0 && (toks[i-1].text == "aged" || toks[i-1].text == "age") {
		return agePiece(i, j, num), true
	}
	return piece{}, false
}

// zhNumber reads a Chinese numeral below 100 ("七", "十五", "四十", "两")
// from consecutive CJK tokens.
func zhNumber(toks []token, i int) (int, int) {
	total, cur, n := 0, 0, 0
	for i+n < len(toks) && toks[i+n].cjk && (n == 0 || toks[i+n].start == toks[i+n-1].end) {
		r := []rune(toks[i+n].text)[0]
		if r == '十' {
			if cur == 0 {
				cur = 1
			}
			total += cur * 10
			cur = 0
		} else if d := strings.IndexRune("零一二三四五六七八九", r); d >= 0 {
			cur = len([]rune("零一二三四五六七八九"[:d]))
		} else if r == '两' {
			cur = 2
		} else {
			break
		}
		n++
	}
	if n == 0 {
		return -1, 0
	}
	return total + cur, n
}

func isASCIIDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func leadingInt(s string) (int, string) {
	j := 0
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	if j == 0 || j > 3 {
		return -1, s
	}
	v, _ := strconv.Atoi(s[:j])
	return v, s[j:]
}

func agePiece(from, to, years int) piece {
	return piece{from: from, to: to, kind: kindMap, assigns: []assign{{cat: Age, val: AgeForYears(years)}}}
}

// AgeForYears maps an age in years to the Age value.
func AgeForYears(y int) string {
	switch {
	case y < 13:
		return "child"
	case y < 20:
		return "teen"
	case y < 40:
		return "young_adult"
	case y < 60:
		return "middle_aged"
	}
	return "elderly"
}
