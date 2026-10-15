package telephony

import (
	"regexp"
	"strings"
)

// Small local models (Qwen3 4B, Gemma 4 E2B) write HANGUP at the end of
// every reply, the very first one included (thread #44: an English call
// hung up after its opening line). So the model's HANGUP is a request:
// the agent ends the call only when the conversation has reached an end.

// MinCallerTurnsToHangup is how many times the other person must have
// spoken before the agent's own goodbye ends the call; before that, the
// goodbye must come with a final outcome (booked, done, not_done).
const MinCallerTurnsToHangup = 2

// HangupCheck is what the agent knows when the model asks to hang up.
type HangupCheck struct {
	CallerTurns int    // the other person's transcribed turns so far
	LastCaller  string // their last words
	Say         string // what the agent says in this reply (or, when it says nothing, its last line)
	Outcome     string // the call's outcome so far (this reply's included)
	WrapUp      bool   // the call is about to reach its limit
}

// Hangup reasons (for the log).
const (
	HangupOpening   = "opening turn"
	HangupNoGoodbye = "no goodbye yet"
	HangupTooEarly  = "too early"
	HangupWrapUp    = "wrap-up"
	HangupCallerBye = "the other person said goodbye"
	HangupFinished  = "finished"
)

// AllowHangup decides whether a HANGUP request ends the call, and why:
// never on the opening turn; when the other person said goodbye or time
// is up; otherwise only when the agent closes (a goodbye, or thanks that
// ask nothing more) and either the task has a final outcome or the other
// person has spoken at least MinCallerTurnsToHangup times. An outcome
// alone is not enough: some models report one on every turn.
func AllowHangup(c HangupCheck) (bool, string) {
	switch {
	case c.WrapUp:
		return true, HangupWrapUp
	case c.CallerTurns == 0:
		return false, HangupOpening
	case IsGoodbye(c.LastCaller):
		return true, HangupCallerBye
	case !Closing(c.Say):
		return false, HangupNoGoodbye
	case finalOutcome(c.Outcome) || c.CallerTurns >= MinCallerTurnsToHangup:
		return true, HangupFinished
	}
	return false, HangupTooEarly
}

// finalOutcome: the task was settled one way or the other (needs_you is
// not: small models report it on every turn).
func finalOutcome(o string) bool {
	switch o {
	case OutcomeBooked, OutcomeDone, OutcomeNotDone:
		return true
	}
	return false
}

var (
	// goodbyeRE: farewells in the languages calls are made in. Latin
	// words need word boundaries ("bye" but not "byelaw").
	goodbyeRE  = regexp.MustCompile(`(?i)\b(?:good[- ]?bye|bye(?:[- ]bye)?|bye for now|see you|see ya|take care|talk (?:to you )?(?:soon|later)|have a (?:good|great|nice|lovely|wonderful) (?:day|evening|night|weekend|one|afternoon|morning)|adi[oó]s|au revoir|tsch[uü]ss|auf wiedersehen|arrivederci|ciao)\b`)
	goodbyeCJK = []string{"再见", "再會", "再会", "拜拜", "回见", "回頭見", "回头见", "下次见", "明天见", "改天见", "先这样", "就这样吧", "先挂了", "さようなら", "失礼します", "안녕히"}
	thanksRE   = regexp.MustCompile(`(?i)\b(?:thanks|thank you|cheers|merci|danke|gracias|grazie)\b`)
	thanksCJK  = []string{"谢谢", "謝謝", "感谢", "感謝", "多谢", "ありがとう", "감사"}
)

// IsGoodbye reports whether text says goodbye.
func IsGoodbye(text string) bool {
	if goodbyeRE.MatchString(text) {
		return true
	}
	for _, w := range goodbyeCJK {
		if strings.Contains(text, w) {
			return true
		}
	}
	return false
}

// Closing reports whether the agent's words close the call: a goodbye,
// or thanks that ask nothing more.
func Closing(text string) bool {
	if IsGoodbye(text) {
		return true
	}
	if strings.ContainsAny(text, "?？") {
		return false
	}
	if thanksRE.MatchString(text) {
		return true
	}
	for _, w := range thanksCJK {
		if strings.Contains(text, w) {
			return true
		}
	}
	return false
}
