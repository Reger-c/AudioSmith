package telephony

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Outcomes of a call.
const (
	OutcomeBooked   = "booked"
	OutcomeDone     = "done"
	OutcomeNotDone  = "not_done"
	OutcomeNeedsYou = "needs_you"
	OutcomeFailed   = "failed"
)

// ValidOutcome reports whether s is an outcome code.
func ValidOutcome(s string) bool {
	switch s {
	case OutcomeBooked, OutcomeDone, OutcomeNotDone, OutcomeNeedsYou, OutcomeFailed:
		return true
	}
	return false
}

// Role of a chat message for the language model.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is one chat message.
type Message struct {
	Role    Role
	Content string
}

// Brief is what the agent knows of its job.
type Brief struct {
	// Task is the user's brief (outbound) or what to do with callers
	// (inbound).
	Task string
	// Inbound: the agent answers a call to the user's number.
	Inbound bool
	// User is who the agent works for ("" = "my user").
	User string
	// Disclosure is what was said first ("" = nothing was).
	Disclosure string
	// Number is the other party's number (outbound), for context.
	Number string
	// Language is the language to start in (BCP-47; "" = the brief's).
	Language string
	// MaxMinutes is the call's limit.
	MaxMinutes int
}

// SystemPrompt is the agent's instructions.
func SystemPrompt(b Brief) string {
	user := strings.TrimSpace(b.User)
	if user == "" {
		user = "your user"
	}
	var s strings.Builder
	if b.Inbound {
		fmt.Fprintf(&s, "You are an AI phone assistant answering calls to the phone number of %s. You are on a live call: what you write is spoken aloud by a synthetic voice, and what the caller says reaches you as a transcript, which may contain recognition mistakes.\n\n", user)
		s.WriteString("What to do with callers:\n")
	} else {
		fmt.Fprintf(&s, "You are an AI phone assistant making a call on behalf of %s. You are on a live call: what you write is spoken aloud by a synthetic voice, and what the other person says reaches you as a transcript, which may contain recognition mistakes.\n\n", user)
		s.WriteString("Your task:\n")
	}
	task := strings.TrimSpace(b.Task)
	if task == "" {
		task = "Take a message: ask who is calling and why, and say you will pass it on."
	}
	s.WriteString(task)
	s.WriteString("\n\nRules:\n")
	fmt.Fprintf(&s, "- You are an AI assistant. If anyone asks, say so plainly. Never claim to be a human or to be %s.\n", user)
	s.WriteString("- Stay on the task. Speak naturally and briefly: one or two short sentences per turn. No lists, markdown, emojis or stage directions.\n")
	s.WriteString("- Never give or ask for payment card numbers, bank details, passwords, PINs or ID numbers, and never agree to pay or to be charged.\n")
	fmt.Fprintf(&s, "- If something needs a decision by %s or you are unsure, say you will check and get back to them, and report the outcome needs_you.\n", user)
	s.WriteString("- When the task is done, or cannot be done, thank them, say goodbye and end the call. Do not keep talking after goodbye.\n")
	s.WriteString("- Reply in the language the other person speaks")
	if b.Language != "" {
		fmt.Fprintf(&s, " (start in %s)", b.Language)
	}
	s.WriteString(".\n")
	if b.MaxMinutes > 0 {
		fmt.Fprintf(&s, "- The call may last %d minutes at most.\n", b.MaxMinutes)
	}
	if b.Disclosure != "" {
		fmt.Fprintf(&s, "- This was already said at the start of the call: %q. Do not repeat it unless asked.\n", b.Disclosure)
	}
	s.WriteString("\nWrite each reply as lines:\n")
	s.WriteString("SAY: what you say next, on one line (always, except when you only end the call)\n")
	s.WriteString("OUTCOME: booked | done | not_done | needs_you, then a dash and a short note for " + user + " — only in your last reply, once the result is clear\n")
	s.WriteString("HANGUP — on its own last line, only in your last reply, after you said goodbye and the other person has answered your request. Never in your first reply, never after a question, never while the conversation goes on.\n")
	s.WriteString("\nMost replies are a single SAY line, for example:\n")
	s.WriteString("SAY: <one or two short sentences>\n")
	s.WriteString("\nOnly the last reply of the call adds the outcome and HANGUP, for example:\n")
	s.WriteString("SAY: <thanks and goodbye>\n")
	s.WriteString("OUTCOME: <booked | done | not_done | needs_you> — <short note>\n")
	s.WriteString("HANGUP\n")
	return s.String()
}

