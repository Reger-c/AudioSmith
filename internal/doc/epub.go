package doc

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

type opfPackage struct {
	Metadata struct {
		Title    []string `xml:"title"`
		Creator  []string `xml:"creator"`
		Language []string `xml:"language"`
	} `xml:"metadata"`
	Manifest []struct {
		ID         string `xml:"id,attr"`
		Href       string `xml:"href,attr"`
		MediaType  string `xml:"media-type,attr"`
		Properties string `xml:"properties,attr"`
	} `xml:"manifest>item"`
	Spine struct {
		Toc   string `xml:"toc,attr"`
		Items []struct {
			IDRef  string `xml:"idref,attr"`
			Linear string `xml:"linear,attr"`
		} `xml:"itemref"`
	} `xml:"spine"`
	Guide []struct {
		Type string `xml:"type,attr"`
		Href string `xml:"href,attr"`
	} `xml:"guide>reference"`
}

// Front-matter types (EPUB 3 landmarks / structural semantics and EPUB 2
// guide types) whose pages are skipped.
var frontTypes = map[string]bool{
	"cover": true, "titlepage": true, "title-page": true, "copyright-page": true, "copyright": true, "toc": true,
	"loi": true, "lot": true, "dedication": true, "colophon": true, "imprint": true, "index": true,
	"halftitlepage": true, "frontispiece": true, "other.ms-coverimage": true, "other.ms-coverimage-standard": true,
	"other.ms-titlepage": true, "contributors": true, "landmarks": true, "page-list": true,
}

var frontName = regexp.MustCompile(`(?i)(^|[^a-z])(cover|title(page)?|half-?title|copyright|toc|contents|dedication|colophon|imprint|frontmatter|front-matter|index|nav)([^a-z]|$)`)

// Short heading-less pages before the first contents entry are front
// matter when they have fewer runes than this.
const frontMatterChars = 1200

type tocEntry struct {
	file, title string
}

func extractEPUB(ctx context.Context, r io.ReaderAt, size int64, lim Limits) (*Document, error) {
	z, err := openZip(r, size, lim)
	if err != nil {
		return nil, err
	}
	opfPath, err := epubRootfile(z)
	if err != nil {
		return nil, err
	}
	raw, err := z.read(opfPath)
	if err != nil {
		return nil, err
	}
	var pkg opfPackage
	if err := newXMLDecoder(raw).Decode(&pkg); err != nil {
		return nil, fmt.Errorf("%w: package document: %v", ErrCorrupt, err)
	}
	doc := &Document{Format: EPUB}
	if len(pkg.Metadata.Title) > 0 {
		doc.Title = oneLine(pkg.Metadata.Title[0])
	}
	if len(pkg.Metadata.Creator) > 0 {
		doc.Author = oneLine(pkg.Metadata.Creator[0])
	}
	if len(pkg.Metadata.Language) > 0 {
		doc.Language = strings.TrimSpace(pkg.Metadata.Language[0])
	}

	byID := map[string]string{} // id → file path
	navFile, ncxFile := "", ""
	for _, it := range pkg.Manifest {
		p := resolve(opfPath, it.Href)
		byID[it.ID] = p
		if strings.Contains(" "+it.Properties+" ", " nav ") {
			navFile = p
		}
		if it.ID == pkg.Spine.Toc || it.MediaType == "application/x-dtbncx+xml" && ncxFile == "" {
			ncxFile = p
		}
	}
	// Titles and front-matter types from the contents.
	var toc []tocEntry
	types := map[string]string{} // file → landmark/guide type
	if navFile != "" && z.has(navFile) {
		if b, err := z.read(navFile); err == nil {
			toc, types = parseNav(b, navFile)
		}
	}
	if len(toc) == 0 && ncxFile != "" && z.has(ncxFile) {
		if b, err := z.read(ncxFile); err == nil {
			toc = parseNCX(b, ncxFile)
		}
	}
	for _, g := range pkg.Guide {
		f := resolve(opfPath, g.Href)
		if _, ok := types[f]; !ok {
			types[f] = strings.ToLower(g.Type)
		}
	}
	tocTitle := map[string]string{}
	for _, e := range toc {
		if _, ok := tocTitle[e.file]; !ok {
			tocTitle[e.file] = e.title
		}
	}

	seenToc := false
	for _, it := range pkg.Spine.Items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file, ok := byID[it.IDRef]
		if !ok || strings.EqualFold(it.Linear, "no") || file == navFile || !z.has(file) {
			continue
		}
		title, inToc := tocTitle[file]
		if t := types[file]; frontTypes[t] {
			continue
		}
		b, err := z.read(file)
		if err != nil {
			return nil, err
		}
		h, err := extractHTML(b)
		if err != nil {
			continue // one unreadable page does not sink the book
		}
		body := h.paras
		chars := 0
		for _, p := range body {
			chars += utf8.RuneCountInString(p)
		}
		if chars == 0 {
			continue
		}
		hasHeading := len(h.headings) > 0
		if !inToc && !seenToc {
			// Before the contents start: skip short pages that look like
			// front matter.
			if chars < frontMatterChars && (!hasHeading || frontName.MatchString(path.Base(file)) || frontName.MatchString(it.IDRef)) {
				continue
			}
		}
		if frontName.MatchString(strings.TrimSuffix(path.Base(file), path.Ext(file))) && chars < frontMatterChars && !inToc {
			continue
		}
		if inToc {
			seenToc = true
		}
		// The title: contents entry, else the first heading. A leading
		// heading that repeats the title is dropped from the text.
		if title == "" && hasHeading && h.headings[0].para == 0 {
			title = h.headings[0].text
		}
		for len(body) > 0 && len(h.headings) > 0 && h.headings[0].para == len(h.paras)-len(body) &&
			title != "" && sameTitle(body[0], title) {
			body = body[1:]
			h.headings = h.headings[1:]
		}
		text := strings.Join(body, "\n\n")
		// A page not in the contents and without a heading continues the
		// previous chapter (books split long chapters across files).
		if title == "" && !inToc && len(doc.Chapters) > 0 && seenToc {
			last := &doc.Chapters[len(doc.Chapters)-1]
			last.Text = strings.TrimSpace(last.Text + "\n\n" + text)
			continue
		}
		doc.Chapters = append(doc.Chapters, Chapter{Title: oneLine(title), Text: text})
	}
	return doc, nil
}

