package markup

// Converting text to a dialogue block and back (the Audiobook's Insert →
// Dialogue passage, and its editor's Turn back into paragraphs; plan
// §3.3). A conversion keeps every word: quoted speech becomes a speaker's
// turn, the words around it (the speech verbs, the action) narration lines,
// so the book is read as written.

import (
	"strings"

	"github.com/hehehai/AudioSmith/internal/text"
	"github.com/hehehai/AudioSmith/internal/text/autocast"
)

// WrapOptions say how a block is written.
type WrapOptions struct {
	Open, Close string // the markers: "[对话]" and "[/对话]", or "[dialogue]" …
	// Colon follows a name: "：" (Chinese: directions in （）) or ": ".
	Colon string
	// Known are the book's names: a quote nobody is named for goes to the
	// name the narration beside it starts with.
	Known []string
}

// Wrapped is a conversion: the block and what it found.
type Wrapped struct {
	Text     string
	Speakers []string
	Turns    int
	Unknown  int // turns nobody was found for, written "？："
}

type wrapItem struct {
	turn      bool
	name, dir string
	text      string
	para      int
}

// WrapDialogue makes sel a dialogue block: [voice:NAME] paragraphs and
// screenplay lines become NAME's turns, quotes the turns of whom the speech
// verb names (or the narration beside them starts with, or, between two
// speakers, the other one), everything else narration.
func WrapDialogue(sel string, o WrapOptions) Wrapped {
	var items []wrapItem
	for pi, para := range paragraphs(sel) {
		if name, rest, ok := leadingVoice(para); ok {
			items = append(items, wrapItem{turn: name != "", name: name, text: flat(rest), para: pi})
			continue
		}
		for _, l := range autocast.Detect(para, nil).Lines {
			it := wrapItem{text: flat(l.Text), para: pi, name: l.Speaker, dir: l.Direction}
			it.turn = l.Speaker != "" || l.Kind == autocast.Quote
			items = append(items, it)
		}
	}
	// The names, in the cast's spelling when it has them.
	known := map[string]string{}
	for _, n := range o.Known {
		known[NameKey(n)] = n
	}
	var w Wrapped
	speaker := func(n string) string {
		k := NameKey(n)
		if s, ok := known[k]; ok {
			return s
		}
		known[k] = n
		return n
	}
	found := map[string]bool{}
	for i := range items {
		if items[i].name != "" {
			items[i].name = speaker(items[i].name)
			if !found[NameKey(items[i].name)] {
				found[NameKey(items[i].name)] = true
				w.Speakers = append(w.Speakers, items[i].name)
			}
		}
	}
	for i := range items {
		it := &items[i]
		if !it.turn || it.name != "" {
			continue
		}
		it.name = nameBeside(items, i, known)
		if it.name == "" && len(w.Speakers) == 2 {
			it.name = otherSpeaker(items, i, w.Speakers)
		}
		if it.name != "" && !found[NameKey(it.name)] {
			found[NameKey(it.name)] = true
			w.Speakers = append(w.Speakers, it.name)
		}
	}
	var b strings.Builder
	b.WriteString(o.Open)
	zh := strings.HasPrefix(o.Colon, "：")
	for _, it := range items {
		if strings.TrimSpace(it.text) == "" {
			continue
		}
		b.WriteByte('\n')
		if it.turn {
			w.Turns++
			switch {
			case it.name == "" && zh:
				w.Unknown++
				b.WriteString("？")
			case it.name == "":
				w.Unknown++
				b.WriteString("?")
			default:
				b.WriteString(it.name)
			}
			if it.dir != "" {
				if zh {
					b.WriteString("（" + it.dir + "）")
				} else {
					b.WriteString(" (" + it.dir + ")")
				}
			}
			b.WriteString(o.Colon)
		}
		b.WriteString(it.text)
	}
	b.WriteString("\n" + o.Close)
	w.Text = b.String()
	return w
}

// paragraphs splits s at blank lines.
func paragraphs(s string) []string {
	var out []string
	var cur []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) == "" {
			if len(cur) > 0 {
				out = append(out, strings.Join(cur, "\n"))
			}
			cur = nil
			continue
		}
		cur = append(cur, strings.TrimRight(l, "\r"))
	}
	if len(cur) > 0 {
		out = append(out, strings.Join(cur, "\n"))
	}
	return out
}

