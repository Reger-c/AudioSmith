package subtitle

import (
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hehehai/AudioSmith/internal/text"
)

// ASSOptions configure WriteASS. Zero fields take the defaults noted.
type ASSOptions struct {
	Title              string
	PlayResX, PlayResY int    // 1920 × 1080
	Font               string // "Arial"; pick a CJK font (e.g. "PingFang SC") for CJK text
	FontSize           int    // 64
	Bold               bool
	// Colours in ASS &HAABBGGRR form (see ASSColor). In karaoke, text not
	// yet sung shows in Secondary and switches to Primary when sung.
	Primary, Secondary, OutlineColour, Back string  // white, yellow, black, 50% black
	Outline, Shadow                         float64 // 3, 1
	Alignment                               int     // numpad layout: 2 = bottom centre
	MarginV                                 int     // 50
	// Karaoke writes per-word highlight tags from Cue.Words.
	Karaoke bool
	// KaraokeTag is "k" (instant), "kf" (sweep) or "ko" (outline); "" = "k".
	KaraokeTag string
}

// ASSColor returns r, g, b and alpha (0 = opaque) as an ASS colour.
func ASSColor(r, g, b, alpha uint8) string {
	return fmt.Sprintf("&H%02X%02X%02X%02X", alpha, b, g, r)
}

func (o ASSOptions) withDefaults() ASSOptions {
	if o.PlayResX == 0 {
		o.PlayResX = 1920
	}
	if o.PlayResY == 0 {
		o.PlayResY = 1080
	}
	if o.Font == "" {
		o.Font = "Arial"
	}
	if o.FontSize == 0 {
		o.FontSize = 64
	}
	if o.Primary == "" {
		o.Primary = ASSColor(255, 255, 255, 0)
	}
	if o.Secondary == "" {
		o.Secondary = ASSColor(255, 220, 0, 0)
	}
	if o.OutlineColour == "" {
		o.OutlineColour = ASSColor(0, 0, 0, 0)
	}
	if o.Back == "" {
		o.Back = ASSColor(0, 0, 0, 0x80)
	}
	if o.Outline == 0 {
		o.Outline = 3
	}
	if o.Shadow == 0 {
		o.Shadow = 1
	}
	if o.Alignment == 0 {
		o.Alignment = 2
	}
	if o.MarginV == 0 {
		o.MarginV = 50
	}
	switch o.KaraokeTag {
	case "k", "kf", "ko":
	default:
		o.KaraokeTag = "k"
	}
	return o
}

// assTime writes "H:MM:SS.cc".
func assTime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	cs := (d.Milliseconds() + 5) / 10
	return fmt.Sprintf("%d:%02d:%02d.%02d", cs/360000, cs/6000%60, cs/100%60, cs%100)
}

// assEscape makes s safe in a Dialogue text field: override braces and
// backslashes become full-width look-alikes, line breaks become \N.
func assEscape(s string) string {
	return strings.NewReplacer("\\", "＼", "{", "｛", "}", "｝", "\r", "", "\n", `\N`).Replace(s)
}

