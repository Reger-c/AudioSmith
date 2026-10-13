// Package subtitle parses and writes subtitles: SRT and WebVTT in both
// directions, ASS (with karaoke word highlighting) for writing, and
// Netflix-style segmentation of timed words into readable cues.
//
// Parsing is lenient: malformed blocks are skipped and reported as
// diagnostics (byte spans into the input), never as errors.
package subtitle

import (
	"fmt"
	"html"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hehehai/AudioSmith/internal/text"
)

// Word is a timed word (or karaoke syllable).
type Word struct {
	Text       string
	Start, End time.Duration
}

// Cue is one subtitle.
type Cue struct {
	ID         string // VTT identifier or SRT number, as written; "" when absent
	Start, End time.Duration
	Text       string // plain text, lines joined with "\n"
	Speaker    string // VTT <v Name>, "" when absent
	Words      []Word // word timings when known (VTT inline timestamps, ASR)
}

// Format is a subtitle format.
type Format string

const (
	FormatSRT Format = "srt"
	FormatVTT Format = "vtt"
)

// Diagnostic codes.
const (
	CodeBadTiming = "subtitle.bad_timing" // a block without a valid "start --> end" line
	CodeBadOrder  = "subtitle.end_before_start"
)

// Parse detects the format (a "WEBVTT" header means VTT, anything else is
// read as SRT) and parses s. Input should already be UTF-8 (see textenc).
func Parse(s string) (Format, []Cue, []text.Diagnostic) {
	t := strings.TrimPrefix(s, "\ufeff")
	if strings.HasPrefix(t, "WEBVTT") {
		cues, ds := ParseVTT(s)
		return FormatVTT, cues, ds
	}
	cues, ds := ParseSRT(s)
	return FormatSRT, cues, ds
}

// block is a run of non-blank lines.
type block struct {
	start int
	lines []string
	offs  []int
}

func blocks(s string) []block {
	var out []block
	var cur *block
	off := 0
	for _, line := range strings.SplitAfter(s, "\n") {
		l := strings.TrimRight(line, "\r\n")
		if strings.TrimSpace(l) == "" {
			cur = nil
		} else {
			if cur == nil {
				out = append(out, block{start: off})
				cur = &out[len(out)-1]
			}
			cur.lines = append(cur.lines, l)
			cur.offs = append(cur.offs, off)
		}
		off += len(line)
	}
	return out
}

var timingRe = regexp.MustCompile(`^\s*((?:\d+:)?\d{1,2}:\d{1,2}[,.]\d{1,3})\s*-->\s*((?:\d+:)?\d{1,2}:\d{1,2}[,.]\d{1,3})(.*)$`)

// ParseTimestamp parses "01:02:03,456", "01:02:03.456" or "02:03.456".
func ParseTimestamp(s string) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	frac := "0"
	if i := strings.LastIndexAny(s, ",."); i >= 0 {
		frac, s = s[i+1:], s[:i]
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	var h, m, sec int
	var err error
	if len(parts) == 3 {
		if h, err = strconv.Atoi(parts[0]); err != nil {
			return 0, false
		}
		parts = parts[1:]
	}
	if m, err = strconv.Atoi(parts[0]); err != nil {
		return 0, false
	}
	if sec, err = strconv.Atoi(parts[1]); err != nil || m > 59 || sec > 59 {
		return 0, false
	}
	for len(frac) < 3 {
		frac += "0"
	}
	ms, err := strconv.Atoi(frac[:3])
	if err != nil {
		return 0, false
	}
	return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(sec)*time.Second + time.Duration(ms)*time.Millisecond, true
}

func (p *parseState) timing(b block, i int) (time.Duration, time.Duration, string, bool) {
	m := timingRe.FindStringSubmatch(b.lines[i])
	if m == nil {
		return 0, 0, "", false
	}
	a, ok1 := ParseTimestamp(m[1])
	z, ok2 := ParseTimestamp(m[2])
	if !ok1 || !ok2 {
		return 0, 0, "", false
	}
	if z < a {
		p.diag(CodeBadOrder, b)
		z = a
	}
	return a, z, m[3], true
}

type parseState struct {
	src   string
	diags []text.Diagnostic
}

