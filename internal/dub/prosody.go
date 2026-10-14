package dub

import (
	"slices"
	"strings"

	"github.com/hehehai/AudioSmith/internal/dsp"
	"github.com/hehehai/AudioSmith/internal/dsp/analysis"
)

// Prosody Mirror ("Mirror source delivery"): each line of the source is
// measured (pitch, its movement, loudness, speaking rate, voicing) against
// its speaker's usual delivery, and lines that stand out get a direction
// that says how ("whispered", "quick, energetic"), so the dub delivers
// them alike. Only lines without a direction get one, and only speakers
// with at least MirrorMinLines lines are measured.

// MirrorMinLines is how many lines a speaker needs for a baseline.
const MirrorMinLines = 3

// mirrorWords turns analysis.Compare's codes into direction words, in the
// order they are written.
var mirrorWords = []struct{ code, word string }{
	{"whispered", "whispered"},
	{"faster", "quick"},
	{"slower", "slow"},
	{"louder", "loud"},
	{"softer", "soft"},
	{"higher", "higher pitch"},
	{"lower", "lower pitch"},
	{"animated", "energetic"},
	{"flat", "calm"},
}

// MirrorDirection turns delta codes into a direction ("" when none).
func MirrorDirection(codes []string) string {
	var words []string
	for _, mw := range mirrorWords {
		if slices.Contains(codes, mw.code) {
			if mw.code == "softer" && slices.Contains(codes, "whispered") {
				continue
			}
			words = append(words, mw.word)
		}
	}
	return strings.Join(words, ", ")
}

// MirrorResult says what Mirror found.
type MirrorResult struct {
	Measured int               // lines measured
	Applied  map[string]string // directions written, by segment id
}

// Mirror measures the lines in audio (the separated voices if there are)
// and fills the empty directions. It changes p.
func (p *Project) Mirror(audio dsp.Buffer) MirrorResult {
	res := MirrorResult{Applied: map[string]string{}}
	if audio.Frames() == 0 {
		return res
	}
	type measured struct {
		i int
		p analysis.Prosody
	}
	bySpeaker := map[string][]measured{}
	for i, s := range p.Segments {
		from := dsp.FramesAt(s.Start, audio.Rate)
		to := min(dsp.FramesAt(s.End, audio.Rate), audio.Frames())
		if to-from < audio.Rate/4 {
			continue
		}
		pr := analysis.Analyze(audio.Slice(from, to))
		if pr.SpeechSeconds < 0.2 {
			continue
		}
		bySpeaker[s.Speaker] = append(bySpeaker[s.Speaker], measured{i, pr})
		res.Measured++
	}
	for _, lines := range bySpeaker {
		if len(lines) < MirrorMinLines {
			continue
		}
		all := make([]analysis.Prosody, len(lines))
		for k, m := range lines {
			all[k] = m.p
		}
		base := analysis.Baseline(all)
		for _, m := range lines {
			s := &p.Segments[m.i]
			if strings.TrimSpace(s.Direction) != "" {
				continue
			}
			if d := MirrorDirection(analysis.Compare(m.p, base).Directions); d != "" {
				s.Direction = d
				res.Applied[s.ID] = d
			}
		}
	}
	return res
}
