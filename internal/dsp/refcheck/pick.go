package refcheck

import "math"

// Limits of a usable rendered candidate (#81 §4.3: 3–4 takes of a
// designed or built-in voice, one kept).
const (
	PickMinSpeech = 8.0  // seconds
	PickMaxLength = 15.0 // seconds, after Tidy
	PickMaxCER    = 0.03
	PickMaxPause  = 1.0 // seconds, before Tidy
)

// Candidate is a rendered take: Raw measured as rendered, Tidy as it
// would be kept.
type Candidate struct {
	Raw, Tidy Report
}

// Faults are the candidate's reasons to be passed over: a recognizer that
// hears other words, a length out of 8–15 s, a long pause, a flat
// intonation, noise (a broken render) or clipping.
func (c Candidate) Faults() []string {
	var out []string
	if c.Tidy.CER > PickMaxCER {
		out = append(out, CodeMismatch)
	}
	if c.Tidy.Speech < PickMinSpeech || c.Tidy.Seconds > PickMaxLength {
		out = append(out, "length")
	}
	if c.Raw.LongestPause > PickMaxPause {
		out = append(out, "pause")
	}
	if c.Tidy.F0 > 0 && c.Tidy.F0Range < FlatRange {
		out = append(out, CodeFlat)
	}
	if c.Raw.SNR < NoisySNR {
		out = append(out, CodeNoisy)
	}
	if c.Raw.Clipped > ClipLimit {
		out = append(out, CodeClipping)
	}
	return out
}

// Score ranks candidates without faults: closest to the voice's pitch
// (median F0 wantF0, when known: a take that drifted to another voice is
// the worst miss), then the livelier intonation, the fewer pauses and a
// pace closer to conversation.
func (c Candidate) Score(wantF0 float64) float64 {
	r := c.Tidy
	s := 0.0
	if wantF0 > 0 && r.F0 > 0 {
		s -= 1.5 * math.Abs(12*math.Log2(r.F0/wantF0))
	}
	s += 0.25 * min(r.F0Range, 12)
	s -= 0.3 * r.PausesPer10
	if r.Rate > 0 {
		s += min(r.Rate/r.SlowRate(), 1.4)
	}
	return s
}

// Pick returns the index of the best candidate: the best scored among
// those without faults, else the one with the fewest (then the best
// scored); -1 for none.
func Pick(cs []Candidate, wantF0 float64) int {
	best, bestFaults, bestScore := -1, 0, 0.0
	for i, c := range cs {
		f, s := len(c.Faults()), c.Score(wantF0)
		if best < 0 || f < bestFaults || f == bestFaults && s > bestScore {
			best, bestFaults, bestScore = i, f, s
		}
	}
	return best
}
