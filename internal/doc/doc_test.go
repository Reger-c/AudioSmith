package doc

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func chapters(d *Document) []string {
	var out []string
	for _, c := range d.Chapters {
		out = append(out, c.Title+" | "+c.Text)
	}
	return out
}

func read(t *testing.T, f Format, data []byte, lim Limits) (*Document, error) {
	t.Helper()
	return Read(context.Background(), f, bytes.NewReader(data), int64(len(data)), lim)
}

func TestChapterize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"english", "Title page text\n\nChapter 1\n\nIt was dark.\nStill dark.\n\n\n\nMore.\nCHAPTER TWO: The Storm\nRain.\nPrologue\nBefore.",
			[]string{" | Title page text", "Chapter 1 | It was dark.\nStill dark.\n\nMore.", "CHAPTER TWO: The Storm | Rain.", "Prologue | Before."}},
		{"not headings", "Part of the reason was fear.\nChapter and verse are cited.\nThe chapter 1 ends.",
			[]string{" | Part of the reason was fear.\nChapter and verse are cited.\nThe chapter 1 ends."}},
		{"chinese", "序章\n很久以前。\n第一章 开始\n他来了。\n第12章\n她走了。\n番外 后来\n完。",
			[]string{"序章 | 很久以前。", "第一章 开始 | 他来了。", "第12章 | 她走了。", "番外 后来 | 完。"}},
		{"japanese and korean", "第1話 出会い\nこんにちは。\n제2장 이별\n안녕.",
			[]string{"第1話 出会い | こんにちは。", "제2장 이별 | 안녕."}},
		{"markup headings", "# One\nA\n# \nB",
			[]string{"One | A", " | B"}},
		{"european", "Capítulo 3\nHola.\nChapitre IV\nBonjour.\nKapitel 5\nHallo.",
			[]string{"Capítulo 3 | Hola.", "Chapitre IV | Bonjour.", "Kapitel 5 | Hallo."}},
		{"long line is not a heading", "Chapter 1 " + strings.Repeat("x", 60) + "\ntext",
			[]string{" | Chapter 1 " + strings.Repeat("x", 60) + "\ntext"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := chapters(&Document{Chapters: Chapterize(c.in)})
			if !slices.Equal(got, c.want) {
				t.Errorf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestTXT(t *testing.T) {
	gbk, _ := simplifiedchinese.GBK.NewEncoder().Bytes([]byte("第一章 开始\r\n今天天气很好，我们一起去公园散步吧。\r\n第二章 结束\r\n他们回家了。"))
	d, err := read(t, TXT, gbk, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"第一章 开始 | 今天天气很好，我们一起去公园散步吧。", "第二章 结束 | 他们回家了。"}
	if got := chapters(d); !slices.Equal(got, want) {
		t.Errorf("GBK txt: %q", got)
	}
	if s := d.Script(); s != "# 第一章 开始\n\n今天天气很好，我们一起去公园散步吧。\n\n# 第二章 结束\n\n他们回家了。" {
		t.Errorf("Script %q", s)
	}
	if _, err := read(t, TXT, []byte(" \n\n\t"), Limits{}); !errors.Is(err, ErrNoText) {
		t.Errorf("empty: %v", err)
	}
	if _, err := read(t, TXT, []byte("hello world"), Limits{MaxFileBytes: 5}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("file limit: %v", err)
	}
	if _, err := read(t, TXT, []byte("hello world"), Limits{MaxChars: 5}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("char limit: %v", err)
	}
}

func TestMarkdown(t *testing.T) {
	src := "---\ntitle: x\n---\n# Book *One*\n\nSome **bold** and _italic_ text with a [link](http://x.y) and ![img](a.png) and `code`.\n\n" +
		"## A section\n\n- item one\n- [x] item two\n1. first\n\n> quoted line\n\n```go\nfmt.Println()\n```\n\n" +
		"| a | b |\n|---|---|\n| 1 | 2 |\n\nsnake_case_name stays, \\*escaped\\*.\n\n[ref]: http://example.com\n\nSetext Title\n============\n\nBody[^1].\n***\nEnd <b>html</b>."
	got := MarkdownText(src)
	want := "# Book One\n\nSome bold and italic text with a link and  and code.\n\nA section\n\nitem one\nitem two\nfirst\n\nquoted line\n\n" +
		"a, b\n1, 2\n\nsnake_case_name stays, *escaped*.\n\n# Setext Title\n\nBody.\n\nEnd html."
	if got != want {
		t.Errorf("MarkdownText\n got  %q\n want %q", got, want)
	}
	d, err := read(t, Markdown, []byte(src), Limits{})
	if err != nil || len(d.Chapters) != 2 || d.Chapters[0].Title != "Book One" || d.Chapters[1].Title != "Setext Title" {
		t.Errorf("markdown chapters %v %q", err, chapters(d))
	}
}

func TestSubtitlesImport(t *testing.T) {
	srt := "1\n00:00:01,000 --> 00:00:02,000\n<i>Hello</i>\nthere\n\n2\n00:00:03,000 --> 00:00:04,000\n42\n"
	d, err := read(t, SRT, []byte(srt), Limits{})
	if err != nil || d.Text() != "Hello there\n42" || d.Format != SRT {
		t.Errorf("srt: %v %q", err, d.Text())
	}
	vtt := "WEBVTT\n\n00:01.000 --> 00:02.000\n<v A>Hi\n"
	d, err = read(t, VTT, []byte(vtt), Limits{})
	if err != nil || d.Text() != "Hi" || d.Format != VTT {
		t.Errorf("vtt: %v %+v", err, d)
	}
}

type zipFile struct{ name, body string }

func makeZip(t *testing.T, files ...zipFile) []byte {
	t.Helper()
	return makeZipF(files)
}

func makeZipF(files []zipFile) []byte {
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, f := range files {
		fw, err := w.Create(f.name)
		if err != nil {
			panic(err)
		}
		io.WriteString(fw, f.body)
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return b.Bytes()
}

const container = `<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0"><rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`

func xhtml(title, body string) string {
	return `<?xml version="1.0" encoding="utf-8"?><html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><head><title>` +
		title + `</title><style>p{}</style></head><body>` + body + `</body></html>`
}

func TestEPUB3(t *testing.T) {
	opf := `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
<dc:title>The Lighthouse</dc:title><dc:creator>A. Writer</dc:creator><dc:language>en</dc:language></metadata>
<manifest><item id="cover" href="cover.xhtml" media-type="application/xhtml+xml"/>
<item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
<item id="copy" href="text/copyright.xhtml" media-type="application/xhtml+xml"/>
<item id="c1" href="text/ch%201.xhtml" media-type="application/xhtml+xml"/>
<item id="c1b" href="text/ch1-part2.xhtml" media-type="application/xhtml+xml"/>
<item id="c2" href="text/ch2.xhtml" media-type="application/xhtml+xml"/>
<item id="notes" href="text/notes.xhtml" media-type="application/xhtml+xml"/></manifest>
<spine><itemref idref="cover"/><itemref idref="nav" linear="no"/><itemref idref="copy"/><itemref idref="c1"/><itemref idref="c1b"/><itemref idref="c2"/><itemref idref="notes" linear="no"/></spine></package>`
	nav := xhtml("Contents", `<nav epub:type="toc"><ol><li><a href="text/ch%201.xhtml">One: Arrival</a></li><li><a href="text/ch2.xhtml#start">Two</a></li></ol></nav>
<nav epub:type="landmarks"><ol><li><a epub:type="cover" href="cover.xhtml">Cover</a></li><li><a epub:type="copyright-page" href="text/copyright.xhtml">©</a></li></ol></nav>`)
	files := []zipFile{
		{"mimetype", "application/epub+zip"},
		{"META-INF/container.xml", container},
		{"OEBPS/content.opf", opf},
		{"OEBPS/nav.xhtml", nav},
		{"OEBPS/cover.xhtml", xhtml("Cover", `<img src="c.jpg"/>`)},
		{"OEBPS/text/copyright.xhtml", xhtml("c", `<p>© 2026 Somebody. All rights reserved.</p>`)},
		{"OEBPS/text/ch 1.xhtml", xhtml("1", `<h1>One: Arrival</h1><p>The keeper   climbed<br/>the stairs.<span epub:type="pagebreak" title="5">5</span></p><p>He&nbsp;smiled<a epub:type="noteref" href="notes.xhtml#n1">1</a>.</p><script>alert(1)</script>`)},
		{"OEBPS/text/ch1-part2.xhtml", xhtml("1b", `<p>The night went on.</p>`)},
		{"OEBPS/text/ch2.xhtml", xhtml("2", `<section><h2 id="start">Chapter Two</h2><p>Morning <ruby>漢字<rt>かんじ</rt></ruby> came.</p><aside epub:type="footnote"><p>A note.</p></aside></section>`)},
		{"OEBPS/text/notes.xhtml", xhtml("n", `<p id="n1">Note one.</p>`)},
	}
	d, err := read(t, EPUB, makeZip(t, files...), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Title != "The Lighthouse" || d.Author != "A. Writer" || d.Language != "en" {
		t.Errorf("metadata %+v", d)
	}
	want := []string{
		"One: Arrival | The keeper climbed\nthe stairs.\n\nHe\u00a0smiled.\n\nThe night went on.",
		"Two | Chapter Two\n\nMorning 漢字 came.",
	}
	if got := chapters(d); !slices.Equal(got, want) {
		t.Errorf("chapters\n got  %q\n want %q", got, want)
	}
}

func TestEPUB2(t *testing.T) {
	opf := `<?xml version="1.0" encoding="UTF-8"?><package xmlns="http://www.idpf.org/2007/opf" version="2.0"><metadata><dc:title xmlns:dc="http://purl.org/dc/elements/1.1/">旧书</dc:title></metadata>
<manifest><item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/><item id="t" href="title.html" media-type="application/xhtml+xml"/>
<item id="a" href="a.html" media-type="application/xhtml+xml"/><item id="b" href="b.html" media-type="application/xhtml+xml"/></manifest>
<spine toc="ncx"><itemref idref="t"/><itemref idref="a"/><itemref idref="b"/></spine><guide><reference type="title-page" href="title.html"/></guide></package>`
	ncx := `<?xml version="1.0"?><ncx xmlns="http://www.daisy.org/z3986/2005/ncx/"><navMap>
<navPoint id="p1"><navLabel><text>第一章 相遇</text></navLabel><content src="a.html"/>
<navPoint id="p1a"><navLabel><text>小节</text></navLabel><content src="a.html#s"/></navPoint></navPoint>
<navPoint id="p2"><navLabel><text>第二章</text></navLabel><content src="b.html"/></navPoint></navMap></ncx>`
	gbHTML, _ := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(`<?xml version="1.0" encoding="gbk"?><html><head><title>b</title></head><body><p>他们分别了。</p></body></html>`))
	files := []zipFile{
		{"META-INF/container.xml", strings.ReplaceAll(container, "OEBPS/", "")},
		{"content.opf", opf},
		{"toc.ncx", ncx},
		{"title.html", xhtml("t", `<h1>旧书</h1><p>作者</p>`)},
		{"a.html", xhtml("a", `<p>他们相遇了。</p>`)},
		{"b.html", string(gbHTML)},
	}
	d, err := read(t, EPUB, makeZip(t, files...), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"第一章 相遇 | 他们相遇了。", "第二章 | 他们分别了。"}
	if got := chapters(d); !slices.Equal(got, want) || d.Title != "旧书" {
		t.Errorf("chapters %q title %q", got, d.Title)
	}
}

func TestZipLimits(t *testing.T) {
	big := strings.Repeat("a", 1<<20)
	data := makeZip(t, zipFile{"META-INF/container.xml", container}, zipFile{"OEBPS/content.opf", big})
	if _, err := read(t, EPUB, data, Limits{MaxUncompressed: 1 << 19}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("uncompressed limit: %v", err)
	}
	var files []zipFile
	for i := 0; i < 30; i++ {
		files = append(files, zipFile{name: "f" + string(rune('a'+i%26)) + string(rune('a'+i/26)), body: "x"})
	}
	if _, err := read(t, EPUB, makeZip(t, files...), Limits{MaxEntries: 10}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("entry limit: %v", err)
	}
	if _, err := read(t, EPUB, []byte("not a zip"), Limits{}); !errors.Is(err, ErrCorrupt) {
		t.Errorf("corrupt: %v", err)
	}
	if _, err := read(t, DOCX, makeZip(t, zipFile{"x.txt", "x"}), Limits{}); !errors.Is(err, ErrCorrupt) {
		t.Errorf("docx without document.xml: %v", err)
	}
}

const wordNS = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006"`

func wp(style, text string) string {
	ppr := ""
	if style != "" {
		ppr = `<w:pPr><w:pStyle w:val="` + style + `"/></w:pPr>`
	}
	return `<w:p>` + ppr + `<w:r><w:t xml:space="preserve">` + text + `</w:t></w:r></w:p>`
}

func TestDOCX(t *testing.T) {
	body := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:document ` + wordNS + `><w:body>` +
		wp("Title", "My Book") + wp("", "Preface text.") +
		wp("Kop1", "Chapter One") +
		`<w:p><w:r><w:t>Hello </w:t></w:r><w:r><w:t>world</w:t><w:tab/><w:t>tabbed</w:t><w:br/><w:t>broken</w:t></w:r>` +
		`<w:del><w:r><w:delText>deleted</w:delText></w:r></w:del><w:r><w:fldChar/><w:instrText>PAGE</w:instrText></w:r></w:p>` +
		wp("Heading2", "A subsection") +
		`<w:tbl><w:tr><w:tc>` + wp("", "cell text") + `</w:tc></w:tr></w:tbl>` +
		`<w:p><w:r><mc:AlternateContent><mc:Choice><w:t>box</w:t></mc:Choice><mc:Fallback><w:t>box</w:t></mc:Fallback></mc:AlternateContent></w:r></w:p>` +
		wp("Kop1", "第二章") + wp("", "中文段落。") +
		`</w:body></w:document>`
	styles := `<?xml version="1.0"?><w:styles ` + wordNS + `>` +
		`<w:style w:type="paragraph" w:styleId="Kop1"><w:name w:val="heading 1"/></w:style>` +
		`<w:style w:type="paragraph" w:styleId="Title"><w:name w:val="Title"/></w:style></w:styles>`
	core := `<?xml version="1.0"?><cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:creator>Me</dc:creator><dc:language>en-GB</dc:language></cp:coreProperties>`
	d, err := read(t, DOCX, makeZip(t, zipFile{"word/document.xml", body}, zipFile{"word/styles.xml", styles}, zipFile{"docProps/core.xml", core}), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{" | Preface text.", "Chapter One | Hello world\ttabbed\nbroken\n\nA subsection\n\ncell text\n\nbox", "第二章 | 中文段落。"}
	if got := chapters(d); !slices.Equal(got, want) {
		t.Errorf("chapters\n got  %q\n want %q", got, want)
	}
	if d.Title != "My Book" || d.Author != "Me" || d.Language != "en-GB" {
		t.Errorf("metadata %+v", d)
	}
	// Without heading styles, plain-text heading detection applies.
	plain := `<w:document ` + wordNS + `><w:body>` + wp("", "Chapter 1") + wp("", "Text.") + wp("", "Chapter 2") + wp("", "More.") + `</w:body></w:document>`
	d, err = read(t, DOCX, makeZip(t, zipFile{"word/document.xml", plain}), Limits{})
	if err != nil || !slices.Equal(chapters(d), []string{"Chapter 1 | Text.", "Chapter 2 | More."}) {
		t.Errorf("plain docx: %v %q", err, chapters(d))
	}
}

func TestOpenAndRegister(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "story.TXT")
	os.WriteFile(p, []byte("Chapter 1\nHi."), 0o644)
	d, err := Open(context.Background(), p, Limits{})
	if err != nil || d.Format != TXT || len(d.Chapters) != 1 {
		t.Errorf("Open: %v %+v", err, d)
	}
	if _, err := Open(context.Background(), filepath.Join(dir, "x.pdf"), Limits{}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("pdf before registration: %v", err)
	}
	// The app plugs in a PDF extractor (PDFKit via the Swift helper).
	Register(PDF, []string{".pdf"}, ExtractorFunc(func(ctx context.Context, r io.ReaderAt, size int64, lim Limits) (*Document, error) {
		return &Document{Chapters: Chapterize("Chapter 1\nFrom PDF.")}, nil
	}))
	defer func() {
		mu.Lock()
		delete(extractors, PDF)
		delete(extensions, ".pdf")
		mu.Unlock()
	}()
	os.WriteFile(filepath.Join(dir, "x.pdf"), []byte("%PDF"), 0o644)
	d, err = Open(context.Background(), filepath.Join(dir, "x.pdf"), Limits{})
	if err != nil || d.Format != PDF || d.Chapters[0].Text != "From PDF." {
		t.Errorf("registered pdf: %v %+v", err, d)
	}
	if !slices.Contains(Extensions(), ".epub") {
		t.Error("Extensions")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Open(ctx, p, Limits{}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}

func TestCueSheet(t *testing.T) {
	marks := MarksFromDurations([]RenderedChapter{
		{"Intro", 65500 * time.Millisecond, true},
		{"Broken", time.Hour, false},
		{"", 3600 * time.Second, true},
		{"Tab\tand\nnewline", time.Second, true},
	})
	got := CueSheet(marks, nil)
	want := "00:00:00\tIntro\n00:01:05\tChapter 2\n01:01:05\tTab and newline"
	if got != want {
		t.Errorf("CueSheet\n%q\nwant\n%q", got, want)
	}
	if got := CueSheet(marks[1:2], func(n int) string { return "第" + string(rune('0'+n)) + "章" }); got != "00:01:05\t第1章" {
		t.Errorf("untitled %q", got)
	}
	if CueSheet(nil, nil) != "" {
		t.Error("empty")
	}
}
