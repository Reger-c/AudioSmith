package core

import (
	"crypto/rand"
	"time"
)

// crockford is Crockford's base32 alphabet, as ULIDs use: no I, L, O, U.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewID returns a ULID for t: 26 characters, the first 10 encoding the
// millisecond and the rest 80 random bits. IDs sort by creation time as
// strings, are unique across goroutines and processes, and are safe in
// file names. Every new entity (take, voice, project, job…) uses one.
//
// IDs are opaque everywhere else: takes made before M0 keep their
// "20060102-150405.000" ids, and code must not parse ids of any shape.
func NewID(t time.Time) string {
	var b [16]byte
	ms := uint64(t.UnixMilli())
	for i := 5; i >= 0; i-- {
		b[i] = byte(ms)
		ms >>= 8
	}
	if _, err := rand.Read(b[6:]); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	// 128 bits → 26 base32 digits (the first carries 3 bits).
	var out [26]byte
	var acc uint64
	bits := 0
	j := 25
	for i := 15; i >= 0; i-- {
		acc |= uint64(b[i]) << bits
		bits += 8
		for bits >= 5 {
			out[j] = crockford[acc&31]
			j--
			acc >>= 5
			bits -= 5
		}
	}
	out[0] = crockford[acc&31]
	return string(out[:])
}
