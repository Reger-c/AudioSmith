package doc

import (
	"context"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/hehehai/AudioSmith/internal/doc/subtitle"
	"github.com/hehehai/AudioSmith/internal/doc/textenc"
	"github.com/hehehai/AudioSmith/internal/text"
)

// MaxHeadingChars is the longest line plain-text chapter detection treats
// as a heading.
const MaxHeadingChars = 60

var (
	latinHeading = regexp.MustCompile(`(?i)^(chapter|chap\.|part|book|section|volume|vol\.|cap[ií]tulo|parte|libro|chapitre|partie|livre|kapitel|teil|buch|abschnitt)\s+` +
		`([0-9]+|[ivxlcdm]+|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen|twenty|thirty|forty|fifty|` +
		`first|second|third|fourth|fifth|sixth|seventh|eighth|ninth|tenth|last|final|uno|dos|tres|cuatro|cinco|primero|primera|un|une|deux|trois|premier|première|eins|zwei|drei|erster|erstes|erste)\b`)
	namedHeading = regexp.MustCompile(`(?i)^(prologue|prolog|pr[oó]logo|epilogue|epilog|ep[ií]logo|[ée]pilogue|preface|pr[ée]face|prefacio|vorwort|foreword|introduction|introducci[oó]n|einleitung|afterword|interlude|nachwort|postface|avant-propos)\b\s*([:.\-–—].*)?$`)
	cjkHeading   = regexp.MustCompile(`^(第[0-9０-９零〇一二三四五六七八九十百千两兩]+[章回节節卷部篇集话話幕]|序章|序言|序幕|楔子|引子|尾声|尾聲|后记|後記|番外|终章|終章|前言|プロローグ|エピローグ|あとがき|まえがき|제\s*[0-9]+\s*[장화부권편]|프롤로그|에필로그)`)
)

// IsChapterHeading reports whether a line of plain text looks like a
// chapter heading: "# Title", or a short line such as "Chapter 1",
// "CHAPTER IV: The End", "Prologue", "第一章 开始", "第3話", "제1장".
func IsChapterHeading(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" || utf8.RuneCountInString(line) > MaxHeadingChars {
		return false
	}
	if text.IsHeading(line) {
		return true
	}
	return latinHeading.MatchString(line) || namedHeading.MatchString(line) || cjkHeading.MatchString(line)
}

// Chapterize splits plain text into chapters at headings (see
// IsChapterHeading). Text before the first heading becomes an untitled
// chapter. Runs of blank lines collapse to one.
func Chapterize(s string) []Chapter {
	var out []Chapter
	cur := Chapter{}
	var body []string
	blank := false
	flush := func() {
		cur.Text = strings.TrimSpace(strings.Join(body, "\n"))
		if cur.Text != "" || cur.Title != "" {
			out = append(out, cur)
		}
		body, blank = nil, false
	}
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if IsChapterHeading(trimmed) {
			flush()
			title := trimmed
			if text.IsHeading(trimmed) {
				title = strings.TrimSpace(trimmed[1:])
			}
			cur = Chapter{Title: title}
			continue
		}
		if trimmed == "" {
			blank = len(body) > 0
			continue
		}
		if blank {
			body = append(body, "")
			blank = false
		}
		body = append(body, strings.TrimRight(line, " \t　"))
	}
	flush()
	return out
}

func extractTXT(ctx context.Context, r io.ReaderAt, size int64, lim Limits) (*Document, error) {
	b, err := readAll(r, size)
	if err != nil {
		return nil, err
	}
	s, _ := textenc.Decode(b)
	return &Document{Format: TXT, Chapters: Chapterize(s)}, nil
}

func extractSubtitles(ctx context.Context, r io.ReaderAt, size int64, lim Limits) (*Document, error) {
	b, err := readAll(r, size)
	if err != nil {
		return nil, err
	}
	s, _ := textenc.Decode(b)
	f, cues, _ := subtitle.Parse(s)
	lines := make([]string, 0, len(cues))
	for _, c := range cues {
		if t := strings.Join(strings.Fields(c.Text), " "); t != "" {
			lines = append(lines, t)
		}
	}
	d := &Document{Format: SRT, Chapters: []Chapter{{Text: strings.Join(lines, "\n")}}}
	if f == subtitle.FormatVTT {
		d.Format = VTT
	}
	return d, nil
}

// ---- Markdown ----

