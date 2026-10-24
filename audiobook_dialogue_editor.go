package main

// The manuscript editor's dialogue passages (audiobook_dialogue.go): the
// markers in the accent, each speaker's name in a colour of their own,
// stage directions grey and italic; the passage on a faint tint with a
// dot per line in the gutter (amber when it lacks a closer or a voice),
// "2 人 · 5 句 · 多人对话 · 轻量" after its opening marker; cast names
// offered as a passage's line is begun; and in the context menu, Preview
// this dialogue and Turn back into paragraphs.

import (
	"hash/fnv"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/text"
	"github.com/hehehai/AudioSmith/internal/text/markup"
)

// Token kinds of a passage in the manuscript's theme.
const (
	tokenMarker    = editor.TokenMeta     // [对话] [/对话]
	tokenDirection = editor.TokenEmphasis // （低声） and whole-line stage directions
)

// speakerKinds colour the speakers' names: the token kinds a manuscript
// has no other use for.
var speakerKinds = []editor.TokenKind{editor.TokenProperty, editor.TokenConstant, editor.TokenNumber, editor.TokenAttribute,
	editor.TokenTag, editor.TokenLink}

// speakerSlots give each name the next colour as the editor first meets
// it, so the first six speakers of a book have colours of their own and a
// name keeps its colour while the book is open.
type speakerSlots struct{ slot map[string]int }

func (s *speakerSlots) kind(name string) editor.TokenKind {
	k := markup.NameKey(name)
	if s == nil {
		h := fnv.New32a()
		h.Write([]byte(k))
		return speakerKinds[h.Sum32()%uint32(len(speakerKinds))]
	}
	if s.slot == nil {
		s.slot = map[string]int{}
	}
	n, ok := s.slot[k]
	if !ok {
		n = len(s.slot)
		s.slot[k] = n
	}
	return speakerKinds[n%len(speakerKinds)]
}

// speakerColors are the names' colours, light and dark.
func speakerColors(t *ui.Theme) []ui.Color {
	if t.Dark {
		return []ui.Color{ui.Hex("#2bb3a3"), ui.Hex("#e0a24a"), ui.Hex("#8f8bf5"), ui.Hex("#ec7fb0"), ui.Hex("#6cb4ee"), ui.Hex("#a3c75c")}
	}
	return []ui.Color{ui.Hex("#0b7a6e"), ui.Hex("#a3530a"), ui.Hex("#4b46c8"), ui.Hex("#b4256a"), ui.Hex("#1d6fb8"), ui.Hex("#5a7a12")}
}

// tokenizeTags tokenizes s (markup outside a passage) from offset base.
func tokenizeTags(s string, base int, tokens []editor.Token) []editor.Token {
	pos := 0
	for _, sp := range text.Tags(s) {
		if sp.Start > pos {
			tokens = append(tokens, editor.Token{End: base + sp.Start, Kind: editor.TokenText})
		}
		kind := tagKind(sp.Of(s))
		if kind == editor.TokenInvalid { // [voice:…]: valid here
			kind = editor.TokenVariable
		}
		tokens = append(tokens, editor.Token{End: base + sp.End, Kind: kind})
		pos = sp.End
	}
	if pos < len(s) {
		tokens = append(tokens, editor.Token{End: base + len(s), Kind: editor.TokenText})
	}
	return tokens
}

// tokenizeBlockLine tokenizes s, a line (or part of one) inside a passage,
// from offset base.
func tokenizeBlockLine(slots *speakerSlots, s string, base int, tokens []editor.Token) []editor.Token {
	l, ok := markup.DialogueLineOf(s)
	if !ok {
		if s != "" {
			tokens = append(tokens, editor.Token{End: base + len(s), Kind: editor.TokenText})
		}
		return tokens
	}
	if l.Kind == markup.DialogueDirection {
		return append(tokens, editor.Token{End: base + len(s), Kind: tokenDirection})
	}
	pos := 0
	mark := func(sp text.Span, kind editor.TokenKind) {
		if sp.Len() == 0 || sp.Start < pos {
			return
		}
		if sp.Start > pos {
			tokens = tokenizeTags(s[pos:sp.Start], base+pos, tokens)
		}
		tokens = append(tokens, editor.Token{End: base + sp.End, Kind: kind})
		pos = sp.End
	}
	if l.Kind == markup.DialogueTurn {
		switch {
		case l.Unknown || l.Name == "":
			mark(l.NameSpan, editor.TokenInvalid)
		default:
			mark(l.NameSpan, slots.kind(l.Name))
		}
		mark(l.DirSpan, tokenDirection)
	}
	if pos < len(s) {
		tokens = tokenizeTags(s[pos:], base+pos, tokens)
	}
	return tokens
}

