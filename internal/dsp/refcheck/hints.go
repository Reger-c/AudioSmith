package refcheck

// Hint codes, most important first. Leading and trailing silence, long
// pauses and loudness have none: Tidy fixes them.
const (
	CodeNoSpeech  = "no_speech"  // under MinSpeech of speech
	CodeTooShort  = "too_short"  // under ShortSpeech
	CodeVeryNoisy = "very_noisy" // SNR under VeryNoisySNR: record again
	CodeClipping  = "clipping"   // more than ClipLimit of the samples at full scale
	CodeMismatch  = "mismatch"   // the transcript is not what was heard
	CodeNoisy     = "noisy"      // SNR under NoisySNR: denoised automatically
	CodeShort     = "short"      // dialogue: under DialogueSpeech
	CodeFlat      = "flat"       // F0 spread under FlatRange
	CodeSlow      = "slow"       // dialogue: slower than SlowRate*
)

// Severity: a Warn hint is worth fixing (record again, fix the text); an
// Info one is a nudge, or says what was done about it.
type Severity int

const (
	Info Severity = iota
	Warn
)

// Hint is one finding: a code, its severity and the measured value.
type Hint struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Value    float64  `json:"value"`
}

// Grade is a reference's overall quality.
type Grade string

const (
	Good    Grade = "good"
	OK      Grade = "ok"
	Poor    Grade = "poor"
	Unknown Grade = ""
)

// SlowRate is the rate under which a reference sounds read rather than
// spoken.
func (r Report) SlowRate() float64 {
	if r.CJK {
		return SlowRateCJK
	}
	return SlowRateWords
}

// Hints are the report's findings for use u, most important first.
func (r Report) Hints(u Use) []Hint {
	var out []Hint
	add := func(code string, s Severity, v float64) { out = append(out, Hint{Code: code, Severity: s, Value: v}) }
	switch {
	case r.Speech < MinSpeech:
		add(CodeNoSpeech, Warn, r.Speech)
		return out
	case r.Speech < ShortSpeech:
		add(CodeTooShort, Warn, r.Speech)
	}
	if r.SNR < VeryNoisySNR {
		add(CodeVeryNoisy, Warn, r.SNR)
	}
	if r.Clipped > ClipLimit {
		add(CodeClipping, Warn, r.Clipped)
	}
	if r.CER > MismatchCER {
		add(CodeMismatch, Warn, r.CER)
	}
	if r.SNR >= VeryNoisySNR && r.SNR < NoisySNR {
		add(CodeNoisy, Info, r.SNR)
	}
	if u == UseDialogue && r.Speech >= ShortSpeech && r.Speech < DialogueSpeech {
		add(CodeShort, Info, r.Speech)
	}
	if r.F0 > 0 && r.F0Range < FlatRange {
		add(CodeFlat, Info, r.F0Range)
	}
	if u == UseDialogue && r.Rate > 0 && r.Rate < r.SlowRate() {
		add(CodeSlow, Info, r.Rate)
	}
	return out
}

// Grade is Poor with a Warn hint, OK with an Info one, else Good.
func (r Report) Grade(u Use) Grade {
	g := Good
	for _, h := range r.Hints(u) {
		if h.Severity == Warn {
			return Poor
		}
		g = OK
	}
	return g
}
