// Package markup parses AudioSmith's SSML-lite script markup into a typed
// token stream with diagnostics, for the Studio, Stories and Audiobook
// editors and their validation UIs. It does not lower the tokens for any
// engine; that is the synthesis layer's job.
//
// Grammar (tag names are case-insensitive, spaces inside brackets allowed):
//
//	# Title            chapter heading: "#" + space at the start of a line (H1 only; "##" is text)
//	[pause]            350 ms of silence
//	[pause 500ms]      explicit pause: N, Nms, Ns or N.Ns; capped at 10 s
//	[slow] … [/slow]   0.85× speed      [fast] … [/fast]   1.15× speed
//	[emphasis] … [/emphasis]            [spell] … [/spell] (letter by letter)
//	[voice:NAME]       switch voice; [voice:] or [voice:default] resets
//	[laughter] [sigh]… non-verbal tags (Options.NonVerbal), passed through
//	[[word|say]]       one-off pronunciation override
//	[对话] … [/对话]    a dialogue block (also [dialogue], 【对话】; dialogue.go)
//	blank line         paragraph break
//
// Any other bracket token is Unknown (it will be read aloud literally) and
// reported once per distinct tag.
package markup

import (
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/hehehai/AudioSmith/internal/text"
)

// Kind is a token's type.
type Kind uint8

const (
	Text      Kind = iota // spoken text: Text
	Pause                 // silence: Millis
	StyleOn               // [slow] [fast] [emphasis] [spell]: Name
	StyleOff              // [/slow] …: Name
	Voice                 // [voice:NAME]: Name ("" = default voice)
	Chapter               // "# Title": Name is the title (may be "")
	NonVerbal             // [laughter] …: Name (lower-case)
	Override              // [[word|say]]: Text = word, Say = respelling
	Paragraph             // blank line(s) between paragraphs
	Unknown               // unrecognised [tag]: Text = the raw tag, Name = inner text
	Dialogue              // a dialogue block's marker: Name "open" or "close" (dialogue.go); never spoken
)

var kindNames = [...]string{"text", "pause", "style_on", "style_off", "voice", "chapter", "non_verbal", "override", "paragraph", "unknown", "dialogue"}

func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "kind(" + strconv.Itoa(int(k)) + ")"
}

// Styles.
const (
	Slow     = "slow"
	Fast     = "fast"
	Emphasis = "emphasis"
	Spell    = "spell"
)

// Token is one element of a parsed script. Span points into the source.
type Token struct {
	Kind   Kind
	Span   text.Span
	Text   string
	Name   string
	Say    string
	Millis int
}

// Diagnostic codes. Params are listed next to each.
const (
	CodeUnknownTag    = "markup.unknown_tag"    // tag: the raw tag
	CodeEmptyChapter  = "markup.empty_chapter"  // title, index (0-based)
	CodeBadPause      = "markup.bad_pause"      // tag
	CodePauseClamped  = "markup.pause_clamped"  // tag, max_ms
	CodeUnclosedStyle = "markup.unclosed_style" // style
	CodeStrayClose    = "markup.stray_close"    // style
	CodeBadOverride   = "markup.bad_override"   // tag
	CodeUnknownVoice  = "markup.unknown_voice"  // voice (from Validate)
)

// DefaultNonVerbal is the non-verbal tag vocabulary recognised by default:
// OmniVoice's 13 tags plus [breath].
var DefaultNonVerbal = []string{
	"laughter", "sigh", "breath",
	"confirmation-en",
	"question-en", "question-ah", "question-oh", "question-ei", "question-yi",
	"surprise-ah", "surprise-oh", "surprise-wa", "surprise-yo",
	"dissatisfaction-hnn",
}

// Options configure Parse. The zero value is the default grammar.
type Options struct {
	// NonVerbal lists the non-verbal tags (case-insensitive); nil means
	// DefaultNonVerbal.
	NonVerbal []string
	// DefaultPauseMillis is the length of a bare [pause]; 0 means 350.
	DefaultPauseMillis int
	// MaxPauseMillis caps [pause N]; 0 means 10000.
	MaxPauseMillis int
	// NoChapters treats "# Title" lines as text (the Studio editor).
	NoChapters bool
}

