package main

// The Audiobook page's logic (/audiobook): the manuscript editor (the
// script markup with "# " chapters and [voice:NAME] highlighted), the
// markup toolbar, importing a book, the sample, the cast found in the
// manuscript, and the book's pronunciations.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui/editor"
	"github.com/hehehai/AudioSmith/internal/doc"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/text"
	"github.com/hehehai/AudioSmith/internal/text/markup"
)

// maxBookBytes caps a manuscript import.
const maxBookBytes = 64 << 20

// newManuscriptEditor is the Audiobook's editor.
func newManuscriptEditor() *editor.State {
	buf := editor.NewBuffer("")
	buf.SetLanguage(manuscriptLanguage)
	return &editor.State{
		Buffer:          buf,
		SoftWrap:        true,
		LineNumbers:     editor.LineNumbersOff,
		NoFolding:       true,
		NoIndentGuides:  true,
		NoAutoClose:     true,
		NoActiveLine:    true,
		NoScrollPastEnd: true,
		LineHeight:      1.6,
		Label:           i18n.T("Manuscript"),
	}
}

// manuscriptLanguage highlights the markup of a manuscript: Studio's
// tags (tagKind), "# " chapter headings, and [voice:] as valid.
var manuscriptLanguage = &editor.Language{
	Name:      "AudioSmith manuscript",
	Tokenizer: editor.TokenizerFunc(tokenizeManuscript),
}

func tokenizeManuscript(line string, state uint64, tokens []editor.Token) ([]editor.Token, uint64) {
	if text.IsHeading(line) {
		return append(tokens, editor.Token{End: len(line), Kind: editor.TokenHeading}), state
	}
	pos := 0
	for _, sp := range text.Tags(line) {
		if sp.Start > pos {
			tokens = append(tokens, editor.Token{End: sp.Start, Kind: editor.TokenText})
		}
		kind := tagKind(sp.Of(line))
		if kind == editor.TokenInvalid { // [voice:…]: valid here
			kind = editor.TokenVariable
		}
		tokens = append(tokens, editor.Token{End: sp.End, Kind: kind})
		pos = sp.End
	}
	if pos < len(line) {
		tokens = append(tokens, editor.Token{End: len(line), Kind: editor.TokenText})
	}
	return tokens, state
}

// syncManuscript reconciles the document's script and the editor.
func (a *app) syncManuscript(p *lfPage) {
	if p.ed == nil {
		return
	}
	if p.doc.Script != p.edSynced {
		if p.ed.Buffer.String() != p.doc.Script {
			p.ed.Buffer.Set(p.doc.Script)
		}
		p.edSynced, p.edVersion = p.doc.Script, p.ed.Buffer.Version()
		p.scriptRev++
		return
	}
	if v := p.ed.Buffer.Version(); v != p.edVersion {
		p.edVersion = v
		p.doc.Script = p.ed.Buffer.String()
		p.edSynced = p.doc.Script
		p.scriptRev++
	}
}

// setManuscript replaces the manuscript as one undoable edit.
func (a *app) setManuscript(p *lfPage, s string) {
	p.doc.Script = s
	a.syncManuscript(p)
}

// insertMarkup inserts markup at the caret (replacing the selection).
func (a *app) insertMarkup(p *lfPage, s string) {
	a.syncManuscript(p)
	p.ed.Insert(s)
	a.syncManuscript(p)
}

// wrapMarkup wraps the selection in open … close.
func (a *app) wrapMarkup(p *lfPage, open, close string) {
	sel := p.ed.SelectedText()
	a.insertMarkup(p, open+sel+close)
	if sel == "" {
		// The caret between the tags, ready to type.
		at := p.ed.Cursor() - len(close)
		p.ed.Select(at, at)
	}
}

// insertVoice inserts [voice:NAME] with NAME selected, to type over.
func (a *app) insertVoice(p *lfPage) {
	name := i18n.T("NAME")
	a.insertMarkup(p, "[voice:"+name+"]")
	end := p.ed.Cursor() - 1
	p.ed.Select(end-len(name), end)
}

// importBook asks for a book and replaces the manuscript with it.
func (a *app) importBook(p *lfPage) {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.win, Title: i18n.T("Import a book"),
			Filters: []mygo.FileFilter{{Name: i18n.T("Books"), Extensions: []string{"txt", "md", "markdown", "epub", "docx"}}}})
		if err != nil || len(paths) == 0 {
			return
		}
		a.post(func() { a.importBookFrom(p, paths[0]) })
	}()
}

