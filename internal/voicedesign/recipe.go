package voicedesign

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/hehehai/AudioSmith/internal/text"
)

// MaxSeed is the largest seed (2³¹−1), so seeds fit every engine's int32.
const MaxSeed int64 = 1<<31 - 1

// Recipe is everything that defines a designed voice: the description as
// written, the effective attributes (the description's mapping plus the
// user's explicit picks) and the seed that fixes the identity. A recipe is
// what takes store as their design recipe and what .asvoice bundles carry.
type Recipe struct {
	Description string     `json:"description,omitempty"`
	Attrs       Attributes `json:"attrs"`
	Seed        int64      `json:"seed"`
}

// Normalize returns the recipe in canonical form: the description in NFC
// with white space collapsed and capped at MaxDescription runes, canonical
// attributes, and the seed folded into [0, MaxSeed].
func (r Recipe) Normalize() Recipe {
	d := text.NormalizeForKey(r.Description)
	if utf8.RuneCountInString(d) > MaxDescription {
		d = string([]rune(d)[:MaxDescription])
	}
	return Recipe{Description: d, Attrs: r.Attrs.Canonical(), Seed: FoldSeed(r.Seed)}
}

// Validate checks the seed range, the description length and the
// attributes.
func (r Recipe) Validate() error {
	if r.Seed < 0 || r.Seed > MaxSeed {
		return fmt.Errorf("voicedesign: seed %d out of range [0, %d]", r.Seed, MaxSeed)
	}
	if n := utf8.RuneCountInString(r.Description); n > MaxDescription {
		return fmt.Errorf("voicedesign: description has %d characters (limit %d)", n, MaxDescription)
	}
	return r.Attrs.Validate()
}

// Picks are the attribute values set on top of what the description says
// (the user's explicit choices).
func (r Recipe) Picks() []Value {
	return r.Attrs.Diff(Parse(r.Description).Attrs)
}

// keyVersion changes whenever the key layout or Normalize changes, so old
// cache entries stop matching instead of being misread.
const keyVersion = "audiosmith.voicedesign.v1"

// Key is a stable content hash of the normalised recipe (hex SHA-256,
// 64 chars). Equal recipes have equal keys across runs and machines.
func (r Recipe) Key() string {
	n := r.Normalize()
	b, _ := json.Marshal(n) // field order is fixed by the struct
	h := sha256.New()
	h.Write([]byte(keyVersion))
	h.Write([]byte{0})
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

// Key hashes any parts after the recipe key; used for preview cache keys
// that add the model and the sample text.
func Key(parts ...string) string {
	h := sha256.New()
	h.Write([]byte(keyVersion))
	for _, p := range parts {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// NewSeed returns a random seed in [0, MaxSeed].
func NewSeed() int64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return int64(binary.LittleEndian.Uint64(b[:]) & uint64(MaxSeed))
}

// SeedFrom derives a stable seed from a string (an archetype id, a name),
// so a voice gets the same identity everywhere.
func SeedFrom(s string) int64 {
	sum := sha256.Sum256([]byte("audiosmith.seed\x00" + s))
	return int64(binary.LittleEndian.Uint64(sum[:8]) & uint64(MaxSeed))
}

// FoldSeed maps any int64 into [0, MaxSeed] (non-negative values keep
// their low 31 bits).
func FoldSeed(s int64) int64 {
	return int64(uint64(s) & uint64(MaxSeed))
}

// ParseSeed reads a seed typed by the user ("42", " 2147483647 ").
func ParseSeed(s string) (int64, error) {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("voicedesign: seed %q is not a whole number", s)
	}
	if v < 0 || v > MaxSeed {
		return 0, fmt.Errorf("voicedesign: seed %d out of range [0, %d]", v, MaxSeed)
	}
	return v, nil
}