// Document is a parsed script.
type Document struct {
	Source string
	Tokens []Token
	Diags  []text.Diagnostic
}

// Parse parses src. It never fails: malformed markup becomes text or
// Unknown tokens plus diagnostics.
func Parse(src string, opt *Options) *Document {
	var o Options
	if opt != nil {
		o = *opt
	}
	if o.DefaultPauseMillis == 0 {
		o.DefaultPauseMillis = 350
	}
	if o.MaxPauseMillis == 0 {
		o.MaxPauseMillis = 10000
	}
	nv := o.NonVerbal
	if nv == nil {
		nv = DefaultNonVerbal
	}
	p := parser{src: src, opt: o, nonVerbal: map[string]bool{}, reported: map[string]bool{}}
	for _, n := range nv {
		p.nonVerbal[strings.ToLower(n)] = true
	}
	p.run()
	return &Document{Source: src, Tokens: p.toks, Diags: p.diags}
}

type parser struct {
	src       string
	opt       Options
	nonVerbal map[string]bool
	toks      []Token
	diags     []text.Diagnostic
	reported  map[string]bool
	open      []Token // open styles
}

func (p *parser) diag(code string, sev text.Severity, sp text.Span, kv ...string) {
	params := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		params[kv[i]] = kv[i+1]
	}
	p.diags = append(p.diags, text.Diagnostic{Code: code, Severity: sev, Span: sp, Params: params})
}

func (p *parser) run() {
	src := p.src
	pendingBlank := false
	lineStart := 0
	for lineStart <= len(src) {
		end := strings.IndexByte(src[lineStart:], '\n')
		next := 0
		if end < 0 {
			end = len(src)
			next = len(src) + 1
		} else {
			end += lineStart
			next = end + 1
		}
		line := src[lineStart:end]
		body := strings.TrimRight(line, "\r")
		switch {
		case strings.TrimSpace(body) == "":
			if p.hasContent() {
				pendingBlank = true
			}
			// Keep the newline inside the previous text token so offsets
			// stay contiguous for editors.
		case !p.opt.NoChapters && text.IsHeading(body):
			p.closeStyles()
			title := strings.TrimSpace(body[1:])
			p.toks = append(p.toks, Token{Kind: Chapter, Span: text.Span{Start: lineStart, End: lineStart + len(body)}, Name: title})
			pendingBlank = false
		default:
			if pendingBlank {
				p.toks = append(p.toks, Token{Kind: Paragraph, Span: text.Span{Start: lineStart, End: lineStart}})
				pendingBlank = false
			}
			// Text lines keep their line break so a following line joins
			// with a space-equivalent rather than gluing words together.
			stop := end
			if next <= len(src) && end < len(src) {
				stop = end + 1
			}
			p.inline(lineStart, stop)
		}
		lineStart = next
	}
	p.closeStyles()
	p.mergeText()
	p.checkChapters()
}

func (p *parser) hasContent() bool {
	for i := len(p.toks) - 1; i >= 0; i-- {
		switch p.toks[i].Kind {
		case Chapter, Paragraph:
			return false
		default:
			return true
		}
	}
	return false
}

// inline tokenises src[start:end] (one line).
func (p *parser) inline(start, end int) {
	seg := p.src[start:end]
	prev := 0
	tags := text.Tags(seg)
	if strings.Contains(seg, "【") {
		// 【对话】 markers are tags too.
		for _, m := range markers(seg) {
			if strings.HasPrefix(m.span.Of(seg), "【") {
				tags = append(tags, m.span)
			}
		}
		slices.SortFunc(tags, func(a, b text.Span) int { return a.Start - b.Start })
	}
	for _, t := range tags {
		if t.Start > prev {
			p.addText(start+prev, start+t.Start)
		}
		p.tag(text.Span{Start: start + t.Start, End: start + t.End})
		prev = t.End
	}
	if prev < len(seg) {
		p.addText(start+prev, end)
	}
}

