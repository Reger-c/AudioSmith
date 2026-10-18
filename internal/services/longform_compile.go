package services

// The longform compiler: a Stories or Audiobook document, its voices
// resolved for the model, into a render manifest (Compile), with the
// problems it finds; split from longform_doc.go.

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/text"
	"github.com/hehehai/AudioSmith/internal/text/markup"
)

// compiler builds a spec.
type compiler struct {
	d        *LongformDoc
	resolve  VoiceResolver
	spec     LongformSpec
	problems []Problem
	reported map[string]bool
	repeats  map[string]int
	blocks   []DialogueBlockInfo // a manuscript's dialogue blocks (book_dialogue.go)
	names    []string            // its [voice:] names, once asked for
}

func (c *compiler) problem(p Problem) {
	k := p.Code + "\x00" + strconv.Itoa(p.Chapter) + "\x00" + strconv.Itoa(p.Line)
	for _, v := range p.Params {
		k += "\x00" + v
	}
	if c.reported[k] {
		return
	}
	c.reported[k] = true
	c.problems = append(c.problems, p)
}

// voice resolves a reference into the spec's voices; "" when it can't.
func (c *compiler) voice(ref string) (string, bool) {
	if ref == "" || c.resolve == nil {
		return "", false
	}
	key, t, ok := c.resolve(ref)
	if !ok {
		return "", false
	}
	if key == "" {
		key = ref
	}
	c.spec.Voices[key] = t
	return key, true
}

// Compile turns a document into a render manifest. Problems with Blocking
// set mean the spec must not be rendered.
func Compile(d *LongformDoc, resolve VoiceResolver) (LongformSpec, []Problem) {
	c := &compiler{d: d, resolve: resolve, reported: map[string]bool{}, repeats: map[string]int{}}
	return c.compile()
}

func (c *compiler) compile() (LongformSpec, []Problem) {
	d := c.d
	c.spec = LongformSpec{Kind: d.Kind, Title: strings.TrimSpace(d.Title), Model: d.Model, Lang: d.Language, Seed: d.Seed,
		Voices: map[string]core.Take{}, Production: d.Production, Book: d.Book, Format: d.Format, Stems: d.Stems,
		Lexicon: cleanLexicon(d.Lexicon)}
	if d.Kind != core.ProjectAudiobook {
		c.spec.Scene = strings.TrimSpace(d.Scene)
	}
	if d.Production.Seed != 0 {
		c.spec.Seed = d.Production.Seed
	}
	if d.Model == "" {
		c.problem(Problem{Code: ProblemNoModel, Blocking: true, Chapter: -1, Line: -1})
	}
	for _, w := range DuplicateWords(d.Lexicon) {
		c.problem(Problem{Code: ProblemDuplicateWord, Blocking: true, Chapter: -1, Line: -1, Params: map[string]string{"word": w}})
	}
	if d.Kind == core.ProjectAudiobook {
		c.manuscript()
	} else {
		c.story()
	}
	spoken := false
	for _, ch := range c.spec.Chapters {
		if len(ch.Lines) > 0 {
			spoken = true
		}
	}
	if !spoken {
		c.problem(Problem{Code: ProblemNothingToSay, Blocking: true, Chapter: -1, Line: -1})
	}
	return c.spec, c.problems
}

