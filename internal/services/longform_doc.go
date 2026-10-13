package services

// Longform documents: the body of a Stories or Audiobook project (also the
// pages' drafts), and the compiler that turns one into a render manifest
// (LongformSpec) once the UI has resolved its voices for the model.
//
// A Stories document is a list of lines, each with a character (whose voice
// the cast sets), an optional voice of its own, a speed and a pause after
// it; "chapter" lines start chapters. An Audiobook document is one
// manuscript in the script markup: "# Title" lines start chapters and
// [voice:NAME] switches voice until the next switch or chapter; NAME is
// looked up in the book's cast, then among the voices.
//
// Voices are referred to by a voice reference (VoiceRef): a saved voice's
// id, "speaker:<id>" for one of the model's own speakers, or (in [voice:]
// tags) a name. The compiler asks a VoiceResolver for what a reference
// means for the model: the voice fields of a recipe.

import (
	"crypto/rand"
	"encoding/binary"
	"strconv"
	"strings"
	"unicode"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/text"
	"github.com/hehehai/AudioSmith/internal/text/markup"
)

// LongformVersion is the version of LongformDoc.
const LongformVersion = 1

// Loudness targets of a book (dsp/loudness presets).
const (
	LoudnessOff     = ""
	LoudnessACX     = "acx"     // −19 LUFS, −3 dBTP (Audible)
	LoudnessPodcast = "podcast" // −16 LUFS, −1.5 dBTP
)

// Output formats of a book.
const (
	FormatWAV = "wav"
	FormatM4B = "m4b" // needs the engine's media.encode (EncodeSupported)
)

// SpeakerRef prefixes a VoiceRef naming one of the model's own speakers.
const SpeakerRef = "speaker:"

// LongformDoc is a Stories or Audiobook project (core.Project.Body) and the
// page's draft.
type LongformDoc struct {
	Version  int              `json:"version"`
	Kind     core.ProjectKind `json:"kind"` // ProjectStory or ProjectAudiobook
	Title    string           `json:"title"`
	Model    string           `json:"model"`
	Voice    string           `json:"voice,omitempty"`    // the default (narrator) voice: a VoiceRef
	Language string           `json:"language,omitempty"` // BCP-47; "" = auto
	Speed    float64          `json:"speed,omitempty"`    // 0.5–2; 0 = 1
	Format   string           `json:"format,omitempty"`   // FormatWAV ("" too) or FormatM4B
	// Seed is the project's own seed: chapters are rendered with it unless
	// Production.Seed pins another, so a render can be made again and its
	// chunks come from the cache.
	Seed int64 `json:"seed,omitempty"`

	// Stories.
	Lines      []StoryLine  `json:"lines,omitempty"`
	Cast       []CastMember `json:"cast,omitempty"`
	ImportText string       `json:"importText,omitempty"` // the review buffer
	SplitBy    string       `json:"splitBy,omitempty"`    // "sentences", "paragraphs" (default), "chapters"
	MaxChars   int          `json:"maxChars,omitempty"`   // per line when splitting by sentences; 0 = 500

	// Audiobook.
	Script    string            `json:"script,omitempty"`
	VoiceCast map[string]string `json:"voiceCast,omitempty"` // [voice:NAME] → VoiceRef

	Lexicon    []LexiconEntry `json:"lexicon,omitempty"` // the book's pronunciations, over the dictionary
	Production Production     `json:"production"`
	Book       BookInfo       `json:"book"`
	Stems      bool           `json:"stems,omitempty"` // write a stem per character
	// Render is the id of the last full render.
	Render string `json:"render,omitempty"`
}

// StoryLine is one line of a story.
type StoryLine struct {
	ID        string  `json:"id"`
	Text      string  `json:"text"`                // script markup; a chapter line's title
	Character string  `json:"character,omitempty"` // a CastMember's ID; "" = the default voice
	Voice     string  `json:"voice,omitempty"`     // a VoiceRef that overrides the character's
	Speed     float64 `json:"speed,omitempty"`     // 0 = the story's
	// PauseAfter is milliseconds of silence after the line, in place of the
	// gap between lines; 0 = the gap.
	PauseAfter int  `json:"pauseAfter,omitempty"`
	Chapter    bool `json:"chapter,omitempty"` // a chapter heading: Text is its title
}

// CastMember is a character of a story and its voice.
type CastMember struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Voice string `json:"voice,omitempty"` // a VoiceRef; "" = the default voice
}

// LexiconEntry is one of a book's pronunciations: Word is said as Say.
type LexiconEntry struct {
	Word string `json:"word"`
	Say  string `json:"say"`
}

