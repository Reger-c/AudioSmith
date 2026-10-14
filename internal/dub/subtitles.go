package dub

import (
	"bytes"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hehehai/AudioSmith/internal/doc/subtitle"
)

// --- importing a subtitle file as the transcript ------------------------------

// ImportStats says what an import of subtitles did.
type ImportStats struct {
	Imported  int `json:"imported"`
	Malformed int `json:"malformed"` // blocks the parser skipped
	Empty     int `json:"empty"`     // cues without text
	Dropped   int `json:"dropped"`   // cues mostly inside the one before
	Clamped   int `json:"clamped"`   // cues cut at the end of the source
}

// ImportSubtitles replaces the transcript with the cues of an SRT or VTT
// file. Each new line keeps the speaker (and so the voice) of the old
// line it overlaps most; a VTT voice tag (<v Name>) names a new speaker.
// Translations and renders of the old lines are gone; the source
// language stays. duration (0 = unknown) bounds the cues.
func (p *Project) ImportSubtitles(data string, duration float64) ImportStats {
	_, cues, diags := subtitle.Parse(data)
	var st ImportStats
	st.Malformed = len(diags)
	old := p.Segments
	var segs []Segment
	names := map[string]string{} // VTT voice → label
	for _, c := range cues {
		txt := strings.Join(strings.Fields(strings.ReplaceAll(c.Text, "\n", " ")), " ")
		if txt == "" {
			st.Empty++
			continue
		}
		start, end := c.Start.Seconds(), c.End.Seconds()
		if duration > 0 {
			if start >= duration {
				st.Clamped++
				continue
			}
			if end > duration {
				end = duration
				st.Clamped++
			}
		}
		if n := len(segs); n > 0 {
			prev := &segs[n-1]
			if start < prev.End {
				// Mostly inside the one before: drop it; else trim that one.
				if prev.End-start > (end-start)/2 {
					st.Dropped++
					continue
				}
				prev.End = start
			}
		}
		s := p.NewSegment(round3(start), round3(end), "", txt)
		if c.Speaker != "" {
			if l, ok := names[c.Speaker]; ok {
				s.Speaker = l
			} else {
				l = "S" + strconv.Itoa(len(names)+1)
				names[c.Speaker] = l
				s.Speaker = l
			}
		} else {
			s.Speaker = speakerByOverlap(old, start, end)
		}
		for _, w := range c.Words {
			s.Words = append(s.Words, Word{Text: w.Text, Start: w.Start.Seconds(), End: w.End.Seconds()})
		}
		segs = append(segs, s)
	}
	if len(names) > 0 {
		p.Names = map[string]string{}
		for name, l := range names {
			p.Names[l] = name
		}
	}
	for i := range segs {
		if segs[i].Speaker == "" {
			segs[i].Speaker = "S1"
		}
	}
	st.Imported = len(segs)
	p.Segments = segs
	p.Tracks = nil
	return st
}

// speakerByOverlap is the speaker of the old segment overlapping start–end
// most ("" when none does).
func speakerByOverlap(old []Segment, start, end float64) string {
	best, bestOv := "", 0.0
	for _, s := range old {
		ov := min(end, s.End) - max(start, s.Start)
		if ov > bestOv {
			best, bestOv = s.Speaker, ov
		}
	}
	return best
}

// --- pasting a translation ------------------------------------------------------

// PasteMode is how pasted text maps onto the segments.
type PasteMode string

const (
	PasteTimed    PasteMode = "timed"    // SRT/VTT: by time overlap
	PasteNumbered PasteMode = "numbered" // "1. …", "2) …", "[3] …": by number
	PastePlain    PasteMode = "plain"    // one line per segment, in order
)

// PasteEntry is one pasted line.
type PasteEntry struct {
	Number     int // numbered mode (1-based); 0 otherwise
	Start, End float64
	Text       string
}

var numberedRE = regexp.MustCompile(`^\s*(?:\[(\d+)\]|(\d+)[.)、:：])\s*(.*)$`)

