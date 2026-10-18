package main

// The Audiobook's dialogue passages (plan/dialogue-dub-audiobook-plan.md
// §3, thread #88): "[对话] … [/对话]" blocks of "名字：台词" lines that a
// dialogue model speaks a scene at a time (services/book_dialogue.go).
// Insert → Dialogue passage writes an empty one (the two latest names of
// the manuscript, else Speaker A and B, the first name selected), or
// makes the selection one, every word kept (markup.WrapDialogue), with
// Undo. The block's names are the book's cast: a name nobody is cast as
// gets a voice of its own, unlike the others', when the book renders
// (or at 「自动分配」). The dialogue model is used when it can run here
// and is downloaded; otherwise each line is read in its voice.
//
// Files: audiobook_dialogue.go (this: the actions), the editor's styling
// audiobook_dialogue_editor.go, the inspector's section
// audiobook_dialogue_view.go.

import (
	"slices"
	"strconv"
	"strings"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/hub"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/text"
	"github.com/hehehai/AudioSmith/internal/text/markup"
)

// dialogueMarkers are the markers Insert writes: [对话] in the Chinese
// interface, [dialogue] in the others (the parser takes both, and 【对话】).
func dialogueMarkers() (open, close string) {
	w := i18n.X("dialogue block tag", "dialogue")
	return "[" + w + "]", "[/" + w + "]"
}

// dialogueColon follows a name in a block: "：" in Chinese, ": " else.
func dialogueColon() string { return i18n.X("dialogue block name", ": ") }

// recentNames are the last two names of the manuscript before offset at
// (its [voice:] switches and block speakers), the latest first; else
// Speaker A and B.
func recentNames(script string, at int) [2]string {
	before := script[:min(max(at, 0), len(script))]
	type named struct {
		at   int
		name string
	}
	var all []named
	for _, t := range markup.Parse(before, nil).Tokens {
		if t.Kind == markup.Voice {
			all = append(all, named{t.Span.Start, t.Name})
		}
	}
	blocks, _ := markup.DialogueBlocks(before)
	for _, b := range blocks {
		for _, l := range b.Lines {
			if l.Kind == markup.DialogueTurn {
				all = append(all, named{l.Line.Start, l.Name})
			}
		}
	}
	slices.SortStableFunc(all, func(x, y named) int { return x.at - y.at })
	var seen []string // latest last
	for _, n := range all {
		if n.name == "" || strings.EqualFold(n.name, "default") {
			continue
		}
		seen = slices.DeleteFunc(seen, func(s string) bool { return markup.NameKey(s) == markup.NameKey(n.name) })
		seen = append(seen, n.name)
	}
	out := [2]string{i18n.T("Speaker A"), i18n.T("Speaker B")}
	for i := 0; i < 2 && i < len(seen); i++ {
		out[i] = seen[len(seen)-1-i]
	}
	if markup.NameKey(out[0]) == markup.NameKey(out[1]) {
		out[1] = i18n.T("Speaker A")
	}
	return out
}

// blankAround are the line breaks that set text inserted between before
// and after apart as paragraphs.
func blankAround(before, after string) (string, string) {
	pre, post := "\n\n", "\n\n"
	switch {
	case before == "" || strings.HasSuffix(before, "\n\n"):
		pre = ""
	case strings.HasSuffix(before, "\n"):
		pre = "\n"
	}
	switch {
	case after == "":
		post = ""
	case strings.HasPrefix(after, "\n\n"):
		post = ""
	case strings.HasPrefix(after, "\n"):
		post = "\n"
	}
	return pre, post
}

// lineRange widens [start, end) to whole lines of s.
func lineRange(s string, start, end int) (int, int) {
	if i := strings.LastIndexByte(s[:start], '\n'); i >= 0 {
		start = i + 1
	} else {
		start = 0
	}
	if end > start && s[end-1] == '\n' {
		end--
	}
	if i := strings.IndexByte(s[end:], '\n'); i >= 0 {
		end += i
	} else {
		end = len(s)
	}
	return start, end
}

