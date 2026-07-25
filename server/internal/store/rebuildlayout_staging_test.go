package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/s45vprubg/yfitops/server/internal/admin"
)

// rebuildlayout_staging_test.go exercises the real PostgresRepo.RebuildLayout
// transaction against a live Postgres (the QA sweep could only code-review it).
// It runs ONLY when YFI_TEST_DSN is set; otherwise it self-skips so the normal
// suite (no infra) is unaffected. It operates on a throwaway board it creates
// and deletes — never touching real data.
func TestStaging_RebuildLayout_AtomicRollback(t *testing.T) {
	dsn := os.Getenv("YFI_TEST_DSN")
	if dsn == "" {
		t.Skip("YFI_TEST_DSN not set; skipping live-Postgres RebuildLayout test")
	}
	ctx := context.Background()
	repo, err := NewPostgresRepo(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	boardID := "brd_qatest_rebuild_" + itoa(time.Now().UnixNano())
	if err := repo.CreateBoard(ctx, boardID, "qa-rebuild-test"); err != nil {
		t.Fatalf("create board: %v", err)
	}
	t.Cleanup(func() {
		// Best-effort teardown of the throwaway board.
		_, _ = repo.pool.Exec(ctx, `DELETE FROM board_layout_cell_tracks WHERE board_id=$1`, boardID)
		_, _ = repo.pool.Exec(ctx, `DELETE FROM board_layout_cells WHERE board_id=$1`, boardID)
		_, _ = repo.pool.Exec(ctx, `DELETE FROM board_tracks WHERE board_id=$1`, boardID)
		_, _ = repo.pool.Exec(ctx, `DELETE FROM boards WHERE id=$1`, boardID)
	})

	// Seed two real tracks in the board library so a good placement can succeed.
	for _, tid := range []string{boardID + "_t1", boardID + "_t2"} {
		if err := repo.AddTrack(ctx, &admin.Track{
			ID: tid, BoardID: boardID, Artist: "A", Song: "S", SpotifyURI: "spotify:track:" + tid,
		}); err != nil {
			t.Fatalf("seed track %s: %v", tid, err)
		}
	}

	// 1) A GOOD rebuild commits and is observable.
	good := []admin.LayoutColumn{
		{Category: "Cat1", Placements: []admin.LayoutPlacement{{Row: 1, Col: 1, TrackID: boardID + "_t1", Pos: 0}}},
		{Category: "Cat2", Placements: []admin.LayoutPlacement{{Row: 1, Col: 2, TrackID: boardID + "_t2", Pos: 0}}},
	}
	if err := repo.RebuildLayout(ctx, boardID, 2, good); err != nil {
		t.Fatalf("good rebuild failed: %v", err)
	}
	cellsAfterGood := countRows(t, repo, `SELECT count(*) FROM board_layout_cells WHERE board_id=$1`, boardID)
	placAfterGood := countRows(t, repo, `SELECT count(*) FROM board_layout_cell_tracks WHERE board_id=$1`, boardID)
	if cellsAfterGood == 0 || placAfterGood != 2 {
		t.Fatalf("good rebuild not committed: cells=%d placements=%d (want cells>0, placements=2)", cellsAfterGood, placAfterGood)
	}

	// 2) A BAD rebuild (a placement referencing a nonexistent track -> FK
	// violation mid-transaction) must roll back ENTIRELY, leaving the prior good
	// layout intact rather than a half-wiped board.
	bad := []admin.LayoutColumn{
		{Category: "NewCat1", Placements: []admin.LayoutPlacement{{Row: 1, Col: 1, TrackID: boardID + "_t1", Pos: 0}}},
		{Category: "NewCat2", Placements: []admin.LayoutPlacement{{Row: 1, Col: 2, TrackID: "does_not_exist_track", Pos: 0}}},
	}
	if err := repo.RebuildLayout(ctx, boardID, 2, bad); err == nil {
		t.Fatalf("bad rebuild unexpectedly succeeded; expected FK violation")
	}

	// The board must be UNCHANGED from the good state (full rollback), NOT wiped.
	cellsAfterBad := countRows(t, repo, `SELECT count(*) FROM board_layout_cells WHERE board_id=$1`, boardID)
	placAfterBad := countRows(t, repo, `SELECT count(*) FROM board_layout_cell_tracks WHERE board_id=$1`, boardID)
	if cellsAfterBad != cellsAfterGood || placAfterBad != placAfterGood {
		t.Fatalf("ROLLBACK FAILED: after a failed rebuild the board changed (cells %d->%d, placements %d->%d); a mid-build error corrupted the layout",
			cellsAfterGood, cellsAfterBad, placAfterGood, placAfterBad)
	}
	// And the categories must still be the ORIGINAL ones, not the half-applied new ones.
	cat := scanText(t, repo, `SELECT category FROM board_layout_cells WHERE board_id=$1 AND row=1 AND col=1`, boardID)
	if cat != "Cat1" {
		t.Fatalf("ROLLBACK FAILED: category was mutated to %q (want original \"Cat1\")", cat)
	}
	t.Logf("RebuildLayout tx verified: good commit (cells=%d, placements=%d), bad rebuild rolled back cleanly", cellsAfterGood, placAfterGood)
}

func countRows(t *testing.T, repo *PostgresRepo, q, arg string) int {
	t.Helper()
	var n int
	if err := repo.pool.QueryRow(context.Background(), q, arg).Scan(&n); err != nil {
		t.Fatalf("count query: %v", err)
	}
	return n
}

func scanText(t *testing.T, repo *PostgresRepo, q, arg string) string {
	t.Helper()
	var s string
	if err := repo.pool.QueryRow(context.Background(), q, arg).Scan(&s); err != nil {
		t.Fatalf("text query: %v", err)
	}
	return s
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