func (p *parser) addText(a, b int) {
	p.toks = append(p.toks, Token{Kind: Text, Span: text.Span{Start: a, End: b}, Text: p.src[a:b]})
}

func (p *parser) tag(sp text.Span) {
	raw := sp.Of(p.src)
	if strings.HasPrefix(raw, "【") {
		open, _ := dialogueWord(strings.TrimSuffix(strings.TrimPrefix(raw, "【"), "】"))
		p.toks = append(p.toks, Token{Kind: Dialogue, Span: sp, Name: openClose(open)})
		return
	}
	if strings.HasPrefix(raw, "[[") {
		word, say, ok := text.SplitOverride(raw)
		if !ok || word == "" || say == "" {
			p.diag(CodeBadOverride, text.Warning, sp, "tag", raw)
			p.addText(sp.Start, sp.End)
			return
		}
		p.toks = append(p.toks, Token{Kind: Override, Span: sp, Text: word, Say: say})
		return
	}
	inner := strings.TrimSpace(raw[1 : len(raw)-1])
	lower := strings.ToLower(inner)
	name, arg := splitTag(lower)
	switch {
	case name == "pause":
		ms, ok := p.pauseMillis(arg)
		if !ok {
			p.diag(CodeBadPause, text.Warning, sp, "tag", raw)
			ms = p.opt.DefaultPauseMillis
		} else if ms > p.opt.MaxPauseMillis {
			p.diag(CodePauseClamped, text.Info, sp, "tag", raw, "max_ms", strconv.Itoa(p.opt.MaxPauseMillis))
			ms = p.opt.MaxPauseMillis
		}
		p.toks = append(p.toks, Token{Kind: Pause, Span: sp, Millis: ms})
	case strings.HasPrefix(lower, "voice:") || lower == "voice":
		v := ""
		if i := strings.IndexByte(inner, ':'); i >= 0 {
			v = strings.TrimSpace(inner[i+1:])
		}
		if strings.EqualFold(v, "default") {
			v = ""
		}
		p.toks = append(p.toks, Token{Kind: Voice, Span: sp, Name: v})
	case isStyle(lower) && arg == "":
		p.toks = append(p.toks, Token{Kind: StyleOn, Span: sp, Name: lower})
		p.open = append(p.open, p.toks[len(p.toks)-1])
	case strings.HasPrefix(lower, "/") && isStyle(strings.TrimSpace(lower[1:])):
		st := strings.TrimSpace(lower[1:])
		i := slices.IndexFunc(p.open, func(t Token) bool { return t.Name == st })
		if i < 0 {
			p.diag(CodeStrayClose, text.Warning, sp, "style", st)
		} else {
			// Close the innermost matching style.
			for j := len(p.open) - 1; j >= 0; j-- {
				if p.open[j].Name == st {
					p.open = slices.Delete(p.open, j, j+1)
					break
				}
			}
		}
		p.toks = append(p.toks, Token{Kind: StyleOff, Span: sp, Name: st})
	case p.nonVerbal[lower]:
		p.toks = append(p.toks, Token{Kind: NonVerbal, Span: sp, Name: lower})
	case isDialogueWord(inner):
		open, _ := dialogueWord(inner)
		p.toks = append(p.toks, Token{Kind: Dialogue, Span: sp, Name: openClose(open)})
	default:
		if !p.reported[lower] {
			p.reported[lower] = true
			p.diag(CodeUnknownTag, text.Warning, sp, "tag", raw)
		}
		p.toks = append(p.toks, Token{Kind: Unknown, Span: sp, Text: raw, Name: inner})
	}
}

// splitTag splits "pause 500ms" / "pause:500ms" / "pause=1s" into name and
// argument.
func splitTag(s string) (name, arg string) {
	i := strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || r == ':' || r == '=' })
	if i < 0 {
		return s, ""
	}
	return s[:i], strings.TrimSpace(strings.TrimLeft(s[i:], " \t:="))
}