var (
	mdImage    = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	mdLink     = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	mdRefLink  = regexp.MustCompile(`\[([^\]]+)\]\[[^\]]*\]`)
	mdFootRef  = regexp.MustCompile(`\[\^[^\]]+\]`)
	mdLinkDef  = regexp.MustCompile(`^\s{0,3}\[[^\]]+\]:\s+\S+`)
	mdAutoLink = regexp.MustCompile(`<(?:https?|mailto):[^>]+>`)
	mdCode     = regexp.MustCompile("`+([^`]+)`+")
	mdStrong   = regexp.MustCompile(`(\*\*|__)(\S(?:.*?\S)?)(\*\*|__)`)
	mdEmStar   = regexp.MustCompile(`\*(\S(?:[^*]*\S)?)\*`)
	mdEmUnder  = regexp.MustCompile(`(^|[\s(])_(\S(?:[^_]*\S)?)_($|[\s).,!?;:])`)
	mdStrike   = regexp.MustCompile(`~~(.+?)~~`)
	mdEscape   = regexp.MustCompile("\\\\([\\\\`*_{}\\[\\]()#+\\-.!>|~])")
	mdHTML     = regexp.MustCompile(`</?[A-Za-z][^>]*>`)
	mdComment  = regexp.MustCompile(`<!--.*?-->`)
	mdList     = regexp.MustCompile(`^\s*(?:[-*+]|\d{1,9}[.)])\s+(?:\[[ xX]\]\s+)?`)
	mdQuote    = regexp.MustCompile(`^\s{0,3}(?:>\s?)+`)
	mdRule     = regexp.MustCompile(`^\s{0,3}(?:(?:\*\s*){3,}|(?:-\s*){3,}|(?:_\s*){3,})$`)
	mdTableSep = regexp.MustCompile(`^\s*\|?\s*:?-{2,}:?\s*(?:\|\s*:?-{2,}:?\s*)*\|?\s*$`)
	mdSubHead  = regexp.MustCompile(`^\s{0,3}#{2,6}\s+(.*?)\s*#*\s*$`)
	mdSetext   = regexp.MustCompile(`^\s{0,3}(=+|-+)\s*$`)
)

func isTableSep(line string) bool { return strings.Contains(line, "|") && mdTableSep.MatchString(line) }

func mdInline(s string) string {
	// Escaped characters are hidden in the private-use area while the
	// other rules run, then restored.
	s = mdEscape.ReplaceAllStringFunc(s, func(m string) string { return string(rune(0xE000 + rune(m[1]))) })
	s = mdComment.ReplaceAllString(s, "")
	s = mdImage.ReplaceAllString(s, "")
	s = mdLink.ReplaceAllString(s, "$1")
	s = mdRefLink.ReplaceAllString(s, "$1")
	s = mdFootRef.ReplaceAllString(s, "")
	s = mdAutoLink.ReplaceAllString(s, "")
	s = mdCode.ReplaceAllString(s, "$1")
	s = mdStrong.ReplaceAllString(s, "$2")
	s = mdEmStar.ReplaceAllString(s, "$1")
	s = mdEmUnder.ReplaceAllString(s, "$1$2$3")
	s = mdStrike.ReplaceAllString(s, "$1")
	s = mdHTML.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		if r >= 0xE000 && r < 0xE080 {
			return r - 0xE000
		}
		return r
	}, s)
}

// MarkdownText converts Markdown to an AudioSmith script: "# " headings
// stay chapter headings, deeper headings become paragraphs, and code,
// images, link targets, emphasis marks, HTML, tables' pipes and list
// bullets are removed.
func MarkdownText(src string) string {
	lines := strings.Split(src, "\n")
	var out []string
	inFence, fence := false, ""
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" { // YAML front matter
		for i := 1; i < len(lines); i++ {
			if t := strings.TrimSpace(lines[i]); t == "---" || t == "..." {
				lines = lines[i+1:]
				break
			}
		}
	}
	para := func() {
		if len(out) > 0 && out[len(out)-1] != "" {
			out = append(out, "")
		}
	}
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], " \t\r")
		trimmed := strings.TrimSpace(line)
		if inFence {
			if strings.HasPrefix(trimmed, fence) {
				inFence = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence, fence = true, trimmed[:3]
			para()
			continue
		}
		// Setext headings: a text line underlined with === or ---.
		if trimmed != "" && i+1 < len(lines) && mdSetext.MatchString(lines[i+1]) && !mdList.MatchString(line) && !mdRule.MatchString(line) {
			para()
			if strings.HasPrefix(strings.TrimSpace(lines[i+1]), "=") {
				out = append(out, "# "+mdInline(trimmed))
			} else {
				out = append(out, mdInline(trimmed))
			}
			out = append(out, "")
			i++
			continue
		}
		switch {
		case trimmed == "":
			para()
		case text.IsHeading(trimmed):
			para()
			out = append(out, "# "+mdInline(strings.TrimSpace(strings.TrimRight(trimmed[1:], "# "))), "")
		case mdSubHead.MatchString(line):
			para()
			out = append(out, mdInline(mdSubHead.FindStringSubmatch(line)[1]), "")
		case isTableSep(line):
			// a table's header separator: nothing to read
		case mdRule.MatchString(line), mdLinkDef.MatchString(line):
			para()
		case strings.HasPrefix(trimmed, "|") || (strings.Count(trimmed, "|") >= 2 && i+1 < len(lines) && isTableSep(lines[i+1])):
			var cells []string
			for _, c := range strings.Split(strings.Trim(trimmed, "|"), "|") {
				if c = strings.TrimSpace(mdInline(c)); c != "" {
					cells = append(cells, c)
				}
			}
			out = append(out, strings.Join(cells, ", "))
		default:
			line = mdQuote.ReplaceAllString(line, "")
			line = mdList.ReplaceAllString(line, "")
			if t := strings.TrimSpace(mdInline(line)); t != "" {
				out = append(out, t)
			}
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func extractMarkdown(ctx context.Context, r io.ReaderAt, size int64, lim Limits) (*Document, error) {
	b, err := readAll(r, size)
	if err != nil {
		return nil, err
	}
	s, _ := textenc.Decode(b)
	d := &Document{Format: Markdown, Chapters: Chapterize(MarkdownText(s))}
	return d, nil
}
