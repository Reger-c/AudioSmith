package dub

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/hehehai/AudioSmith/internal/text"
)

// --- second-pass check ------------------------------------------------------------

// DriftLimit is the error rate above which a line is flagged.
const DriftLimit = 0.35

// Drift is how far what was heard is from what should have been said:
// the edit distance over the expected length, in characters for
// unspaced scripts (Chinese, Japanese, Thai…), else in words; case,
// punctuation and spacing do not count. 0 is a perfect match; it may
// exceed 1 when much more was heard.
func Drift(expected, heard string) float64 {
	a, b := tokens(expected), tokens(heard)
	if len(a) == 0 {
		if len(b) == 0 {
			return 0
		}
		return 1
	}
	return round3(float64(editDistance(a, b)) / float64(len(a)))
}

// CharDrift is Drift in characters for every script, spaces and
// punctuation left out: a recognizer that writes "hinterher geschaut"
// for "hinterhergeschaut" made no error (thread #93: references are
// checked so, words would count it twice).
func CharDrift(expected, heard string) float64 {
	chars := func(s string) []string {
		var out []string
		for _, r := range strings.Join(tokens(s), "") {
			out = append(out, string(r))
		}
		return out
	}
	a, b := chars(expected), chars(heard)
	if len(a) == 0 {
		if len(b) == 0 {
			return 0
		}
		return 1
	}
	return round3(float64(editDistance(a, b)) / float64(len(a)))
}

// tokens splits s for Drift: unspaced characters one by one, other text
// by words, without punctuation, folded to lower case.
func tokens(s string) []string {
	var out []string
	var word []rune
	flush := func() {
		if len(word) > 0 {
			out = append(out, string(word))
			word = word[:0]
		}
	}
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsPunct(r) || unicode.IsSymbol(r) || unicode.IsSpace(r):
			flush()
		case text.IsUnspaced(r) || isThai(r):
			flush()
			out = append(out, string(r))
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'':
			word = append(word, r)
		default:
			flush()
		}
	}
	flush()
	return out
}

func isThai(r rune) bool { return r >= 0x0E00 && r <= 0x0EFF || r >= 0x0E80 && r <= 0x0EFF }

