package store

import (
	"context"
	"fmt"
	"time"

	"github.com/s45vprubg/yfitops/server/internal/admin"
	"github.com/s45vprubg/yfitops/server/internal/game"
)

// Compile-time proof that *PostgresRepo satisfies admin.AdminStore.
var _ admin.AdminStore = (*PostgresRepo)(nil)

func (r *PostgresRepo) CreateBoard(ctx context.Context, id, name string) error {
	now := time.Now().UnixMilli()
	_, err := r.pool.Exec(ctx,
		`INSERT INTO boards (id, name, cols, created_at, updated_at)
		 VALUES ($1, $2, 1, $3, $4)`,
		id, name, now, now)
	if err != nil {
		return fmt.Errorf("store: create board: %w", err)
	}
	return nil
}

func (r *PostgresRepo) ListBoards(ctx context.Context) ([]admin.Board, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, name, cols, created_at, updated_at FROM boards ORDER BY updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list boards: %w", err)
	}
	defer rows.Close()

	var boards []admin.Board
	for rows.Next() {
		var b admin.Board
		if err := rows.Scan(&b.ID, &b.Name, &b.Cols, &b.CreatedAt, &b.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: scan board: %w", err)
		}
		boards = append(boards, b)
	}
	return boards, rows.Err()
}

func (r *PostgresRepo) GetBoard(ctx context.Context, id string) (*admin.Board, error) {
	var b admin.Board
	err := r.pool.QueryRow(ctx,
		`SELECT id, name, cols, created_at, updated_at FROM boards WHERE id = $1`, id).
		Scan(&b.ID, &b.Name, &b.Cols, &b.CreatedAt, &b.UpdatedAt)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return nil, nil
		}
		return nil, fmt.Errorf("store: get board %s: %w", id, err)
	}
	return &b, nil
}

func (r *PostgresRepo) RenameBoard(ctx context.Context, id, name string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE boards SET name = $1, updated_at = $2 WHERE id = $3`,
		name, time.Now().UnixMilli(), id)
	if err != nil {
		return fmt.Errorf("store: rename board: %w", err)
	}
	return nil
}

func (r *PostgresRepo) DeleteBoard(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM boards WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("store: delete board: %w", err)
	}
	return nil
}

func (r *PostgresRepo) UpdateBoardCols(ctx context.Context, id string, cols int) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE boards SET cols = $1, updated_at = $2 WHERE id = $3`,
		cols, time.Now().UnixMilli(), id)
	if err != nil {
		return fmt.Errorf("store: update board cols: %w", err)
	}
	return nil
}

func (r *PostgresRepo) AddTrack(ctx context.Context, t *admin.Track) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO board_tracks (id, board_id, spotify_uri, artist, song, album_art, duration_ms, created_at, has_synced_lyrics, lyrics_override)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 ON CONFLICT (board_id, spotify_uri) DO NOTHING`,
		t.ID, t.BoardID, t.SpotifyURI, t.Artist, t.Song, t.AlbumArt, t.DurationMs, t.CreatedAt, t.HasSyncedLyrics, t.LyricsOverride)
	if err != nil {
		return fmt.Errorf("store: add track: %w", err)
	}
	return nil
}

// SetTrackLyrics updates the probe result / override for a track. Either arg may
// be nil to leave that column unchanged.
func (r *PostgresRepo) SetTrackLyrics(ctx context.Context, trackID string, hasSynced *bool, override *bool) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE board_tracks
		    SET has_synced_lyrics = COALESCE($2, has_synced_lyrics),
		        lyrics_override   = COALESCE($3, lyrics_override)
		  WHERE id = $1`,
		trackID, hasSynced, override)
	if err != nil {
		return fmt.Errorf("store: set track lyrics: %w", err)
	}
	return nil
}

