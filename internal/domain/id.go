package domain

import (
	"crypto/rand"
	"math/big"
	"sync"
	"time"
)

// crockford is the Crockford base32 alphabet used by ULID.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewID returns a prefixed, time-sortable random identifier such as "tsk_01JD...".
// The body is a 26 character ULID: 48-bit millisecond timestamp + 80 random bits.
// IDs are monotonic within a process: two IDs created in the same millisecond sort in creation order,
// which keeps FIFO ordering (ORDER BY created_at, id) stable.
func NewID(prefix string) string {
	return prefix + "_" + newULID(time.Now())
}

var ulidState struct {
	mu      sync.Mutex
	lastMS  uint64
	lastRnd [10]byte
}

func newULID(t time.Time) string {
	var b [16]byte
	ms := uint64(t.UnixMilli())

	ulidState.mu.Lock()
	if ms <= ulidState.lastMS {
		// Same (or earlier, clock skew) millisecond: reuse the last timestamp and increment the random part.
		ms = ulidState.lastMS
		rnd := ulidState.lastRnd
		for i := len(rnd) - 1; i >= 0; i-- {
			rnd[i]++
			if rnd[i] != 0 {
				break
			}
		}
		ulidState.lastRnd = rnd
	} else {
		if _, err := rand.Read(ulidState.lastRnd[:]); err != nil {
			ulidState.mu.Unlock()
			// crypto/rand failing means the OS entropy source is broken; no safe fallback exists.
			panic("domain: crypto/rand failed: " + err.Error())
		}
		ulidState.lastMS = ms
	}
	copy(b[6:], ulidState.lastRnd[:])
	ulidState.mu.Unlock()

	for i := 5; i >= 0; i-- {
		b[i] = byte(ms)
		ms >>= 8
	}
	n := new(big.Int).SetBytes(b[:])
	mod := big.NewInt(32)
	rem := new(big.Int)
	out := make([]byte, 26)
	for i := len(out) - 1; i >= 0; i-- {
		n.QuoRem(n, mod, rem)
		out[i] = crockford[rem.Int64()]
	}
	return string(out)
}