// Production are a render's synthesis and joining settings.
type Production struct {
	Steps    int     `json:"steps,omitempty"`    // diffusion steps; 0 = the model's
	Guidance float64 `json:"guidance,omitempty"` // CFG; 0 = the model's
	Seed     int64   `json:"seed,omitempty"`     // 0 = the project's seed
	// VaryRepeats gives identical lines takes of their own instead of
	// reusing one recording.
	VaryRepeats    bool `json:"varyRepeats,omitempty"`
	LineGapMs      int  `json:"lineGapMs"`      // silence between lines (0–2000)
	ParagraphGapMs int  `json:"paragraphGapMs"` // between paragraphs (0–3000)
	// KeepEdges keeps the silence engines pad onto each chunk; the default
	// trims it before joining.
	KeepEdges bool `json:"keepEdges,omitempty"`
}

// DefaultProduction are the production settings of a new project.
func DefaultProduction() Production { return Production{LineGapMs: 400, ParagraphGapMs: 800} }

// BookInfo are a book's details: the metadata of encoded books, the cover
// and the loudness target.
type BookInfo struct {
	Author      string `json:"author,omitempty"`
	Narrator    string `json:"narrator,omitempty"`
	Year        string `json:"year,omitempty"`
	Genre       string `json:"genre,omitempty"`
	Description string `json:"description,omitempty"`
	// Cover is a PNG or JPEG of at most 8 MB (absolute path).
	Cover    string `json:"cover,omitempty"`
	Loudness string `json:"loudness,omitempty"` // LoudnessOff, LoudnessACX or LoudnessPodcast
}

// MaxCoverBytes caps a cover image.
const MaxCoverBytes = 8 << 20

// NewLongformDoc is a new, empty project of kind for model.
func NewLongformDoc(kind core.ProjectKind, model string) LongformDoc {
	var b [8]byte
	rand.Read(b[:])
	return LongformDoc{Version: LongformVersion, Kind: kind, Model: model, Speed: 1, Format: FormatWAV,
		Seed: int64(binary.LittleEndian.Uint64(b[:])>>34) + 1, Production: DefaultProduction()}
}

// VoiceResolver says what a voice reference (or a name in a [voice:] tag)
// means for the document's model: a stable key for it and the voice
// fields of a recipe (Voice, RefAudio, RefText, VoicePrompt, Seed,
// Language, VoiceID…). ok is false for a reference it does not know.
type VoiceResolver func(ref string) (key string, voice core.Take, ok bool)

// LongformSpec is what a longform render renders: the voices resolved for
// the model, and the chapters as lines of voiced spans.
type LongformSpec struct {
	Project string           `json:"project,omitempty"` // "" for a draft
	Kind    core.ProjectKind `json:"kind"`
	Title   string           `json:"title"`
	Model   string           `json:"model"`
	Lang    string           `json:"lang,omitempty"` // BCP-47, for normalisation and the dictionary
	Seed    int64            `json:"seed,omitempty"`
	// Voices are the voice fields of the recipes by key (SpanSpec.Voice).
	Voices     map[string]core.Take `json:"voices"`
	Chapters   []ChapterSpec        `json:"chapters"`
	Production Production           `json:"production"`
	Book       BookInfo             `json:"book"`
	Format     string               `json:"format,omitempty"`
	Stems      bool                 `json:"stems,omitempty"`
	Lexicon    []LexiconEntry       `json:"lexicon,omitempty"`
	// Audition renders the chapters for listening only: no book, no
	// stems, no loudness target (a chapter or a line preview).
	Audition bool `json:"audition,omitempty"`
}

// ChapterSpec is one chapter: a title ("" = untitled) and its lines.
type ChapterSpec struct {
	Title string     `json:"title,omitempty"`
	Lines []LineSpec `json:"lines"`
}

// LineSpec is one line (a story line, a paragraph of a manuscript).
type LineSpec struct {
	ID         string     `json:"id,omitempty"`
	Character  string     `json:"character,omitempty"` // for stems; "" = the narrator
	Spans      []SpanSpec `json:"spans"`
	Speed      float64    `json:"speed,omitempty"` // 0 or 1 = normal
	PauseAfter int        `json:"pauseAfter,omitempty"`
	Paragraph  bool       `json:"paragraph,omitempty"` // the paragraph gap before it
	// Repeat counts the earlier lines with the same text and voice (for
	// VaryRepeats).
	Repeat int `json:"repeat,omitempty"`
}

// SpanSpec is a part of a line in one voice.
type SpanSpec struct {
	Voice     string `json:"voice"` // a key of LongformSpec.Voices
	Character string `json:"character,omitempty"`
	Text      string `json:"text"` // script markup without [voice:] tags
}

