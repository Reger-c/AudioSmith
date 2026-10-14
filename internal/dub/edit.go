package dub

import (
	"math"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hehehai/AudioSmith/internal/text"
)

// Edits of the transcript editor. Each works on the project in place and
// reports whether it changed anything; the UI applies the same edit to
// its copy and to the saved document (services.Dubs.Update), and keeps
// an undo snapshot (History) before it.

// MinSegment is the shortest segment an edit makes, seconds.
const MinSegment = 0.2

// Sort keeps the segments in time order (stable for equal starts).
func (p *Project) Sort() {
	slices.SortStableFunc(p.Segments, func(a, b Segment) int {
		switch {
		case a.Start < b.Start:
			return -1
		case a.Start > b.Start:
			return 1
		}
		return 0
	})
}

// SetText changes what segment id says: lang "" is the source text, else
// that language's translation. A new source text drops the translations
// (they translated the old one) and the word timings.
func (p *Project) SetText(id, lang, text string) bool {
	s, ok := p.Segment(id)
	if !ok {
		return false
	}
	text = strings.TrimSpace(text)
	if lang == "" {
		if s.Text == text {
			return false
		}
		s.Text, s.Words, s.Lines = text, nil, nil
		return true
	}
	l := s.Lines[lang]
	if l.Text == text {
		return false
	}
	l.Text, l.Edited, l.Err, l.Skipped, l.Fit, l.Shorter = text, true, "", false, nil, ""
	if s.Lines == nil {
		s.Lines = map[string]Line{}
	}
	s.Lines[lang] = l
	return true
}

// UseShorter replaces a line with its suggested shorter rewrite.
func (p *Project) UseShorter(id, lang string) bool {
	s, ok := p.Segment(id)
	if !ok || s.Lines[lang].Shorter == "" {
		return false
	}
	l := s.Lines[lang]
	l.Draft, l.Text, l.Shorter, l.Fit, l.Edited = l.Text, l.Shorter, "", nil, true
	s.Lines[lang] = l
	return true
}

// SetTiming moves segment id to start–end, kept inside the source
// (duration, 0 = unknown) and at least MinSegment long. With clampNeighbours
// it may not cross the segments before and after it.
func (p *Project) SetTiming(id string, start, end, duration float64, clampNeighbours bool) bool {
	i := p.Index(id)
	if i < 0 {
		return false
	}
	lo, hi := 0.0, math.Inf(1)
	if duration > 0 {
		hi = duration
	}
	if clampNeighbours {
		if i > 0 {
			lo = max(lo, p.Segments[i-1].End)
		}
		if i+1 < len(p.Segments) {
			hi = min(hi, p.Segments[i+1].Start)
		}
	}
	start = max(start, lo)
	end = min(end, hi)
	if end-start < MinSegment {
		if start+MinSegment <= hi {
			end = start + MinSegment
		} else {
			start = max(end-MinSegment, lo)
		}
	}
	start, end = round3(start), round3(end)
	s := &p.Segments[i]
	if s.Start == start && s.End == end {
		return false
	}
	s.Start, s.End = start, end
	p.Sort()
	return true
}

// Nudge moves segment id's start (edge "start"), end ("end") or both
// ("move") by delta seconds.
func (p *Project) Nudge(id, edge string, delta, duration float64) bool {
	s, ok := p.Segment(id)
	if !ok {
		return false
	}
	start, end := s.Start, s.End
	switch edge {
	case "start":
		start += delta
	case "end":
		end += delta
	default:
		start, end = start+delta, end+delta
		if start < 0 {
			start, end = 0, end-start
		}
		if duration > 0 && end > duration {
			start, end = start-(end-duration), duration
		}
	}
	return p.SetTiming(id, start, end, duration, false)
}

