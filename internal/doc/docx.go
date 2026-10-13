package doc

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// docxPara is one paragraph of a Word document with its heading level
// (0 = body text, 1–9 = heading, -1 = the "Title" style).
type docxPara struct {
	text  string
	level int
}

type docxStyle struct {
	name    string
	basedOn string
	outline int // w:outlineLvl + 1; 0 when none
}

func extractDOCX(ctx context.Context, r io.ReaderAt, size int64, lim Limits) (*Document, error) {
	z, err := openZip(r, size, lim)
	if err != nil {
		return nil, err
	}
	body, err := z.read("word/document.xml")
	if err != nil {
		return nil, err
	}
	styles := map[string]docxStyle{}
	if z.has("word/styles.xml") {
		if b, err := z.read("word/styles.xml"); err == nil {
			styles = parseDocxStyles(b)
		}
	}
	paras, err := parseDocxBody(body, styles)
	if err != nil {
		return nil, err
	}
	doc := &Document{Format: DOCX}
	if z.has("docProps/core.xml") {
		if b, err := z.read("docProps/core.xml"); err == nil {
			var core struct {
				Title    string `xml:"title"`
				Creator  string `xml:"creator"`
				Language string `xml:"language"`
			}
			if newXMLDecoder(b).Decode(&core) == nil {
				doc.Title, doc.Author, doc.Language = oneLine(core.Title), oneLine(core.Creator), strings.TrimSpace(core.Language)
			}
		}
	}
	// Chapters start at the highest heading level the document uses.
	top := 0
	for _, p := range paras {
		if p.level > 0 && (top == 0 || p.level < top) {
			top = p.level
		}
	}
	var cur *Chapter
	var text []string
	flush := func() {
		if cur != nil {
			cur.Text = strings.Join(text, "\n\n")
			doc.Chapters = append(doc.Chapters, *cur)
		}
		text = nil
	}
	for i, p := range paras {
		switch {
		case p.level == -1 && i == 0 && doc.Title == "":
			doc.Title = p.text // a leading Title paragraph names the document
		case top > 0 && p.level == top:
			flush()
			cur = &Chapter{Title: p.text}
		default:
			if cur == nil {
				cur = &Chapter{}
			}
			text = append(text, p.text)
		}
	}
	flush()
	if top == 0 && len(doc.Chapters) == 1 {
		// No heading styles: fall back to plain-text heading detection.
		doc.Chapters = Chapterize(doc.Chapters[0].Text)
	}
	return doc, nil
}

func parseDocxStyles(b []byte) map[string]docxStyle {
	out := map[string]docxStyle{}
	d := newXMLDecoder(b)
	var id string
	var st docxStyle
	in := false
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "style":
				in, id, st = attr(t, "type") == "paragraph" || attr(t, "type") == "", attr(t, "styleId"), docxStyle{}
			case "name":
				if in {
					st.name = strings.ToLower(attr(t, "val"))
				}
			case "basedOn":
				if in {
					st.basedOn = attr(t, "val")
				}
			case "outlineLvl":
				if in {
					if n, err := strconv.Atoi(attr(t, "val")); err == nil && n < 9 {
						st.outline = n + 1
					}
				}
			}
		case xml.EndElement:
			if t.Name.Local == "style" && in {
				out[id] = st
				in = false
			}
		}
	}
	return out
}

// styleLevel resolves a paragraph style's heading level, following basedOn.
func styleLevel(styles map[string]docxStyle, id string) int {
	for hops := 0; id != "" && hops < 10; hops++ {
		st, ok := styles[id]
		name := st.name
		if !ok {
			name = strings.ToLower(id) // "Heading1" without styles.xml
		}
		if name == "title" {
			return -1
		}
		for _, prefix := range []string{"heading ", "heading"} {
			if strings.HasPrefix(name, prefix) {
				if n, err := strconv.Atoi(strings.TrimSpace(name[len(prefix):])); err == nil && n >= 1 && n <= 9 {
					return n
				}
			}
		}
		if st.outline > 0 {
			return st.outline
		}
		if !ok {
			return 0
		}
		id = st.basedOn
	}
	return 0
}

// parseDocxBody collects paragraphs in document order. Deleted text,
// field codes and the fallback copies inside mc:AlternateContent are
// skipped; tabs and breaks become white space.
func parseDocxBody(b []byte, styles map[string]docxStyle) ([]docxPara, error) {
	d := newXMLDecoder(b)
	type open struct {
		text    strings.Builder
		level   int
		outline int
	}
	var stack []*open
	var out []docxPara
	skip := 0
	inText := false
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			if len(out) > 0 {
				break
			}
			return nil, fmt.Errorf("%w: document.xml: %v", ErrCorrupt, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := t.Name.Local
			if skip > 0 {
				skip++
				continue
			}
			switch name {
			case "Fallback", "delText", "instrText", "footnoteReference", "endnoteReference", "commentReference":
				skip = 1
			case "p":
				stack = append(stack, &open{})
			case "pStyle":
				if len(stack) > 0 {
					stack[len(stack)-1].level = styleLevel(styles, attr(t, "val"))
				}
			case "outlineLvl":
				if len(stack) > 0 {
					if n, err := strconv.Atoi(attr(t, "val")); err == nil && n < 9 {
						stack[len(stack)-1].outline = n + 1
					}
				}
			case "t":
				inText = true
			case "tab":
				if len(stack) > 0 {
					stack[len(stack)-1].text.WriteByte('\t')
				}
			case "br", "cr":
				if len(stack) > 0 && attr(t, "type") != "page" {
					stack[len(stack)-1].text.WriteByte('\n')
				}
			case "noBreakHyphen":
				if len(stack) > 0 {
					stack[len(stack)-1].text.WriteByte('-')
				}
			}
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				if len(stack) == 0 {
					continue
				}
				p := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				txt := strings.TrimSpace(p.text.String())
				if txt == "" {
					continue
				}
				lvl := p.level
				if lvl == 0 && p.outline > 0 {
					lvl = p.outline
				}
				out = append(out, docxPara{text: txt, level: lvl})
			}
		case xml.CharData:
			if inText && skip == 0 && len(stack) > 0 {
				stack[len(stack)-1].text.Write(t)
			}
		}
	}
	return out, nil
}
