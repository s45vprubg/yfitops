package admin

import (
	"context"
	"errors"

	"github.com/s45vprubg/yfitops/server/internal/game"
)

// ErrDailyDoubleTrackExists is returned by AddDailyDoubleTrack when the
// (board_id, spotify_uri) pair already has a row — the store's INSERT ...
// ON CONFLICT DO NOTHING found nothing to insert. The handler must not
// respond 201 with the caller's (unsaved) payload in this case; it should
// surface a real conflict (dd-api-1).
var ErrDailyDoubleTrackExists = errors.New("daily double track already exists for this board and spotify uri")

// ErrDailyDoubleTrackNotFound is returned by RemoveDailyDoubleTrack when no
// row matched the given (boardID, trackID) pair — either the track id does
// not exist at all, or it exists but belongs to a different board (dd-api-2).
var ErrDailyDoubleTrackNotFound = errors.New("daily double track not found on this board")

// AdminStore defines the data operations the admin REST handlers need.
type AdminStore interface {
	// Boards
	CreateBoard(ctx context.Context, id, name string) error
	ListBoards(ctx context.Context) ([]Board, error)
	GetBoard(ctx context.Context, id string) (*Board, error)
	RenameBoard(ctx context.Context, id, name string) error
	DeleteBoard(ctx context.Context, id string) error
	UpdateBoardCols(ctx context.Context, id string, cols int) error
	SetDailyDoubleCount(ctx context.Context, id string, count int) error

	// Daily Double bucket (board-scoped, independent of the grid's Track library)
	// AddDailyDoubleTrack returns ErrDailyDoubleTrackExists on a duplicate
	// (board_id, spotify_uri) instead of silently no-opping.
	AddDailyDoubleTrack(ctx context.Context, t *DailyDoubleTrack) error
	ListDailyDoubleTracks(ctx context.Context, boardID string) ([]DailyDoubleTrack, error)
	// RemoveDailyDoubleTrack deletes a track scoped to boardID; it returns
	// ErrDailyDoubleTrackNotFound if trackID doesn't exist or belongs to a
	// different board, rather than silently deleting cross-board.
	RemoveDailyDoubleTrack(ctx context.Context, boardID, trackID string) error

	// Tracks (board-scoped library)
	AddTrack(ctx context.Context, t *Track) error
	ListTracks(ctx context.Context, boardID string) ([]Track, error)
	UnplacedTracks(ctx context.Context, boardID string) ([]Track, error)
	DeleteTrack(ctx context.Context, trackID string) error
	// GetTrack looks up a single board_tracks row by id (needed to resolve a
	// track's artist/song/duration/spotifyUri for the requery/manual-lyrics
	// endpoints). Returns nil, nil if no such track exists.
	GetTrack(ctx context.Context, trackID string) (*Track, error)
	// SetTrackOverride sets the admin play-anyway override for a track.
	SetTrackOverride(ctx context.Context, trackID string, override bool) error

	// Lyrics text cache (global, keyed by Spotify URI — shared by every board
	// and the Daily Double bucket; a song is only ever fetched/entered once).
	GetLyricsStatus(ctx context.Context, spotifyURI string) (*LyricsRecord, error)
	GetLyricsStatusesForURIs(ctx context.Context, uris []string) (map[string]LyricsRecord, error)
	UpsertLyricsResolved(ctx context.Context, spotifyURI, lrcText, source string) error
	UpsertLyricsNotFound(ctx context.Context, spotifyURI string) error
	UpsertLyricsError(ctx context.Context, spotifyURI, errMsg string) error

	// Layout
	AddColumn(ctx context.Context, boardID string, col int, category string) error
	RemoveColumn(ctx context.Context, boardID string, col int) error
	RenameCategory(ctx context.Context, boardID string, col int, name string) error
	PlaceTrack(ctx context.Context, boardID string, row, col int, trackID string, pos int) error
	UnplaceTrack(ctx context.Context, boardID string, row, col int, trackID string) error
	GetLayout(ctx context.Context, boardID string) (*Layout, error)
	// RebuildLayout atomically replaces a board's entire layout: within one
	// transaction it clears the existing columns/placements, sets the column
	// count, then recreates the given columns and their track placements. Used by
	// the AI builder so a mid-build failure can't leave a corrupt half-built board.
	RebuildLayout(ctx context.Context, boardID string, cols int, columns []LayoutColumn) error

	// Game-time
	LoadBoardByID(ctx context.Context, boardID string) (*game.Board, error)
	AttachBoard(ctx context.Context, sessionID, boardID string) error

	// Solo Karaoke Library (standalone, global holding pool — not board-
	// scoped; mirrors ListTracks/AddTrack/DeleteTrack's shape).
	AddSoloKaraokeLibraryTrack(ctx context.Context, t *SoloKaraokeTrack) error
	ListSoloKaraokeLibraryTracks(ctx context.Context) ([]SoloKaraokeTrack, error)
	RemoveSoloKaraokeLibraryTrack(ctx context.Context, trackID string) error

	// Solo Karaoke Playlist (standalone, global ordered queue — the engine
	// plays directly from this list). AddSoloKaraokePlaylistTrack appends at
	// the end (position = current max + 1). ReorderSoloKaraokePlaylist
	// atomically rewrites every row's position in one transaction (mirrors
	// RebuildLayout's "atomically replace everything" pattern — avoids
	// off-by-one arithmetic from a sequence of individual position updates).
	AddSoloKaraokePlaylistTrack(ctx context.Context, t *SoloKaraokeTrack) error
	ListSoloKaraokePlaylistTracks(ctx context.Context) ([]SoloKaraokeTrack, error)
	RemoveSoloKaraokePlaylistTrack(ctx context.Context, trackID string) error
	ReorderSoloKaraokePlaylist(ctx context.Context, orderedIDs []string) error

	// GetSoloKaraokeTrack looks up a single track by id across BOTH buckets
	// (library first, then playlist) — needed to resolve a track's artist/
	// song/duration/spotifyUri for the requery/manual-lyrics endpoints,
	// mirroring GetTrack's role for board_tracks. Returns nil, nil if no such
	// track exists in either bucket.
	GetSoloKaraokeTrack(ctx context.Context, trackID string) (*SoloKaraokeTrack, error)
}