// OpeningNote is the first user message: the call was answered (or
// picked up), start talking.
func OpeningNote(b Brief) string {
	if b.Inbound {
		return "[The call was picked up. Greet the caller and ask how you can help.]"
	}
	return "[The call was answered. Introduce the reason for your call.]"
}

// CallerLine is how a caller's words reach the model.
func CallerLine(text string) string { return text }

// WrapUpNote tells the model time is nearly up.
const WrapUpNote = "[About 30 seconds are left in this call: wrap up now, say goodbye and end the call.]"

// UserLine tells the model what the user said in their own words (take-over).
func UserLine(text string) string {
	return "[Your user took over and said this to the other person themselves: " + strconvQuote(text) + "]"
}

func strconvQuote(s string) string { return fmt.Sprintf("%q", s) }

// Reply is a parsed model reply.
type Reply struct {
	Say     []string // sentences, in order
	Outcome string   // an outcome code, or ""
	Note    string   // the outcome's note
	Hangup  bool
}

// ReplyParser reads a reply as it streams and hands out sentences to say
// as soon as each is complete. Not goroutine-safe.
type ReplyParser struct {
	buf     strings.Builder // the current line
	say     strings.Builder // SAY text not yet handed out
	inSay   bool            // the current line is a SAY line (or unprefixed)
	decided bool            // the current line's kind is known
	carry   string          // the start of a rune split between deltas
	reply   Reply
}

// incompleteTail is the length of an unfinished UTF-8 sequence at the end
// of s.
func incompleteTail(s string) int {
	for i := 1; i <= 3 && i <= len(s); i++ {
		c := s[len(s)-i]
		if c < 0x80 {
			return 0
		}
		if c >= 0xC0 { // a leading byte: is its sequence complete?
			need := 2
			switch {
			case c >= 0xF0:
				need = 4
			case c >= 0xE0:
				need = 3
			}
			if i < need {
				return i
			}
			return 0
		}
	}
	return 0
}

var outcomeRE = regexp.MustCompile(`(?i)^\s*(booked|done|not[ _-]?done|needs[ _-]?you|failed)\b\s*[-—–:,.]?\s*(.*)$`)

// Feed adds streamed text and returns the sentences it completes. A
// rune split between deltas waits for its end.
func (p *ReplyParser) Feed(delta string) []string {
	var out []string
	if p.carry != "" {
		delta, p.carry = p.carry+delta, ""
	}
	if n := incompleteTail(delta); n > 0 {
		delta, p.carry = delta[:len(delta)-n], delta[len(delta)-n:]
	}
	for _, r := range delta {
		if r == '\n' {
			out = append(out, p.endLine()...)
			continue
		}
		p.buf.WriteRune(r)
		if !p.decided {
			p.classify(false) // copies the line so far when it is speech
		} else if p.inSay {
			p.say.WriteRune(r)
		}
		if p.decided && p.inSay {
			out = append(out, p.sentences(false)...)
		}
	}
	return out
}

// Finish ends the reply: the rest to say, and the whole reply.
func (p *ReplyParser) Finish() ([]string, Reply) {
	if p.carry != "" {
		p.buf.WriteString(p.carry)
		p.carry = ""
	}
	out := p.endLine()
	return out, p.reply
}

