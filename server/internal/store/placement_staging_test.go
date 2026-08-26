package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/s45vprubg/yfitops/server/internal/admin"
)

// placement_staging_test.go covers QA sweep finding store-3: a track must live in
// at most ONE cell per board. 0002_boards.sql claimed that invariant in a comment
// but never enforced it — the primary key is (board_id, row, col, track_id), so
// the same track in two different cells was two legal rows, and PlaceTrack's
// upsert (which targeted that PK) inserted a duplicate instead of moving the
// track. A duplicated track occupies two cells while UnplacedTracks still counts
// it as placed, so the board silently serves the same song twice.
//
// Migration 0006_unique_placement.sql adds uq_blct_board_track (board_id,
// track_id) and PlaceTrack now conflicts on it.
//
// Runs ONLY when YFI_TEST_DSN is set (same convention as the other staging
// tests); otherwise it self-skips so the infra-less suite is unaffected. It
// operates on a throwaway board it creates and deletes.
func TestStaging_PlaceTrack_MovesInsteadOfDuplicating(t *testing.T) {
	dsn := os.Getenv("YFI_TEST_DSN")
	if dsn == "" {
		t.Skip("YFI_TEST_DSN not set; skipping live-Postgres placement test")
	}
	ctx := context.Background()
	repo, err := NewPostgresRepo(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	boardID := "brd_qatest_place_" + itoa(time.Now().UnixNano())
	if err := repo.CreateBoard(ctx, boardID, "qa-placement-test"); err != nil {
		t.Fatalf("create board: %v", err)
	}
	t.Cleanup(func() {
		_, _ = repo.pool.Exec(ctx, `DELETE FROM board_layout_cell_tracks WHERE board_id=$1`, boardID)
		_, _ = repo.pool.Exec(ctx, `DELETE FROM board_layout_cells WHERE board_id=$1`, boardID)
		_, _ = repo.pool.Exec(ctx, `DELETE FROM board_tracks WHERE board_id=$1`, boardID)
		_, _ = repo.pool.Exec(ctx, `DELETE FROM boards WHERE id=$1`, boardID)
	})

	trackID := boardID + "_t1"
	if err := repo.AddTrack(ctx, &admin.Track{
		ID: trackID, BoardID: boardID, Artist: "A", Song: "S",
		SpotifyURI: "spotify:track:" + trackID,
	}); err != nil {
		t.Fatalf("seed track: %v", err)
	}
	// Two columns so there is a second cell to move into (AddColumn creates all
	// 5 rows of a column, which the composite FK requires).
	for col, cat := range map[int]string{1: "c1", 2: "c2"} {
		if err := repo.AddColumn(ctx, boardID, col, cat); err != nil {
			t.Fatalf("add column %d: %v", col, err)
		}
	}

	// Place at (1,1) and mark it played, as a real game would.
	if err := repo.PlaceTrack(ctx, boardID, 1, 1, trackID, 0); err != nil {
		t.Fatalf("initial place: %v", err)
	}
	if _, err := repo.pool.Exec(ctx,
		`UPDATE board_layout_cell_tracks SET played = TRUE WHERE board_id=$1 AND track_id=$2`,
		boardID, trackID); err != nil {
		t.Fatalf("mark played: %v", err)
	}

	// THE REGRESSION: place the same track into a DIFFERENT cell.
	if err := repo.PlaceTrack(ctx, boardID, 1, 2, trackID, 3); err != nil {
		t.Fatalf("cross-cell re-place failed: %v", err)
	}

	n := countPlacements(t, repo, boardID, trackID)
	if n != 1 {
		t.Fatalf("store-3 REGRESSION: track occupies %d cells after a cross-cell re-place (want 1); the board would serve the same song twice", n)
	}
	row, col, pos, played := placementOf(t, repo, boardID, trackID)
	if row != 1 || col != 2 || pos != 3 {
		t.Errorf("track did not move: at (row=%d,col=%d,pos=%d), want (1,2,3)", row, col, pos)
	}
	if !played {
		t.Error("played flag lost on move; a played track moved between cells could be served again")
	}

	// A same-cell re-place must still just update pos — the pre-existing
	// behaviour, which the new conflict target must not break.
	if err := repo.PlaceTrack(ctx, boardID, 1, 2, trackID, 7); err != nil {
		t.Fatalf("same-cell re-place failed: %v", err)
	}
	if n := countPlacements(t, repo, boardID, trackID); n != 1 {
		t.Fatalf("same-cell re-place produced %d rows (want 1)", n)
	}
	if _, _, pos, _ := placementOf(t, repo, boardID, trackID); pos != 7 {
		t.Errorf("same-cell re-place did not update pos: got %d, want 7", pos)
	}

	// And the constraint itself must reject a raw duplicate, so the guarantee
	// does not depend on every caller going through PlaceTrack.
	_, err = repo.pool.Exec(ctx,
		`INSERT INTO board_layout_cell_tracks (board_id, row, col, track_id, pos)
		 VALUES ($1, 2, 1, $2, 0)`, boardID, trackID)
	if err == nil {
		t.Fatal("raw duplicate insert succeeded: uq_blct_board_track is missing — run migration 0006")
	}
	t.Logf("store-3 verified: cross-cell re-place moves (1 row, played preserved); raw duplicate rejected by the unique index")
}

func countPlacements(t *testing.T, repo *PostgresRepo, boardID, trackID string) int {
	t.Helper()
	var n int
	if err := repo.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM board_layout_cell_tracks WHERE board_id=$1 AND track_id=$2`,
		boardID, trackID).Scan(&n); err != nil {
		t.Fatalf("count placements: %v", err)
	}
	return n
}

func placementOf(t *testing.T, repo *PostgresRepo, boardID, trackID string) (row, col, pos int, played bool) {
	t.Helper()
	if err := repo.pool.QueryRow(context.Background(),
		`SELECT row, col, pos, played FROM board_layout_cell_tracks
		  WHERE board_id=$1 AND track_id=$2`,
		boardID, trackID).Scan(&row, &col, &pos, &played); err != nil {
		t.Fatalf("read placement: %v", err)
	}
	return row, col, pos, played
}