// story compiles the lines of a story.
func (c *compiler) story() {
	d := c.d
	cast := map[string]CastMember{}
	names := map[string]CastMember{}
	for _, m := range d.Cast {
		cast[m.ID] = m
		if n := strings.ToLower(strings.TrimSpace(m.Name)); n != "" {
			names[n] = m
		}
	}
	var cur *ChapterSpec
	chapter := func(title string) {
		c.spec.Chapters = append(c.spec.Chapters, ChapterSpec{Title: title})
		cur = &c.spec.Chapters[len(c.spec.Chapters)-1]
	}
	for i, l := range d.Lines {
		if l.Chapter {
			title := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l.Text), "#"))
			if cur != nil && len(cur.Lines) == 0 && cur.Title != "" {
				c.problem(Problem{Code: ProblemEmptyChapter, Chapter: len(c.spec.Chapters) - 1, Line: i, Params: map[string]string{"title": cur.Title}})
			}
			chapter(title)
			continue
		}
		if !hasSpeech(l.Text) {
			continue
		}
		if cur == nil {
			chapter("")
		}
		ch := len(c.spec.Chapters) - 1
		// The line's voice: its own, its character's, the story's.
		ref, who, persona, baseline := d.Voice, "", "", ""
		if m, ok := cast[l.Character]; ok {
			who, persona, baseline = m.Name, strings.TrimSpace(m.Persona), NormEmotion(m.Baseline)
			if m.Voice != "" {
				ref = m.Voice
			}
		}
		if l.Voice != "" {
			ref = l.Voice
		}
		key, ok := c.voice(ref)
		if !ok {
			if ref == "" {
				c.problem(Problem{Code: ProblemNoVoice, Blocking: true, Chapter: ch, Line: i, Params: map[string]string{"line": strconv.Itoa(i + 1)}})
			} else {
				c.problem(Problem{Code: ProblemMissingVoice, Blocking: true, Chapter: ch, Line: i, Params: map[string]string{"voice": ref}})
			}
			continue
		}
		speed := l.Speed
		if speed <= 0 {
			speed = d.Speed
		}
		lines := c.spans(l.Text, key, who, false, ch, i, func(name string) (string, string, bool) {
			if name == "" || strings.EqualFold(name, "default") {
				return key, who, true
			}
			if m, ok := names[strings.ToLower(name)]; ok {
				if m.Voice == "" {
					return key, m.Name, true // a character without a voice of its own reads in the line's
				}
				return c.castVoice(m.Voice, m.Name, ch, i)
			}
			if k, ok := c.voice(name); ok {
				return k, name, true
			}
			c.problem(Problem{Code: ProblemUnknownVoice, Chapter: ch, Line: i, Params: map[string]string{"voice": name}})
			return key, who, true
		})
		for _, ls := range lines {
			ls.ID, ls.Speed, ls.PauseAfter = l.ID, speed, l.PauseAfter
			ls.Direction, ls.Persona = strings.TrimSpace(l.Direction), persona
			if ls.Direction != "" {
				ls.Intensity = min(max(l.Intensity, 0), IntensityStrong)
			}
			if e := NormEmotion(l.Emotion); e != "" {
				ls.Emotion, ls.Intensity, ls.Turn, ls.Baseline = e, min(max(l.Intensity, 0), IntensityStrong), l.Turn, baseline
				if e == EmotionSteady && baseline != "" && baseline != EmotionSteady {
					// The character's baseline going on, faintly.
					ls.Emotion, ls.Intensity = baseline, min(ls.Intensity, IntensityLight)
				}
			}
			cur.Lines = append(cur.Lines, c.counted(ls))
		}
	}
	if cur != nil && len(cur.Lines) == 0 && cur.Title != "" {
		c.problem(Problem{Code: ProblemEmptyChapter, Chapter: len(c.spec.Chapters) - 1, Line: -1, Params: map[string]string{"title": cur.Title}})
	}
	// Drop chapters without lines (headings in a row).
	out := c.spec.Chapters[:0]
	for _, ch := range c.spec.Chapters {
		if len(ch.Lines) > 0 {
			out = append(out, ch)
		}
	}
	c.spec.Chapters = out
}

// castVoice resolves a cast member's voice: a voice that is gone blocks.
func (c *compiler) castVoice(ref, name string, ch, line int) (string, string, bool) {
	k, ok := c.voice(ref)
	if !ok {
		c.problem(Problem{Code: ProblemMissingVoice, Blocking: true, Chapter: ch, Line: line, Params: map[string]string{"voice": name}})
		return "", "", false
	}
	return k, name, true
}