// LayoutColumn describes one category column to (re)create in RebuildLayout,
// with its track placements. Col is implied by the slice index (1-based).
type LayoutColumn struct {
	Category   string
	Placements []LayoutPlacement
}

// LayoutPlacement is a single track placed at (Row, Col) with ordering Pos.
type LayoutPlacement struct {
	Row     int
	Col     int
	TrackID string
	Pos     int
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
	Year       int    `json:"year"`
	Genre      string `json:"genre"`
}

// BoardReloader allows the admin handler to push a new board to the live engine.
type BoardReloader interface {
	ReloadBoard(b *game.Board)
	StartGame() error
	ResetToLobby() error
}

// SoloKaraokeReloader lets the admin handler push the current Solo Karaoke
// Playlist to the live engine's in-memory queue after a playlist mutation
// (add/delete/reorder/import) — so the engine never queries Postgres itself.
// The handler builds the []*game.Track from whatever AdminStore just
// returned (the same shape ReloadBoard's caller already builds a *game.Board
// from Postgres data elsewhere), converting the lightweight REST
// SoloKaraokeTrack rows into the engine's own domain type.
type SoloKaraokeReloader interface {
	ReloadSoloKaraokeQueue(tracks []*game.Track)
}

// LyricsProber fetches raw LRC lyric text for a track from LRCLIB, for the
// grey-out / karaoke gate. found=false,err=nil means LRCLIB confirmed no
// lyrics exist; err != nil means the probe failed transiently and should be
// retried later (callers must NOT treat an error as "no lyrics").
type LyricsProber interface {
	Probe(ctx context.Context, artist, song string, durationSec int) (lrcText string, found bool, err error)
}

// AICategory / AIProposal mirror ai.Category / ai.Proposal without importing the
// ai package into admin (keeps the handler's deps light + testable).
type AICategory struct {
	Name     string   `json:"name"`
	TrackIDs []string `json:"trackIds"`
}
type AIProposal struct {
	Categories []AICategory `json:"categories"`
}

// AITrack is the minimal track info handed to the categorizer.
type AITrack struct {
	ID     string
	Artist string
	Song   string
}

// Categorizer proposes Jeopardy category buckets for a set of tracks (AI board
// builder). Optional — nil means the feature is disabled (endpoint 503s).
type Categorizer interface {
	BuildCategories(ctx context.Context, tracks []AITrack, rows, cols int) (*AIProposal, error)
}