func (p *parseState) diag(code string, b block) {
	end := b.offs[len(b.offs)-1] + len(b.lines[len(b.lines)-1])
	p.diags = append(p.diags, text.Diagnostic{Code: code, Severity: text.Warning, Span: text.Span{Start: b.start, End: end},
		Params: map[string]string{"line": strconv.Itoa(1 + strings.Count(p.src[:b.start], "\n"))}})
}

var (
	assOverride = regexp.MustCompile(`\{\\[^}]*\}`)
	tagRe       = regexp.MustCompile(`</?[A-Za-z][^>]*>`)
)

// ParseSRT parses SubRip text. Markup (<i>, <b>, <font>, ASS overrides
// like {\an8}) is removed from Text.
func ParseSRT(s string) ([]Cue, []text.Diagnostic) {
	s = strings.TrimPrefix(s, "\ufeff")
	p := &parseState{src: s}
	var out []Cue
	for _, b := range blocks(s) {
		ti := 0
		id := ""
		if len(b.lines) > 1 && !strings.Contains(b.lines[0], "-->") {
			id, ti = strings.TrimSpace(b.lines[0]), 1
		}
		a, z, _, ok := p.timing(b, ti)
		if !ok {
			p.diag(CodeBadTiming, b)
			continue
		}
		var lines []string
		for _, l := range b.lines[ti+1:] {
			l = assOverride.ReplaceAllString(l, "")
			l = html.UnescapeString(tagRe.ReplaceAllString(l, ""))
			if t := strings.TrimSpace(l); t != "" {
				lines = append(lines, t)
			}
		}
		out = append(out, Cue{ID: id, Start: a, End: z, Text: strings.Join(lines, "\n")})
	}
	return out, p.diags
}

var (
	vttTS    = regexp.MustCompile(`<((?:\d+:)?\d{2}:\d{2}\.\d{3})>`)
	vttVoice = regexp.MustCompile(`<v(?:\.[^ >]*)?\s+([^>]*)>`)
	vttRuby  = regexp.MustCompile(`<rt>.*?</rt>`)
)

// ParseVTT parses WebVTT. NOTE, STYLE and REGION blocks are skipped; the
// first <v Speaker> becomes Speaker; inline timestamps become Words (one
// per timed segment); ruby annotations (<rt>) are dropped.
func ParseVTT(s string) ([]Cue, []text.Diagnostic) {
	s = strings.TrimPrefix(s, "\ufeff")
	p := &parseState{src: s}
	var out []Cue
	for n, b := range blocks(s) {
		first := b.lines[0]
		if n == 0 && strings.HasPrefix(first, "WEBVTT") {
			// The header block may hold metadata lines; a cue cannot
			// share it without a blank line, but be lenient.
			rest := 1
			for rest < len(b.lines) && !strings.Contains(b.lines[rest], "-->") {
				rest++
			}
			if rest >= len(b.lines) {
				continue
			}
			b.lines, b.offs = b.lines[rest:], b.offs[rest:]
			first = b.lines[0]
		}
		if strings.HasPrefix(first, "NOTE") || first == "STYLE" || first == "REGION" {
			continue
		}
		ti := 0
		id := ""
		if !strings.Contains(first, "-->") {
			if len(b.lines) < 2 {
				p.diag(CodeBadTiming, b)
				continue
			}
			id, ti = strings.TrimSpace(first), 1
		}
		a, z, _, ok := p.timing(b, ti)
		if !ok {
			p.diag(CodeBadTiming, b)
			continue
		}
		c := Cue{ID: id, Start: a, End: z}
		payload := strings.Join(b.lines[ti+1:], "\n")
		if m := vttVoice.FindStringSubmatch(payload); m != nil {
			c.Speaker = strings.TrimSpace(m[1])
		}
		payload = vttRuby.ReplaceAllString(payload, "")
		if vttTS.MatchString(payload) {
			c.Words = vttWords(payload, a, z)
		}
		payload = vttTS.ReplaceAllString(payload, "")
		payload = html.UnescapeString(tagRe.ReplaceAllString(payload, ""))
		var lines []string
		for _, l := range strings.Split(payload, "\n") {
			if t := strings.TrimSpace(l); t != "" {
				lines = append(lines, t)
			}
		}
		c.Text = strings.Join(lines, "\n")
		out = append(out, c)
	}
	return out, p.diags
}