// manuscript compiles an audiobook's script: a line per paragraph.
func (c *compiler) manuscript() {
	d := c.d
	doc := markup.Parse(d.Script, nil)
	blocks, bdiags := markup.DialogueBlocks(d.Script)
	c.blockProblems(bdiags)
	for _, dg := range doc.Diags {
		switch dg.Code {
		case markup.CodeUnknownTag:
			c.problem(Problem{Code: ProblemUnknownTag, Chapter: -1, Line: -1, Span: dg.Span, Params: dg.Params})
		case markup.CodeEmptyChapter:
			c.problem(Problem{Code: ProblemEmptyChapter, Chapter: -1, Line: -1, Span: dg.Span, Params: map[string]string{"title": dg.Params["title"]}})
		}
	}
	def, defOK := c.voice(d.Voice)
	narrator := ""
	lookup := func(ch int) func(name string) (string, string, bool) {
		return func(name string) (string, string, bool) {
			if name == "" || strings.EqualFold(name, "default") {
				return def, narrator, defOK
			}
			for n, ref := range d.VoiceCast {
				if strings.EqualFold(n, name) && ref != "" {
					return c.castVoice(ref, name, ch, -1)
				}
			}
			if k, ok := c.voice(name); ok {
				return k, name, true
			}
			c.problem(Problem{Code: ProblemUnknownVoice, Chapter: ch, Line: -1, Params: map[string]string{"voice": name}})
			return def, narrator, defOK
		}
	}
	for ci, info := range doc.Chapters() {
		ch := ChapterSpec{Title: info.Title}
		if !info.Heading {
			ch.Title = ""
		}
		toks := doc.Tokens[info.First:info.End]
		if len(toks) > 0 && toks[0].Kind == markup.Chapter {
			toks = toks[1:]
		}
		if len(toks) == 0 {
			continue
		}
		src := d.Script[toks[0].Span.Start:toks[len(toks)-1].Span.End]
		if !defOK && hasSpeech(src) {
			if d.Voice == "" {
				c.problem(Problem{Code: ProblemNoVoice, Blocking: true, Chapter: ci, Line: -1})
			} else {
				c.problem(Problem{Code: ProblemMissingVoice, Blocking: true, Chapter: ci, Line: -1, Params: map[string]string{"voice": d.Voice}})
			}
		}
		start, end := toks[0].Span.Start, toks[len(toks)-1].Span.End
		var lines []LineSpec
		if bs := blocksIn(blocks, start, end); len(bs) > 0 {
			lines = c.withBlocks(d.Script, start, end, bs, bookChapter{ch: ci, spec: len(c.spec.Chapters), def: def, narrator: narrator, defOK: defOK, lookup: lookup(ci)})
		} else {
			lines = c.spans(src, def, narrator, true, ci, -1, lookup(ci))
		}
		for _, ls := range lines {
			ls.Speed = d.Speed
			ch.Lines = append(ch.Lines, c.counted(ls))
		}
		if len(ch.Lines) > 0 {
			ch.Lines[0].Paragraph = false
		}
		c.spec.Chapters = append(c.spec.Chapters, ch)
	}
	out := c.spec.Chapters[:0]
	index := make([]int, len(c.spec.Chapters)) // before → after
	for i, ch := range c.spec.Chapters {
		index[i] = len(out)
		if len(ch.Lines) > 0 {
			out = append(out, ch)
		}
	}
	c.spec.Chapters = out
	for i := range c.blocks {
		c.blocks[i].Chapter = index[c.blocks[i].Chapter]
	}
}

// counted sets a line's Repeat: how many earlier lines say the same with
// the same voices.
func (c *compiler) counted(l LineSpec) LineSpec {
	var b strings.Builder
	for _, s := range l.Spans {
		b.WriteString(s.Voice)
		b.WriteByte(0)
		b.WriteString(text.NormalizeForKey(s.Text))
		b.WriteByte(0)
	}
	k := b.String()
	l.Repeat = c.repeats[k]
	c.repeats[k]++
	return l
}

