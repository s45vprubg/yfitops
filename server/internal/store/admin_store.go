package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

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
		`SELECT id, name, cols, created_at, updated_at, daily_double_count FROM boards ORDER BY updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list boards: %w", err)
	}
	defer rows.Close()

	var boards []admin.Board
	for rows.Next() {
		var b admin.Board
		if err := rows.Scan(&b.ID, &b.Name, &b.Cols, &b.CreatedAt, &b.UpdatedAt, &b.DailyDoubleCount); err != nil {
			return nil, fmt.Errorf("store: scan board: %w", err)
		}
		boards = append(boards, b)
	}
	return boards, rows.Err()
}

func (r *PostgresRepo) GetBoard(ctx context.Context, id string) (*admin.Board, error) {
	var b admin.Board
	err := r.pool.QueryRow(ctx,
		`SELECT id, name, cols, created_at, updated_at, daily_double_count FROM boards WHERE id = $1`, id).
		Scan(&b.ID, &b.Name, &b.Cols, &b.CreatedAt, &b.UpdatedAt, &b.DailyDoubleCount)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
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

// SetDailyDoubleCount sets how many Daily Double cells to randomly assign each
// Start Game (§7 sidenote). Cell assignment itself is never persisted.
func (r *PostgresRepo) SetDailyDoubleCount(ctx context.Context, id string, count int) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE boards SET daily_double_count = $1, updated_at = $2 WHERE id = $3`,
		count, time.Now().UnixMilli(), id)
	if err != nil {
		return fmt.Errorf("store: set daily double count: %w", err)
	}
	return nil
}

