// Package store provides the persistence + atomic-buzz seams for the game
// engine: a Redis-backed single-winner buzzer lock and a Postgres-backed
// repository, each with an in-memory counterpart for tests. See ports.go in
// internal/game for the BuzzLock and GameRepo contracts this package fulfils.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/s45vprubg/yfitops/server/internal/game"
)

// RedisLock is the production BuzzLock (design_doc §3.4). The atomic
// single-winner guarantee rides on Redis `SET key val NX`: the first buzz to
// land the key is the sole winner, every later buzz observes the key already
// set and gets won=false. This is the anti-cheat core, so the claim is a
// single round-trip with no read-then-write race.
type RedisLock struct {
	rdb *redis.Client
	// ttl bounds how long a claimed round survives if Release is somehow
	// missed (crash mid-round). A round is normally far shorter than this.
	ttl time.Duration
}

// compile-time check against the fixed contract.
var _ game.BuzzLock = (*RedisLock)(nil)

const (
	redisKeyPrefix = "buzz:"
	defaultLockTTL = 5 * time.Minute
)

// NewRedisLock connects to addr, pings to confirm liveness, and returns a
// ready lock. addr is a host:port (e.g. "localhost:6379").
func NewRedisLock(addr string) (*RedisLock, error) {
	// Fail fast on boot: a short dial timeout and no dial retries so an absent
	// Redis falls back to the in-memory lock in ~1s rather than ~5s.
	rdb := redis.NewClient(&redis.Options{
		Addr:        addr,
		DialTimeout: 1 * time.Second,
		MaxRetries:  -1,
		PoolSize:    8,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("store: redis ping %s: %w", addr, err)
	}
	return &RedisLock{rdb: rdb, ttl: defaultLockTTL}, nil
}

func (l *RedisLock) key(roundKey string) string { return redisKeyPrefix + roundKey }

// TryAcquire atomically claims roundKey for playerID. SET NX returns true only
// for the first caller; all subsequent callers on the same key get won=false
// until Release clears it (design_doc §3.4, §4 "Atomic buzz").
func (l *RedisLock) TryAcquire(ctx context.Context, roundKey, playerID string) (bool, error) {
	won, err := l.rdb.SetNX(ctx, l.key(roundKey), playerID, l.ttl).Result()
	if err != nil {
		return false, fmt.Errorf("store: buzz SETNX %s: %w", roundKey, err)
	}
	return won, nil
}

// Release deletes the key so a fresh round can claim it.
func (l *RedisLock) Release(ctx context.Context, roundKey string) error {
	if err := l.rdb.Del(ctx, l.key(roundKey)).Err(); err != nil {
		return fmt.Errorf("store: buzz release %s: %w", roundKey, err)
	}
	return nil
}

// Holder returns the winning playerID, or "" if the round is unclaimed.
func (l *RedisLock) Holder(ctx context.Context, roundKey string) (string, error) {
	v, err := l.rdb.Get(ctx, l.key(roundKey)).Result()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: buzz holder %s: %w", roundKey, err)
	}
	return v, nil
}

// Close releases the underlying connection pool.
func (l *RedisLock) Close() error { return l.rdb.Close() }
