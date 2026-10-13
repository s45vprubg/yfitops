package admin

// Domain types for the admin REST API. These are separate from the game engine's
// types to avoid coupling the CRUD layer to the live game model.

// Lyrics status values for LyricsRecord/Track.LyricsStatus/DailyDoubleTrack.LyricsStatus.
// An empty string (LyricsStatusUnknown) means no track_lyrics row exists yet
// for the song — distinct from LyricsStatusPending, which means a probe WAS
// attempted but errored transiently (last_error set) and should be retried.
const (
	LyricsStatusUnknown  = ""
	LyricsStatusPending  = "pending"
	LyricsStatusNotFound = "not_found"
	LyricsStatusResolved = "resolved"
)

// LyricsRecord is one row of the global per-song lyrics cache (track_lyrics),
// keyed by Spotify URI and shared by every board/Daily Double bucket
// placement of that song — a song is only ever fetched or manually entered
// once.
type LyricsRecord struct {
	SpotifyURI string `json:"spotifyUri"`
	Status     string `json:"status"`
	Source     string `json:"source,omitempty"` // "lrclib" | "manual", set when Status == resolved
	LastError  string `json:"lastError,omitempty"`
}

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
//
// Lyrics are a hard requirement here (unlike the grid, there is no override):
// AddDailyDoubleTrack rejects a track whose song has no resolved entry in the
// global lyrics cache, so LyricsStatus will in practice always read
// "resolved" for a track that made it into the bucket. It's still reported so
// the holding area can show status for a song BEFORE it's added.
type DailyDoubleTrack struct {
	ID           string `json:"id"`
	BoardID      string `json:"boardId"`
	SpotifyURI   string `json:"spotifyUri"`
	Artist       string `json:"artist"`
	Song         string `json:"song"`
	AlbumArt     string `json:"albumArt"`
	DurationMs   int64  `json:"durationMs"`
	CreatedAt    int64  `json:"createdAt"`
	LyricsStatus string `json:"lyricsStatus"`
}

// Track is a board-scoped track in the library.
//
// LyricsStatus mirrors the global track_lyrics cache for this track's song
// (LyricsStatusUnknown/Pending/NotFound/Resolved). LyricsOverride lets an
// admin allow a lyric-less track to play anyway (default off — karaoke needs
// words); it stays a per-placement column since the same song could in
// principle be allowed on one board and not another.
type Track struct {
	ID             string `json:"id"`
	BoardID        string `json:"boardId"`
	SpotifyURI     string `json:"spotifyUri"`
	Artist         string `json:"artist"`
	Song           string `json:"song"`
	AlbumArt       string `json:"albumArt"`
	DurationMs     int64  `json:"durationMs"`
	CreatedAt      int64  `json:"createdAt"`
	LyricsStatus   string `json:"lyricsStatus"`
	LyricsOverride bool   `json:"lyricsOverride"`
	Year           int    `json:"year"`  // release year (0 = unknown)
	Genre          string `json:"genre"` // primary artist genre ("" = unknown)
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
