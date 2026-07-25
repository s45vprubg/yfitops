package anticheat

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"sync"
)

// NonceGate implements the replay/staleness protection of design_doc §4D:
// every state transition increments a server-owned monotonic counter. The
// current counter value is broadcast in each server envelope; clients echo it
// back on buzz/action requests. Validate accepts an action only if its nonce
// exactly equals the current counter, and Bump advances the counter on every
// state transition — so any action stamped with a prior counter value is stale
// and discarded, blocking automated playback-macro replay.
//
// NOTE: the counter is transmitted in cleartext and is not an unforgeable or
// unpredictable opaque token. Protection comes from exact-equality + bump-on-
// every-transition, not from secrecy of the counter value. The HMAC helpers
// below are not on the wire path (see Token).
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

// Token returns an HMAC of the current counter. It is NOT part of the wire
// protocol: the engine transmits the raw counter (see Current) and Validate
// compares the raw counter. This method has no production callers and is
// exercised only by anticheat_test.go; it does not add unforgeability or
// unpredictability to the deployed scheme.
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