// WriteASS writes cues as an Advanced SubStation Alpha script. With
// Karaoke, every cue that has Words gets one {\k} tag per word, timed from
// word start to the next word's start (pauses stay on the previous word),
// with a lead-in tag when the first word starts after the cue.
func WriteASS(w io.Writer, cues []Cue, opt ASSOptions) error {
	o := opt.withDefaults()
	bold := 0
	if o.Bold {
		bold = -1
	}
	var b strings.Builder
	b.WriteString("[Script Info]\n")
	if o.Title != "" {
		fmt.Fprintf(&b, "Title: %s\n", oneLine(o.Title))
	}
	fmt.Fprintf(&b, "ScriptType: v4.00+\nWrapStyle: 0\nScaledBorderAndShadow: yes\nPlayResX: %d\nPlayResY: %d\n\n", o.PlayResX, o.PlayResY)
	b.WriteString("[V4+ Styles]\n")
	b.WriteString("Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")
	fmt.Fprintf(&b, "Style: Default,%s,%d,%s,%s,%s,%s,%d,0,0,0,100,100,0,0,1,%g,%g,%d,60,60,%d,1\n\n",
		strings.ReplaceAll(o.Font, ",", " "), o.FontSize, o.Primary, o.Secondary, o.OutlineColour, o.Back, bold, o.Outline, o.Shadow, o.Alignment, o.MarginV)
	b.WriteString("[Events]\n")
	b.WriteString("Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")
	for _, c := range cues {
		body := assEscape(cleanText(c.Text))
		if o.Karaoke && len(c.Words) > 0 {
			body = karaoke(c, o.KaraokeTag)
		}
		if body == "" {
			continue
		}
		fmt.Fprintf(&b, "Dialogue: 0,%s,%s,Default,%s,0,0,0,,%s\n", assTime(c.Start), assTime(c.End),
			strings.ReplaceAll(oneLine(c.Speaker), ",", " "), body)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func karaoke(c Cue, tag string) string {
	cs := func(d time.Duration) int64 { return ((d - c.Start).Milliseconds() + 5) / 10 }
	// The line break of a two-line cue, as a count of non-space runes.
	breakAt := -1
	if i := strings.IndexByte(cleanText(c.Text), '\n'); i >= 0 {
		breakAt = nonSpaceLen(cleanText(c.Text)[:i])
	}
	var b strings.Builder
	if lead := cs(c.Words[0].Start); lead > 0 {
		fmt.Fprintf(&b, `{\%s%d}`, tag, lead)
	}
	seen := 0
	for i, w := range c.Words {
		end := c.End
		if i+1 < len(c.Words) {
			end = c.Words[i+1].Start
		}
		start := max(w.Start, c.Start)
		d := cs(end) - cs(start)
		if d < 0 {
			d = 0
		}
		fmt.Fprintf(&b, `{\%s%d}%s`, tag, d, assEscape(w.Text))
		seen += nonSpaceLen(w.Text)
		if i+1 < len(c.Words) {
			switch {
			case breakAt >= 0 && seen >= breakAt:
				b.WriteString(`\N`)
				breakAt = -1
			case needsSpace(w.Text, c.Words[i+1].Text):
				b.WriteByte(' ')
			}
		}
	}
	return b.String()
}

func nonSpaceLen(s string) int {
	n := 0
	for _, r := range s {
		if !unicode.IsSpace(r) {
			n++
		}
	}
	return n
}

// needsSpace reports whether two adjacent words are written with a space
// between them: not around CJK/Thai characters, not before closing
// punctuation and not after an opening bracket.
func needsSpace(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	la, _ := utf8.DecodeLastRuneInString(a)
	fb, _ := utf8.DecodeRuneInString(b)
	if text.IsUnspaced(la) || text.IsUnspaced(fb) {
		return false
	}
	if strings.ContainsRune(".,!?;:%)]}»”’…", fb) || strings.ContainsRune("([{«“‘¿¡", la) {
		return false
	}
	if (fb == '\'' || fb == '’') && utf8.RuneCountInString(b) <= 3 { // "John" + "'s", "do" + "n't"
		return false
	}
	return true
}

// JoinWords joins word texts with spaces where the language uses them.
func JoinWords(ws []Word) string {
	var b strings.Builder
	for i, w := range ws {
		t := strings.TrimSpace(w.Text)
		if t == "" {
			continue
		}
		if i > 0 && b.Len() > 0 && needsSpace(lastWord(b.String()), t) {
			b.WriteByte(' ')
		}
		b.WriteString(t)
	}
	return b.String()
}

func lastWord(s string) string {
	if i := strings.LastIndexByte(s, ' '); i >= 0 {
		return s[i+1:]
	}
	return s
}
