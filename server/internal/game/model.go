package game

import "sync"

// model.go holds the domain entities shared by the engine and the leaf
// implementations. The engine owns the live mutable state; these types are the
// vocabulary. Field-level concurrency is the engine's responsibility (it runs
// a single command loop), except where a type carries its own mutex.

// Track is a single curated song in a cell's pool. Metadata here is TRUSTED and
// must never be serialized to mobile (§4A).
type Track struct {
	ID         string
	SpotifyURI string
	Artist     string
	Song       string
	AlbumArt   string
	DurationMs int64
	Played     bool // exhausted within its cell pool
}

// Cell is one coordinate on the Jeopardy grid (§7). It holds a pool of 4-6
// tracks; the cell greys out only when the whole pool is exhausted.
type Cell struct {
	Row         int
	Col         int
	Category    string
	Tracks      []*Track
	DailyDouble bool // hidden daily-double flag (§7)
}

// Exhausted reports whether every track in the pool has been played.
func (c *Cell) Exhausted() bool {
	for _, t := range c.Tracks {
		if !t.Played {
			return false
		}
	}
	return true
}

// TracksLeft counts unplayed tracks in the pool.
func (c *Cell) TracksLeft() int {
	n := 0
	for _, t := range c.Tracks {
		if !t.Played {
			n++
		}
	}
	return n
}

// Board is the full grid (§7). Categories label columns; rows set difficulty.
type Board struct {
	Rows  int
	Cols  int
	Cells [][]*Cell // [row][col]
}

// Player is an ephemeral attendee session (§3.2). Keyed by device fingerprint
// so a player can drop and resume their exact score.
type Player struct {
	ID       string
	Handle   string
	DeviceFP string
	Score    int
	RTTMs    int  // moving-average RTT from heartbeats (§4C)
	Active   bool // counts toward the skip-vote Active User pool (§3.8)
	Banned   bool
	// IdleRounds tracks consecutive inactive rounds for the 2-round timeout (§3.8).
	IdleRounds int
	// GuessedThisTrack: players who already guessed are locked out of re-guessing
	// the current track (§3.4, §3.6).
	GuessedThisTrack bool
}

// Session is one game instance (§3, §11 game_sessions). Persisted for audit.
type Session struct {
	ID               string
	CreatedAt        int64
	SkipThresholdPct int
	State            string
	mu               sync.Mutex
}

func (s *Session) Lock()   { s.mu.Lock() }
func (s *Session) Unlock() { s.mu.Unlock() }