func sameTitle(a, b string) bool {
	fa, fb := strings.ToLower(oneLine(a)), strings.ToLower(oneLine(b))
	return fa == fb || strings.HasPrefix(fb, fa) || strings.HasPrefix(fa, fb)
}

func epubRootfile(z *zipBox) (string, error) {
	b, err := z.read("META-INF/container.xml")
	if err != nil {
		// Some files omit the container: look for a lone .opf.
		for name := range z.files {
			if strings.HasSuffix(strings.ToLower(name), ".opf") {
				return name, nil
			}
		}
		return "", fmt.Errorf("%w: no META-INF/container.xml", ErrCorrupt)
	}
	var c struct {
		Rootfiles []struct {
			FullPath  string `xml:"full-path,attr"`
			MediaType string `xml:"media-type,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	if err := newXMLDecoder(b).Decode(&c); err != nil || len(c.Rootfiles) == 0 {
		return "", fmt.Errorf("%w: bad container.xml", ErrCorrupt)
	}
	for _, rf := range c.Rootfiles {
		if rf.MediaType == "" || rf.MediaType == "application/oebps-package+xml" {
			return path.Clean(rf.FullPath), nil
		}
	}
	return path.Clean(c.Rootfiles[0].FullPath), nil
}

// parseNav reads an EPUB 3 navigation document: the "toc" nav's links in
// order, and the "landmarks" nav's types by file.
func parseNav(b []byte, navFile string) ([]tocEntry, map[string]string) {
	var toc []tocEntry
	types := map[string]string{}
	d := newXMLDecoder(b)
	navKind := ""
	depth, navDepth := 0, 0
	var href, typ string
	var label strings.Builder
	inA := false
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			switch strings.ToLower(t.Name.Local) {
			case "nav":
				navKind, navDepth = attr(t, "type"), depth
				if navKind == "" {
					navKind = "toc"
				}
			case "a":
				if navKind != "" {
					inA, href, typ = true, attr(t, "href"), attr(t, "type")
					label.Reset()
				}
			}
		case xml.EndElement:
			name := strings.ToLower(t.Name.Local)
			if name == "a" && inA {
				inA = false
				f := resolve(navFile, href)
				switch {
				case strings.Contains(navKind, "landmarks"):
					types[f] = strings.ToLower(typ)
				case strings.Contains(navKind, "toc"):
					toc = append(toc, tocEntry{file: f, title: oneLine(label.String())})
				}
			}
			if name == "nav" && depth == navDepth {
				navKind = ""
			}
			depth--
		case xml.CharData:
			if inA {
				label.Write(t)
			}
		}
	}
	return toc, types
}

// parseNCX reads an EPUB 2 NCX navMap in document order.
func parseNCX(b []byte, ncxFile string) []tocEntry {
	var toc []tocEntry
	d := newXMLDecoder(b)
	type point struct {
		label string
		src   string
	}
	var stack []*point
	inText := false
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "navPoint":
				stack = append(stack, &point{})
			case "text":
				inText = len(stack) > 0
			case "content":
				if len(stack) > 0 {
					p := stack[len(stack)-1]
					p.src = attr(t, "src")
					// Emit at the content element so parents precede children.
					toc = append(toc, tocEntry{file: resolve(ncxFile, p.src), title: oneLine(p.label)})
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "navPoint":
				if len(stack) > 0 {
					stack = stack[:len(stack)-1]
				}
			case "text":
				inText = false
			}
		case xml.CharData:
			if inText && len(stack) > 0 {
				stack[len(stack)-1].label += string(t)
			}
		}
	}
	return toc
}

// attr returns the value of the attribute with local name name (any
// namespace: epub:type and type both match "type").
func attr(t xml.StartElement, name string) string {
	for _, a := range t.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}
