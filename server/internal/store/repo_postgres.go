package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/s45vprubg/yfitops/server/internal/game"
	"github.com/s45vprubg/yfitops/server/internal/protocol"
)

// PostgresRepo is the production GameRepo (design_doc §2, §11). It is the audit
// + curation layer: game sessions, players, curated tracks, board cells, final
// scores, and a jsonb event log. Live state is the engine's; this is durable.
type PostgresRepo struct {
	pool *pgxpool.Pool
}

var _ game.GameRepo = (*PostgresRepo)(nil)

// NewPostgresRepo opens a connection pool to dsn and pings to confirm it.
func NewPostgresRepo(ctx context.Context, dsn string) (*PostgresRepo, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("store: pgx pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: pgx ping: %w", err)
	}
	return &PostgresRepo{pool: pool}, nil
}

// Close releases the connection pool.
func (r *PostgresRepo) Close() { r.pool.Close() }

// CreateSession inserts a new game instance (§11 game_sessions).
func (r *PostgresRepo) CreateSession(ctx context.Context, s *game.Session) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO game_sessions (id, created_at, skip_threshold_pct, state)
		 VALUES ($1, $2, $3, $4)`,
		s.ID, s.CreatedAt, s.SkipThresholdPct, s.State)
	if err != nil {
		return fmt.Errorf("store: create session %s: %w", s.ID, err)
	}
	return nil
}

// SaveScore upserts a player's final score for the session (§3.2 resume).
func (r *PostgresRepo) SaveScore(ctx context.Context, sessionID, playerID, handle string, score int) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO score_log (session_id, player_id, handle, score, updated_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (session_id, player_id)
		 DO UPDATE SET handle = EXCLUDED.handle,
		               score  = EXCLUDED.score,
		               updated_at = EXCLUDED.updated_at`,
		sessionID, playerID, handle, score, time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("store: save score %s/%s: %w", sessionID, playerID, err)
	}
	return nil
}

// LogEvent appends an audit record with detail stored as jsonb.
func (r *PostgresRepo) LogEvent(ctx context.Context, sessionID, kind string, detail map[string]any) error {
	if detail == nil {
		detail = map[string]any{}
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("store: marshal event detail: %w", err)
	}
	_, err = r.pool.Exec(ctx,
		`INSERT INTO event_log (session_id, kind, detail, created_at)
		 VALUES ($1, $2, $3, $4)`,
		sessionID, kind, raw, time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("store: log event %s/%s: %w", sessionID, kind, err)
	}
	return nil
}

// LoadBoard reconstructs the Board/Cell/Track grid for a session from
// board_cells + board_cell_tracks + tracks (§7). Dimensions are derived from
// the max row/col present.
func (r *PostgresRepo) LoadBoard(ctx context.Context, sessionID string) (*game.Board, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT bc.row, bc.col, bc.category, bc.daily_double,
		        t.id, t.spotify_uri, t.artist, t.song, t.album_art, t.duration_ms,
		        bct.played, bct.pos
		   FROM board_cells bc
		   LEFT JOIN board_cell_tracks bct
		     ON bct.session_id = bc.session_id AND bct.row = bc.row AND bct.col = bc.col
		   LEFT JOIN tracks t ON t.id = bct.track_id
		  WHERE bc.session_id = $1
		  ORDER BY bc.row, bc.col, bct.pos`,
		sessionID)
	if err != nil {
		return nil, fmt.Errorf("store: load board %s: %w", sessionID, err)
	}
	defer rows.Close()

	cells := map[cellCoord]*game.Cell{}
	maxRow, maxCol := 0, 0

	for rows.Next() {
		var (
			row, col    int
			category    string
			dailyDouble bool
			// track columns are nullable (LEFT JOIN may yield a cell with no
			// tracks), so scan into pointers.
			tID, tURI, tArtist, tSong, tArt *string
			tDuration                       *int64
			played                          *bool
			pos                             *int
		)
		if err := rows.Scan(&row, &col, &category, &dailyDouble,
			&tID, &tURI, &tArtist, &tSong, &tArt, &tDuration, &played, &pos); err != nil {
			return nil, fmt.Errorf("store: scan board row: %w", err)
		}
		if row > maxRow {
			maxRow = row
		}
		if col > maxCol {
			maxCol = col
		}
		c := cellCoord{row, col}
		cell := cells[c]
		if cell == nil {
			cell = &game.Cell{Row: row, Col: col, Category: category, DailyDouble: dailyDouble}
			cells[c] = cell
		}
		if tID != nil {
			cell.Tracks = append(cell.Tracks, &game.Track{
				ID:         deref(tID),
				SpotifyURI: deref(tURI),
				Artist:     deref(tArtist),
				Song:       deref(tSong),
				AlbumArt:   deref(tArt),
				DurationMs: derefInt64(tDuration),
				Played:     played != nil && *played,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate board rows: %w", err)
	}
	if len(cells) == 0 {
		return nil, fmt.Errorf("store: no board for session %s", sessionID)
	}

	board := newGridFromCells(cells, maxRow, maxCol)
	return board, nil
}

// Leaderboard returns the top historical scores across all sessions (§11).
func (r *PostgresRepo) Leaderboard(ctx context.Context, limit int) ([]protocol.ScoreEntry, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := r.pool.Query(ctx,
		`SELECT player_id, handle, score
		   FROM score_log
		  ORDER BY score DESC
		  LIMIT $1`,
		limit)
	if err != nil {
		return nil, fmt.Errorf("store: leaderboard: %w", err)
	}
	defer rows.Close()

	out := make([]protocol.ScoreEntry, 0, limit)
	for rows.Next() {
		var e protocol.ScoreEntry
		if err := rows.Scan(&e.ID, &e.Handle, &e.Score); err != nil {
			return nil, fmt.Errorf("store: scan leaderboard row: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate leaderboard rows: %w", err)
	}
	return out, nil
}

// ensure pgx is referenced even if future query paths drop direct use.
var _ = pgx.ErrNoRows

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// cellCoord is a (row, col) grid key shared by the board loaders.
type cellCoord struct{ row, col int }

// newGridFromCells lays out a 1-indexed row/col cell map into the dense [][]*Cell
// grid used by game.Board. Missing coordinates get empty cells so the grid is
// rectangular. Shared with MemRepo's loader.
func newGridFromCells(cells map[cellCoord]*game.Cell, maxRow, maxCol int) *game.Board {
	grid := make([][]*game.Cell, maxRow)
	for i := range grid {
		grid[i] = make([]*game.Cell, maxCol)
	}
	for k, cell := range cells {
		grid[k.row-1][k.col-1] = cell
	}
	for ri := 0; ri < maxRow; ri++ {
		for ci := 0; ci < maxCol; ci++ {
			if grid[ri][ci] == nil {
				grid[ri][ci] = &game.Cell{Row: ri + 1, Col: ci + 1}
			}
		}
	}
	return &game.Board{Rows: maxRow, Cols: maxCol, Cells: grid}
}