// spans splits src into lines (at blank lines when paragraphs) of spans
// at [voice:] tags, starting in voice key; a voice switch lasts until the
// next one. Styles open across a switch are closed at its end and opened
// again after it. lookup resolves a [voice:NAME].
func (c *compiler) spans(src, key, who string, paragraphs bool, ch, line int, lookup func(string) (string, string, bool)) []LineSpec {
	at := voiceAt{key: key, who: who, ok: key != ""}
	return c.spansFrom(src, &at, who, paragraphs, lookup)
}

// voiceAt is the voice a manuscript is in at a point: a [voice:] switch
// lasts until the next one, across the pieces a chapter's dialogue
// blocks cut it into (book_dialogue.go).
type voiceAt struct {
	key, who string
	ok       bool
}

// spansFrom is spans from the voice at, which it leaves as the text ends.
func (c *compiler) spansFrom(src string, at *voiceAt, who string, paragraphs bool, lookup func(string) (string, string, bool)) []LineSpec {
	doc := markup.Parse(src, &markup.Options{NoChapters: true})
	var out []LineSpec
	var cur LineSpec
	var b strings.Builder
	var styles []string
	voice, character, ok := at.key, at.who, at.ok
	defer func() { at.key, at.who, at.ok = voice, character, ok }()
	flushSpan := func() {
		t := strings.TrimSpace(b.String())
		for i := len(styles) - 1; i >= 0; i-- {
			t += "[/" + styles[i] + "]"
		}
		b.Reset()
		if ok && hasSpeechOrPause(t) {
			cur.Spans = append(cur.Spans, SpanSpec{Voice: voice, Character: character, Text: t})
		}
		for _, s := range styles {
			b.WriteString("[" + s + "]")
		}
	}
	flushLine := func(next bool) {
		flushSpan()
		if hasSpokenSpan(cur.Spans) {
			if cur.Character == "" {
				cur.Character = who
			}
			out = append(out, cur)
		}
		cur = LineSpec{Paragraph: next}
	}
	for _, t := range doc.Tokens {
		switch t.Kind {
		case markup.Paragraph:
			if paragraphs {
				flushLine(true)
				continue
			}
			b.WriteString("\n\n")
		case markup.Voice:
			flushSpan()
			voice, character, ok = lookup(t.Name)
		case markup.Dialogue:
			// A stray or nested dialogue marker: never spoken.
		case markup.StyleOn:
			styles = append(styles, t.Name)
			b.WriteString(t.Span.Of(src))
		case markup.StyleOff:
			for i := len(styles) - 1; i >= 0; i-- {
				if styles[i] == t.Name {
					styles = append(styles[:i:i], styles[i+1:]...)
					break
				}
			}
			b.WriteString(t.Span.Of(src))
		default:
			b.WriteString(t.Span.Of(src))
		}
	}
	flushLine(false)
	return out
}

// hasSpeech reports whether markup s has something to say.
func hasSpeech(s string) bool {
	for _, t := range markup.Parse(s, &markup.Options{NoChapters: true}).Tokens {
		switch t.Kind {
		case markup.Text:
			if strings.IndexFunc(t.Text, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0 {
				return true
			}
		case markup.NonVerbal, markup.Override, markup.Unknown:
			return true
		}
	}
	return false
}

func hasSpeechOrPause(s string) bool {
	return hasSpeech(s) || strings.Contains(strings.ToLower(s), "[pause")
}

func hasSpokenSpan(spans []SpanSpec) bool {
	for _, s := range spans {
		if hasSpeech(s.Text) {
			return true
		}
	}
	return false
}

// cleanLexicon drops blank rows.
func cleanLexicon(l []LexiconEntry) []LexiconEntry {
	var out []LexiconEntry
	for _, e := range l {
		e.Word, e.Say = strings.TrimSpace(e.Word), strings.TrimSpace(e.Say)
		if e.Word != "" && e.Say != "" {
			out = append(out, e)
		}
	}
	return out
}