// Split cuts segment id in two. at is a rune offset into the text being
// edited (lang's translation, or the source for ""); -1 picks the
// sentence or word boundary nearest the middle. The time is shared by
// the share of the text before the cut; the other texts are cut at their
// nearest boundary to the same share. It returns the new (second) id.
func (p *Project) Split(id, lang string, at int) (string, bool) {
	i := p.Index(id)
	if i < 0 {
		return "", false
	}
	s := p.Segments[i]
	edited := s.Text
	if lang != "" {
		edited = s.Lines[lang].Text
	}
	n := utf8.RuneCountInString(edited)
	if n < 2 {
		return "", false
	}
	if at < 0 {
		at = Boundary(edited, 0.5)
	}
	at = max(1, min(at, n-1))
	share := float64(at) / float64(n)
	cut := s.Start + (s.End-s.Start)*share
	if cut-s.Start < MinSegment/2 || s.End-cut < MinSegment/2 {
		if s.End-s.Start < MinSegment {
			return "", false
		}
		cut = max(min(cut, s.End-MinSegment/2), s.Start+MinSegment/2)
	}
	first, second := s, s
	first.Words, second.Words = nil, nil
	for _, w := range s.Words {
		if w.Start < cut {
			first.Words = append(first.Words, w)
		} else {
			second.Words = append(second.Words, w)
		}
	}
	second.ID = p.newID()
	first.End, second.Start = round3(cut), round3(cut)
	// The source text.
	srcAt := at
	if lang != "" {
		srcAt = Boundary(s.Text, share)
	}
	first.Text, second.Text = splitRunes(s.Text, srcAt)
	// Speakers of merged lines come back.
	first.Parts, second.Parts = nil, nil
	for _, pt := range s.Parts {
		if pt.Offset < srcAt {
			first.Parts = append(first.Parts, pt)
		} else {
			second.Parts = append(second.Parts, Part{Offset: pt.Offset - srcAt, Speaker: pt.Speaker})
		}
	}
	if len(second.Parts) > 0 && second.Parts[0].Offset <= 0 {
		second.Speaker = second.Parts[0].Speaker
		second.Parts = second.Parts[1:]
	} else if len(first.Parts) > 0 {
		second.Speaker = first.Parts[len(first.Parts)-1].Speaker
	}
	// The translations.
	first.Lines, second.Lines = nil, nil
	for l, line := range s.Lines {
		k := at
		if l != lang {
			k = Boundary(line.Text, share)
		}
		a, b := splitRunes(line.Text, k)
		la, lb := Line{Text: a, Edited: line.Edited}, Line{Text: b, Edited: line.Edited}
		if first.Lines == nil {
			first.Lines, second.Lines = map[string]Line{}, map[string]Line{}
		}
		first.Lines[l], second.Lines[l] = la, lb
	}
	p.Segments[i] = first
	p.Segments = slices.Insert(p.Segments, i+1, second)
	return second.ID, true
}

func splitRunes(s string, at int) (string, string) {
	r := []rune(s)
	at = max(0, min(at, len(r)))
	return strings.TrimSpace(string(r[:at])), strings.TrimSpace(string(r[at:]))
}

// Boundary is the rune offset in s nearest share (0–1) of its length
// where it can be cut: after a sentence end, else after a clause mark,
// else at a space; any character in unspaced scripts (CJK).
func Boundary(s string, share float64) int {
	r := []rune(s)
	n := len(r)
	if n < 2 {
		return n
	}
	target := int(math.Round(float64(n) * share))
	best, bestCost := -1, math.Inf(1)
	for k := 1; k < n; k++ {
		prev, next := r[k-1], r[k]
		var weight float64
		switch {
		case strings.ContainsRune(".!?。！？…", prev) && (unicode.IsSpace(next) || text.IsUnspaced(prev)):
			weight = 0
		case strings.ContainsRune(",;:，；：、—", prev):
			weight = 0.15
		case unicode.IsSpace(next) || unicode.IsSpace(prev):
			weight = 0.3
		case text.IsUnspaced(prev) && text.IsUnspaced(next):
			weight = 0.35
		default:
			continue
		}
		cost := math.Abs(float64(k-target))/float64(n) + weight
		if cost < bestCost {
			best, bestCost = k, cost
		}
	}
	if best < 0 {
		return max(1, min(target, n-1))
	}
	// Cut after the space, not before it.
	for best < n && unicode.IsSpace(r[best]) {
		best++
	}
	return best
}

// Merge joins segment id with the next one (next) or the previous one.
// Texts are joined in every language; the second speaker is remembered
// in Parts, so a later split gives the line back to them.
func (p *Project) Merge(id string, next bool) bool {
	i := p.Index(id)
	if i < 0 {
		return false
	}
	j := i + 1
	if !next {
		j, i = i, i-1
	}
	if i < 0 || j >= len(p.Segments) {
		return false
	}
	a, b := p.Segments[i], p.Segments[j]
	m := a
	m.End = max(a.End, b.End)
	m.Start = min(a.Start, b.Start)
	m.Text = joinText(a.Text, b.Text)
	// Where b's text starts in the joined text.
	offset := utf8.RuneCountInString(m.Text) - utf8.RuneCountInString(strings.TrimSpace(b.Text))
	m.Words = append(slices.Clone(a.Words), b.Words...)
	m.Parts = slices.Clone(a.Parts)
	if b.Speaker != speakerAtEnd(a) {
		m.Parts = append(m.Parts, Part{Offset: offset, Speaker: b.Speaker})
	}
	for _, pt := range b.Parts {
		m.Parts = append(m.Parts, Part{Offset: offset + pt.Offset, Speaker: pt.Speaker})
	}
	m.Lines = nil
	langs := map[string]bool{}
	for l := range a.Lines {
		langs[l] = true
	}
	for l := range b.Lines {
		langs[l] = true
	}
	for l := range langs {
		la, lb := a.Lines[l], b.Lines[l]
		if m.Lines == nil {
			m.Lines = map[string]Line{}
		}
		m.Lines[l] = Line{Text: joinText(la.Text, lb.Text), Edited: la.Edited || lb.Edited}
	}
	m.Volume = max(a.Volume, b.Volume)
	p.Segments[i] = m
	p.Segments = slices.Delete(p.Segments, j, j+1)
	return true
}