// ParsePaste detects the mode of pasted text and reads its entries:
// subtitles (an SRT timing line or a WEBVTT header), numbered lines (most
// non-blank lines start with a number), or plain lines (blank lines only
// separate).
func ParsePaste(s string) (PasteMode, []PasteEntry) {
	s = strings.ReplaceAll(strings.TrimPrefix(s, "\ufeff"), "\r\n", "\n")
	if strings.HasPrefix(strings.TrimSpace(s), "WEBVTT") || strings.Contains(s, "-->") {
		_, cues, _ := subtitle.Parse(s)
		var out []PasteEntry
		for _, c := range cues {
			t := strings.Join(strings.Fields(strings.ReplaceAll(c.Text, "\n", " ")), " ")
			if t != "" {
				out = append(out, PasteEntry{Start: c.Start.Seconds(), End: c.End.Seconds(), Text: t})
			}
		}
		if len(out) > 0 {
			return PasteTimed, out
		}
	}
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	numbered := 0
	for _, l := range lines {
		if numberedRE.MatchString(l) {
			numbered++
		}
	}
	if len(lines) > 0 && numbered*10 >= len(lines)*8 {
		var out []PasteEntry
		for _, l := range lines {
			m := numberedRE.FindStringSubmatch(l)
			if m == nil {
				// A continuation of the line before.
				if n := len(out); n > 0 {
					out[n-1].Text = joinText(out[n-1].Text, l)
				}
				continue
			}
			num, _ := strconv.Atoi(m[1] + m[2])
			out = append(out, PasteEntry{Number: num, Text: strings.TrimSpace(m[3])})
		}
		return PasteNumbered, out
	}
	out := make([]PasteEntry, len(lines))
	for i, l := range lines {
		out[i] = PasteEntry{Text: l}
	}
	return PastePlain, out
}

// PasteRow is one segment's change.
type PasteRow struct {
	ID     string
	Before string
	After  string // "" = no match: left unchanged
}

// PastePreview is what applying a paste would do.
type PastePreview struct {
	Mode    PasteMode
	Rows    []PasteRow // every segment, in order
	Matched int
	Unused  int // pasted entries no segment took
}

// MatchPaste maps entries onto the segments for lang.
func (p *Project) MatchPaste(mode PasteMode, entries []PasteEntry, lang string) PastePreview {
	pv := PastePreview{Mode: mode, Rows: make([]PasteRow, len(p.Segments))}
	used := make([]bool, len(entries))
	for i, s := range p.Segments {
		pv.Rows[i] = PasteRow{ID: s.ID, Before: s.Lines[lang].Text}
	}
	take := func(i, e int) {
		if e < 0 || e >= len(entries) {
			return
		}
		r := &pv.Rows[i]
		r.After = joinText(r.After, entries[e].Text)
		used[e] = true
	}
	switch mode {
	case PasteTimed:
		// Each cue goes to the segment it overlaps most; a segment takes
		// all its cues, in order.
		for e, en := range entries {
			best, bestOv := -1, 0.0
			for i, s := range p.Segments {
				ov := min(en.End, s.End) - max(en.Start, s.Start)
				if ov > bestOv {
					best, bestOv = i, ov
				}
			}
			if best >= 0 {
				take(best, e)
			}
		}
	case PasteNumbered:
		byNum := map[int]int{}
		for e, en := range entries {
			if _, dup := byNum[en.Number]; !dup {
				byNum[en.Number] = e
			}
		}
		for i := range p.Segments {
			if e, ok := byNum[i+1]; ok {
				take(i, e)
			}
		}
		// Numbers that fit no segment: fall back to order for the rest.
		if pv.count() == 0 {
			for i := range p.Segments {
				take(i, i)
			}
		}
	default:
		for i := range p.Segments {
			take(i, i)
		}
	}
	for _, r := range pv.Rows {
		if r.After != "" {
			pv.Matched++
		}
	}
	for _, u := range used {
		if !u {
			pv.Unused++
		}
	}
	return pv
}

func (pv *PastePreview) count() int {
	n := 0
	for _, r := range pv.Rows {
		if r.After != "" {
			n++
		}
	}
	return n
}

// ApplyPaste writes the matched rows into lang (as hand-written lines).
func (p *Project) ApplyPaste(pv PastePreview, lang string) int {
	n := 0
	for _, r := range pv.Rows {
		if r.After != "" && p.SetText(r.ID, lang, r.After) {
			n++
		}
	}
	return n
}

// --- writing subtitles ------------------------------------------------------------

// Subtitle formats.
const (
	SubSRT = "srt"
	SubVTT = "vtt"
	SubASS = "ass" // karaoke
)