// AddDailyDoubleTrack adds a song to a board's standalone Daily Double bucket.
//
// The INSERT is ON CONFLICT (board_id, spotify_uri) DO NOTHING, which returns
// err == nil even when the row already existed and nothing was written
// (dd-api-1). CommandTag.RowsAffected() is the only way pgx exposes whether
// the INSERT actually happened, so a 0-rows result is turned into
// admin.ErrDailyDoubleTrackExists — the handler must not report success (and
// must not echo back the caller's unsaved payload) when this fires.
func (r *PostgresRepo) AddDailyDoubleTrack(ctx context.Context, t *admin.DailyDoubleTrack) error {
	tag, err := r.pool.Exec(ctx,
		`INSERT INTO board_dd_bucket_tracks (id, board_id, spotify_uri, artist, song, album_art, duration_ms, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT (board_id, spotify_uri) DO NOTHING`,
		t.ID, t.BoardID, t.SpotifyURI, t.Artist, t.Song, t.AlbumArt, t.DurationMs, t.CreatedAt)
	if err != nil {
		return fmt.Errorf("store: add daily double track: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return admin.ErrDailyDoubleTrackExists
	}
	return nil
}

func (r *PostgresRepo) ListDailyDoubleTracks(ctx context.Context, boardID string) ([]admin.DailyDoubleTrack, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT ddt.id, ddt.board_id, ddt.spotify_uri, ddt.artist, ddt.song, ddt.album_art, ddt.duration_ms, ddt.created_at, tl.status
		   FROM board_dd_bucket_tracks ddt
		   LEFT JOIN track_lyrics tl ON tl.spotify_uri = ddt.spotify_uri
		  WHERE ddt.board_id = $1 ORDER BY ddt.created_at ASC`, boardID)
	if err != nil {
		return nil, fmt.Errorf("store: list daily double tracks: %w", err)
	}
	defer rows.Close()

	var tracks []admin.DailyDoubleTrack
	for rows.Next() {
		var t admin.DailyDoubleTrack
		var status *string
		if err := rows.Scan(&t.ID, &t.BoardID, &t.SpotifyURI, &t.Artist, &t.Song, &t.AlbumArt, &t.DurationMs, &t.CreatedAt, &status); err != nil {
			return nil, fmt.Errorf("store: scan daily double track: %w", err)
		}
		t.LyricsStatus = deref(status)
		tracks = append(tracks, t)
	}
	return tracks, rows.Err()
}

// RemoveDailyDoubleTrack deletes a track scoped to boardID, mirroring the
// other board-scoped store methods (e.g. UnplaceTrack). Previously the DELETE
// filtered only by id, so a trackID could be deleted through ANY board's URL
// regardless of which board it actually belonged to (dd-api-2). Zero rows
// affected (wrong board, or the id never existed) now surfaces as
// admin.ErrDailyDoubleTrackNotFound so the handler can answer 404 instead of
// a false 204.
func (r *PostgresRepo) RemoveDailyDoubleTrack(ctx context.Context, boardID, trackID string) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM board_dd_bucket_tracks WHERE id = $1 AND board_id = $2`, trackID, boardID)
	if err != nil {
		return fmt.Errorf("store: remove daily double track: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return admin.ErrDailyDoubleTrackNotFound
	}
	return nil
}

func (r *PostgresRepo) AddTrack(ctx context.Context, t *admin.Track) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO board_tracks (id, board_id, spotify_uri, artist, song, album_art, duration_ms, created_at, lyrics_override, year, genre)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		 ON CONFLICT (board_id, spotify_uri) DO NOTHING`,
		t.ID, t.BoardID, t.SpotifyURI, t.Artist, t.Song, t.AlbumArt, t.DurationMs, t.CreatedAt, t.LyricsOverride, t.Year, t.Genre)
	if err != nil {
		return fmt.Errorf("store: add track: %w", err)
	}
	return nil
}

// GetTrack looks up a single board_tracks row by id, joined with the global
// lyrics cache so callers (requery/manual-lyrics endpoints) get a fully
// populated Track without a second query.
func (r *PostgresRepo) GetTrack(ctx context.Context, trackID string) (*admin.Track, error) {
	var t admin.Track
	var status *string
	err := r.pool.QueryRow(ctx,
		`SELECT bt.id, bt.board_id, bt.spotify_uri, bt.artist, bt.song, bt.album_art, bt.duration_ms, bt.created_at,
		        bt.lyrics_override, bt.year, bt.genre, tl.status
		   FROM board_tracks bt
		   LEFT JOIN track_lyrics tl ON tl.spotify_uri = bt.spotify_uri
		  WHERE bt.id = $1`, trackID).
		Scan(&t.ID, &t.BoardID, &t.SpotifyURI, &t.Artist, &t.Song, &t.AlbumArt, &t.DurationMs, &t.CreatedAt,
			&t.LyricsOverride, &t.Year, &t.Genre, &status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("store: get track %s: %w", trackID, err)
	}
	t.LyricsStatus = deref(status)
	return &t, nil
}

// SetTrackOverride sets the admin play-anyway override for a track.
func (r *PostgresRepo) SetTrackOverride(ctx context.Context, trackID string, override bool) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE board_tracks SET lyrics_override = $2 WHERE id = $1`,
		trackID, override)
	if err != nil {
		return fmt.Errorf("store: set track override: %w", err)
	}
	return nil
}

