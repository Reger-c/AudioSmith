// Package doc imports documents into scripts: plain text and Markdown,
// EPUB, DOCX and subtitles (SRT/WebVTT) are built in; PDF is plugged in by
// the app (PDFKit through the Swift helper) with Register. It also writes
// chapter cue sheets. Subtitle parsing and writing live in doc/subtitle.
//
// Every importer is bounded by Limits (file size, decompressed size, ZIP
// entries, output length) so a hostile file cannot exhaust memory.
package doc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
)

// Format identifies an input format.
type Format string

const (
	TXT      Format = "txt"
	Markdown Format = "md"
	EPUB     Format = "epub"
	DOCX     Format = "docx"
	PDF      Format = "pdf"
	SRT      Format = "srt"
	VTT      Format = "vtt"
)

// Document is an imported document.
type Document struct {
	Format   Format
	Title    string // from metadata, "" when unknown
	Author   string
	Language string // BCP-47 from metadata, "" when unknown
	Chapters []Chapter
}

// Chapter is a titled run of text. Text holds paragraphs separated by one
// blank line, without the title.
type Chapter struct {
	Title string
	Text  string
}

// Script renders the document as an AudioSmith script: "# Title" before
// every titled chapter, paragraphs separated by blank lines. An untitled
// chapter has no heading (it continues the previous one when read).
func (d *Document) Script() string {
	var b strings.Builder
	for _, c := range d.Chapters {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		if c.Title != "" {
			b.WriteString("# ")
			b.WriteString(oneLine(c.Title))
			if c.Text != "" {
				b.WriteString("\n\n")
			}
		}
		b.WriteString(c.Text)
	}
	return b.String()
}

