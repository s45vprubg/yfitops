package store

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// layout_empty_cell_staging_test.go covers a crash reported by the owner: the
// admin board builder went to a black screen with "TypeError: Cannot read
// properties of null (reading 'lyricsStatus')" inside Array.filter.
//
// Root cause: GetLayout's row scan only appends to cell.Tracks when the LEFT
// JOIN actually matches a placed track (tID != nil below) -- a cell with NO
// tracks placed left cell.Tracks at its Go zero value, nil. encoding/json
// marshals a nil slice as JSON `null`, not `[]`. The admin frontend's
// `cells.flatMap((c) => c.tracks)` does not spread a non-array return value,
// so that null landed as a literal element of the flattened track list,
// crashing every `.lyricsStatus` read downstream on ANY board with an empty
// cell -- which is the common case, not an edge case.
//
// Runs ONLY when YFI_TEST_DSN is set (same convention as the other staging
// tests); otherwise it self-skips so the infra-less suite is unaffected. It
// operates on a throwaway board it creates and deletes.
func TestStaging_GetLayout_EmptyCellTracksNotNull(t *testing.T) {
	dsn := os.Getenv("YFI_TEST_DSN")
	if dsn == "" {
		t.Skip("YFI_TEST_DSN not set; skipping live-Postgres layout test")
	}
	ctx := context.Background()
	repo, err := NewPostgresRepo(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	boardID := "brd_qatest_emptycell_" + itoa(time.Now().UnixNano())
	if err := repo.CreateBoard(ctx, boardID, "qa-empty-cell-test"); err != nil {
		t.Fatalf("create board: %v", err)
	}
	t.Cleanup(func() {
		_, _ = repo.pool.Exec(ctx, `DELETE FROM board_layout_cell_tracks WHERE board_id=$1`, boardID)
		_, _ = repo.pool.Exec(ctx, `DELETE FROM board_layout_cells WHERE board_id=$1`, boardID)
		_, _ = repo.pool.Exec(ctx, `DELETE FROM board_tracks WHERE board_id=$1`, boardID)
		_, _ = repo.pool.Exec(ctx, `DELETE FROM boards WHERE id=$1`, boardID)
	})

	// One column, no tracks placed anywhere -- every one of its cells has
	// zero tracks, exactly the state that triggered the crash.
	if err := repo.AddColumn(ctx, boardID, 1, "empty column"); err != nil {
		t.Fatalf("add column: %v", err)
	}

	layout, err := repo.GetLayout(ctx, boardID)
	if err != nil {
		t.Fatalf("get layout: %v", err)
	}
	if len(layout.Cells) == 0 {
		t.Fatalf("no cells returned for a freshly-added column")
	}
	for _, c := range layout.Cells {
		if c.Tracks == nil {
			t.Fatalf("cell (row=%d,col=%d).Tracks is nil, want non-nil empty slice -- marshals to JSON null, not []", c.Row, c.Col)
		}
	}

	// The actual regression gate: a nil slice marshals to `null`; only a
	// non-nil (even if empty) slice marshals to `[]`. This is what the
	// frontend's flatMap actually receives over the wire.
	raw, err := json.Marshal(layout.Cells[0])
	if err != nil {
		t.Fatalf("marshal cell: %v", err)
	}
	var decoded struct {
		Tracks json.RawMessage `json:"tracks"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal cell: %v", err)
	}
	if string(decoded.Tracks) != "[]" {
		t.Errorf(`cell JSON "tracks" field = %s, want "[]" (got the nil-slice null bug)`, decoded.Tracks)
	}
}