func editDistance(a, b []string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// CheckLine compares a rendered line with what the check heard.
func CheckLine(id, expected, heard string, rendered, slot float64) QCLine {
	q := QCLine{ID: id, Heard: strings.TrimSpace(heard), Drift: Drift(expected, heard)}
	q.Flagged = q.Drift > DriftLimit
	if slot > 0 {
		q.SyncRatio = round3(rendered / slot)
	}
	return q
}

// QCStale reports whether a track's check no longer matches its lines.
func (t Track) QCStale() bool {
	if t.QC == nil {
		return false
	}
	for id, key := range t.QC.Keys {
		if t.Lines[id].Key != key {
			return true
		}
	}
	return len(t.QC.Keys) != len(t.Lines)
}

// --- fingerprints and staleness --------------------------------------------------------

// VoiceSpec is a resolved voice: what the TTS model is given for a line.
// The app resolves VoiceRefs (saved voices, designs, gallery voices)
// into specs before a run, so jobs carry their whole input.
type VoiceSpec struct {
	Model       string  `json:"model,omitempty"` // "" = the project's
	Voice       string  `json:"voice,omitempty"` // built-in speaker, or a design description
	RefAudio    string  `json:"refAudio,omitempty"`
	RefText     string  `json:"refText,omitempty"`
	VoicePrompt string  `json:"voicePrompt,omitempty"`
	Instruct    string  `json:"instruct,omitempty"`
	Language    string  `json:"language,omitempty"` // the model's language value, when fixed
	Seed        int64   `json:"seed,omitempty"`
	Speed       float64 `json:"speed,omitempty"`
	// Auto: clone the speaker's own reference (filled by the pipeline).
	Auto bool `json:"auto,omitempty"`
	// CloneRef and CloneRefText are a designed voice's reference, for the
	// dialogue model, which clones (thread #83); not part of the key: the
	// dub's model does not use them.
	CloneRef     string `json:"cloneRef,omitempty"`
	CloneRefText string `json:"cloneRefText,omitempty"`
}

func (v VoiceSpec) key() string {
	return strings.Join([]string{v.Model, v.Voice, v.RefAudio, v.RefText, v.VoicePrompt, v.Instruct, v.Language,
		strconv.FormatInt(v.Seed, 10), strconv.FormatFloat(v.Speed, 'g', -1, 64), strconv.FormatBool(v.Auto)}, "\x1f")
}

// Fingerprint identifies what a line's audio depends on: its text, its
// language, its voice, its delivery and its slot (lines are fitted to
// it), and the project's model, speed, steps, guidance, instruction and
// timing strategy. A line whose fingerprint is not the one its render
// was made with is stale; its volume is not part of it (the mix applies
// it).
func (p *Project) Fingerprint(s Segment, lang string, v VoiceSpec) string {
	st := p.Settings
	speed := st.Speed
	if s.Speed > 0 {
		speed = s.Speed
	}
	parts := []string{
		"v1", p.TextFor(s, lang), LangFor(s, lang), v.key(), s.Direction, st.Instruct,
		st.Model, strconv.FormatFloat(speed, 'g', -1, 64), strconv.Itoa(st.Steps),
		strconv.FormatFloat(st.Guidance, 'g', -1, 64), string(st.Timing),
		strconv.FormatFloat(round3(s.Start), 'f', 3, 64), strconv.FormatFloat(round3(s.End), 'f', 3, 64),
		strconv.FormatFloat(round3(roomFor(p, s)), 'f', 2, 64),
	}
	h := sha256.Sum256([]byte(strings.Join(parts, "\x1e")))
	return hex.EncodeToString(h[:12])
}

// roomFor is the silence after s (for the timing strategies that use it).
func roomFor(p *Project, s Segment) float64 {
	if p.Settings.Timing == TimingLipSync || p.Settings.Timing == "" {
		return 0
	}
	i := p.Index(s.ID)
	if i < 0 {
		return 0
	}
	return Room(p.Segments, i, p.Source.Duration)
}

// Stale lists lang's lines that need rendering again: no render, a
// different fingerprint, or a failed one. keys are the fingerprints now
// (by segment id); lines without text are not listed.
func (p *Project) Stale(lang string, keys map[string]string) []string {
	t := p.Tracks[lang]
	var out []string
	for _, s := range p.Segments {
		if strings.TrimSpace(p.TextFor(s, lang)) == "" {
			continue
		}
		r, ok := t.Lines[s.ID]
		if !ok || r.Err != "" || r.Key != keys[s.ID] {
			out = append(out, s.ID)
		}
	}
	return out
}

// Speakable lists the segments with text to say in lang, and those whose
// translation is missing or failed (not skipped).
func (p *Project) Speakable(lang string) (ok, missing []string) {
	for _, s := range p.Segments {
		if strings.TrimSpace(s.Text) == "" && strings.TrimSpace(p.TextFor(s, lang)) == "" {
			continue
		}
		if strings.TrimSpace(p.TextFor(s, lang)) != "" {
			ok = append(ok, s.ID)
		} else if !s.Lines[LangFor(s, lang)].Skipped {
			missing = append(missing, s.ID)
		}
	}
	return ok, missing
}

// Untranslated lists the segments lang still needs a translation of (for
// a run that translates only those).
func (p *Project) Untranslated(lang string) []string {
	var out []string
	for _, s := range p.Segments {
		l := LangFor(s, lang)
		if strings.TrimSpace(s.Text) == "" || sameLanguage(l, p.SourceLanguage()) {
			continue
		}
		if line := s.Lines[l]; strings.TrimSpace(line.Text) == "" && !line.Skipped {
			out = append(out, s.ID)
		}
	}
	return out
}

// Languages are every language some line speaks in the targets' tracks
// (the targets, plus per-line overrides), the targets first.
func (p *Project) Languages() []string {
	out := slices.Clone(p.Targets)
	for _, s := range p.Segments {
		if s.Lang != "" && !slices.Contains(out, s.Lang) && !sameLanguage(s.Lang, p.SourceLanguage()) {
			out = append(out, s.Lang)
		}
	}
	return out
}