// Text is the document's text without chapter headings.
func (d *Document) Text() string {
	parts := make([]string, 0, len(d.Chapters))
	for _, c := range d.Chapters {
		if c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// Chars is the number of runes in all chapter texts and titles.
func (d *Document) Chars() int {
	n := 0
	for _, c := range d.Chapters {
		n += utf8.RuneCountInString(c.Text) + utf8.RuneCountInString(c.Title)
	}
	return n
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// Limits bound an import. Zero fields take the defaults below; a negative
// field means no limit.
type Limits struct {
	MaxFileBytes    int64 // the input file; default 64 MiB
	MaxUncompressed int64 // total bytes inflated from a ZIP container; default 256 MiB
	MaxEntries      int   // ZIP entries; default 20 000
	MaxChars        int   // runes of text in the result; default 5 000 000
}

// Defaults.
const (
	DefaultMaxFileBytes    = 64 << 20
	DefaultMaxUncompressed = 256 << 20
	DefaultMaxEntries      = 20000
	DefaultMaxChars        = 5_000_000
)

func (l Limits) withDefaults() Limits {
	if l.MaxFileBytes == 0 {
		l.MaxFileBytes = DefaultMaxFileBytes
	}
	if l.MaxUncompressed == 0 {
		l.MaxUncompressed = DefaultMaxUncompressed
	}
	if l.MaxEntries == 0 {
		l.MaxEntries = DefaultMaxEntries
	}
	if l.MaxChars == 0 {
		l.MaxChars = DefaultMaxChars
	}
	return l
}

// Errors. Importers wrap them with detail; test with errors.Is.
var (
	ErrUnsupported = errors.New("doc: unsupported format")
	ErrTooLarge    = errors.New("doc: file too large")
	ErrNoText      = errors.New("doc: no text found")
	ErrCorrupt     = errors.New("doc: file is damaged or not of its format")
)

// Extractor reads one format. Implementations must honour lim (already
// defaulted) and ctx, and return ErrNoText when nothing readable is found.
// A PDF extractor plugged in from the app can call Chapterize on the text
// it extracts.
type Extractor interface {
	Extract(ctx context.Context, r io.ReaderAt, size int64, lim Limits) (*Document, error)
}

// ExtractorFunc adapts a function to Extractor.
type ExtractorFunc func(ctx context.Context, r io.ReaderAt, size int64, lim Limits) (*Document, error)

func (f ExtractorFunc) Extract(ctx context.Context, r io.ReaderAt, size int64, lim Limits) (*Document, error) {
	return f(ctx, r, size, lim)
}

var (
	mu         sync.RWMutex
	extractors = map[Format]Extractor{}
	extensions = map[string]Format{}
)

// Register installs (or replaces) the extractor for a format and the file
// extensions (".pdf") that select it.
func Register(f Format, exts []string, x Extractor) {
	mu.Lock()
	defer mu.Unlock()
	extractors[f] = x
	for _, e := range exts {
		extensions[strings.ToLower(e)] = f
	}
}

func init() {
	Register(TXT, []string{".txt", ".text"}, ExtractorFunc(extractTXT))
	Register(Markdown, []string{".md", ".markdown", ".mdown"}, ExtractorFunc(extractMarkdown))
	Register(EPUB, []string{".epub"}, ExtractorFunc(extractEPUB))
	Register(DOCX, []string{".docx"}, ExtractorFunc(extractDOCX))
	Register(SRT, []string{".srt"}, ExtractorFunc(extractSubtitles))
	Register(VTT, []string{".vtt"}, ExtractorFunc(extractSubtitles))
}

// FormatOf returns the format registered for a file name's extension.
func FormatOf(name string) (Format, bool) {
	mu.RLock()
	defer mu.RUnlock()
	f, ok := extensions[strings.ToLower(filepath.Ext(name))]
	if ok {
		_, ok = extractors[f]
	}
	return f, ok
}

// Formats lists the extensions that have an extractor, for an open dialog.
func Extensions() []string {
	mu.RLock()
	defer mu.RUnlock()
	var out []string
	for e, f := range extensions {
		if _, ok := extractors[f]; ok {
			out = append(out, e)
		}
	}
	return out
}

// Open imports the file at path, choosing the extractor by extension.
func Open(ctx context.Context, path string, lim Limits) (*Document, error) {
	f, ok := FormatOf(path)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, filepath.Ext(path))
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil {
		return nil, err
	}
	return Read(ctx, f, file, st.Size(), lim)
}

// Read imports size bytes from r as format f.
func Read(ctx context.Context, f Format, r io.ReaderAt, size int64, lim Limits) (*Document, error) {
	lim = lim.withDefaults()
	if lim.MaxFileBytes > 0 && size > lim.MaxFileBytes {
		return nil, fmt.Errorf("%w: %d bytes (limit %d)", ErrTooLarge, size, lim.MaxFileBytes)
	}
	mu.RLock()
	x := extractors[f]
	mu.RUnlock()
	if x == nil {
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, f)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, err := x.Extract(ctx, r, size, lim)
	if err != nil {
		return nil, err
	}
	if d.Format == "" {
		d.Format = f
	}
	// Drop empty chapters; an all-empty document has no text.
	out := d.Chapters[:0]
	for _, c := range d.Chapters {
		c.Title, c.Text = oneLine(c.Title), strings.TrimSpace(c.Text)
		if c.Text != "" || c.Title != "" {
			out = append(out, c)
		}
	}
	d.Chapters = out
	if strings.TrimSpace(d.Text()) == "" {
		return nil, ErrNoText
	}
	if lim.MaxChars > 0 && d.Chars() > lim.MaxChars {
		return nil, fmt.Errorf("%w: %d characters (limit %d)", ErrTooLarge, d.Chars(), lim.MaxChars)
	}
	return d, nil
}

// readAll reads the whole of r (size bytes).
func readAll(r io.ReaderAt, size int64) ([]byte, error) {
	b := make([]byte, size)
	n, err := r.ReadAt(b, 0)
	if err != nil && !(errors.Is(err, io.EOF) && int64(n) == size) {
		return nil, err
	}
	return b[:n], nil
}