// tokenizeManuscriptWith highlights a manuscript line: Studio's tags (tagKind),
// "# " headings, [voice:] as valid, and dialogue passages (the state: 1
// inside one; a heading ends it).
func tokenizeManuscriptWith(slots *speakerSlots, line string, state uint64, tokens []editor.Token) ([]editor.Token, uint64) {
	if text.IsHeading(line) {
		return append(tokens, editor.Token{End: len(line), Kind: editor.TokenHeading}), 0
	}
	ms := markup.Markers(line)
	if len(ms) == 0 && state == 0 {
		return tokenizeTags(line, 0, tokens), 0
	}
	in := state == 1
	part := func(a, b int) {
		if b <= a {
			return
		}
		if in {
			tokens = tokenizeBlockLine(slots, line[a:b], a, tokens)
		} else {
			tokens = tokenizeTags(line[a:b], a, tokens)
		}
	}
	pos := 0
	for _, m := range ms {
		part(pos, m.Span.Start)
		tokens = append(tokens, editor.Token{End: m.Span.End, Kind: tokenMarker})
		in = m.Open // a nested opener keeps it open, a stray closer closed
		pos = m.Span.End
	}
	part(pos, len(line))
	if in {
		return tokens, 1
	}
	return tokens, 0
}

// bookDeco is the styling of a manuscript's passages, made again when the
// text, the compile or the dialogue model's state changes.
type bookDeco struct {
	key         string
	backgrounds []editor.LineBackground
	marks       []editor.GutterMark
	inlays      []editor.Inlay
	lines       int // the manuscript's lines when made
}

// manuscriptDeco styles the passages of the manuscript editor.
func (a *app) manuscriptDeco(c *ui.Context, p *lfPage) {
	t := c.Theme()
	st := p.ed
	// The document's script is the editor's text from here on, read once
	// (syncManuscript); the deco takes it from the document.
	a.syncManuscript(p)
	e, ready := a.bookDialogueModel(p)
	key := strconv.FormatUint(st.Buffer.Version(), 10) + "|" + p.fp + "|" + strconv.Itoa(p.lfc.rev) + "|" + strconv.FormatBool(ready) + "|" + e.ID + "|" + i18n.Locale() + "|" + strconv.FormatBool(t.Dark)
	if p.deco.key != key && p.deco.key != "" && p.lfc.stale(st.Buffer.Version()) && p.deco.lines == st.Buffer.LineCount() {
		// Typing in a line of a large manuscript whose compile is pending:
		// the passages are on the lines they were on until it lands (its
		// revision is in the key).
		p.deco.key = key
	}
	if p.deco.key != key {
		// The text only when it changed (review #102: 600 KB a frame).
		src := p.doc.Script
		// While a large manuscript's compile is pending (longform_compile.go),
		// its passages' spans are those of the text before: the passages
		// keep their summaries, which the editor moves with the edits, and
		// are matched to them by their order.
		stale := p.lfc.stale(st.Buffer.Version())
		inlays := p.deco.inlays
		p.deco = bookDeco{key: key, lines: st.Buffer.LineCount()}
		blocks, _ := markup.DialogueBlocks(src)
		starts := []int{0}
		for i := 0; i < len(src); i++ {
			if src[i] == '\n' {
				starts = append(starts, i+1)
			}
		}
		lineOf := func(off int) int {
			i, _ := slices.BinarySearch(starts, off+1)
			return max(i-1, 0)
		}
		k := tok(t)
		for i, b := range blocks {
			info, compiled := blockInfo(p, b)
			if stale && len(blocks) == len(p.blocks) {
				info, compiled = p.blocks[i], true
			}
			warn := !b.Closed() || compiled && len(info.Uncast) > 0
			bg, dot := k.Accent.Alpha(0.05), k.Accent.Alpha(0.35)
			if warn {
				bg, dot = t.Warning.Alpha(0.07), t.Warning.Alpha(0.6)
			}
			first, last := lineOf(b.Span.Start), lineOf(max(b.Span.End-1, b.Span.Start))
			p.deco.backgrounds = append(p.deco.backgrounds, editor.LineBackground{First: first, Last: last, Color: bg})
			for l := first; l <= last; l++ {
				p.deco.marks = append(p.deco.marks, editor.GutterMark{Line: l, Color: dot})
			}
			if compiled && !stale {
				p.deco.inlays = append(p.deco.inlays, editor.Inlay{Offset: b.Open.End, Text: "  " + blockSummary(info, e.DisplayName(), ready)})
			}
		}
		if stale {
			p.deco.inlays = inlays
		} else {
			st.SetInlays(p.deco.inlays)
		}
	}
	st.LineBackgrounds, st.GutterMarks = p.deco.backgrounds, p.deco.marks
	if st.OnCompletion == nil {
		st.OnCompletion = func(r editor.CompletionRequest) { a.completeName(p, r) }
		st.ContextMenu = func(m *ui.Menu) { a.blockMenu(p, m) }
	}
}

