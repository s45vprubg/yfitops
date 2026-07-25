package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/s45vprubg/yfitops/server/internal/admin"
	"github.com/s45vprubg/yfitops/server/internal/game"
)

// played_staging_test.go verifies the s2-store-002 fix against live Postgres:
// per-track played state must survive a board reload (a mid-game restart), and
// ClearPlayed must reset it on a new game. Gated on YFI_TEST_DSN; self-skips
// without it. Operates on a throwaway board+session, cleaned up after.
func TestStaging_PlayedStatePersistsAcrossReload(t *testing.T) {
	dsn := os.Getenv("YFI_TEST_DSN")
	if dsn == "" {
		t.Skip("YFI_TEST_DSN not set; skipping live-Postgres played-state test")
	}
	ctx := context.Background()
	repo, err := NewPostgresRepo(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	suffix := itoa(time.Now().UnixNano())
	boardID := "brd_qatest_played_" + suffix
	sessionID := "sess_qatest_played_" + suffix
	trackA := boardID + "_tA"
	trackB := boardID + "_tB"

	if err := repo.CreateBoard(ctx, boardID, "qa-played-test"); err != nil {
		t.Fatalf("create board: %v", err)
	}
	if err := repo.CreateSession(ctx, &game.Session{ID: sessionID, CreatedAt: time.Now().UnixMilli(), SkipThresholdPct: 50, State: "LOBBY"}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	t.Cleanup(func() {
		_, _ = repo.pool.Exec(ctx, `DELETE FROM board_layout_cell_tracks WHERE board_id=$1`, boardID)
		_, _ = repo.pool.Exec(ctx, `DELETE FROM board_layout_cells WHERE board_id=$1`, boardID)
		_, _ = repo.pool.Exec(ctx, `DELETE FROM board_tracks WHERE board_id=$1`, boardID)
		_, _ = repo.pool.Exec(ctx, `DELETE FROM game_sessions WHERE id=$1`, sessionID)
		_, _ = repo.pool.Exec(ctx, `DELETE FROM boards WHERE id=$1`, boardID)
	})

	for _, tid := range []string{trackA, trackB} {
		lyr := true
		if err := repo.AddTrack(ctx, &admin.Track{ID: tid, BoardID: boardID, Artist: "A", Song: "S", SpotifyURI: "spotify:track:" + tid, HasSyncedLyrics: &lyr}); err != nil {
			t.Fatalf("add track %s: %v", tid, err)
		}
	}
	// Two columns, one track each, so both are placeable and loadable.
	rebuild := []admin.LayoutColumn{
		{Category: "C1", Placements: []admin.LayoutPlacement{{Row: 1, Col: 1, TrackID: trackA, Pos: 0}}},
		{Category: "C2", Placements: []admin.LayoutPlacement{{Row: 1, Col: 2, TrackID: trackB, Pos: 0}}},
	}
	if err := repo.RebuildLayout(ctx, boardID, 2, rebuild); err != nil {
		t.Fatalf("rebuild layout: %v", err)
	}
	if err := repo.AttachBoard(ctx, sessionID, boardID); err != nil {
		t.Fatalf("attach board: %v", err)
	}

	// Baseline: fresh load, both tracks unplayed.
	if pA, pB := playedOf(t, repo, boardID, trackA, trackB); pA || pB {
		t.Fatalf("baseline: expected both unplayed, got A=%v B=%v", pA, pB)
	}

	// Consume track A (as the engine does on grade), then reload — A must stay played.
	if err := repo.MarkTrackPlayed(ctx, sessionID, trackA); err != nil {
		t.Fatalf("mark played: %v", err)
	}
	pA, pB := playedOf(t, repo, boardID, trackA, trackB)
	if !pA {
		t.Fatalf("PLAYED NOT PERSISTED: track A reloaded as unplayed after MarkTrackPlayed — a mid-game restart would re-offer a consumed track")
	}
	if pB {
		t.Fatalf("track B wrongly marked played (MarkTrackPlayed hit the wrong row)")
	}

	// New game: ClearPlayed resets everything.
	if err := repo.ClearPlayed(ctx, sessionID); err != nil {
		t.Fatalf("clear played: %v", err)
	}
	if pA, pB := playedOf(t, repo, boardID, trackA, trackB); pA || pB {
		t.Fatalf("ClearPlayed did not reset: A=%v B=%v (want both false)", pA, pB)
	}
	t.Logf("played-state verified: MarkTrackPlayed persists across LoadBoardByID, ClearPlayed resets")
}

// playedOf reloads the board via LoadBoardByID and returns the Played flag for
// the two named tracks — exercising the real restore path the engine uses.
func playedOf(t *testing.T, repo *PostgresRepo, boardID, a, b string) (bool, bool) {
	t.Helper()
	board, err := repo.LoadBoardByID(context.Background(), boardID)
	if err != nil {
		t.Fatalf("load board: %v", err)
	}
	if board == nil {
		t.Fatalf("load board returned nil")
	}
	var pa, pb bool
	for _, row := range board.Cells {
		for _, cell := range row {
			if cell == nil {
				continue
			}
			for _, tr := range cell.Tracks {
				if tr.ID == a {
					pa = tr.Played
				}
				if tr.ID == b {
					pb = tr.Played
				}
			}
		}
	}
	return pa, pb
}
