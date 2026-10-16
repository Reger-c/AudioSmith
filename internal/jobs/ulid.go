package jobs

// Job ids: ULIDs, monotonic within a millisecond.

import (
	"crypto/rand"
	"sync"
	"time"
)

// The last ULID newULID made.
var (
	ulidMu   sync.Mutex
	ulidMS   uint64
	ulidLast [16]byte
	ulidUsed bool
)

// newULID makes a ULID (as core.NewID, which jobs cannot import),
// monotonic within a millisecond so the jobs of a graph sort, and so run,
// in the order they were made.
func newULID(t time.Time) string {
	const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	ms := uint64(t.UnixMilli())
	ulidMu.Lock()
	var b [16]byte
	if ulidUsed && ms == ulidMS {
		b = ulidLast
		for i := 15; i >= 6; i-- {
			b[i]++
			if b[i] != 0 {
				break
			}
		}
	} else {
		for i := 5; i >= 0; i-- {
			b[i] = byte(ms >> (8 * (5 - i)))
		}
		rand.Read(b[6:])
		b[6] &= 0x7f
	}
	ulidMS, ulidLast, ulidUsed = ms, b, true
	ulidMu.Unlock()
	var out [26]byte
	var acc uint64
	bits, j := 0, 25
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