func speakerAtEnd(s Segment) string {
	if n := len(s.Parts); n > 0 {
		return s.Parts[n-1].Speaker
	}
	return s.Speaker
}

// joinText joins two texts with a space, or without one between
// unspaced scripts (CJK).
func joinText(a, b string) string {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	last, _ := utf8.DecodeLastRuneInString(a)
	first, _ := utf8.DecodeRuneInString(b)
	if text.IsUnspaced(last) || text.IsUnspaced(first) {
		return a + b
	}
	return a + " " + b
}

// Insert adds an empty line after segment id (or at the start when id is
// ""), in the silence after it: up to 2 s, at least MinSegment, sharing
// the speaker and voice of the line before. It returns the new id.
func (p *Project) Insert(after string, duration float64) string {
	i := p.Index(after)
	start := 0.0
	var prev Segment
	if i >= 0 {
		prev = p.Segments[i]
		start = prev.End
	}
	end := start + 2
	if i+1 < len(p.Segments) {
		end = min(end, p.Segments[i+1].Start)
	}
	if duration > 0 {
		end = min(end, duration)
		start = min(start, max(duration-MinSegment, 0))
	}
	if end-start < MinSegment {
		end = start + MinSegment
	}
	s := p.NewSegment(round3(start), round3(end), prev.Speaker, "")
	s.Voice = prev.Voice
	if s.Speaker == "" {
		if l := p.SpeakerLabels(); len(l) > 0 {
			s.Speaker = l[0]
		} else {
			s.Speaker = "S1"
		}
	}
	p.Segments = slices.Insert(p.Segments, i+1, s)
	return s.ID
}

// Delete removes segments.
func (p *Project) Delete(ids ...string) bool {
	n := len(p.Segments)
	p.Segments = slices.DeleteFunc(p.Segments, func(s Segment) bool { return slices.Contains(ids, s.ID) })
	return len(p.Segments) != n
}

// SetVoice casts segments with a voice ("" = their speaker's).
func (p *Project) SetVoice(ref string, ids ...string) bool {
	changed := false
	for i := range p.Segments {
		if slices.Contains(ids, p.Segments[i].ID) && p.Segments[i].Voice != ref {
			p.Segments[i].Voice, changed = ref, true
		}
	}
	return changed
}

// SetLang makes segments speak lang in every track ("" = the track's).
func (p *Project) SetLang(lang string, ids ...string) bool {
	changed := false
	for i := range p.Segments {
		if slices.Contains(ids, p.Segments[i].ID) && p.Segments[i].Lang != lang {
			p.Segments[i].Lang, changed = lang, true
		}
	}
	return changed
}

// SetSpeaker gives segments to a speaker.
func (p *Project) SetSpeaker(speaker string, ids ...string) bool {
	changed := false
	for i := range p.Segments {
		if slices.Contains(ids, p.Segments[i].ID) && p.Segments[i].Speaker != speaker {
			p.Segments[i].Speaker, p.Segments[i].Parts, changed = speaker, nil, true
		}
	}
	return changed
}

// CastSpeaker casts a speaker with a voice ("" = their automatic clone).
func (p *Project) CastSpeaker(speaker, ref string) bool {
	if p.Cast[speaker] == ref {
		return false
	}
	if p.Cast == nil {
		p.Cast = map[string]string{}
	}
	if ref == "" {
		delete(p.Cast, speaker)
	} else {
		p.Cast[speaker] = ref
	}
	return true
}

// RestoreSource drops every translation and its errors (↺ Restore).
func (p *Project) RestoreSource() bool {
	changed := false
	for i := range p.Segments {
		if len(p.Segments[i].Lines) > 0 {
			p.Segments[i].Lines, changed = nil, true
		}
	}
	return changed
}