func isStyle(s string) bool {
	return s == Slow || s == Fast || s == Emphasis || s == Spell
}

// pauseMillis parses "", "500", "500ms", "500 ms", "1.5s", "2 sec".
func (p *parser) pauseMillis(arg string) (int, bool) {
	if arg == "" {
		return p.opt.DefaultPauseMillis, true
	}
	arg = strings.ReplaceAll(arg, " ", "")
	mult := 1.0
	switch {
	case strings.HasSuffix(arg, "ms"):
		arg = arg[:len(arg)-2]
	case strings.HasSuffix(arg, "sec"):
		arg, mult = arg[:len(arg)-3], 1000
	case strings.HasSuffix(arg, "s"):
		arg, mult = arg[:len(arg)-1], 1000
	}
	v, err := strconv.ParseFloat(arg, 64)
	if err != nil || v < 0 || v != v {
		return 0, false
	}
	ms := v * mult
	if ms > 1e9 {
		ms = 1e9
	}
	return int(ms + 0.5), true
}

func (p *parser) closeStyles() {
	for _, t := range p.open {
		p.diag(CodeUnclosedStyle, text.Warning, t.Span, "style", t.Name)
	}
	p.open = p.open[:0]
}

// mergeText joins adjacent text tokens (lines of one paragraph).
func (p *parser) mergeText() {
	out := p.toks[:0]
	for _, t := range p.toks {
		if n := len(out); n > 0 && t.Kind == Text && out[n-1].Kind == Text && out[n-1].Span.End == t.Span.Start {
			out[n-1].Span.End = t.Span.End
			out[n-1].Text = out[n-1].Span.Of(p.src)
			continue
		}
		out = append(out, t)
	}
	p.toks = out
}

func (p *parser) checkChapters() {
	for i, ch := range chapters(p.toks) {
		if ch.Heading && !ch.Spoken {
			p.diag(CodeEmptyChapter, text.Warning, p.toks[ch.First].Span, "title", ch.Title, "index", strconv.Itoa(i))
		}
	}
	slices.SortStableFunc(p.diags, func(a, b text.Diagnostic) int { return a.Span.Start - b.Span.Start })
}

// ChapterInfo describes one chapter of a document. Text before the first
// heading forms an untitled chapter (Heading false) when it has content.
type ChapterInfo struct {
	Title   string
	Heading bool // starts with a "# Title" line
	First   int  // index of the first token (the heading, if any)
	End     int  // index after the last token
	Spoken  bool // has text, a non-verbal tag or an override
}

// Chapters splits the token stream into chapters.
func (d *Document) Chapters() []ChapterInfo { return chapters(d.Tokens) }

func chapters(toks []Token) []ChapterInfo {
	var out []ChapterInfo
	cur := ChapterInfo{}
	finish := func(end int) {
		cur.End = end
		if cur.Heading || hasSubstance(toks[cur.First:end]) {
			out = append(out, cur)
		}
	}
	for i, t := range toks {
		if t.Kind == Chapter {
			finish(i)
			cur = ChapterInfo{Title: t.Name, Heading: true, First: i}
			continue
		}
		if spoken(t) {
			cur.Spoken = true
		}
	}
	finish(len(toks))
	return out
}

func spoken(t Token) bool {
	switch t.Kind {
	case Text:
		return strings.IndexFunc(t.Text, func(r rune) bool { return text.IsWordRune(r) }) >= 0
	case NonVerbal, Override, Unknown:
		return true
	}
	return false
}

func hasSubstance(toks []Token) bool {
	for _, t := range toks {
		if spoken(t) || t.Kind == Pause || t.Kind == Voice {
			return true
		}
	}
	return false
}