// importBookFrom reads a book in the background (chapterised) and makes
// it the manuscript, offering Undo.
func (a *app) importBookFrom(p *lfPage, path string) {
	if p.importing {
		return
	}
	p.importing = true
	a.background(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		d, err := doc.Open(ctx, path, doc.Limits{MaxFileBytes: maxBookBytes})
		a.post(func() {
			p.importing = false
			if err != nil {
				a.toast(bookImportError(path, err))
				return
			}
			s := d.Script()
			if p.doc.Title == "" && d.Title != "" {
				p.doc.Title = d.Title
			}
			if p.doc.Book.Author == "" && d.Author != "" {
				p.doc.Book.Author = d.Author
			}
			if d.Language != "" && p.doc.Language == "" {
				p.doc.Language = d.Language
			}
			a.setManuscript(p, s)
			n := len(markup.Parse(s, nil).Chapters())
			a.composer.undoToast = "" // not the Studio's
			a.toastUndo(i18n.N(n, "Imported %[2]s · %[1]d chapter", "Imported %[2]s · %[1]d chapters", n, filepath.Base(path)), func() {
				p.ed.Buffer.Undo()
				a.syncManuscript(p)
			})
		})
	})
}

// bookImportError explains why a book could not be imported.
func bookImportError(path string, err error) string {
	name := filepath.Base(path)
	switch {
	case errors.Is(err, doc.ErrUnsupported):
		return i18n.T("%s: this kind of file cannot be imported (PDF comes later).", name)
	case errors.Is(err, doc.ErrTooLarge):
		return i18n.T("%s is too large to import (64 MB at most).", name)
	case errors.Is(err, doc.ErrNoText):
		return i18n.T("%s has no text to import.", name)
	case errors.Is(err, os.ErrNotExist):
		return i18n.T("%s was not found.", name)
	}
	return i18n.T("Could not import %s: %v", name, err)
}

// toastUndo shows a toast with Undo on the next build.
func (a *app) toastUndo(msg string, undo func()) {
	a.lfUndo = append(a.lfUndo, lfUndoToast{msg, undo})
}

// lfUndoToast is a toast with an Undo action.
type lfUndoToast struct {
	msg  string
	undo func()
}

// sampleManuscript is the Audiobook's sample: chapters, two voices,
// pauses, styles and reactions.
func sampleManuscript() string {
	return i18n.T("# The Night Ferry") + "\n\n" +
		i18n.T("The last ferry left the harbour at eleven. [pause 500ms] Fog had settled on the water like wool.") + "\n\n" +
		"[voice:Ines]" + i18n.T("Is this the boat to Harrow Point?") + "\n\n" +
		"[voice:Tomas][slow]" + i18n.T("It is the only boat,") + "[/slow] " + i18n.T("the ferryman said. [laughter] Sit anywhere you like.") + "\n\n" +
		"[voice:default]" + i18n.T("She chose the bench by the lantern, and watched the lights of the town go out one by one.") + "\n\n" +
		i18n.T("# Harrow Point") + "\n\n" +
		i18n.T("By morning the fog had lifted. [pause 1s] The island was smaller than the map promised.") + "\n\n" +
		"[voice:Ines][fast]" + i18n.T("We're here already?") + "[/fast] [sigh] " + i18n.T("I slept the whole way.") + "\n\n" +
		"[voice:Tomas]" + i18n.T("Everyone does. The crossing is [emphasis]longer[/emphasis] than it looks. Mind the sign: it says [spell]NO[/spell] swimming.")
}

// loadSample fills an empty manuscript with the sample.
func (a *app) loadSample(p *lfPage) {
	a.setManuscript(p, sampleManuscript())
	if p.doc.Title == "" {
		p.doc.Title = i18n.T("The Night Ferry")
	}
	// Cast the two voices from the library when there are some.
	e := entryFor(p.doc.Model)
	vs := a.lfVoices(e)
	if p.doc.VoiceCast == nil {
		p.doc.VoiceCast = map[string]string{}
	}
	for i, name := range []string{"Ines", "Tomas"} {
		if p.doc.VoiceCast[name] == "" && len(vs) > 0 {
			p.doc.VoiceCast[name] = vs[(i+1)%len(vs)].ref
		}
	}
	if p.doc.Voice == "" && len(vs) > 0 {
		p.doc.Voice = vs[0].ref
	}
}

// bookCast are the names the manuscript casts with [voice:NAME].
func bookCast(p *lfPage) []string { return castNamesOf(p.doc.Script) }

func castNamesOf(script string) []string {
	var out []string
	for _, n := range markup.Parse(script, nil).VoiceNames() {
		if !strings.EqualFold(n, "default") && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}