func (r *PostgresRepo) ListTracks(ctx context.Context, boardID string) ([]admin.Track, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, board_id, spotify_uri, artist, song, album_art, duration_ms, created_at, has_synced_lyrics, lyrics_override
		   FROM board_tracks WHERE board_id = $1 ORDER BY created_at DESC`, boardID)
	if err != nil {
		return nil, fmt.Errorf("store: list tracks: %w", err)
	}
	defer rows.Close()

	var tracks []admin.Track
	for rows.Next() {
		var t admin.Track
		if err := rows.Scan(&t.ID, &t.BoardID, &t.SpotifyURI, &t.Artist, &t.Song, &t.AlbumArt, &t.DurationMs, &t.CreatedAt, &t.HasSyncedLyrics, &t.LyricsOverride); err != nil {
			return nil, fmt.Errorf("store: scan track: %w", err)
		}
		tracks = append(tracks, t)
	}
	return tracks, rows.Err()
}

func (r *PostgresRepo) UnplacedTracks(ctx context.Context, boardID string) ([]admin.Track, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT bt.id, bt.board_id, bt.spotify_uri, bt.artist, bt.song, bt.album_art, bt.duration_ms, bt.created_at, bt.has_synced_lyrics, bt.lyrics_override
		   FROM board_tracks bt
		  WHERE bt.board_id = $1
		    AND bt.id NOT IN (
		        SELECT blct.track_id FROM board_layout_cell_tracks blct WHERE blct.board_id = $1
		    )
		  ORDER BY bt.created_at DESC`, boardID)
	if err != nil {
		return nil, fmt.Errorf("store: unplaced tracks: %w", err)
	}
	defer rows.Close()

	var tracks []admin.Track
	for rows.Next() {
		var t admin.Track
		if err := rows.Scan(&t.ID, &t.BoardID, &t.SpotifyURI, &t.Artist, &t.Song, &t.AlbumArt, &t.DurationMs, &t.CreatedAt, &t.HasSyncedLyrics, &t.LyricsOverride); err != nil {
			return nil, fmt.Errorf("store: scan unplaced track: %w", err)
		}
		tracks = append(tracks, t)
	}
	return tracks, rows.Err()
}

func (r *PostgresRepo) DeleteTrack(ctx context.Context, trackID string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM board_tracks WHERE id = $1`, trackID)
	if err != nil {
		return fmt.Errorf("store: delete track: %w", err)
	}
	return nil
}

func (r *PostgresRepo) AddColumn(ctx context.Context, boardID string, col int, category string) error {
	for row := 1; row <= 5; row++ {
		_, err := r.pool.Exec(ctx,
			`INSERT INTO board_layout_cells (board_id, row, col, category, daily_double)
			 VALUES ($1, $2, $3, $4, FALSE)
			 ON CONFLICT (board_id, row, col) DO NOTHING`,
			boardID, row, col, category)
		if err != nil {
			return fmt.Errorf("store: add column cell row=%d col=%d: %w", row, col, err)
		}
	}
	return nil
}

func (r *PostgresRepo) RemoveColumn(ctx context.Context, boardID string, col int) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM board_layout_cells WHERE board_id = $1 AND col = $2`,
		boardID, col)
	if err != nil {
		return fmt.Errorf("store: remove column: %w", err)
	}
	return nil
}

func (r *PostgresRepo) RenameCategory(ctx context.Context, boardID string, col int, name string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE board_layout_cells SET category = $1 WHERE board_id = $2 AND col = $3`,
		name, boardID, col)
	if err != nil {
		return fmt.Errorf("store: rename category: %w", err)
	}
	return nil
}

func (r *PostgresRepo) PlaceTrack(ctx context.Context, boardID string, row, col int, trackID string, pos int) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO board_layout_cell_tracks (board_id, row, col, track_id, pos)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (board_id, row, col, track_id) DO UPDATE SET pos = EXCLUDED.pos`,
		boardID, row, col, trackID, pos)
	if err != nil {
		return fmt.Errorf("store: place track: %w", err)
	}
	return nil
}

