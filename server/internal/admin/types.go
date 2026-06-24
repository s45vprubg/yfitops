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
}

// Track is a board-scoped track in the library.
type Track struct {
	ID         string `json:"id"`
	BoardID    string `json:"boardId"`
	SpotifyURI string `json:"spotifyUri"`
	Artist     string `json:"artist"`
	Song       string `json:"song"`
	AlbumArt   string `json:"albumArt"`
	DurationMs int64  `json:"durationMs"`
	CreatedAt  int64  `json:"createdAt"`
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
