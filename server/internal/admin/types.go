package admin

// Domain types for the admin REST API. These are separate from the game engine's
// types to avoid coupling the CRUD layer to the live game model.

// Board is an independent named board that can be attached to a game session.
type Board struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Cols      int    `json:"cols"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
	// DailyDoubleCount is the number of Daily Double cells to randomly assign
	// (balanced across categories) each time this board's game is started.
	// Default 2; cell assignment itself is never persisted, only this count.
	DailyDoubleCount int `json:"dailyDoubleCount"`
}

// DailyDoubleTrack is a song in a board's standalone Daily Double bucket —
// independent of the grid's category-scoped Track library. A Daily Double
// draws up to 5 of these as choices for the contestant.
type DailyDoubleTrack struct {
	ID         string `json:"id"`
	BoardID    string `json:"boardId"`
	SpotifyURI string `json:"spotifyUri"`
	Artist     string `json:"artist"`
	Song       string `json:"song"`
	AlbumArt   string `json:"albumArt"`
	DurationMs int64  `json:"durationMs"`
	CreatedAt  int64  `json:"createdAt"`
}

// Track is a board-scoped track in the library.
//
// HasSyncedLyrics is a tri-state via pointer: nil = not yet checked, else the
// LRCLIB probe result. LyricsOverride lets an admin allow a lyric-less track to
// play anyway (default off — karaoke needs words).
type Track struct {
	ID              string `json:"id"`
	BoardID         string `json:"boardId"`
	SpotifyURI      string `json:"spotifyUri"`
	Artist          string `json:"artist"`
	Song            string `json:"song"`
	AlbumArt        string `json:"albumArt"`
	DurationMs      int64  `json:"durationMs"`
	CreatedAt       int64  `json:"createdAt"`
	HasSyncedLyrics *bool  `json:"hasSyncedLyrics"`
	LyricsOverride  bool   `json:"lyricsOverride"`
	Year            int    `json:"year"`  // release year (0 = unknown)
	Genre           string `json:"genre"` // primary artist genre ("" = unknown)
}

// LayoutCell represents one cell in the grid with its placed tracks.
type LayoutCell struct {
	Row      int     `json:"row"`
	Col      int     `json:"col"`
	Category string  `json:"category"`
	Tracks   []Track `json:"tracks"`
}

// Layout is the full grid state for a board.
type Layout struct {
	Cols  int          `json:"cols"`
	Cells []LayoutCell `json:"cells"`
}