// classify decides the kind of the current line from its start.
func (p *ReplyParser) classify(final bool) {
	line := strings.TrimLeft(p.buf.String(), " \t*>-")
	up := strings.ToUpper(line)
	for _, kw := range []string{"SAY:", "OUTCOME:", "HANGUP", "END_CALL", "ACTION:"} {
		if strings.HasPrefix(up, kw) {
			p.decided = true
			p.inSay = kw == "SAY:"
			if p.inSay {
				p.say.WriteString(strings.TrimLeft(line[len(kw):], " \t"))
			}
			return
		}
		if !final && strings.HasPrefix(kw, up) {
			return // could still become a keyword
		}
	}
	if final || line != "" {
		// No keyword starts this way: an unprefixed line is speech.
		p.decided, p.inSay = true, true
		p.say.WriteString(line)
	}
}

func (p *ReplyParser) endLine() []string {
	if !p.decided && strings.TrimSpace(p.buf.String()) != "" {
		p.classify(true)
	}
	line := strings.TrimSpace(strings.TrimLeft(p.buf.String(), " \t*>-"))
	up := strings.ToUpper(line)
	var out []string
	switch {
	case p.inSay:
		out = p.sentences(true)
	case strings.HasPrefix(up, "OUTCOME:"):
		if m := outcomeRE.FindStringSubmatch(line[len("OUTCOME:"):]); m != nil {
			p.reply.Outcome = normOutcome(m[1])
			p.reply.Note = strings.TrimSpace(m[2])
		}
	case strings.HasPrefix(up, "HANGUP"), strings.HasPrefix(up, "END_CALL"):
		p.reply.Hangup = true
	case strings.HasPrefix(up, "ACTION:"):
		a := strings.ToLower(strings.TrimSpace(line[len("ACTION:"):]))
		if strings.Contains(a, "end") || strings.Contains(a, "hang") {
			p.reply.Hangup = true
		}
		if strings.Contains(a, "escalate") && p.reply.Outcome == "" {
			p.reply.Outcome = OutcomeNeedsYou
		}
	}
	p.buf.Reset()
	p.decided, p.inSay = false, false
	return out
}

func normOutcome(s string) string {
	s = strings.ToLower(s)
	s = strings.NewReplacer(" ", "_", "-", "_").Replace(s)
	switch s {
	case "notdone":
		return OutcomeNotDone
	case "needsyou":
		return OutcomeNeedsYou
	}
	return s
}

// sentences hands out the complete sentences of the SAY text (all of it
// when final).
func (p *ReplyParser) sentences(final bool) []string {
	text := p.say.String()
	var out []string
	for {
		i := sentenceEnd(text)
		if i < 0 {
			break
		}
		if s := p.speech(text[:i]); s != "" {
			out = append(out, s)
		}
		text = text[i:]
	}
	if final {
		if s := p.speech(text); s != "" {
			out = append(out, s)
		}
		text = ""
	}
	p.say.Reset()
	p.say.WriteString(text)
	p.reply.Say = append(p.reply.Say, out...)
	return out
}

// hangupTokenRE matches an end-of-call token written inside speech
// ("Goodbye! HANGUP", "[END_CALL]"): upper case only, so that "hang up"
// in a sentence stays.
var hangupTokenRE = regexp.MustCompile(`[\[(<*]*\b(?:HANGUP|HANG UP|END_CALL|END CALL)\b[\])>*]*[.!。！]?`)

// speech cleans a sentence to say; an end-of-call token in it is not
// spoken but noted as a hangup request.
func (p *ReplyParser) speech(s string) string {
	if hangupTokenRE.MatchString(s) {
		p.reply.Hangup = true
		s = hangupTokenRE.ReplaceAllString(s, "")
	}
	return cleanSpeech(s)
}