// SubtitleOptions choose what a subtitle file carries.
type SubtitleOptions struct {
	Format string // SubSRT, SubVTT or SubASS
	// Lang is the language of the text: a target, or "" for the source.
	Lang string
	// Dual adds the source under the translation in each cue.
	Dual bool
	// Split makes Netflix-style cues (≤ 42 characters a line, ≤ 2 lines,
	// ≤ 17 characters a second; less for CJK) instead of a cue per line.
	Split bool
}

// Cues are the subtitles of the project in lang ("" = the source): one
// per segment with text, or Netflix-style cues with Split. Translated
// lines are timed by their segment; source words keep their timings
// while the text is as transcribed (karaoke), else words are spread
// evenly.
func (p *Project) Cues(o SubtitleOptions) []subtitle.Cue {
	var segs []subtitle.Segment
	var dual []string
	for _, s := range p.Segments {
		txt := s.Text
		if o.Lang != "" {
			txt = p.TextFor(s, o.Lang)
		}
		txt = strings.TrimSpace(txt)
		if txt == "" {
			continue
		}
		seg := subtitle.Segment{Start: dur(s.Start), End: dur(s.End), Text: txt, Speaker: p.SpeakerName(s.Speaker)}
		if o.Lang == "" && len(s.Words) > 0 {
			for _, w := range s.Words {
				seg.Words = append(seg.Words, subtitle.Word{Text: w.Text, Start: dur(w.Start), End: dur(w.End)})
			}
		}
		segs = append(segs, seg)
		dual = append(dual, strings.TrimSpace(s.Text))
	}
	var cues []subtitle.Cue
	if o.Split && !o.Dual {
		lang := o.Lang
		if lang == "" {
			lang = p.SourceLanguage()
		}
		cues, _ = subtitle.Split(segs, subtitle.Defaults(lang))
	} else {
		for i, sg := range segs {
			c := subtitle.Cue{Start: sg.Start, End: sg.End, Text: sg.Text, Speaker: sg.Speaker, Words: sg.Words}
			if o.Dual && o.Lang != "" && dual[i] != "" && dual[i] != sg.Text {
				c.Text = sg.Text + "\n" + dual[i]
				c.Words = nil
			}
			cues = append(cues, c)
		}
	}
	if o.Format == SubASS {
		for i := range cues {
			if len(cues[i].Words) == 0 {
				cues[i].Words = spreadWords(cues[i])
			}
		}
	}
	for i := range cues {
		cues[i].ID = strconv.Itoa(i + 1)
	}
	return cues
}

// spreadWords times a cue's words evenly over it, by their length.
func spreadWords(c subtitle.Cue) []subtitle.Word {
	var words []string
	if hasUnspaced(c.Text) {
		for _, r := range strings.ReplaceAll(c.Text, "\n", "") {
			words = append(words, string(r))
		}
	} else {
		words = strings.Fields(c.Text)
	}
	total := 0
	for _, w := range words {
		total += len([]rune(w))
	}
	if total == 0 {
		return nil
	}
	span := float64(c.End - c.Start)
	var out []subtitle.Word
	at := float64(c.Start)
	for _, w := range words {
		d := span * float64(len([]rune(w))) / float64(total)
		out = append(out, subtitle.Word{Text: w, Start: time.Duration(at), End: time.Duration(at + d)})
		at += d
	}
	return out
}

// Subtitles writes the project's subtitles in o's format.
func (p *Project) Subtitles(o SubtitleOptions) ([]byte, error) {
	cues := p.Cues(o)
	var b bytes.Buffer
	var err error
	switch o.Format {
	case SubVTT:
		err = subtitle.WriteVTT(&b, cues, subtitle.VTTOptions{})
	case SubASS:
		font, sample := "Arial", firstText(p).Text
		if o.Lang != "" {
			sample = p.TextFor(firstText(p), o.Lang)
		}
		if hasUnspaced(sample) {
			font = "PingFang SC"
		}
		err = subtitle.WriteASS(&b, cues, subtitle.ASSOptions{Title: p.Title, Karaoke: true, KaraokeTag: "kf", Font: font})
	default:
		err = subtitle.WriteSRT(&b, cues)
	}
	return b.Bytes(), err
}

func firstText(p *Project) Segment {
	for _, s := range p.Segments {
		if strings.TrimSpace(s.Text) != "" {
			return s
		}
	}
	return Segment{}
}

func dur(s float64) time.Duration { return time.Duration(math.Round(s * float64(time.Second))) }
