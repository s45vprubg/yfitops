package store

import (
	"context"
	"sync"

	"github.com/s45vprubg/yfitops/server/internal/game"
)

// MemLock is an in-memory BuzzLock for tests that don't need a live Redis.
// It mirrors RedisLock's single-winner semantics with a mutex+map: the first
// goroutine to claim a roundKey wins, all others get won=false until Release.
type MemLock struct {
	mu      sync.Mutex
	holders map[string]string // roundKey -> winning playerID
}

var _ game.BuzzLock = (*MemLock)(nil)

// NewMemLock returns a ready in-memory lock.
func NewMemLock() *MemLock {
	return &MemLock{holders: make(map[string]string)}
}

// TryAcquire claims roundKey for playerID iff unclaimed. The mutex makes the
// check-and-set atomic, matching Redis SET NX (design_doc §3.4).
func (l *MemLock) TryAcquire(_ context.Context, roundKey, playerID string) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, taken := l.holders[roundKey]; taken {
		return false, nil
	}
	l.holders[roundKey] = playerID
	return true, nil
}

// Release clears the claim on roundKey.
func (l *MemLock) Release(_ context.Context, roundKey string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.holders, roundKey)
	return nil
}

// Holder returns the winning playerID, or "" if unclaimed.
func (l *MemLock) Holder(_ context.Context, roundKey string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.holders[roundKey], nil
}
