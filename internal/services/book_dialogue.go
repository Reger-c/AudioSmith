package services

// An audiobook's dialogue blocks (plan/dialogue-dub-audiobook-plan.md §3,
// thread #88): "[对话] … [/对话]" in a manuscript (text/markup
// dialogue.go) marks a scene for the dialogue model. The compiler makes
// each line of a block a line of its own (LineSpec.Block): a turn in its
// speaker's voice (the book's cast: the names of [voice:NAME] and of the
// blocks are one cast), narration in the narrator's; stage directions are
// not read, a [pause] line lengthens the gap it stands in. Around a block
// the chapter is as it was: a [voice:] switch before it lasts after it.
//
// When the book renders with whole dialogue (Production.Dialogue), each
// block's turns go to the dialogue model a segment at a time
// (story_dialogue.go with dialogue.BookRules: the narration inside a
// block does not end its segment, blocks never join); the rest stays on
// the book's model. Without the dialogue model, or for a block of one
// speaker or in another language, the lines are read one by one in their
// voices. Either way a block's gaps are the dialogue pacing's (tight
// turns), and its lines made by the dialogue model are brought to the
// loudness of the chapter's narration (book_dialogue_render.go).
//
// A manuscript without blocks compiles exactly as before.

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/text"
	"github.com/hehehai/AudioSmith/internal/text/markup"
)

// DialogueBlockInfo is one dialogue block of a compiled manuscript, for
// the inspector and the API.
type DialogueBlockInfo struct {
	// Chapter indexes the spec's chapters; Index is its lines' Block.
	Chapter int
	Index   int
	Span    text.Span // in the manuscript: the opening marker to the closer
	// Speakers are the named speakers in order (the cast's spelling);
	// Uncast those without a voice, which are given one when the book
	// renders (they would read in the narrator's).
	Speakers []string
	Uncast   []string
	Turns    int // lines with a speaker
	Lines    int // lines read (turns and narration)
	Unknown  int // "？" turns
	// Language is false for a block in a language the dialogue models do
	// not speak (lang): read line by line.
	Language bool
	Lang     string
	// Tags are the delivery and non-verbal tags of its turns, which the
	// dialogue model leaves out.
	Tags []string
}

// CompileBook is Compile with the manuscript's dialogue blocks.
func CompileBook(d *LongformDoc, resolve VoiceResolver) (LongformSpec, []Problem, []DialogueBlockInfo) {
	c := &compiler{d: d, resolve: resolve, reported: map[string]bool{}, repeats: map[string]int{}}
	spec, probs := c.compile()
	return spec, probs, c.blocks
}

// blockProblems reports the markers' problems.
func (c *compiler) blockProblems(diags []text.Diagnostic) {
	for _, dg := range diags {
		p := Problem{Chapter: -1, Line: -1, Span: dg.Span}
		switch dg.Code {
		case markup.CodeUnclosedDialogue:
			p.Code = ProblemDialogueUnclosed
		case markup.CodeDialogueHeading:
			p.Code, p.Params = ProblemDialogueUnclosed, map[string]string{"at": "heading"}
		case markup.CodeNestedDialogue:
			p.Code = ProblemDialogueNested
		case markup.CodeStrayDialogueClose:
			p.Code = ProblemDialogueStrayClose
		case markup.CodeEmptyDialogue:
			p.Code = ProblemDialogueEmpty
		default:
			continue
		}
		c.problem(p)
	}
}

// blocksIn are the blocks that start in src[start:end].
func blocksIn(blocks []markup.DialogueBlock, start, end int) []markup.DialogueBlock {
	var out []markup.DialogueBlock
	for _, b := range blocks {
		if b.Span.Start >= start && b.Span.Start < end {
			out = append(out, b)
		}
	}
	return out
}

// bookChapter is what compiling a chapter with blocks needs.
type bookChapter struct {
	ch            int    // the document's chapter (problems)
	spec          int    // its index among the spec's chapters before empty ones go
	def, narrator string // the narrator's voice
	defOK         bool
	lookup        func(string) (string, string, bool)
}

// withBlocks compiles src[start:end], a chapter with blocks: the text
// around them as paragraphs (the voice going on across a block), each
// block line by line.
func (c *compiler) withBlocks(src string, start, end int, blocks []markup.DialogueBlock, bc bookChapter) []LineSpec {
	var out []LineSpec
	at := voiceAt{key: bc.def, who: bc.narrator, ok: bc.defOK}
	add := func(lines []LineSpec) {
		if len(lines) > 0 && len(out) > 0 {
			lines[0].Paragraph = true // a paragraph after a block, a block after text
		}
		out = append(out, lines...)
	}
	pos := start
	for i, b := range blocks {
		if b.Span.Start > pos {
			add(c.spansFrom(src[pos:b.Span.Start], &at, bc.narrator, true, bc.lookup))
		}
		lines, lead := c.block(src, b, i+1, bc)
		if lead > 0 && len(out) > 0 && len(lines) > 0 {
			// A pause before the first line: the gap before the block grows.
			l := &out[len(out)-1]
			l.PauseAfter = max(l.PauseAfter, c.d.Production.ParagraphGapMs) + lead
		}
		add(lines)
		pos = b.Span.End
	}
	if pos < end {
		add(c.spansFrom(src[pos:end], &at, bc.narrator, true, bc.lookup))
	}
	return out
}

// castName is how the book's cast spells a block's name: its VoiceCast
// key or [voice:] name of the same NameKey, else as written.
func (c *compiler) castName(name string) (string, string) {
	k := markup.NameKey(name)
	for n, ref := range c.d.VoiceCast {
		if markup.NameKey(n) == k && ref != "" {
			return n, ref
		}
	}
	if c.names == nil {
		c.names = append(VoiceNames(c.d.Script), "")
	}
	for _, n := range c.names {
		if n != "" && markup.NameKey(n) == k {
			return n, ""
		}
	}
	return name, ""
}