// Problem is something the compiler found: Blocking ones stop a render.
// The UI translates Code with Params.
type Problem struct {
	Code     string            `json:"code"`
	Blocking bool              `json:"blocking,omitempty"`
	Chapter  int               `json:"chapter"`        // 0-based; -1 = none
	Line     int               `json:"line"`           // the story line's index, -1 = none
	Span     text.Span         `json:"span,omitzero"`  // in the manuscript (Audiobook)
	Params   map[string]string `json:"params,omitzero"`
}

// Problem codes (Params in brackets).
const (
	ProblemNoModel       = "longform.no_model"
	ProblemNothingToSay  = "longform.nothing_to_say"
	ProblemNoVoice       = "longform.no_voice"       // a line without a voice [line]
	ProblemMissingVoice  = "longform.missing_voice"  // a voice that is gone [voice]
	ProblemUnknownVoice  = "longform.unknown_voice"  // [voice:NAME] nobody is cast as: the line's voice reads it [voice]
	ProblemEmptyChapter  = "longform.empty_chapter"  // [title]
	ProblemUnknownTag    = "longform.unknown_tag"    // read aloud [tag]
	ProblemDuplicateWord = "longform.duplicate_word" // the lexicon lists a word twice [word]
)

// Untitled is the title of chapter n (1-based) without one, for cue sheets
// and metadata ("Chapter 3"); the UI translates its own.
func Untitled(n int) string { return "Chapter " + strconv.Itoa(n) }

// compiler builds a spec.
type compiler struct {
	d        *LongformDoc
	resolve  VoiceResolver
	spec     LongformSpec
	problems []Problem
	reported map[string]bool
	repeats  map[string]int
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
	c.spec = LongformSpec{Kind: d.Kind, Title: strings.TrimSpace(d.Title), Model: d.Model, Lang: d.Language, Seed: d.Seed,
		Voices: map[string]core.Take{}, Production: d.Production, Book: d.Book, Format: d.Format, Stems: d.Stems,
		Lexicon: cleanLexicon(d.Lexicon)}
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
		ref, who := d.Voice, ""
		if m, ok := cast[l.Character]; ok {
			who = m.Name
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
		for _, ls := range c.spans(src, def, narrator, true, ci, -1, lookup(ci)) {
			ls.Speed = d.Speed
			ch.Lines = append(ch.Lines, c.counted(ls))
		}
		if len(ch.Lines) > 0 {
			ch.Lines[0].Paragraph = false
		}
		c.spec.Chapters = append(c.spec.Chapters, ch)
	}
	out := c.spec.Chapters[:0]
	for _, ch := range c.spec.Chapters {
		if len(ch.Lines) > 0 {
			out = append(out, ch)
		}
	}
	c.spec.Chapters = out
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
	doc := markup.Parse(src, &markup.Options{NoChapters: true})
	var out []LineSpec
	var cur LineSpec
	var b strings.Builder
	var styles []string
	voice, character := key, who
	ok := key != ""
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

// DuplicateWords lists the words a book's lexicon has more than once
// (case-insensitive), each once.
func DuplicateWords(l []LexiconEntry) []string {
	seen := map[string]int{}
	var out []string
	for _, e := range l {
		w := strings.ToLower(strings.TrimSpace(e.Word))
		if w == "" {
			continue
		}
		seen[w]++
		if seen[w] == 2 {
			out = append(out, strings.TrimSpace(e.Word))
		}
	}
	return out
}

// VoiceNames are the [voice:NAME] names of a manuscript, in order of first
// appearance ("default" left out).
func VoiceNames(script string) []string {
	var out []string
	for _, n := range markup.Parse(script, nil).VoiceNames() {
		if !strings.EqualFold(n, "default") {
			out = append(out, n)
		}
	}
	return out
}

// ScriptStats are the stats line of a script: chapters, words (and
// unspaced characters) and the estimated runtime at speed.
type ScriptStats struct {
	Chapters int
	Words    int
	Chars    int
	Seconds  float64
}

// StatsOf counts a manuscript ("# " chapters) or a story's lines.
func StatsOf(script string, speed float64) ScriptStats {
	st := markup.Parse(script, nil).Stats()
	if speed <= 0 {
		speed = 1
	}
	return ScriptStats{Chapters: st.Chapters, Words: st.Words, Chars: st.Chars,
		Seconds: text.Estimate(st.Words, st.Chars).Seconds()/speed + float64(st.PauseMillis)/1000}
}

// StoryScript is a story's lines as one manuscript (chapter lines as
// headings), for stats and search.
func StoryScript(lines []StoryLine) string {
	var b strings.Builder
	for _, l := range lines {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		if l.Chapter {
			b.WriteString("# ")
			b.WriteString(strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l.Text), "#")))
			continue
		}
		b.WriteString(l.Text)
	}
	return b.String()
}
