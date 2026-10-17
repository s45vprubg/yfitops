package admin

import "errors"

// solokaraoke_types.go holds the domain types + sentinel errors for Solo
// Karaoke mode's standalone Library/Playlist buckets (0010_solo_karaoke.sql).
// Mirrors types.go's Track/DailyDoubleTrack shape; kept in its own file
// rather than appended to types.go since the feature is a self-contained
// addition with its own store/handler files.

// ErrSoloKaraokeLibraryTrackExists is returned by AddSoloKaraokeLibraryTrack
// when a row with this spotifyUri already exists — mirrors
// ErrDailyDoubleTrackExists (dd-api-1): the store's INSERT ... ON CONFLICT DO
// NOTHING succeeds (err == nil) even when nothing was written, so the
// handler must not report success with the caller's unsaved payload.
var ErrSoloKaraokeLibraryTrackExists = errors.New("solo karaoke library track already exists for this spotify uri")

// ErrSoloKaraokeLibraryTrackNotFound is returned by RemoveSoloKaraokeLibraryTrack
// when trackID doesn't exist.
var ErrSoloKaraokeLibraryTrackNotFound = errors.New("solo karaoke library track not found")

// ErrSoloKaraokePlaylistTrackExists mirrors ErrSoloKaraokeLibraryTrackExists
// for the playlist bucket.
var ErrSoloKaraokePlaylistTrackExists = errors.New("solo karaoke playlist track already exists for this spotify uri")

// ErrSoloKaraokePlaylistTrackNotFound mirrors ErrSoloKaraokeLibraryTrackNotFound
// for the playlist bucket.
var ErrSoloKaraokePlaylistTrackNotFound = errors.New("solo karaoke playlist track not found")

// SoloKaraokeTrack is a song in either of Solo Karaoke's two standalone
// buckets — the Library (an unordered holding pool admin builds via
// search/playlist-import, mirrors the Board Builder's holding area) or the
// Playlist (an ordered queue the engine actually plays from, drag-selected
// out of the Library). Position is populated (and meaningful) only for
// playlist tracks; library tracks are ordered by CreatedAt, exactly like
// Track/DailyDoubleTrack.
//
// Lyrics gating is soft here (unlike the Daily Double bucket's hard
// requirement): LyricsStatus is reported for the UI to flag a track, but
// adding one never fails for missing/unresolved lyrics.
type SoloKaraokeTrack struct {
	ID           string `json:"id"`
	SpotifyURI   string `json:"spotifyUri"`
	Artist       string `json:"artist"`
	Song         string `json:"song"`
	AlbumArt     string `json:"albumArt"`
	DurationMs   int64  `json:"durationMs"`
	CreatedAt    int64  `json:"createdAt"`
	Position     int    `json:"position"` // playlist only; 0 for library tracks
	LyricsStatus string `json:"lyricsStatus"`
}
