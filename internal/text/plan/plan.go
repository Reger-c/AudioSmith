// Package plan turns a script into what a TTS engine is asked to say: the
// markup parsed (internal/text/markup) and lowered for one engine, the
// pronunciation dictionary and inline overrides applied, written forms
// normalised, and the result cut into chunks the engine can take, with
// the silences and joins between them.
//
// Lowering per engine:
//   - [pause …] becomes real silence for every engine.
//   - Non-verbal tags ([laughter], [sigh]…) are passed to engines that
//     render them (Options.NonVerbal); for the others they are removed,
//     with a warning.
//   - [slow] / [fast] change the speed factor of their text when the
//     engine takes one (Options.Speed); otherwise they are ignored, with a
//     warning. [spell] spells its text letter by letter; [emphasis] has no
//     lowering yet and is ignored quietly.
//   - [voice:NAME] belongs to multi-voice scripts (Stories): outside them
//     (Options.Voices false) it is ignored, with a warning.
//   - Unknown bracket tags go verbatim to bracket-aware engines
//     (Options.NonVerbal: OmniVoice reads pinyin and phonemes in brackets)
//     and are removed for the others.
//
// The result is pure data (no audio, no I/O). Problems are diagnostics with
// codes and parameters (text.Diagnostic) that the UI translates.
//
// Imports: text, text/markup, text/normalize, text/lexicon.
package plan

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hehehai/AudioSmith/internal/text"
	"github.com/hehehai/AudioSmith/internal/text/lexicon"
	"github.com/hehehai/AudioSmith/internal/text/markup"
	"github.com/hehehai/AudioSmith/internal/text/normalize"
)

// Diagnostic codes added to markup's (markup.Code*). Params in brackets.
const (
	CodeNonVerbalRemoved = "plan.nonverbal_removed" // tag: the model cannot render it
	CodeTagRemoved       = "plan.tag_removed"       // tag: an unknown tag left out
	CodeVoiceIgnored     = "plan.voice_ignored"     // voice: [voice:] outside Stories
	CodeStyleIgnored     = "plan.style_ignored"     // style: [slow]/[fast] without a speed control
	CodeNothingToSay     = "plan.nothing_to_say"    // the script has no speech
)

// Options configure Build.
type Options struct {
	// Lang is the script's language for normalisation and the dictionary
	// (BCP-47); "" guesses from the text.
	Lang string
	// NonVerbal: the engine renders non-verbal tags and bracketed
	// phonemes itself (OmniVoice).
	NonVerbal bool
	// Speed: the engine takes a speed factor, so [slow]/[fast] work.
	Speed bool
	// Voices: [voice:NAME] is allowed (Stories). In the Studio it is not.
	Voices bool
	// MaxChars caps a chunk (runes) of spaced scripts; MaxCharsCJK of
	// scripts mostly in Han, kana or Hangul. 0 = text.DefaultMaxChars
	// (MaxCharsCJK 0 = MaxChars).
	MaxChars, MaxCharsCJK int
	// NoNormalize leaves numbers, dates and abbreviations as written.
	NoNormalize bool
	// Lexicon is the compiled pronunciation dictionary; nil applies only
	// inline [[word|say]] overrides.
	Lexicon *lexicon.Dictionary
}

// Break is how a speech segment joins the audio before it.
type Break uint8

const (
	BreakNone      Break = iota // the first segment, or after a silence
	BreakChunk                  // inside a sentence cut for the engine: crossfade
	BreakSentence               // between sentences
	BreakLine                   // at a line break
	BreakParagraph              // between paragraphs
)

// Kind is a segment's type.
type Kind uint8

const (
	Speech  Kind = iota // Text for the engine
	Silence             // Millis of silence ([pause])
)

// Segment is one step of a render: a chunk of speech or a silence.
type Segment struct {
	Kind   Kind
	Text   string  // Speech: what the engine gets
	Speed  float64 // Speech: multiplier of the take's speed ([slow] 0.85, [fast] 1.15; else 1)
	Break  Break   // Speech: the join before it
	Millis int     // Silence
	// Source is the part of the script the segment came from (its block:
	// the text between two pauses or paragraph breaks), for highlighting.
	Source text.Span
}

// Plan is a lowered, chunked script.
type Plan struct {
	Segments []Segment
	Diags    []text.Diagnostic
}

