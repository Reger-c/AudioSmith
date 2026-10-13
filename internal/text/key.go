package text

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// keyVersion changes whenever Normalize or the key layout changes, so old
// cache entries stop matching instead of being misread.
const keyVersion = "audiosmith.chunk.v1"

// NormalizeForKey is the text identity used by Key: Unicode NFC, runs of
// white space collapsed to one space, trimmed. Case and punctuation are
// kept because they change how a sentence is spoken.
func NormalizeForKey(s string) string {
	s = norm.NFC.String(s)
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Key is the content address of a synthesised chunk: a hex SHA-256 of the
// normalised text and every parameter that changes the audio (model,
// revision, voice, language, seed, speed, steps…). Params are hashed in key
// order, so map iteration order does not matter; format numbers the same
// way every time (strconv.FormatFloat(v, 'g', -1, 64)).
func Key(text string, params map[string]string) string {
	h := sha256.New()
	write := func(s string) {
		// Length-prefix every field so ("ab","c") ≠ ("a","bc").
		var n [8]byte
		l := len(s)
		for i := range n {
			n[i] = byte(l >> (8 * i))
		}
		h.Write(n[:])
		h.Write([]byte(s))
	}
	write(keyVersion)
	write(NormalizeForKey(text))
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		write(k)
		write(params[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}
