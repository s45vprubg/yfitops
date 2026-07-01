package admin

import (
	"context"

	"github.com/s45vprubg/yfitops/server/internal/game"
)

// AdminStore defines the data operations the admin REST handlers need.
type AdminStore interface {
	// Boards
	CreateBoard(ctx context.Context, id, name string) error
	ListBoards(ctx context.Context) ([]Board, error)
	GetBoard(ctx context.Context, id string) (*Board, error)
	RenameBoard(ctx context.Context, id, name string) error
	DeleteBoard(ctx context.Context, id string) error
	UpdateBoardCols(ctx context.Context, id string, cols int) error

	// Tracks (board-scoped library)
	AddTrack(ctx context.Context, t *Track) error
	ListTracks(ctx context.Context, boardID string) ([]Track, error)
	UnplacedTracks(ctx context.Context, boardID string) ([]Track, error)
	DeleteTrack(ctx context.Context, trackID string) error
	// SetTrackLyrics updates the lyric-availability probe result and/or the
	// admin play-override for a track. Nil args leave that column unchanged.
	SetTrackLyrics(ctx context.Context, trackID string, hasSynced *bool, override *bool) error

	// Layout
	AddColumn(ctx context.Context, boardID string, col int, category string) error
	RemoveColumn(ctx context.Context, boardID string, col int) error
	RenameCategory(ctx context.Context, boardID string, col int, name string) error
	PlaceTrack(ctx context.Context, boardID string, row, col int, trackID string, pos int) error
	UnplaceTrack(ctx context.Context, boardID string, row, col int, trackID string) error
	GetLayout(ctx context.Context, boardID string) (*Layout, error)

	// Game-time
	LoadBoardByID(ctx context.Context, boardID string) (*game.Board, error)
	AttachBoard(ctx context.Context, sessionID, boardID string) error
}

// SpotifySearcher abstracts Spotify search for the admin handler.
type SpotifySearcher interface {
	Search(ctx context.Context, query string, limit int) ([]SpotifyResult, error)
	GetPlaylistTracks(ctx context.Context, playlistID string) ([]SpotifyResult, error)
	// ValidToken returns a non-expired Spotify access token, refreshing first
	// if needed. Served to the Stage so its Web Playback SDK getOAuthToken
	// callback always has a live token through a multi-hour game.
	ValidToken(ctx context.Context) (string, error)
}

// SpotifyResult mirrors spotify.SearchResult to avoid an import dependency on
// the spotify package from the admin handler tests.
type SpotifyResult struct {
	URI        string `json:"uri"`
	Artist     string `json:"artist"`
	Song       string `json:"song"`
	AlbumArt   string `json:"albumArt"`
	DurationMs int64  `json:"durationMs"`
}

// BoardReloader allows the admin handler to push a new board to the live engine.
type BoardReloader interface {
	ReloadBoard(b *game.Board)
	StartGame() error
	ResetToLobby() error
}

// LyricsProber checks whether a track has synced lyrics (for the grey-out /
// karaoke gate). Returns true if LRCLIB has time-coded lyrics for the track.
type LyricsProber interface {
	HasSyncedLyrics(ctx context.Context, artist, song string, durationSec int) bool
}