// Speech returns the speech segments' indexes.
func (p *Plan) Speech() []int {
	var out []int
	for i, s := range p.Segments {
		if s.Kind == Speech {
			out = append(out, i)
		}
	}
	return out
}

// Chars is the length of the speech, in runes, for estimates.
func (p *Plan) Chars() int {
	n := 0
	for _, s := range p.Segments {
		if s.Kind == Speech {
			n += utf8.RuneCountInString(s.Text)
		}
	}
	return n
}

// PauseMillis is the total of the explicit silences.
func (p *Plan) PauseMillis() int {
	n := 0
	for _, s := range p.Segments {
		if s.Kind == Silence {
			n += s.Millis
		}
	}
	return n
}

// Speed factors of [slow] and [fast].
const (
	SlowFactor = 0.85
	FastFactor = 1.15
)

// block is the text between two pauses, paragraph breaks or speed changes.
type block struct {
	text  strings.Builder
	speed float64
	brk   Break
	src   text.Span
	empty bool
}

// Build parses src and plans its render.
func Build(src string, o Options) Plan {
	doc := markup.Parse(src, &markup.Options{NoChapters: true})
	var p Plan
	p.Diags = append(p.Diags, doc.Diags...)
	reported := map[string]bool{}
	warn := func(code string, sp text.Span, key, value string) {
		if reported[code+"\x00"+value] {
			return
		}
		reported[code+"\x00"+value] = true
		p.Diags = append(p.Diags, text.Diagnostic{Code: code, Severity: text.Warning, Span: sp, Params: map[string]string{key: value}})
	}

	var styles []string
	has := func(s string) bool {
		for _, x := range styles {
			if x == s {
				return true
			}
		}
		return false
	}
	speedNow := func() float64 {
		if !o.Speed {
			return 1
		}
		// The innermost of [slow]/[fast] wins.
		for i := len(styles) - 1; i >= 0; i-- {
			switch styles[i] {
			case markup.Slow:
				return SlowFactor
			case markup.Fast:
				return FastFactor
			}
		}
		return 1
	}

	cur := &block{speed: 1, empty: true}
	pending := BreakNone
	flush := func(next Break) {
		if !cur.empty {
			p.addBlock(cur, o)
			pending = next
		} else if next > pending {
			pending = next
		}
		cur = &block{speed: speedNow(), brk: pending, empty: true}
	}
	add := func(s string, sp text.Span) {
		if s == "" {
			return
		}
		if cur.empty {
			cur.src = sp
			cur.brk = pending
			cur.speed = speedNow()
			cur.empty = false
		}
		cur.text.WriteString(s)
		cur.src.End = sp.End
	}

	for _, t := range doc.Tokens {
		switch t.Kind {
		case markup.Text:
			s := t.Text
			if has(markup.Spell) {
				s = spell(s)
			}
			add(s, t.Span)
		case markup.Override:
			// Kept as markup: the dictionary step substitutes it and
			// nothing matches inside it.
			add(t.Span.Of(src), t.Span)
		case markup.NonVerbal:
			if o.NonVerbal {
				add(" ["+t.Name+"] ", t.Span)
			} else {
				warn(CodeNonVerbalRemoved, t.Span, "tag", "["+t.Name+"]")
				add(" ", t.Span)
			}
		case markup.Unknown:
			if o.NonVerbal {
				add(t.Text, t.Span)
			} else {
				warn(CodeTagRemoved, t.Span, "tag", t.Text)
				add(" ", t.Span)
			}
		case markup.Voice:
			if !o.Voices {
				name := t.Name
				if name == "" {
					name = "default"
				}
				warn(CodeVoiceIgnored, t.Span, "voice", name)
			}
		case markup.Pause:
			flush(BreakNone)
			p.Segments = append(p.Segments, Segment{Kind: Silence, Millis: t.Millis, Source: t.Span})
			pending = BreakNone
		case markup.Paragraph:
			flush(BreakParagraph)
		case markup.StyleOn, markup.StyleOff:
			if (t.Name == markup.Slow || t.Name == markup.Fast) && !o.Speed {
				warn(CodeStyleIgnored, t.Span, "style", t.Name)
			}
			before := speedNow()
			if t.Kind == markup.StyleOn {
				styles = append(styles, t.Name)
			} else {
				for i := len(styles) - 1; i >= 0; i-- {
					if styles[i] == t.Name {
						styles = append(styles[:i:i], styles[i+1:]...)
						break
					}
				}
			}
			if speedNow() != before {
				flush(BreakChunk)
			}
		}
	}
	flush(BreakNone)
	if len(p.Speech()) == 0 {
		p.Diags = append(p.Diags, text.Diagnostic{Code: CodeNothingToSay, Severity: text.Error, Params: map[string]string{}})
	}
	return p
}