// sentenceEnd is the index just after the first complete sentence of s
// (its end punctuation followed by a space, or CJK end punctuation), or
// -1.
func sentenceEnd(s string) int {
	for i, r := range s {
		switch r {
		case '。', '！', '？', '；':
			return i + utf8.RuneLen(r)
		case '.', '!', '?', ';':
			j := i + 1
			for j < len(s) && strings.ContainsRune(`.!?"')”’`, rune(s[j])) {
				j++
			}
			if j < len(s) && (s[j] == ' ' || s[j] == '\t') {
				// "Dr. Smith", "3.5": too short to be a sentence.
				if i < 4 {
					continue
				}
				return j
			}
		}
	}
	return -1
}

// cleanSpeech removes what should not be spoken: markdown and brackets.
func cleanSpeech(s string) string {
	s = strings.NewReplacer("**", "", "__", "", "`", "", "#", "").Replace(s)
	return strings.TrimFunc(s, func(r rune) bool { return unicode.IsSpace(r) || r == '*' })
}

var longNumberRE = regexp.MustCompile(`\d(?:[ \-.]?\d){8,}`)

// Guard checks a sentence before it is spoken: long numbers (cards,
// accounts, IDs: nine digits or more) that are not in the brief are
// withheld. It returns the sentence to say and whether it was changed.
func Guard(sentence, brief string) (string, bool) {
	allowed := digitsOnly(brief)
	changed := false
	out := longNumberRE.ReplaceAllStringFunc(sentence, func(m string) string {
		d := digitsOnly(m)
		if allowed != "" && strings.Contains(allowed, d) {
			return m
		}
		changed = true
		return "[number withheld]"
	})
	if changed {
		return "I'm sorry, I can't share that number on this call.", true
	}
	return out, false
}

// digitsOnly keeps the digits of s, with a separator between runs, so
// that a number can only match one run.
func digitsOnly(s string) string {
	var b strings.Builder
	last := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			last = true
		case r == ' ' || r == '-' || r == '.':
			// separators inside a number
		default:
			if last {
				b.WriteByte('|')
				last = false
			}
		}
	}
	return b.String()
}

// SummaryPrompt asks for the call's outcome and summary from its
// transcript (lines "Agent: …" / "Them: …").
func SummaryPrompt(b Brief, transcript string) []Message {
	user := strings.TrimSpace(b.User)
	if user == "" {
		user = "the user"
	}
	sys := "You summarise phone calls an AI assistant made or answered for " + user + ". Write for " + user + ", in the language of the task, plainly and briefly."
	var q strings.Builder
	if b.Inbound {
		q.WriteString("The assistant answered a call. Its instructions were:\n")
	} else {
		q.WriteString("The assistant made a call. Its task was:\n")
	}
	q.WriteString(strings.TrimSpace(b.Task))
	q.WriteString("\n\nTranscript:\n")
	q.WriteString(transcript)
	q.WriteString("\n\nAnswer with exactly two lines:\nOUTCOME: booked | done | not_done | needs_you\nSUMMARY: two or three sentences: what happened, what was agreed (dates, times, names, numbers), and anything " + user + " must do.")
	return []Message{{Role: RoleSystem, Content: sys}, {Role: RoleUser, Content: q.String()}}
}

// ParseSummary reads the answer to SummaryPrompt.
func ParseSummary(text string) (outcome, summary string) {
	var rest []string
	for _, line := range strings.Split(text, "\n") {
		l := strings.TrimSpace(strings.TrimLeft(line, "*-# "))
		up := strings.ToUpper(l)
		switch {
		case strings.HasPrefix(up, "OUTCOME:"):
			if m := outcomeRE.FindStringSubmatch(l[len("OUTCOME:"):]); m != nil {
				outcome = normOutcome(m[1])
			}
		case strings.HasPrefix(up, "SUMMARY:"):
			rest = append(rest, strings.TrimSpace(l[len("SUMMARY:"):]))
		case l != "" && len(rest) > 0:
			rest = append(rest, l)
		}
	}
	summary = strings.TrimSpace(strings.Join(rest, " "))
	if summary == "" && outcome == "" {
		summary = strings.TrimSpace(text)
	}
	if !ValidOutcome(outcome) {
		outcome = ""
	}
	return outcome, summary
}