func (r *PostgresRepo) ListTracks(ctx context.Context, boardID string) ([]admin.Track, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT bt.id, bt.board_id, bt.spotify_uri, bt.artist, bt.song, bt.album_art, bt.duration_ms, bt.created_at,
		        bt.lyrics_override, bt.year, bt.genre, tl.status
		   FROM board_tracks bt
		   LEFT JOIN track_lyrics tl ON tl.spotify_uri = bt.spotify_uri
		  WHERE bt.board_id = $1 ORDER BY bt.created_at ASC`, boardID)
	if err != nil {
		return nil, fmt.Errorf("store: list tracks: %w", err)
	}
	defer rows.Close()

	var tracks []admin.Track
	for rows.Next() {
		var t admin.Track
		var status *string
		if err := rows.Scan(&t.ID, &t.BoardID, &t.SpotifyURI, &t.Artist, &t.Song, &t.AlbumArt, &t.DurationMs, &t.CreatedAt, &t.LyricsOverride, &t.Year, &t.Genre, &status); err != nil {
			return nil, fmt.Errorf("store: scan track: %w", err)
		}
		t.LyricsStatus = deref(status)
		tracks = append(tracks, t)
	}
	return tracks, rows.Err()
}

func (r *PostgresRepo) UnplacedTracks(ctx context.Context, boardID string) ([]admin.Track, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT bt.id, bt.board_id, bt.spotify_uri, bt.artist, bt.song, bt.album_art, bt.duration_ms, bt.created_at,
		        bt.lyrics_override, bt.year, bt.genre, tl.status
		   FROM board_tracks bt
		   LEFT JOIN track_lyrics tl ON tl.spotify_uri = bt.spotify_uri
		  WHERE bt.board_id = $1
		    AND bt.id NOT IN (
		        SELECT blct.track_id FROM board_layout_cell_tracks blct WHERE blct.board_id = $1
		    )
		  ORDER BY bt.created_at ASC`, boardID)
	if err != nil {
		return nil, fmt.Errorf("store: unplaced tracks: %w", err)
	}
	defer rows.Close()

	var tracks []admin.Track
	for rows.Next() {
		var t admin.Track
		var status *string
		if err := rows.Scan(&t.ID, &t.BoardID, &t.SpotifyURI, &t.Artist, &t.Song, &t.AlbumArt, &t.DurationMs, &t.CreatedAt, &t.LyricsOverride, &t.Year, &t.Genre, &status); err != nil {
			return nil, fmt.Errorf("store: scan unplaced track: %w", err)
		}
		t.LyricsStatus = deref(status)
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

// --- Global lyrics cache (track_lyrics), keyed by Spotify URI -------------
//
// Shared by board_tracks and board_dd_bucket_tracks via a join on
// spotify_uri (there is no FK — board_dd_bucket_tracks is a fully
// independent table — spotify_uri is the only identity the two share).

func (r *PostgresRepo) GetLyricsStatus(ctx context.Context, spotifyURI string) (*admin.LyricsRecord, error) {
	var rec admin.LyricsRecord
	var source, lastErr *string
	err := r.pool.QueryRow(ctx,
		`SELECT spotify_uri, status, source, last_error FROM track_lyrics WHERE spotify_uri = $1`, spotifyURI).
		Scan(&rec.SpotifyURI, &rec.Status, &source, &lastErr)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("store: get lyrics status: %w", err)
	}
	rec.Source = deref(source)
	rec.LastError = deref(lastErr)
	return &rec, nil
}

// GetLyricsStatusesForURIs batch-fetches lyrics cache rows for many URIs at
// once (used by rescanLyrics/probeLyricsBatch to find never-attempted songs
// without an N+1 query per track). Only URIs with an existing row are
// present in the returned map — a missing key means never attempted.
func (r *PostgresRepo) GetLyricsStatusesForURIs(ctx context.Context, uris []string) (map[string]admin.LyricsRecord, error) {
	out := map[string]admin.LyricsRecord{}
	if len(uris) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx,
		`SELECT spotify_uri, status, source, last_error FROM track_lyrics WHERE spotify_uri = ANY($1)`, uris)
	if err != nil {
		return nil, fmt.Errorf("store: get lyrics statuses: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var rec admin.LyricsRecord
		var source, lastErr *string
		if err := rows.Scan(&rec.SpotifyURI, &rec.Status, &source, &lastErr); err != nil {
			return nil, fmt.Errorf("store: scan lyrics status: %w", err)
		}
		rec.Source = deref(source)
		rec.LastError = deref(lastErr)
		out[rec.SpotifyURI] = rec
	}
	return out, rows.Err()
}

func (r *PostgresRepo) UpsertLyricsResolved(ctx context.Context, spotifyURI, lrcText, source string) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO track_lyrics (spotify_uri, lrc_text, status, source, last_error, checked_at, updated_at)
		 VALUES ($1, $2, 'resolved', $3, NULL, now(), now())
		 ON CONFLICT (spotify_uri) DO UPDATE
		   SET lrc_text = EXCLUDED.lrc_text, status = 'resolved', source = EXCLUDED.source,
		       last_error = NULL, checked_at = now(), updated_at = now()`,
		spotifyURI, lrcText, source)
	if err != nil {
		return fmt.Errorf("store: upsert lyrics resolved: %w", err)
	}
	return nil
}

func (r *PostgresRepo) UpsertLyricsNotFound(ctx context.Context, spotifyURI string) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO track_lyrics (spotify_uri, status, last_error, checked_at, updated_at)
		 VALUES ($1, 'not_found', NULL, now(), now())
		 ON CONFLICT (spotify_uri) DO UPDATE
		   SET status = 'not_found', last_error = NULL, checked_at = now(), updated_at = now()`,
		spotifyURI)
	if err != nil {
		return fmt.Errorf("store: upsert lyrics not found: %w", err)
	}
	return nil
}