// blockInfo is the compiled passage that starts where b does.
func blockInfo(p *lfPage, b markup.DialogueBlock) (services.DialogueBlockInfo, bool) {
	for _, info := range p.blocks {
		if info.Span.Start == b.Span.Start {
			return info, true
		}
	}
	return services.DialogueBlockInfo{}, false
}

// blockSummary is a passage's line: speakers, lines, and how it is made.
func blockSummary(b services.DialogueBlockInfo, model string, ready bool) string {
	parts := []string{i18n.N(len(b.Speakers), "%d speaker", "%d speakers", len(b.Speakers)), i18n.N(b.Turns, "%d dialogue line", "%d dialogue lines", b.Turns)}
	if ready && b.Language && len(b.Speakers) > 1 {
		parts = append(parts, model)
	} else {
		parts = append(parts, i18n.T("line by line"))
	}
	return strings.Join(parts, " · ")
}

// completeName offers the cast's names as a passage's line is begun.
func (a *app) completeName(p *lfPage, r editor.CompletionRequest) {
	src := p.ed.Buffer.String()
	at := min(max(r.Offset, 0), len(src))
	head := src[strings.LastIndexByte(src[:at], '\n')+1 : at]
	if strings.ContainsAny(head, ":：") || strings.TrimSpace(head) != strings.TrimSpace(r.Prefix) {
		return
	}
	if _, ok := blockAt(src, at); !ok {
		return
	}
	colon := dialogueColon()
	var items []editor.Completion
	for _, n := range bookCast(p) {
		items = append(items, editor.Completion{Label: n, Insert: n + colon, Kind: editor.CompletionVariable, Detail: i18n.T("Cast")})
	}
	if len(items) > 0 {
		p.ed.ShowCompletions(r.Offset-len(r.Prefix), items)
	}
}

// blockMenu adds a passage's actions to the editor's context menu, with
// the caret in one.
func (a *app) blockMenu(p *lfPage, m *ui.Menu) {
	src := p.ed.Buffer.String()
	at := p.ed.Cursor()
	b, ok := blockAt(src, at)
	if !ok {
		return
	}
	m.Separator()
	info, compiled := blockInfo(p, b)
	if m.Item(i18n.T("Preview this dialogue")).Disabled(!compiled || a.engState != engineReady).Chosen() {
		a.startLf(p, blockWhat(info))
	}
	if m.Item(i18n.T("Turn back into paragraphs")).Disabled(a.lfLocked(p)).Chosen() {
		a.unwrapDialogue(p, at)
	}
}
