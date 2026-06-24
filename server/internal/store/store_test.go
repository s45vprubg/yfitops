package store

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/s45vprubg/yfitops/server/internal/game"
)

// TestMemLockSingleWinner is the anti-cheat core check (design_doc §3.4, §4):
// under concurrent contention exactly one buzz wins a round key.
func TestMemLockSingleWinner(t *testing.T) {
	cases := []struct {
		name       string
		players    int
		concurrent int // goroutines hammering the same key
	}{
		{"two players", 2, 2},
		{"small crowd", 10, 10},
		{"big crowd", 200, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lock := NewMemLock()
			ctx := context.Background()
			const roundKey = "session1:r3c4:track7"

			var (
				wins  int
				mu    sync.Mutex
				start = make(chan struct{})
				wg    sync.WaitGroup
			)
			for i := 0; i < tc.concurrent; i++ {
				pid := fmt.Sprintf("p%d", i)
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start // line everyone up so the race is real
					won, err := lock.TryAcquire(ctx, roundKey, pid)
					if err != nil {
						t.Errorf("TryAcquire: %v", err)
						return
					}
					if won {
						mu.Lock()
						wins++
						mu.Unlock()
					}
				}()
			}
			close(start)
			wg.Wait()

			if wins != 1 {
				t.Fatalf("expected exactly 1 winner, got %d", wins)
			}
			// Holder must be set and stable; releasing reopens the round.
			h, err := lock.Holder(ctx, roundKey)
			if err != nil {
				t.Fatalf("Holder: %v", err)
			}
			if h == "" {
				t.Fatal("Holder empty after a win")
			}
			if err := lock.Release(ctx, roundKey); err != nil {
				t.Fatalf("Release: %v", err)
			}
			if h2, _ := lock.Holder(ctx, roundKey); h2 != "" {
				t.Fatalf("Holder %q after Release, want empty", h2)
			}
			// After release a brand-new caller wins.
			won, err := lock.TryAcquire(ctx, roundKey, "late")
			if err != nil || !won {
				t.Fatalf("post-release acquire: won=%v err=%v", won, err)
			}
		})
	}
}

func TestMemLockDistinctKeysIndependent(t *testing.T) {
	lock := NewMemLock()
	ctx := context.Background()
	for _, k := range []string{"a", "b", "c"} {
		won, err := lock.TryAcquire(ctx, k, "p1")
		if err != nil || !won {
			t.Fatalf("key %s: won=%v err=%v", k, won, err)
		}
	}
}

func TestMemRepoSessionAndScores(t *testing.T) {
	repo := NewMemRepo()
	ctx := context.Background()

	if err := repo.CreateSession(ctx, &game.Session{ID: "s1", CreatedAt: 1, SkipThresholdPct: 70}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := repo.CreateSession(ctx, &game.Session{ID: "s1"}); err == nil {
		t.Fatal("duplicate session should error")
	}

	scores := []struct {
		pid, handle string
		score       int
	}{
		{"p1", "Alice", 300},
		{"p2", "Bob", 500},
		{"p3", "Cara", 150},
	}
	for _, s := range scores {
		if err := repo.SaveScore(ctx, "s1", s.pid, s.handle, s.score); err != nil {
			t.Fatalf("SaveScore: %v", err)
		}
	}
	// upsert: re-saving overwrites, not duplicates.
	if err := repo.SaveScore(ctx, "s1", "p1", "Alice", 999); err != nil {
		t.Fatalf("SaveScore upsert: %v", err)
	}

	lb, err := repo.Leaderboard(ctx, 2)
	if err != nil {
		t.Fatalf("Leaderboard: %v", err)
	}
	if len(lb) != 2 {
		t.Fatalf("limit 2: got %d entries", len(lb))
	}
	if lb[0].Score != 999 || lb[0].ID != "p1" {
		t.Fatalf("top entry = %+v, want p1/999", lb[0])
	}
	if lb[1].Score != 500 {
		t.Fatalf("second entry = %+v, want score 500", lb[1])
	}
}

func TestMemRepoLogEvent(t *testing.T) {
	repo := NewMemRepo()
	ctx := context.Background()
	if err := repo.LogEvent(ctx, "s1", "buzz", map[string]any{"playerID": "p1", "won": true}); err != nil {
		t.Fatalf("LogEvent: %v", err)
	}
	ev := repo.Events()
	if len(ev) != 1 || ev[0].Kind != "buzz" || ev[0].Detail["won"] != true {
		t.Fatalf("unexpected events: %+v", ev)
	}
}

func TestMemRepoSeedSampleBoard(t *testing.T) {
	repo := NewMemRepo()
	ctx := context.Background()
	if err := repo.CreateSession(ctx, &game.Session{ID: "s1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	seeded := repo.SeedSampleBoard("s1")

	if seeded.Rows != 5 || seeded.Cols != 5 {
		t.Fatalf("board dims = %dx%d, want 5x5", seeded.Rows, seeded.Cols)
	}

	b, err := repo.LoadBoard(ctx, "s1")
	if err != nil {
		t.Fatalf("LoadBoard: %v", err)
	}

	dailyDoubles := 0
	for r := 0; r < b.Rows; r++ {
		for c := 0; c < b.Cols; c++ {
			cell := b.Cells[r][c]
			if cell == nil {
				t.Fatalf("nil cell at %d,%d", r, c)
			}
			n := len(cell.Tracks)
			if n < 4 || n > 6 {
				t.Fatalf("cell %d,%d has %d tracks, want 4-6", cell.Row, cell.Col, n)
			}
			if cell.Exhausted() {
				t.Fatalf("fresh cell %d,%d reports exhausted", cell.Row, cell.Col)
			}
			if cell.TracksLeft() != n {
				t.Fatalf("cell %d,%d TracksLeft=%d, want %d", cell.Row, cell.Col, cell.TracksLeft(), n)
			}
			if cell.DailyDouble {
				dailyDoubles++
			}
		}
	}
	if dailyDoubles != 1 {
		t.Fatalf("expected exactly 1 daily double, got %d", dailyDoubles)
	}

	// Exercise Exhausted once a cell's pool is fully played.
	cell := b.Cells[0][0]
	for _, tr := range cell.Tracks {
		tr.Played = true
	}
	if !cell.Exhausted() {
		t.Fatal("cell with all tracks played should be exhausted")
	}
}

func TestMemRepoLoadBoardMissing(t *testing.T) {
	repo := NewMemRepo()
	if _, err := repo.LoadBoard(context.Background(), "nope"); err == nil {
		t.Fatal("LoadBoard on unknown session should error")
	}
}
