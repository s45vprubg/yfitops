package store

import (
	"context"
	"os"
	"testing"
	"time"
)

// drop_point_staging_test.go verifies the global per-song drop-point cache
// (track_drop_point, migration 0009) against live Postgres — specifically
// the never-clobber-manual upsert rule, which lives in the SQL's ON
// CONFLICT ... WHERE clause and can't be exercised by the fake store used
// in the game package's unit tests. Gated on YFI_TEST_DSN; self-skips
// without it, mirroring lyrics_cache_staging_test.go.
func TestStaging_TrackDropPoint_NeverClobberManual(t *testing.T) {
	dsn := os.Getenv("YFI_TEST_DSN")
	if dsn == "" {
		t.Skip("YFI_TEST_DSN not set; skipping live-Postgres track_drop_point test")
	}
	ctx := context.Background()
	repo, err := NewPostgresRepo(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	uri := "spotify:track:qatest_droppoint_" + itoa(time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = repo.pool.Exec(ctx, `DELETE FROM track_drop_point WHERE spotify_uri=$1`, uri)
	})

	// Never attempted: a miss.
	if _, ok, err := repo.GetDropPoint(ctx, uri); err != nil || ok {
		t.Fatalf("GetDropPoint (unattempted) = ok=%v err=%v, want ok=false err=nil", ok, err)
	}

	// First write of either kind is always allowed — an automated guess
	// seeds the row.
	if err := repo.SetDropPoint(ctx, uri, 10000, "youtube_heatmap"); err != nil {
		t.Fatalf("SetDropPoint (first, auto): %v", err)
	}
	if ms, ok, err := repo.GetDropPoint(ctx, uri); err != nil || !ok || ms != 10000 {
		t.Fatalf("GetDropPoint after first auto write = %d, %v, %v; want 10000, true, nil", ms, ok, err)
	}

	// auto -> auto: a later automated job may freely overwrite a prior
	// automated guess.
	if err := repo.SetDropPoint(ctx, uri, 11000, "lyrics_chorus"); err != nil {
		t.Fatalf("SetDropPoint (auto over auto): %v", err)
	}
	if ms, _, _ := repo.GetDropPoint(ctx, uri); ms != 11000 {
		t.Fatalf("GetDropPoint after auto-over-auto = %d, want 11000", ms)
	}

	// manual -> wins over the existing auto row.
	if err := repo.SetDropPoint(ctx, uri, 20000, "manual"); err != nil {
		t.Fatalf("SetDropPoint (manual over auto): %v", err)
	}
	if ms, _, _ := repo.GetDropPoint(ctx, uri); ms != 20000 {
		t.Fatalf("GetDropPoint after manual-over-auto = %d, want 20000", ms)
	}

	// auto -> manual-exists: THE core rule. An automated write must be a
	// silent no-op against an existing manual row — the human's mark must
	// survive untouched.
	if err := repo.SetDropPoint(ctx, uri, 99999, "youtube_heatmap"); err != nil {
		t.Fatalf("SetDropPoint (auto attempting to clobber manual): %v", err)
	}
	if ms, _, _ := repo.GetDropPoint(ctx, uri); ms != 20000 {
		t.Fatalf("GetDropPoint after auto-vs-manual = %d, want 20000 (manual must survive, not 99999)", ms)
	}

	// manual -> manual: the admin adjusting their own mark always wins.
	if err := repo.SetDropPoint(ctx, uri, 25000, "manual"); err != nil {
		t.Fatalf("SetDropPoint (manual over manual): %v", err)
	}
	if ms, _, _ := repo.GetDropPoint(ctx, uri); ms != 25000 {
		t.Fatalf("GetDropPoint after manual-over-manual = %d, want 25000", ms)
	}
}