// insertDialogue is Insert → Dialogue passage: an empty block at the
// caret, or the selected lines made one.
func (a *app) insertDialogue(p *lfPage) {
	a.syncManuscript(p)
	s := p.ed.Buffer.String()
	sel := p.ed.Selection()
	start, end := min(sel.Anchor, sel.Head), max(sel.Anchor, sel.Head)
	start, end = min(max(start, 0), len(s)), min(max(end, 0), len(s))
	open, close := dialogueMarkers()
	colon := dialogueColon()
	had := len(p.blocks) > 0
	if strings.TrimSpace(s[start:end]) == "" {
		names := recentNames(s, start)
		pre, post := blankAround(s[:start], s[end:])
		tpl := open + "\n" + names[0] + colon + "\n" + names[1] + colon + "\n" + close
		p.ed.Select(start, end)
		a.insertMarkup(p, pre+tpl+post)
		at := start + len(pre) + len(open) + 1
		p.ed.Select(at, at+len(names[0]))
	} else {
		start, end = lineRange(s, start, end)
		chosen := s[start:end]
		if strings.Contains(chosen, "\n#") || text.IsHeading(chosen) || len(markup.Parse(chosen, nil).Chapters()) > 1 {
			a.toast(i18n.T("A dialogue passage stays within one chapter: select lines without a heading."))
			return
		}
		if blocks, diags := markup.DialogueBlocks(chosen); len(blocks) > 0 || len(diags) > 0 {
			a.toast(i18n.T("The selection already has a dialogue passage."))
			return
		}
		w := markup.WrapDialogue(chosen, markup.WrapOptions{Open: open, Close: close, Colon: colon, Known: bookCast(p)})
		pre, post := blankAround(s[:start], s[end:])
		p.ed.Select(start, end)
		a.insertMarkup(p, pre+w.Text+post)
		msg := i18n.N(len(w.Speakers), "Made a dialogue passage: %[1]d speaker · %[2]s", "Made a dialogue passage: %[1]d speakers · %[2]s",
			len(w.Speakers), i18n.N(w.Turns, "%d line", "%d lines", w.Turns))
		if w.Unknown > 0 {
			msg += i18n.N(w.Unknown, "; %d line has no speaker, marked “？”", "; %d lines have no speaker, marked “？”", w.Unknown)
		}
		a.composer.undoToast = ""
		a.toastUndo(msg, func() {
			p.ed.Buffer.Undo()
			a.syncManuscript(p)
		})
	}
	if !had {
		a.bookDialogueOn(p)
	}
	p.open["dialogue"] = true
}

// bookDialogueOn turns whole dialogue on for a book's first passage, with
// the model this Mac can run (left off when none can).
func (a *app) bookDialogueOn(p *lfPage) {
	d := &p.doc
	if e, _, why := dialogueChoice(a.macMemoryGB(), d); why == "" {
		d.Production.Dialogue, d.Production.DialogueModel = true, e.Repo
	}
}

// bookDialogueDefault turns whole dialogue on for a book whose manuscript
// has passages the user never chose a dialogue model for (written by hand,
// imported): DialogueModel stays empty until then.
func (a *app) bookDialogueDefault(p *lfPage) {
	if p.kind == core.ProjectAudiobook && len(p.blocks) > 0 && p.doc.Production.DialogueModel == "" && a.macMemoryGB() > 0 {
		a.bookDialogueOn(p)
	}
}

// unwrapDialogue turns the block at offset at back into paragraphs.
func (a *app) unwrapDialogue(p *lfPage, at int) {
	a.syncManuscript(p)
	s := p.ed.Buffer.String()
	b, ok := blockAt(s, at)
	if !ok {
		return
	}
	out := markup.UnwrapDialogue(s, b, markup.VoiceAt(s, b.Span.Start))
	p.ed.Select(b.Span.Start, b.Span.End)
	a.insertMarkup(p, out)
}

// blockAt is the block of s at offset at (its markers included).
func blockAt(s string, at int) (markup.DialogueBlock, bool) {
	blocks, _ := markup.DialogueBlocks(s)
	for _, b := range blocks {
		if at >= b.Span.Start && at <= b.Span.End {
			return b, true
		}
	}
	return markup.DialogueBlock{}, false
}

