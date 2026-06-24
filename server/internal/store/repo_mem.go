package store

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/s45vprubg/yfitops/server/internal/game"
	"github.com/s45vprubg/yfitops/server/internal/protocol"
)

// MemRepo is an in-memory GameRepo for tests that don't need a live Postgres.
// It is concurrency-safe (the engine and tests may touch it from goroutines).
type MemRepo struct {
	mu       sync.Mutex
	sessions map[string]*game.Session
	boards   map[string]*game.Board                    // sessionID -> board
	scores   map[string]map[string]protocol.ScoreEntry // sessionID -> playerID -> entry
	events   []memEvent
}

type memEvent struct {
	SessionID string
	Kind      string
	Detail    map[string]any
	CreatedAt int64
}

var _ game.GameRepo = (*MemRepo)(nil)

// NewMemRepo returns an empty in-memory repo.
func NewMemRepo() *MemRepo {
	return &MemRepo{
		sessions: make(map[string]*game.Session),
		boards:   make(map[string]*game.Board),
		scores:   make(map[string]map[string]protocol.ScoreEntry),
	}
}

func (r *MemRepo) CreateSession(_ context.Context, s *game.Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.sessions[s.ID]; dup {
		return fmt.Errorf("store: session %s already exists", s.ID)
	}
	r.sessions[s.ID] = s
	return nil
}

func (r *MemRepo) SaveScore(_ context.Context, sessionID, playerID, handle string, score int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.scores[sessionID] == nil {
		r.scores[sessionID] = make(map[string]protocol.ScoreEntry)
	}
	r.scores[sessionID][playerID] = protocol.ScoreEntry{ID: playerID, Handle: handle, Score: score}
	return nil
}

func (r *MemRepo) LogEvent(_ context.Context, sessionID, kind string, detail map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, memEvent{
		SessionID: sessionID,
		Kind:      kind,
		Detail:    detail,
		CreatedAt: time.Now().UnixMilli(),
	})
	return nil
}

func (r *MemRepo) LoadBoard(_ context.Context, sessionID string) (*game.Board, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.boards[sessionID]
	if b == nil {
		return nil, fmt.Errorf("store: no board for session %s", sessionID)
	}
	return b, nil
}

func (r *MemRepo) Leaderboard(_ context.Context, limit int) ([]protocol.ScoreEntry, error) {
	if limit <= 0 {
		limit = 10
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var all []protocol.ScoreEntry
	for _, byPlayer := range r.scores {
		for _, e := range byPlayer {
			all = append(all, e)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Score > all[j].Score })
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// SetBoard installs a pre-built board for a session (test helper / seeding).
func (r *MemRepo) SetBoard(sessionID string, b *game.Board) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.boards[sessionID] = b
}

// Events returns a copy of the logged events (test helper).
func (r *MemRepo) Events() []memEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]memEvent, len(r.events))
	copy(out, r.events)
	return out
}

// SeedSampleBoard builds and installs a 5x5 board for sessionID with 4-6 tracks
// per cell (§7), then returns it. Categories label columns; rows set difficulty.
// Track counts vary per cell so Exhausted/TracksLeft logic gets exercised.
func (r *MemRepo) SeedSampleBoard(sessionID string) *game.Board {
	b := SampleBoard()
	r.SetBoard(sessionID, b)
	return b
}

// SampleBoard returns a fresh 5x5 board with 4-6 tracks per cell. Standalone so
// engine/transport tests can use it without a repo.
func SampleBoard() *game.Board {
	const rows, cols = 5, 5
	categories := []string{"80s Anthems", "One-Hit Wonders", "Movie Themes", "Hip-Hop", "Karaoke Classics"}

	grid := make([][]*game.Cell, rows)
	for ri := 0; ri < rows; ri++ {
		grid[ri] = make([]*game.Cell, cols)
		for ci := 0; ci < cols; ci++ {
			row, col := ri+1, ci+1
			// 4..6 tracks per cell, deterministic spread.
			n := 4 + (row+col)%3
			tracks := make([]*game.Track, n)
			for k := 0; k < n; k++ {
				id := fmt.Sprintf("t-%d-%d-%d", row, col, k)
				tracks[k] = &game.Track{
					ID:         id,
					SpotifyURI: "spotify:track:" + id,
					Artist:     fmt.Sprintf("Artist %d-%d-%d", row, col, k),
					Song:       fmt.Sprintf("Song %d-%d-%d", row, col, k),
					DurationMs: 180_000,
				}
			}
			grid[ri][ci] = &game.Cell{
				Row:         row,
				Col:         col,
				Category:    categories[ci],
				Tracks:      tracks,
				DailyDouble: row == 3 && col == 4, // one hidden daily double (§7)
			}
		}
	}
	return &game.Board{Rows: rows, Cols: cols, Cells: grid}
}
