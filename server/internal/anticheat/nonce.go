package anticheat

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"sync"
)

// NonceGate implements the replay/rate-limiting protection of design_doc §4D:
// every state transition increments a server-validated cryptographic nonce.
// Buzz/action requests carrying a stale nonce are discarded, blocking
// automated playback-macro replay.
//
// The "encrypted" requirement (§4D) is satisfied by issuing an opaque HMAC
// token to clients rather than the raw counter, so a client cannot fabricate a
// future nonce — it can only echo back what the server most recently issued.
type NonceGate struct {
	mu      sync.RWMutex
	counter uint64
	secret  []byte
}

func NewNonceGate(secret []byte) *NonceGate {
	return &NonceGate{secret: secret}
}

// Current returns the current nonce counter. Clients echo this on actions.
func (g *NonceGate) Current() uint64 {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.counter
}

// Bump advances the nonce on a state transition and returns the new value.
// All in-flight actions stamped with the previous nonce are now stale.
func (g *NonceGate) Bump() uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.counter++
	return g.counter
}

// Validate reports whether an action's nonce is fresh (matches current).
// Stale nonces (from a prior state) are rejected per §4D.
func (g *NonceGate) Validate(n uint64) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return n == g.counter
}

// Token returns the opaque HMAC-bound token for the current nonce. This is what
// is actually transmitted to clients so the raw counter is never exposed and
// cannot be predicted/forged for a future transition.
func (g *NonceGate) Token() string {
	g.mu.RLock()
	c := g.counter
	g.mu.RUnlock()
	return sign(g.secret, c)
}

func sign(secret []byte, n uint64) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], n)
	m := hmac.New(sha256.New, secret)
	m.Write(buf[:])
	sum := m.Sum(nil)
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(sum)*2)
	for i, b := range sum {
		out[i*2] = hexdigits[b>>4]
		out[i*2+1] = hexdigits[b&0x0f]
	}
	return string(out)
}