// SkipFailed marks lang's failed lines skipped: the dub keeps them silent.
func (p *Project) SkipFailed(lang string) int {
	n := 0
	for i := range p.Segments {
		l, ok := p.Segments[i].Lines[lang]
		if ok && l.Err != "" && !l.Skipped {
			l.Skipped = true
			p.Segments[i].Lines[lang] = l
			n++
		}
	}
	return n
}

// Cleanup merges fragments: a line shorter than 1 s or of one or two
// words, said by the same speaker less than 0.3 s after the line before
// (neither locked), joins it. It returns how many lines were merged.
func (p *Project) Cleanup() int {
	merged := 0
	for i := 1; i < len(p.Segments); {
		a, b := p.Segments[i-1], p.Segments[i]
		w, _ := text.Count(b.Text)
		short := b.Duration() < 1 || (w > 0 && w <= 2 && !hasUnspaced(b.Text)) || utf8.RuneCountInString(strings.TrimSpace(b.Text)) <= 3
		if short && !a.Locked && !b.Locked && a.Speaker == b.Speaker && b.Start-a.End < 0.3 && b.Start >= a.Start {
			if p.Merge(b.ID, false) {
				merged++
				continue
			}
		}
		i++
	}
	return merged
}

func hasUnspaced(s string) bool {
	for _, r := range s {
		if text.IsUnspaced(r) {
			return true
		}
	}
	return false
}

// Overlaps lists the segments that overlap the next one in time.
func (p *Project) Overlaps() []string {
	var out []string
	for i := 0; i+1 < len(p.Segments); i++ {
		if p.Segments[i].End > p.Segments[i+1].Start+0.01 {
			out = append(out, p.Segments[i].ID)
		}
	}
	return out
}

// History is the editor's undo and redo: snapshots of the segments (and
// the cast), at most HistoryLimit.
type History struct {
	undo, redo []Snapshot
}

// HistoryLimit is how many steps Undo goes back.
const HistoryLimit = 50

// Snapshot is the editable part of a project.
type Snapshot struct {
	Segments []Segment
	Cast     map[string]string
	NextSeg  int
	Label    string // what the edit after it did ("split", "typing"…)
}

// Take snapshots p.
func Take(p *Project, label string) Snapshot {
	cast := make(map[string]string, len(p.Cast))
	for k, v := range p.Cast {
		cast[k] = v
	}
	return Snapshot{Segments: CloneSegments(p.Segments), Cast: cast, NextSeg: p.NextSeg, Label: label}
}

// Restore puts a snapshot back into p.
func (s Snapshot) Restore(p *Project) {
	p.Segments = CloneSegments(s.Segments)
	p.Cast = make(map[string]string, len(s.Cast))
	for k, v := range s.Cast {
		p.Cast[k] = v
	}
	p.NextSeg = max(p.NextSeg, s.NextSeg)
}

// Push records the state before an edit; redo is dropped.
func (h *History) Push(s Snapshot) {
	h.undo = append(h.undo, s)
	if len(h.undo) > HistoryLimit {
		h.undo = slices.Delete(h.undo, 0, len(h.undo)-HistoryLimit)
	}
	h.redo = nil
}

// Coalesce reports whether the last step has this label (typing bursts
// make one step: the caller skips Push).
func (h *History) Coalesce(label string) bool {
	return label != "" && len(h.undo) > 0 && h.undo[len(h.undo)-1].Label == label
}

// Undo restores the last snapshot into p, keeping p's state for Redo.
func (h *History) Undo(p *Project) bool {
	if len(h.undo) == 0 {
		return false
	}
	s := h.undo[len(h.undo)-1]
	h.undo = h.undo[:len(h.undo)-1]
	h.redo = append(h.redo, Take(p, s.Label))
	s.Restore(p)
	return true
}

// Redo re-applies the last undone edit.
func (h *History) Redo(p *Project) bool {
	if len(h.redo) == 0 {
		return false
	}
	s := h.redo[len(h.redo)-1]
	h.redo = h.redo[:len(h.redo)-1]
	h.undo = append(h.undo, Take(p, s.Label))
	s.Restore(p)
	return true
}

// Reset forgets every step (a new transcript, a translation).
func (h *History) Reset() { h.undo, h.redo = nil, nil }

// CanUndo and CanRedo say whether there is a step.
func (h *History) CanUndo() bool { return len(h.undo) > 0 }
func (h *History) CanRedo() bool { return len(h.redo) > 0 }

// Len is how many undo steps there are.
func (h *History) Len() int { return len(h.undo) }