// UpsertLyricsError records a transient probe failure. Status stays/becomes
// 'pending' — never 'not_found' — so a timeout/429/5xx can never be mistaken
// for a confirmed absence, and never overwrites an existing 'resolved' row:
// a song that's already known-good must not be knocked back to pending by a
// later transient error on an unrelated requery of the same song.
func (r *PostgresRepo) UpsertLyricsError(ctx context.Context, spotifyURI, errMsg string) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO track_lyrics (spotify_uri, status, last_error, checked_at, updated_at)
		 VALUES ($1, 'pending', $2, now(), now())
		 ON CONFLICT (spotify_uri) DO UPDATE
		   SET status = CASE WHEN track_lyrics.status = 'resolved' THEN track_lyrics.status ELSE 'pending' END,
		       last_error = $2, checked_at = now(), updated_at = now()`,
		spotifyURI, errMsg)
	if err != nil {
		return fmt.Errorf("store: upsert lyrics error: %w", err)
	}
	return nil
}

// GetResolvedLyrics implements game.LyricsTextStore for the engine's
// play-time path: a DB hit here means the song was already resolved during
// board building (fetched or manually entered), so the live round never
// needs to call LRCLIB at all.
func (r *PostgresRepo) GetResolvedLyrics(ctx context.Context, spotifyURI string) (string, bool, error) {
	var lrcText *string
	var status string
	err := r.pool.QueryRow(ctx,
		`SELECT lrc_text, status FROM track_lyrics WHERE spotify_uri = $1`, spotifyURI).
		Scan(&lrcText, &status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("store: get resolved lyrics: %w", err)
	}
	if status != "resolved" || lrcText == nil {
		return "", false, nil
	}
	return *lrcText, true, nil
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

// RebuildLayout atomically replaces the board's layout in a single transaction:
// it deletes all existing layout cells + placements, updates the column count,
// then recreates each column's cells and track placements. Mirrors the SQL of
// RemoveColumn / UpdateBoardCols / AddColumn / PlaceTrack, run against the tx.
//
// Verified against a live Postgres by TestStaging_RebuildLayout_AtomicRollback
// (YFI_TEST_DSN-gated): a good rebuild commits cells + placements, and a
// rebuild that fails partway rolls back with no partial layout left behind.
func (r *PostgresRepo) RebuildLayout(ctx context.Context, boardID string, cols int, columns []admin.LayoutColumn) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: rebuild layout: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	// Clear the existing layout (cells + their track placements). Mirror
	// RemoveColumn but for the whole board.
	if _, err := tx.Exec(ctx,
		`DELETE FROM board_layout_cell_tracks WHERE board_id = $1`, boardID); err != nil {
		return fmt.Errorf("store: rebuild layout: clear placements: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM board_layout_cells WHERE board_id = $1`, boardID); err != nil {
		return fmt.Errorf("store: rebuild layout: clear cells: %w", err)
	}

	// Update the board column count. Mirror UpdateBoardCols.
	if _, err := tx.Exec(ctx,
		`UPDATE boards SET cols = $1, updated_at = $2 WHERE id = $3`,
		cols, time.Now().UnixMilli(), boardID); err != nil {
		return fmt.Errorf("store: rebuild layout: update cols: %w", err)
	}

	// Recreate each column's cells (mirror AddColumn) then place tracks
	// (mirror PlaceTrack).
	for i, c := range columns {
		col := i + 1
		for row := 1; row <= 5; row++ {
			if _, err := tx.Exec(ctx,
				`INSERT INTO board_layout_cells (board_id, row, col, category, daily_double)
				 VALUES ($1, $2, $3, $4, FALSE)
				 ON CONFLICT (board_id, row, col) DO NOTHING`,
				boardID, row, col, c.Category); err != nil {
				return fmt.Errorf("store: rebuild layout: add cell row=%d col=%d: %w", row, col, err)
			}
		}
		// Same conflict target as PlaceTrack (uq_blct_board_track, migration
		// 0006) rather than the PK: with the unique index in place, a payload
		// listing one track in two cells would otherwise raise a unique
		// violation and roll the whole rebuild back. Conflicting on
		// (board_id, track_id) degrades that to last-placement-wins, keeping
		// both write paths under one rule. Callers should still dedup — the
		// only current caller (admin.aiBuild) does, via its `placed` map.
		for _, p := range c.Placements {
			if _, err := tx.Exec(ctx,
				`INSERT INTO board_layout_cell_tracks (board_id, row, col, track_id, pos)
				 VALUES ($1, $2, $3, $4, $5)
				 ON CONFLICT (board_id, track_id) DO UPDATE
				   SET row = EXCLUDED.row, col = EXCLUDED.col, pos = EXCLUDED.pos`,
				boardID, p.Row, p.Col, p.TrackID, p.Pos); err != nil {
				return fmt.Errorf("store: rebuild layout: place track row=%d col=%d: %w", p.Row, p.Col, err)
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: rebuild layout: commit: %w", err)
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

// PlaceTrack places a track in a cell, MOVING it if it is already placed
// elsewhere on the same board.
//
// The conflict target is the uq_blct_board_track unique index from migration
// 0006, not the (board_id, row, col, track_id) primary key. Conflicting on the
// PK only deduped a re-place into the SAME cell; placing into a DIFFERENT cell
// inserted a second row, leaving one track in two cells (store-3). Conflicting
// on (board_id, track_id) makes that case a single-statement move instead.
//
// played is deliberately absent from the SET list so a track keeps its
// already-played state when it is moved between cells.
func (r *PostgresRepo) PlaceTrack(ctx context.Context, boardID string, row, col int, trackID string, pos int) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO board_layout_cell_tracks (board_id, row, col, track_id, pos)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (board_id, track_id) DO UPDATE
		   SET row = EXCLUDED.row, col = EXCLUDED.col, pos = EXCLUDED.pos`,
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
		        bt.lyrics_override, bt.year, bt.genre, tl.status,
		        blct.pos
		   FROM board_layout_cells blc
		   LEFT JOIN board_layout_cell_tracks blct
		     ON blct.board_id = blc.board_id AND blct.row = blc.row AND blct.col = blc.col
		   LEFT JOIN board_tracks bt ON bt.id = blct.track_id
		   LEFT JOIN track_lyrics tl ON tl.spotify_uri = bt.spotify_uri
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
			tOverride                                  *bool
			tYear                                       *int
			tGenre, tLyricsStatus                       *string
			pos                                         *int
		)
		if err := rows.Scan(&row, &col, &category,
			&tID, &tBoardID, &tURI, &tArtist, &tSong, &tArt, &tDuration, &tCreated,
			&tOverride, &tYear, &tGenre, &tLyricsStatus,
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
				ID:             deref(tID),
				BoardID:        deref(tBoardID),
				SpotifyURI:     deref(tURI),
				Artist:         deref(tArtist),
				Song:           deref(tSong),
				AlbumArt:       deref(tArt),
				DurationMs:     derefInt64(tDuration),
				CreatedAt:      derefInt64(tCreated),
				LyricsStatus:   deref(tLyricsStatus),
				LyricsOverride: derefBool(tOverride),
				Year:           derefIntPtr(tYear),
				Genre:          deref(tGenre),
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
		        bt.lyrics_override, bt.year, bt.genre, tl.status,
		        blct.pos, blct.played
		   FROM board_layout_cells blc
		   LEFT JOIN board_layout_cell_tracks blct
		     ON blct.board_id = blc.board_id AND blct.row = blc.row AND blct.col = blc.col
		   LEFT JOIN board_tracks bt ON bt.id = blct.track_id
		   LEFT JOIN track_lyrics tl ON tl.spotify_uri = bt.spotify_uri
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
			tOverride                       *bool
			tYear                           *int
			tGenre, tLyricsStatus           *string
			pos                             *int
			played                          *bool
		)
		if err := rows.Scan(&row, &col, &category, &dailyDouble,
			&tID, &tURI, &tArtist, &tSong, &tArt, &tDuration, &tOverride, &tYear, &tGenre, &tLyricsStatus, &pos, &played); err != nil {
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
			// Playable: resolved lyrics in the global cache, or an admin
			// override. A song that's never been attempted (tl row entirely
			// absent, tLyricsStatus nil — e.g. no lyrics prober configured,
			// demo mode) is treated as playable so an unchecked board still
			// works; a song that WAS checked and came back not_found/pending
			// is not playable unless overridden. This mirrors the pre-cache
			// semantics exactly (nil == *true, *false == not playable), just
			// sourced from the dedup'd global cache instead of a per-track
			// column that a rescan could silently clobber.
			hasLyrics := tLyricsStatus == nil || *tLyricsStatus == admin.LyricsStatusResolved
			playable := hasLyrics || derefBool(tOverride)
			year := 0
			if tYear != nil {
				year = *tYear
			}
			cell.Tracks = append(cell.Tracks, &game.Track{
				ID:         deref(tID),
				SpotifyURI: deref(tURI),
				Artist:     deref(tArtist),
				Song:       deref(tSong),
				AlbumArt:   deref(tArt),
				DurationMs: derefInt64(tDuration),
				Playable:   playable,
				Year:       year,
				Genre:      deref(tGenre),
				Played:     derefBool(played),
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate board-by-id rows: %w", err)
	}
	if len(cells) == 0 {
		return nil, fmt.Errorf("store: no layout for board %s", boardID)
	}

	b := newGridFromCells(cells, maxRow, maxCol)

	if err := r.pool.QueryRow(ctx,
		`SELECT daily_double_count FROM boards WHERE id = $1`, boardID).
		Scan(&b.DailyDoubleCount); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("store: load board-by-id daily double count: %w", err)
	}

	ddRows, err := r.pool.Query(ctx,
		`SELECT id, spotify_uri, artist, song, album_art, duration_ms
		   FROM board_dd_bucket_tracks WHERE board_id = $1 ORDER BY created_at ASC`, boardID)
	if err != nil {
		return nil, fmt.Errorf("store: load board-by-id dd bucket: %w", err)
	}
	defer ddRows.Close()
	for ddRows.Next() {
		var t game.Track
		if err := ddRows.Scan(&t.ID, &t.SpotifyURI, &t.Artist, &t.Song, &t.AlbumArt, &t.DurationMs); err != nil {
			return nil, fmt.Errorf("store: scan dd bucket track: %w", err)
		}
		t.Playable = true
		b.DDBucket = append(b.DDBucket, &t)
	}
	if err := ddRows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate dd bucket rows: %w", err)
	}

	return b, nil
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

// MarkTrackPlayed persists that trackID has been consumed on the session's
// attached board, so a mid-game restart won't re-offer it (§7). Resolves the
// board via game_sessions.board_id so the engine can call it with just the
// session + track it already knows.
func (r *PostgresRepo) MarkTrackPlayed(ctx context.Context, sessionID, trackID string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE board_layout_cell_tracks SET played = TRUE
		  WHERE board_id = (SELECT board_id FROM game_sessions WHERE id = $1)
		    AND track_id = $2`,
		sessionID, trackID)
	if err != nil {
		return fmt.Errorf("store: mark track played: %w", err)
	}
	return nil
}

// ClearPlayed resets played=FALSE for every placement on the session's board
// (New Game).
func (r *PostgresRepo) ClearPlayed(ctx context.Context, sessionID string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE board_layout_cell_tracks SET played = FALSE
		  WHERE board_id = (SELECT board_id FROM game_sessions WHERE id = $1)`,
		sessionID)
	if err != nil {
		return fmt.Errorf("store: clear played: %w", err)
	}
	return nil
}