// VoiceNames returns the distinct non-empty [voice:NAME] names in order of
// first appearance (compared case-sensitively, as profile names are).
func (d *Document) VoiceNames() []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range d.Tokens {
		if t.Kind == Voice && t.Name != "" && !seen[t.Name] {
			seen[t.Name] = true
			out = append(out, t.Name)
		}
	}
	return out
}

// Validate returns the parse diagnostics plus one CodeUnknownVoice per
// distinct voice name for which known returns false (known nil: none).
func (d *Document) Validate(known func(name string) bool) []text.Diagnostic {
	out := slices.Clone(d.Diags)
	if known == nil {
		return out
	}
	seen := map[string]bool{}
	for _, t := range d.Tokens {
		if t.Kind == Voice && t.Name != "" && !seen[t.Name] {
			seen[t.Name] = true
			if !known(t.Name) {
				out = append(out, text.Diagnostic{Code: CodeUnknownVoice, Severity: text.Warning, Span: t.Span,
					Params: map[string]string{"voice": t.Name}})
			}
		}
	}
	slices.SortStableFunc(out, func(a, b text.Diagnostic) int { return a.Span.Start - b.Span.Start })
	return out
}

// Stats summarises a document for the editor's stats line.
type Stats struct {
	Chapters    int // at least 1 when there is any content
	Words       int // spaced-script words (text.Count)
	Chars       int // unspaced-script characters (text.Count)
	PauseMillis int
}

// Stats counts words outside markup, chapters and explicit pauses.
func (d *Document) Stats() Stats {
	var s Stats
	for _, t := range d.Tokens {
		switch t.Kind {
		case Text:
			w, c := text.Count(t.Text)
			s.Words += w
			s.Chars += c
		case Override:
			w, c := text.Count(t.Text)
			s.Words += w
			s.Chars += c
		case Pause:
			s.PauseMillis += t.Millis
		}
	}
	s.Chapters = len(d.Chapters())
	if s.Chapters == 0 && (s.Words > 0 || s.Chars > 0) {
		s.Chapters = 1
	}
	return s
}

// Run is a piece of the flattened script with the markup state in effect:
// what the synthesis layer turns into engine calls.
type Run struct {
	Token            // Text, NonVerbal, Override, Pause or Unknown
	Chapter int      // index into Chapters()
	Voice   string   // "" = default voice
	Styles  []string // active styles, outermost first (shared; do not modify)
}

// Has reports whether style is active.
func (r Run) Has(style string) bool { return slices.Contains(r.Styles, style) }

// Runs flattens the document: voice and style state are applied to every
// spoken token and pause. Voice resets at each chapter; styles left open
// end with their chapter.
func (d *Document) Runs() []Run {
	var out []Run
	chs := d.Chapters()
	chapterAt := func(i int) int {
		for c := len(chs) - 1; c >= 0; c-- {
			if i >= chs[c].First {
				return c
			}
		}
		return 0
	}
	voice := ""
	var styles []string
	for i, t := range d.Tokens {
		switch t.Kind {
		case Chapter:
			voice, styles = "", nil
		case Voice:
			voice = t.Name
		case StyleOn:
			styles = append(slices.Clip(styles), t.Name)
		case StyleOff:
			for k := len(styles) - 1; k >= 0; k-- {
				if styles[k] == t.Name {
					styles = slices.Delete(slices.Clone(styles), k, k+1)
					break
				}
			}
		case Paragraph, Dialogue:
		default:
			out = append(out, Run{Token: t, Chapter: chapterAt(i), Voice: voice, Styles: styles})
		}
	}
	return out
}

// Plain returns the document's spoken text with all markup removed:
// overrides become their word, non-verbal and unknown tags disappear,
// chapters become their titles on their own line.
func (d *Document) Plain() string {
	var b strings.Builder
	for _, t := range d.Tokens {
		switch t.Kind {
		case Text, Override:
			b.WriteString(t.Text)
		case Chapter:
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
				b.WriteByte('\n')
			}
			b.WriteString(t.Name)
			b.WriteByte('\n')
		case Paragraph:
			b.WriteByte('\n')
		}
	}
	return b.String()
}
