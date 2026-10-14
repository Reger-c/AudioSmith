package doc

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"

	"golang.org/x/text/encoding/htmlindex"
)

// newXMLDecoder returns a lenient decoder for (X)HTML and XML found in
// documents: HTML entities, unclosed void elements and declared legacy
// encodings are accepted.
func newXMLDecoder(b []byte) *xml.Decoder {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	d := xml.NewDecoder(bytes.NewReader(b))
	d.Strict = false
	d.AutoClose = xml.HTMLAutoClose
	d.Entity = xml.HTMLEntity
	d.CharsetReader = func(label string, r io.Reader) (io.Reader, error) {
		enc, err := htmlindex.Get(label)
		if err != nil {
			return nil, err
		}
		return enc.NewDecoder().Reader(r), nil
	}
	return d
}

// newXMLDataDecoder is newXMLDecoder for XML that is not HTML: an EPUB's
// package document, container and NCX. HTML's void elements must not
// apply there: EPUB 3 requires <meta property="dcterms:modified">…</meta>,
// which HTML's auto-closing <meta> turns into a syntax error.
func newXMLDataDecoder(b []byte) *xml.Decoder {
	d := newXMLDecoder(b)
	d.AutoClose = nil
	return d
}

// htmlText is the text of an XHTML content document as paragraphs, plus
// its headings.
type htmlText struct {
	paras    []string
	headings []heading // in order; index into paras
	title    string    // <title>
}

type heading struct {
	level int
	para  int
	text  string
}

var blockTags = map[string]bool{
	"p": true, "div": true, "li": true, "blockquote": true, "section": true, "article": true, "aside": true,
	"tr": true, "dt": true, "dd": true, "pre": true, "figcaption": true, "table": true, "ul": true, "ol": true,
	"header": true, "footer": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"hr": true, "body": true, "main": true, "nav": true, "td": true, "th": true, "caption": true, "address": true,
}

var skipTags = map[string]bool{
	"head": true, "script": true, "style": true, "svg": true, "math": true, "rt": true, "rp": true,
	"noscript": true, "template": true, "object": true, "iframe": true,
}

// skipByType: page-number markers, note references and the notes
// themselves are not read aloud.
func skipByType(attrs []xml.Attr) bool {
	for _, a := range attrs {
		if a.Name.Local != "type" && a.Name.Local != "role" {
			continue
		}
		for _, v := range strings.Fields(a.Value) {
			switch strings.TrimPrefix(v, "doc-") {
			case "pagebreak", "noteref", "footnote", "endnote", "rearnote", "footnotes", "endnotes", "rearnotes":
				return true
			}
		}
	}
	return false
}

// extractHTML walks a content document.
func extractHTML(b []byte) (htmlText, error) {
	var out htmlText
	d := newXMLDecoder(b)
	var cur strings.Builder
	skip := 0          // depth inside skipped elements
	var stack []string // open element names
	inTitle, pre := false, 0
	headLevel := 0
	flush := func() {
		t := strings.TrimSpace(cur.String())
		cur.Reset()
		if t == "" {
			return
		}
		if headLevel > 0 {
			out.headings = append(out.headings, heading{level: headLevel, para: len(out.paras), text: t})
		}
		out.paras = append(out.paras, t)
	}
	for {
		tok, err := d.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			if len(out.paras) > 0 || cur.Len() > 0 {
				break // keep what was read before the damage
			}
			return out, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := strings.ToLower(t.Name.Local)
			stack = append(stack, name)
			if skip > 0 || skipTags[name] && name != "head" || skipByType(t.Attr) {
				skip++
				continue
			}
			if name == "title" {
				inTitle = true
			}
			if name == "br" {
				cur.WriteByte('\n')
			}
			if blockTags[name] {
				flush()
				if len(name) == 2 && name[0] == 'h' && name[1] >= '1' && name[1] <= '6' {
					headLevel = int(name[1] - '0')
				}
				if name == "pre" {
					pre++
				}
			}
		case xml.EndElement:
			name := strings.ToLower(t.Name.Local)
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			if skip > 0 {
				skip--
				continue
			}
			if name == "title" {
				inTitle = false
			}
			if blockTags[name] {
				flush()
				if len(name) == 2 && name[0] == 'h' {
					headLevel = 0
				}
				if name == "pre" && pre > 0 {
					pre--
				}
			}
		case xml.CharData:
			if skip > 0 && !inHead(stack) {
				continue
			}
			if inHead(stack) {
				if inTitle {
					out.title += string(t)
				}
				continue
			}
			s := string(t)
			if pre == 0 {
				s = collapseSpace(s, cur.Len() > 0 && !strings.HasSuffix(cur.String(), " ") && !strings.HasSuffix(cur.String(), "\n"))
			}
			cur.WriteString(s)
		}
	}
	flush()
	out.title = strings.TrimSpace(out.title)
	return out, nil
}

func inHead(stack []string) bool {
	for _, s := range stack {
		if s == "head" {
			return true
		}
	}
	return false
}

// collapseSpace turns runs of white space into one space; a leading space
// is kept only when the text so far does not end in one.
func collapseSpace(s string, keepLead bool) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\f' {
			space = true
			continue
		}
		if space && (b.Len() > 0 || keepLead) {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	if space && (b.Len() > 0 || keepLead) {
		b.WriteByte(' ')
	}
	return b.String()
}
