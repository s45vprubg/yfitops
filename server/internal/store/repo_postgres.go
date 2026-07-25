package store

import (
	"context"
	"encoding/json"
	"errors"
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

// LoadBoard reconstructs the Board/Cell/Track grid for a session by resolving
// the persisted game_sessions.board_id link and loading that board from the
// 0002 layout tables (board_layout_cells + board_layout_cell_tracks +
// board_tracks) via LoadBoardByID. The older 0001 board_cells tables are never
// populated, so the link is the only durable source of a session's board.
//
// If the session has no board attached (board_id null/empty), it returns the
// same "no board for session" error as before so engine.Run boots boardless.
func (r *PostgresRepo) LoadBoard(ctx context.Context, sessionID string) (*game.Board, error) {
	var boardID *string
	err := r.pool.QueryRow(ctx,
		`SELECT board_id FROM game_sessions WHERE id = $1`, sessionID).
		Scan(&boardID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: no board for session %s", sessionID)
		}
		return nil, fmt.Errorf("store: load board %s: %w", sessionID, err)
	}
	if boardID == nil || *boardID == "" {
		return nil, fmt.Errorf("store: no board for session %s", sessionID)
	}
	// Reuse the 0002-layout loader so the *game.Board shape matches exactly what
	// the live attach handler produces.
	return r.LoadBoardByID(ctx, *boardID)
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

func derefBool(p *bool) bool {
	if p == nil {
		return false
	}
	return *p
}

func derefIntPtr(p *int) int {
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