// leadingVoice reads a paragraph starting with [voice:NAME]: NAME ("" for
// the narrator) and the rest.
func leadingVoice(para string) (string, string, bool) {
	p := strings.TrimSpace(para)
	tags := text.Tags(p)
	if len(tags) == 0 || tags[0].Start != 0 {
		return "", "", false
	}
	inner := strings.TrimSpace(p[1 : tags[0].End-1])
	low := strings.ToLower(inner)
	if !strings.HasPrefix(low, "voice:") && low != "voice" {
		return "", "", false
	}
	name := ""
	if i := strings.IndexByte(inner, ':'); i >= 0 {
		name = strings.TrimSpace(inner[i+1:])
	}
	if strings.EqualFold(name, "default") {
		name = ""
	}
	return name, strings.TrimSpace(p[tags[0].End:]), true
}

// flat puts a line's lines on one.
func flat(s string) string {
	parts := strings.Split(s, "\n")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	out := parts[0]
	for _, p := range parts[1:] {
		if p == "" {
			continue
		}
		if r := []rune(out); len(r) > 0 && (text.IsUnspaced(r[len(r)-1]) || text.IsUnspaced([]rune(p)[0])) {
			out += p
		} else {
			out += " " + p
		}
	}
	return out
}

// nameBeside is the known name the narration next to quote i (in its
// paragraph: before it, else after it) starts with, the longest.
func nameBeside(items []wrapItem, i int, known map[string]string) string {
	for _, j := range []int{i - 1, i + 1} {
		if j < 0 || j >= len(items) || items[j].turn || items[j].para != items[i].para {
			continue
		}
		best := ""
		for _, n := range known {
			if strings.HasPrefix(NameKey(items[j].text), NameKey(n)) && len(n) > len(best) {
				best = n
			}
		}
		if best != "" {
			return best
		}
	}
	return ""
}

// otherSpeaker is, between two speakers, the one who did not speak last
// (or, at the start, who does not speak next).
func otherSpeaker(items []wrapItem, i int, two []string) string {
	other := func(n string) string {
		if NameKey(n) == NameKey(two[0]) {
			return two[1]
		}
		return two[0]
	}
	for j := i - 1; j >= 0; j-- {
		if items[j].turn && items[j].name != "" {
			return other(items[j].name)
		}
	}
	for j := i + 1; j < len(items); j++ {
		if items[j].turn && items[j].name != "" {
			return other(items[j].name)
		}
	}
	return ""
}

// UnwrapDialogue writes block b of src back as paragraphs: a turn as
// [voice:NAME] and its words, narration in the voice around the block
// (outer, "" for the narrator's), stage directions and pauses as they
// stand. The voice after the block is outer again.
func UnwrapDialogue(src string, b DialogueBlock, outer string) string {
	back := outer
	if back == "" {
		back = "default"
	}
	cur := outer
	var paras []string
	for _, l := range b.Lines {
		switch {
		case l.Kind == DialogueTurn && l.Name != "":
			p := "[voice:" + l.Name + "]"
			if l.DirSpan.Len() > 0 {
				p += l.DirSpan.Of(src)
			}
			paras = append(paras, p+l.Text.Of(src))
			cur = l.Name
		case l.Kind == DialoguePause:
			paras = append(paras, l.Line.Of(src))
		default:
			s := l.Line.Of(src)
			if l.Kind != DialogueDirection {
				s = l.Text.Of(src)
			}
			if cur != outer {
				s = "[voice:" + back + "]" + s
				cur = outer
			}
			paras = append(paras, s)
		}
	}
	out := strings.Join(paras, "\n\n")
	if cur != outer {
		out += "[voice:" + back + "]"
	}
	return out
}

// VoiceAt is the voice in effect at offset at of a manuscript: the last
// [voice:] switch before it in its chapter, outside dialogue blocks ("":
// the narrator's).
func VoiceAt(src string, at int) string {
	d := Parse(src[:at], nil)
	blocks, _ := DialogueBlocks(src)
	voice := ""
	for _, t := range d.Tokens {
		switch t.Kind {
		case Chapter:
			voice = ""
		case Voice:
			inside := false
			for _, b := range blocks {
				if t.Span.Start >= b.Span.Start && t.Span.Start < b.Span.End {
					inside = true
				}
			}
			if !inside {
				voice = t.Name
			}
		}
	}
	return voice
}