// block compiles block b (Index n of its chapter): its lines, and the
// pause before its first line (ms).
func (c *compiler) block(src string, b markup.DialogueBlock, n int, bc bookChapter) ([]LineSpec, int) {
	info := DialogueBlockInfo{Chapter: bc.spec, Index: n, Span: b.Span, Language: true}
	var out []LineSpec
	pending, lead := 0, 0
	seen := map[string]bool{}
	var words strings.Builder
	for _, l := range b.Lines {
		switch l.Kind {
		case markup.DialogueDirection:
			continue
		case markup.DialoguePause:
			pending += l.Millis
			continue
		}
		say := l.Text.Of(src)
		if !hasSpeech(say) {
			continue
		}
		key, who, ok := bc.def, bc.narrator, bc.defOK
		character := ""
		if l.Kind == markup.DialogueTurn {
			info.Turns++
			if l.Unknown || l.Name == "" {
				info.Unknown++
			} else {
				name, ref := c.castName(l.Name)
				character = name
				if !seen[markup.NameKey(name)] {
					seen[markup.NameKey(name)] = true
					info.Speakers = append(info.Speakers, name)
				}
				switch k, w, cok := c.blockVoice(name, ref, bc.ch); {
				case cok:
					key, who, ok = k, w, true
				default:
					if !containsKey(info.Uncast, name) {
						info.Uncast = append(info.Uncast, name)
					}
				}
			}
			for _, t := range text.Tags(say) {
				if tag := strings.ToLower(t.Of(say)); !strings.HasPrefix(tag, "[[") && !strings.HasPrefix(tag, "[/") && !containsKey(info.Tags, tag) {
					info.Tags = append(info.Tags, tag)
				}
			}
		}
		lines := c.spans(say, key, who, false, bc.ch, -1, bc.lookup)
		if !ok {
			lines = nil
		}
		for _, ls := range lines {
			ls.Block, ls.Character = n, character
			if l.Kind == markup.DialogueTurn {
				ls.Direction = strings.TrimSpace(l.Direction)
			}
			if pending > 0 {
				if len(out) == 0 {
					lead = pending
				} else {
					// The pause lengthens the gap it stands in.
					prev := &out[len(out)-1]
					g, _ := GapsOf(PacingDialogue)
					prev.PauseAfter = int(TurnGap(g, *prev, ls)*1000+0.5) + pending
				}
				pending = 0
			}
			ls.Paragraph = len(out) == 0
			out = append(out, ls)
			info.Lines++
			words.WriteString(say)
			words.WriteByte('\n')
		}
	}
	if pending > 0 && len(out) > 0 {
		// A pause after the last line: the gap after the block grows.
		l := &out[len(out)-1]
		l.PauseAfter = max(l.PauseAfter, c.d.Production.ParagraphGapMs) + pending
	}
	if len(out) == 0 {
		return nil, 0
	}
	info.Lang, info.Language = blockLanguage(c.d.Language, words.String())
	p := Problem{Chapter: bc.ch, Line: -1, Span: b.Open}
	if len(info.Uncast) > 0 {
		p.Code, p.Params = ProblemDialogueUncast, map[string]string{"names": strings.Join(info.Uncast, ", ")}
		c.problem(p)
	}
	if info.Unknown > 0 {
		p.Code, p.Params = ProblemDialogueUnknown, map[string]string{"count": strconv.Itoa(info.Unknown)}
		c.problem(p)
	}
	switch {
	case !info.Language:
		p.Code, p.Params = ProblemDialogueLanguage, map[string]string{"lang": info.Lang}
		c.problem(p)
	case len(info.Speakers) == 1 && info.Unknown == 0:
		p.Code, p.Params = ProblemDialogueOneSpeaker, nil
		c.problem(p)
	case len(info.Tags) > 0:
		p.Code, p.Params = ProblemDialogueTags, map[string]string{"tags": strings.Join(info.Tags, " ")}
		c.problem(p)
	}
	c.blocks = append(c.blocks, info)
	return out, lead
}

// blockVoice resolves a block's speaker: their voice in the cast, else a
// voice of that name; ok is false for a name nobody is cast as.
func (c *compiler) blockVoice(name, ref string, ch int) (string, string, bool) {
	if ref != "" {
		k, who, ok := c.castVoice(ref, name, ch, -1)
		return k, who, ok
	}
	if k, ok := c.voice(name); ok {
		return k, name, true
	}
	return "", "", false
}

func containsKey(list []string, s string) bool {
	for _, x := range list {
		if markup.NameKey(x) == markup.NameKey(s) {
			return true
		}
	}
	return false
}

// blockLanguage is the language of a block's words (the book's, else
// what they look like) and whether the dialogue models speak it
// (Chinese and English).
func blockLanguage(book, words string) (string, bool) {
	lang := strings.ToLower(book)
	if lang == "" {
		lang = "en"
		han := false
		for _, r := range words {
			switch {
			case unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r):
				return "ja", false
			case unicode.Is(unicode.Hangul, r):
				return "ko", false
			case unicode.Is(unicode.Han, r):
				han = true
			}
		}
		if han {
			lang = "zh"
		}
	}
	return lang, strings.HasPrefix(lang, "zh") || strings.HasPrefix(lang, "en")
}

// book reports whether a chapter is an audiobook's.
func (p *ChapterParams) book() bool { return p.Workflow == string(core.ProjectAudiobook) }