// blockWhat is the audition key of a compiled block: "block:<chapter>:<index>".
func blockWhat(b services.DialogueBlockInfo) string {
	return "block:" + strconv.Itoa(b.Chapter) + ":" + strconv.Itoa(b.Index)
}

// blockSpec is a chapter of block what's lines alone, for its preview.
func blockSpec(spec *services.LongformSpec, what string) (services.ChapterSpec, bool) {
	parts := strings.Split(strings.TrimPrefix(what, "block:"), ":")
	if len(parts) != 2 {
		return services.ChapterSpec{}, false
	}
	ch, err1 := strconv.Atoi(parts[0])
	idx, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || ch < 0 || ch >= len(spec.Chapters) {
		return services.ChapterSpec{}, false
	}
	var lines []services.LineSpec
	for _, l := range spec.Chapters[ch].Lines {
		if l.Block == idx {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return services.ChapterSpec{}, false
	}
	lines[0].Paragraph = false
	return services.ChapterSpec{Title: spec.Chapters[ch].Title, Lines: lines}, true
}

// uncastNames are the block speakers of a book nobody is cast as.
func uncastNames(blocks []services.DialogueBlockInfo) []string {
	var out []string
	seen := map[string]bool{}
	for _, b := range blocks {
		for _, n := range b.Uncast {
			if !seen[markup.NameKey(n)] {
				seen[markup.NameKey(n)] = true
				out = append(out, n)
			}
		}
	}
	return out
}

// castUncast gives each block speaker nobody is cast as a voice of its
// own: one the narrator and the cast do not use, while there are any. It
// returns "name: voice" for each, for the toast.
func castUncast(d *services.LongformDoc, blocks []services.DialogueBlockInfo, voices []lfVoice) []string {
	names := uncastNames(blocks)
	if len(names) == 0 || len(voices) == 0 {
		return nil
	}
	used := map[string]bool{d.Voice: true}
	for _, ref := range d.VoiceCast {
		used[ref] = true
	}
	if d.VoiceCast == nil {
		d.VoiceCast = map[string]string{}
	}
	var out []string
	next := 0
	for _, n := range names {
		pick := voices[next%len(voices)]
		for range voices {
			v := voices[next%len(voices)]
			next++
			if !used[v.ref] {
				pick = v
				break
			}
		}
		used[pick.ref] = true
		d.VoiceCast[n] = pick.ref
		out = append(out, n+": "+pick.label)
	}
	return out
}

// autoCastBook casts a book's uncast block speakers before it renders, and
// says so.
func (a *app) autoCastBook(p *lfPage) {
	if p.kind != core.ProjectAudiobook {
		return
	}
	got := castUncast(&p.doc, p.blocks, a.lfVoices(entryFor(p.doc.Model)))
	if len(got) == 0 {
		return
	}
	a.compileLf(p)
	a.toast(i18n.N(len(got), "Gave a dialogue speaker a voice of their own: %[2]s (change it under Cast).",
		"Gave %[1]d dialogue speakers voices of their own: %[2]s (change them under Cast).", len(got), strings.Join(got, ", ")))
}

// bookDialogueModel is the dialogue model a book's render uses, and
// whether it can (downloaded, and this Mac can run it).
func (a *app) bookDialogueModel(p *lfPage) (catalog.Entry, bool) {
	e, _, why := dialogueChoice(a.macMemoryGB(), &p.doc)
	if why != "" || !p.doc.Production.Dialogue {
		return e, false
	}
	m := a.model(e)
	return e, !m.known || m.local.State == hub.Downloaded
}

// bookDialogueSpec readies a book's spec for its render: the dialogue
// model it can use, else the passages read line by line.
func (a *app) bookDialogueSpec(p *lfPage, spec *services.LongformSpec) {
	if p.kind != core.ProjectAudiobook || len(p.blocks) == 0 {
		return
	}
	e, ok := a.bookDialogueModel(p)
	spec.Production.Dialogue = ok
	if ok {
		spec.Production.DialogueModel = e.Repo
	}
}
