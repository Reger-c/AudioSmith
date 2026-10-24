package logs

// Redaction of what AudioSmith logs (#108, code review #100 L3): the
// family's rules (actnowkit/redact) and AudioSmith's own secrets.

import (
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/hehehai/actnowkit/redact"
)

var (
	// The Local API's keys: "as-" and 40 letters and digits (api.NewKey).
	apiKeyRE = regexp.MustCompile(`\bas-[A-Za-z0-9]{40}\b`)
	// A bearer or basic credential: masked before redact.Text, whose
	// Authorization rule masks only the word "Bearer" and keeps the token.
	bearerRE = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`)
	// A Twilio auth token (32 hex digits) where its name says what it is.
	twilioRE = regexp.MustCompile(`(?i)\b(auth[_ -]?token|twilio[._ -]?token|x-twilio-signature)(\s*[:=]\s*"?)[0-9a-zA-Z+/=]{16,}`)
)

// secrets are values the app read from the Keychain (AddSecret): masked
// wherever they show, whatever their shape.
var secrets struct {
	sync.RWMutex
	values []string
}

// AddSecret has Redact mask v from now on: a key or token the app read
// or saved. Short values (under 8 characters) are not added: a PIN or
// a word would mask ordinary text.
func AddSecret(v string) {
	v = strings.TrimSpace(v)
	if len(v) < 8 {
		return
	}
	secrets.Lock()
	defer secrets.Unlock()
	for _, s := range secrets.values {
		if s == v {
			return
		}
	}
	secrets.values = append(secrets.values, v)
}

// home is the user's home folder, which Redact shortens to "~".
var home = func() string {
	h, _ := os.UserHomeDir()
	if len(h) < 2 {
		return ""
	}
	return strings.TrimRight(h, "/")
}()

// Redact removes secrets from a line before it is shown, written or
// bundled: URLs' keys and passwords, tokens and Authorization headers
// (redact.Text), Local API keys, Twilio auth tokens, the values given to
// AddSecret; and it shortens the home folder to "~".
func Redact(s string) string {
	if s == "" {
		return s
	}
	s = bearerRE.ReplaceAllString(s, "${1} "+redact.Mask)
	s = redact.Text(s)
	s = apiKeyRE.ReplaceAllString(s, redact.Mask)
	s = twilioRE.ReplaceAllString(s, "${1}${2}"+redact.Mask)
	secrets.RLock()
	for _, v := range secrets.values {
		s = strings.ReplaceAll(s, v, redact.Mask)
	}
	secrets.RUnlock()
	if home != "" {
		s = shortenHome(s, home)
	}
	return s
}

// shortenHome replaces home at the start of a path ("/Users/x/…" →
// "~/…"); "/Users/xy" stays.
func shortenHome(s, home string) string {
	if !strings.Contains(s, home) {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(s, home)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		end := i + len(home)
		b.WriteString(s[:i])
		if end == len(s) || s[end] == '/' || !isPathChar(s[end]) {
			b.WriteString("~")
		} else {
			b.WriteString(home)
		}
		s = s[end:]
	}
}

func isPathChar(c byte) bool {
	return c == '.' || c == '-' || c == '_' || c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}