// vttWords splits a karaoke payload at its inline timestamps.
func vttWords(payload string, start, end time.Duration) []Word {
	var out []Word
	at := start
	prev := 0
	emit := func(seg string, until time.Duration) {
		seg = strings.TrimSpace(html.UnescapeString(tagRe.ReplaceAllString(seg, "")))
		if seg != "" {
			out = append(out, Word{Text: strings.Join(strings.Fields(seg), " "), Start: at, End: until})
		}
	}
	for _, m := range vttTS.FindAllStringSubmatchIndex(payload, -1) {
		ts, _ := ParseTimestamp(payload[m[2]:m[3]])
		emit(payload[prev:m[0]], ts)
		at, prev = ts, m[1]
	}
	emit(payload[prev:], end)
	return out
}

// ---- writers ----

// FormatTimestamp writes d as "HH:MM:SS<sep>mmm".
func FormatTimestamp(d time.Duration, sep byte) string {
	if d < 0 {
		d = 0
	}
	ms := d.Milliseconds()
	return fmt.Sprintf("%02d:%02d:%02d%c%03d", ms/3600000, ms/60000%60, ms/1000%60, sep, ms%1000)
}

// WriteSRT writes cues as SubRip, numbered from 1. Cues with no text are
// skipped.
func WriteSRT(w io.Writer, cues []Cue) error {
	var b strings.Builder
	n := 0
	for _, c := range cues {
		t := cleanText(c.Text)
		if t == "" {
			continue
		}
		n++
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", n, FormatTimestamp(c.Start, ','), FormatTimestamp(c.End, ','), t)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// VTTOptions configure WriteVTT.
type VTTOptions struct {
	Speakers bool // wrap text in <v Speaker> when Speaker is set
	Karaoke  bool // write Words as inline timestamps
	IDs      bool // write cue IDs (or 1, 2, 3… when empty)
}

// WriteVTT writes cues as WebVTT.
func WriteVTT(w io.Writer, cues []Cue, opt VTTOptions) error {
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	n := 0
	for _, c := range cues {
		t := cleanText(c.Text)
		if t == "" && len(c.Words) == 0 {
			continue
		}
		n++
		if opt.IDs {
			id := c.ID
			if id == "" || strings.Contains(id, "-->") {
				id = strconv.Itoa(n)
			}
			b.WriteString(oneLine(id) + "\n")
		}
		fmt.Fprintf(&b, "%s --> %s\n", FormatTimestamp(c.Start, '.'), FormatTimestamp(c.End, '.'))
		body := vttEscape(t)
		if opt.Karaoke && len(c.Words) > 0 {
			var kb strings.Builder
			for i, wd := range c.Words {
				if i > 0 {
					if wd.Start > c.Start {
						fmt.Fprintf(&kb, "<%s>", FormatTimestamp(wd.Start, '.'))
					}
					if needsSpace(c.Words[i-1].Text, wd.Text) {
						kb.WriteByte(' ')
					}
				}
				kb.WriteString(vttEscape(wd.Text))
			}
			body = kb.String()
		}
		if opt.Speakers && c.Speaker != "" {
			body = "<v " + vttEscape(oneLine(c.Speaker)) + ">" + body
		}
		b.WriteString(body)
		b.WriteString("\n\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func vttEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// cleanText trims lines and drops empty ones (a blank line would end the
// cue in SRT and VTT).
func cleanText(s string) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			lines = append(lines, t)
		}
	}
	return strings.Join(lines, "\n")
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// Bilingual merges two tracks into one whose cues show primary above
// secondary: cue i of primary takes the secondary cue that overlaps it
// most (by time).
func Bilingual(primary, secondary []Cue) []Cue {
	out := make([]Cue, len(primary))
	for i, p := range primary {
		best, bestOv := -1, time.Duration(0)
		for j, s := range secondary {
			ov := min(p.End, s.End) - max(p.Start, s.Start)
			if ov > bestOv {
				best, bestOv = j, ov
			}
		}
		out[i] = p
		out[i].Words = nil
		if best >= 0 {
			if t := cleanText(secondary[best].Text); t != "" {
				out[i].Text = cleanText(p.Text) + "\n" + t
			}
		}
	}
	return out
}
