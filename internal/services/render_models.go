package services

import "slices"

// Traits are what the render pipeline needs to know about a model beyond
// its catalogue capabilities: how much text one engine call takes, and
// how its markup is lowered. They are keyed by the model's family
// (catalog.Entry.Family), with the catalogue's capability names.
//
// The catalogue does not carry these yet (see the M1-G handoff: chunk
// limits, non-verbal tags, steps and determinism belong in catalog.json);
// until it does, this table is their home.
type Traits struct {
	Family string `json:"family"`
	// NonVerbal: renders non-verbal tags ([laughter]…) and bracketed
	// phonemes itself (OmniVoice).
	NonVerbal bool `json:"nonVerbal,omitempty"`
	// Speed: takes a speed factor ("speed" capability).
	Speed bool `json:"speed,omitempty"`
	// Steps: takes a number of diffusion steps ("Voice refinement",
	// performance tiers).
	Steps bool `json:"steps,omitempty"`
	// Duration: can target a duration ("duration" capability).
	Duration bool `json:"duration,omitempty"`
	// Deterministic models say the same text the same way whatever the
	// seed, so the seed is not part of their chunk keys (Kokoro).
	Deterministic bool `json:"deterministic,omitempty"`
	// MaxChars caps the runes of one engine call for spaced scripts,
	// MaxCharsCJK for scripts mostly in Han, kana or Hangul.
	MaxChars    int `json:"maxChars"`
	MaxCharsCJK int `json:"maxCharsCJK"`
}

// Step counts of the performance tiers, for models with Steps.
const (
	StepsFast     = 16
	StepsBalanced = 32 // OmniVoice's default
	StepsQuality  = 48
)

// TraitsFor returns the traits of a model of family with capabilities caps.
func TraitsFor(family string, caps []string) Traits {
	t := Traits{
		Family:   family,
		Speed:    slices.Contains(caps, "speed"),
		Duration: slices.Contains(caps, "duration"),
		MaxChars: 400, MaxCharsCJK: 200,
	}
	switch family {
	case "kokoro", "kitten":
		// Kokoro takes at most 510 phonemes a call: about 300 letters
		// of English, or 100 Chinese characters (pinyin phonemes).
		t.MaxChars, t.MaxCharsCJK = 300, 100
		t.Deterministic = true
	case "qwen3_tts", "qwen3":
		t.MaxChars, t.MaxCharsCJK = 500, 250
	case "omnivoice":
		t.NonVerbal, t.Steps = true, true
		t.MaxChars, t.MaxCharsCJK = 600, 250
	case "marvis", "soprano":
		t.MaxChars, t.MaxCharsCJK = 300, 150
	}
	return t
}
