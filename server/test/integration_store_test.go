// Integration tests against LIVE Redis and Postgres. These are gated behind
// env vars so the default `go test ./...` (no infra) skips them. The scripts/
// helper and the Makefile `integration` target set the vars after starting
// containers.
//
//	YFI_TEST_REDIS=localhost:16379
//	YFI_TEST_PG=postgres://yfitops:yfitops@localhost:15432/yfitops?sslmode=disable
//
// The Redis test is the important one: it proves the atomic single-winner buzz
// (design_doc §3.4, §4) holds against a REAL Redis SET NX under concurrency —
// the in-memory MemLock test can't catch a Lua/round-trip race that only a real
// server would exhibit.
package test

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/s45vprubg/yfitops/server/internal/game"
	"github.com/s45vprubg/yfitops/server/internal/store"
)

func TestIntegration_RedisLock_SingleWinner(t *testing.T) {
	addr := os.Getenv("YFI_TEST_REDIS")
	if addr == "" {
		t.Skip("set YFI_TEST_REDIS to run (e.g. localhost:16379)")
	}
	lock, err := store.NewRedisLock(addr)
	if err != nil {
		t.Fatalf("connect redis: %v", err)
	}
	ctx := context.Background()

	const racers = 200
	roundKey := "itest:round:1"
	_ = lock.Release(ctx, roundKey) // clean slate

	var wins int64
	var winner atomic.Value
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			pid := "p" + itoa(id)
			<-start // barrier so they all hammer SET NX together
			won, err := lock.TryAcquire(ctx, roundKey, pid)
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			if won {
				atomic.AddInt64(&wins, 1)
				winner.Store(pid)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if wins != 1 {
		t.Fatalf("expected exactly ONE winner, got %d (atomic buzz broken §3.4)", wins)
	}
	// The reported holder must match the single winner.
	holder, err := lock.Holder(ctx, roundKey)
	if err != nil {
		t.Fatalf("holder: %v", err)
	}
	if holder != winner.Load().(string) {
		t.Errorf("holder %q != winner %q", holder, winner.Load())
	}
	// After release a new round can be claimed.
	if err := lock.Release(ctx, roundKey); err != nil {
		t.Fatalf("release: %v", err)
	}
	won, _ := lock.TryAcquire(ctx, roundKey, "fresh")
	if !won {
		t.Error("fresh round should be claimable after release")
	}
	_ = lock.Release(ctx, roundKey)
}

func TestIntegration_PostgresRepo(t *testing.T) {
	dsn := os.Getenv("YFI_TEST_PG")
	if dsn == "" {
		t.Skip("set YFI_TEST_PG to run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	repo, err := store.NewPostgresRepo(ctx, dsn)
	if err != nil {
		t.Fatalf("connect pg: %v", err)
	}

	sessID := "itest-sess-" + itoa(int(time.Now().UnixNano()%1000000))
	if err := repo.CreateSession(ctx, &game.Session{ID: sessID, SkipThresholdPct: 50}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := repo.SaveScore(ctx, sessID, "fp-int-1", "neo", 175); err != nil {
		t.Fatalf("save score: %v", err)
	}
	// Upsert: saving again with a higher score should not duplicate the row.
	if err := repo.SaveScore(ctx, sessID, "fp-int-1", "neo", 250); err != nil {
		t.Fatalf("save score upsert: %v", err)
	}
	if err := repo.LogEvent(ctx, sessID, "buzz", map[string]any{"player": "neo", "ms": 1234}); err != nil {
		t.Fatalf("log event: %v", err)
	}
	board, err := repo.Leaderboard(ctx, 10)
	if err != nil {
		t.Fatalf("leaderboard: %v", err)
	}
	found := false
	for _, e := range board {
		if e.Handle == "neo" {
			found = true
			if e.Score != 250 {
				t.Errorf("leaderboard score = %d, want 250 (upsert took max/last)", e.Score)
			}
		}
	}
	if !found {
		t.Error("saved score not present in leaderboard")
	}
}