func (r *PostgresRepo) UnplaceTrack(ctx context.Context, boardID string, row, col int, trackID string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM board_layout_cell_tracks
		  WHERE board_id = $1 AND row = $2 AND col = $3 AND track_id = $4`,
		boardID, row, col, trackID)
	if err != nil {
		return fmt.Errorf("store: unplace track: %w", err)
	}
	return nil
}

func (r *PostgresRepo) GetLayout(ctx context.Context, boardID string) (*admin.Layout, error) {
	board, err := r.GetBoard(ctx, boardID)
	if err != nil || board == nil {
		return nil, err
	}

	rows, err := r.pool.Query(ctx,
		`SELECT blc.row, blc.col, blc.category,
		        bt.id, bt.board_id, bt.spotify_uri, bt.artist, bt.song, bt.album_art, bt.duration_ms, bt.created_at,
		        bt.has_synced_lyrics, bt.lyrics_override,
		        blct.pos
		   FROM board_layout_cells blc
		   LEFT JOIN board_layout_cell_tracks blct
		     ON blct.board_id = blc.board_id AND blct.row = blc.row AND blct.col = blc.col
		   LEFT JOIN board_tracks bt ON bt.id = blct.track_id
		  WHERE blc.board_id = $1
		  ORDER BY blc.col, blc.row, blct.pos`, boardID)
	if err != nil {
		return nil, fmt.Errorf("store: get layout: %w", err)
	}
	defer rows.Close()

	cells := map[cellCoord]*admin.LayoutCell{}
	for rows.Next() {
		var (
			row, col int
			category string
			tID, tBoardID, tURI, tArtist, tSong, tArt *string
			tDuration, tCreated                        *int64
			tHasLyrics, tOverride                      *bool
			pos                                        *int
		)
		if err := rows.Scan(&row, &col, &category,
			&tID, &tBoardID, &tURI, &tArtist, &tSong, &tArt, &tDuration, &tCreated,
			&tHasLyrics, &tOverride,
			&pos); err != nil {
			return nil, fmt.Errorf("store: scan layout row: %w", err)
		}

		key := cellCoord{row, col}
		cell := cells[key]
		if cell == nil {
			cell = &admin.LayoutCell{Row: row, Col: col, Category: category}
			cells[key] = cell
		}
		if tID != nil {
			cell.Tracks = append(cell.Tracks, admin.Track{
				ID:              deref(tID),
				BoardID:         deref(tBoardID),
				SpotifyURI:      deref(tURI),
				Artist:          deref(tArtist),
				Song:            deref(tSong),
				AlbumArt:        deref(tArt),
				DurationMs:      derefInt64(tDuration),
				CreatedAt:       derefInt64(tCreated),
				HasSyncedLyrics: tHasLyrics,
				LyricsOverride:  derefBool(tOverride),
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate layout rows: %w", err)
	}

	var cellList []admin.LayoutCell
	for _, c := range cells {
		cellList = append(cellList, *c)
	}

	return &admin.Layout{Cols: board.Cols, Cells: cellList}, nil
}

func (r *PostgresRepo) LoadBoardByID(ctx context.Context, boardID string) (*game.Board, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT blc.row, blc.col, blc.category, blc.daily_double,
		        bt.id, bt.spotify_uri, bt.artist, bt.song, bt.album_art, bt.duration_ms,
		        bt.has_synced_lyrics, bt.lyrics_override,
		        blct.pos
		   FROM board_layout_cells blc
		   LEFT JOIN board_layout_cell_tracks blct
		     ON blct.board_id = blc.board_id AND blct.row = blc.row AND blct.col = blc.col
		   LEFT JOIN board_tracks bt ON bt.id = blct.track_id
		  WHERE blc.board_id = $1
		  ORDER BY blc.row, blc.col, blct.pos`, boardID)
	if err != nil {
		return nil, fmt.Errorf("store: load board by id %s: %w", boardID, err)
	}
	defer rows.Close()

	cells := map[cellCoord]*game.Cell{}
	maxRow, maxCol := 0, 0

	for rows.Next() {
		var (
			row, col    int
			category    string
			dailyDouble bool
			tID, tURI, tArtist, tSong, tArt *string
			tDuration                       *int64
			tHasLyrics, tOverride           *bool
			pos                             *int
		)
		if err := rows.Scan(&row, &col, &category, &dailyDouble,
			&tID, &tURI, &tArtist, &tSong, &tArt, &tDuration, &tHasLyrics, &tOverride, &pos); err != nil {
			return nil, fmt.Errorf("store: scan board-by-id row: %w", err)
		}
		if row > maxRow {
			maxRow = row
		}
		if col > maxCol {
			maxCol = col
		}

		key := cellCoord{row, col}
		cell := cells[key]
		if cell == nil {
			cell = &game.Cell{Row: row, Col: col, Category: category, DailyDouble: dailyDouble}
			cells[key] = cell
		}
		if tID != nil {
			// Playable: has synced lyrics, or an admin override. A not-yet-probed
			// track (has_synced_lyrics IS NULL) is treated as playable so an
			// unchecked board still works — the builder nags to run a check.
			hasLyrics := tHasLyrics == nil || *tHasLyrics
			playable := hasLyrics || derefBool(tOverride)
			cell.Tracks = append(cell.Tracks, &game.Track{
				ID:         deref(tID),
				SpotifyURI: deref(tURI),
				Artist:     deref(tArtist),
				Song:       deref(tSong),
				AlbumArt:   deref(tArt),
				DurationMs: derefInt64(tDuration),
				Playable:   playable,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate board-by-id rows: %w", err)
	}
	if len(cells) == 0 {
		return nil, fmt.Errorf("store: no layout for board %s", boardID)
	}

	return newGridFromCells(cells, maxRow, maxCol), nil
}

func (r *PostgresRepo) AttachBoard(ctx context.Context, sessionID, boardID string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE game_sessions SET board_id = $1 WHERE id = $2`,
		boardID, sessionID)
	if err != nil {
		return fmt.Errorf("store: attach board: %w", err)
	}
	return nil
}