// addBlock lowers one block: dictionary, normalisation, chunks.
func (p *Plan) addBlock(b *block, o Options) {
	s := o.Lexicon.Apply(b.text.String()).Text
	if !o.NoNormalize {
		s = normalize.Text(s, o.Lang, normalize.Options{})
	}
	max := o.MaxChars
	if mostlyCJK(s) && o.MaxCharsCJK > 0 {
		max = o.MaxCharsCJK
	}
	chunks := text.Chunks(s, text.Options{Mode: text.Pack, MaxChars: max})
	brk := b.brk
	for _, c := range chunks {
		t := strings.Join(strings.Fields(c.Text), " ")
		if mostlyCJK(t) {
			// Spaces between CJK lines are artefacts of line breaks.
			t = joinCJK(c.Text)
		}
		if !speakable(t) {
			continue
		}
		p.Segments = append(p.Segments, Segment{Kind: Speech, Text: t, Speed: b.speed, Break: brk, Source: b.src})
		switch c.Cut {
		case text.CutNatural:
			brk = BreakSentence
		case text.CutLine:
			brk = BreakLine
		default:
			brk = BreakChunk
		}
	}
}

// speakable reports whether s has a letter, a digit or a tag to say.
func speakable(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '[' {
			return true
		}
	}
	return false
}

// mostlyCJK reports whether most letters of s are Han, kana or Hangul.
func mostlyCJK(s string) bool {
	cjk, other := 0, 0
	for _, r := range s {
		switch {
		case text.IsCJK(r):
			cjk++
		case unicode.IsLetter(r):
			other++
		}
	}
	return cjk > other
}

// joinCJK collapses white space, dropping line breaks between CJK
// characters (which would read as pauses) and keeping single spaces
// elsewhere.
func joinCJK(s string) string {
	var b strings.Builder
	var prev rune
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		if space {
			if !(text.IsCJK(prev) || text.IsCJK(r) || unicode.IsPunct(prev)) {
				b.WriteByte(' ')
			}
			space = false
		}
		b.WriteRune(r)
		prev = r
	}
	return b.String()
}

// spell spells words letter by letter: "API" → "A P I".
func spell(s string) string {
	var b strings.Builder
	prevLetter := false
	for _, r := range s {
		isL := unicode.IsLetter(r) || unicode.IsDigit(r)
		if isL && prevLetter && !text.IsCJK(r) {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
		prevLetter = isL
	}
	return b.String()
}

// Seconds estimates how long the plan takes to say at speed (1 = normal),
// pauses included: text.Estimate over the speech, divided by the speed.
func (p *Plan) Seconds(speed float64) float64 {
	if speed <= 0 {
		speed = 1
	}
	total := 0.0
	for _, s := range p.Segments {
		switch s.Kind {
		case Speech:
			w, c := text.Count(s.Text)
			f := s.Speed
			if f <= 0 {
				f = 1
			}
			total += text.Estimate(w, c).Seconds() / (speed * f)
		case Silence:
			total += float64(s.Millis) / 1000
		}
	}
	return total
}

// String is a debugging rendering: one line per segment.
func (p *Plan) String() string {
	var b strings.Builder
	for _, s := range p.Segments {
		switch s.Kind {
		case Speech:
			b.WriteString("say ")
			b.WriteString(strconv.Itoa(int(s.Break)))
			if s.Speed != 1 {
				b.WriteString(" ×" + strconv.FormatFloat(s.Speed, 'g', -1, 64))
			}
			b.WriteString(" " + strconv.Quote(s.Text) + "\n")
		case Silence:
			b.WriteString("pause " + strconv.Itoa(s.Millis) + "\n")
		}
	}
	return b.String()
}
