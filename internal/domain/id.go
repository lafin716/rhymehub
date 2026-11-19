package domain

import (
	"crypto/rand"
	"math/big"
	"time"
)

// crockford is the Crockford base32 alphabet used by ULID.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewID returns a prefixed, time-sortable random identifier such as "tsk_01JD...".
// The body is a 26 character ULID: 48-bit millisecond timestamp + 80 random bits.
func NewID(prefix string) string {
	return prefix + "_" + newULID(time.Now())
}

func newULID(t time.Time) string {
	var b [16]byte
	ms := uint64(t.UnixMilli())
	for i := 5; i >= 0; i-- {
		b[i] = byte(ms)
		ms >>= 8
	}
	if _, err := rand.Read(b[6:]); err != nil {
		// crypto/rand failing means the OS entropy source is broken; no safe fallback exists.
		panic("domain: crypto/rand failed: " + err.Error())
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
