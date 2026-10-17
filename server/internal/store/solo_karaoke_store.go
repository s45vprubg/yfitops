package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/s45vprubg/yfitops/server/internal/admin"
)

// solo_karaoke_store.go implements the Solo Karaoke slice of admin.AdminStore
// (0010_solo_karaoke.sql): two standalone, global tables — a Library
// (unordered holding pool) and a Playlist (ordered queue the engine plays
// from) — neither FK'd to boards. Mirrors admin_store.go's
// AddDailyDoubleTrack/ListDailyDoubleTracks/RemoveDailyDoubleTrack shape for
// each bucket.

// AddSoloKaraokeLibraryTrack adds a song to the global Solo Karaoke Library.
// Mirrors AddDailyDoubleTrack: INSERT ... ON CONFLICT (spotify_uri) DO
// NOTHING returns err == nil even when nothing was written, so a 0-rows
// result is turned into admin.ErrSoloKaraokeLibraryTrackExists.
func (r *PostgresRepo) AddSoloKaraokeLibraryTrack(ctx context.Context, t *admin.SoloKaraokeTrack) error {
	tag, err := r.pool.Exec(ctx,
		`INSERT INTO solo_karaoke_library_tracks (id, spotify_uri, artist, song, album_art, duration_ms, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (spotify_uri) DO NOTHING`,
		t.ID, t.SpotifyURI, t.Artist, t.Song, t.AlbumArt, t.DurationMs, t.CreatedAt)
	if err != nil {
		return fmt.Errorf("store: add solo karaoke library track: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return admin.ErrSoloKaraokeLibraryTrackExists
	}
	return nil
}

func (r *PostgresRepo) ListSoloKaraokeLibraryTracks(ctx context.Context) ([]admin.SoloKaraokeTrack, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT sk.id, sk.spotify_uri, sk.artist, sk.song, sk.album_art, sk.duration_ms, sk.created_at, tl.status
		   FROM solo_karaoke_library_tracks sk
		   LEFT JOIN track_lyrics tl ON tl.spotify_uri = sk.spotify_uri
		  ORDER BY sk.created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list solo karaoke library tracks: %w", err)
	}
	defer rows.Close()

	var tracks []admin.SoloKaraokeTrack
	for rows.Next() {
		var t admin.SoloKaraokeTrack
		var status *string
		if err := rows.Scan(&t.ID, &t.SpotifyURI, &t.Artist, &t.Song, &t.AlbumArt, &t.DurationMs, &t.CreatedAt, &status); err != nil {
			return nil, fmt.Errorf("store: scan solo karaoke library track: %w", err)
		}
		t.LyricsStatus = deref(status)
		tracks = append(tracks, t)
	}
	return tracks, rows.Err()
}

// RemoveSoloKaraokeLibraryTrack deletes a library track by id. Zero rows
// affected surfaces as admin.ErrSoloKaraokeLibraryTrackNotFound so the
// handler can answer 404 instead of a false 204.
func (r *PostgresRepo) RemoveSoloKaraokeLibraryTrack(ctx context.Context, trackID string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM solo_karaoke_library_tracks WHERE id = $1`, trackID)
	if err != nil {
		return fmt.Errorf("store: remove solo karaoke library track: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return admin.ErrSoloKaraokeLibraryTrackNotFound
	}
	return nil
}

// AddSoloKaraokePlaylistTrack appends a song to the end of the Solo Karaoke
// Playlist — position = current max + 1 (0 if the playlist is empty),
// computed inside the same INSERT so a concurrent add can't race onto the
// same position.
func (r *PostgresRepo) AddSoloKaraokePlaylistTrack(ctx context.Context, t *admin.SoloKaraokeTrack) error {
	tag, err := r.pool.Exec(ctx,
		`INSERT INTO solo_karaoke_playlist_tracks (id, spotify_uri, artist, song, album_art, duration_ms, position, created_at)
		 SELECT $1, $2, $3, $4, $5, $6, COALESCE(MAX(position) + 1, 0), $7
		   FROM solo_karaoke_playlist_tracks
		 ON CONFLICT (spotify_uri) DO NOTHING`,
		t.ID, t.SpotifyURI, t.Artist, t.Song, t.AlbumArt, t.DurationMs, t.CreatedAt)
	if err != nil {
		return fmt.Errorf("store: add solo karaoke playlist track: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return admin.ErrSoloKaraokePlaylistTrackExists
	}
	return nil
}

func (r *PostgresRepo) ListSoloKaraokePlaylistTracks(ctx context.Context) ([]admin.SoloKaraokeTrack, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT sk.id, sk.spotify_uri, sk.artist, sk.song, sk.album_art, sk.duration_ms, sk.position, sk.created_at, tl.status
		   FROM solo_karaoke_playlist_tracks sk
		   LEFT JOIN track_lyrics tl ON tl.spotify_uri = sk.spotify_uri
		  ORDER BY sk.position ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list solo karaoke playlist tracks: %w", err)
	}
	defer rows.Close()

	var tracks []admin.SoloKaraokeTrack
	for rows.Next() {
		var t admin.SoloKaraokeTrack
		var status *string
		if err := rows.Scan(&t.ID, &t.SpotifyURI, &t.Artist, &t.Song, &t.AlbumArt, &t.DurationMs, &t.Position, &t.CreatedAt, &status); err != nil {
			return nil, fmt.Errorf("store: scan solo karaoke playlist track: %w", err)
		}
		t.LyricsStatus = deref(status)
		tracks = append(tracks, t)
	}
	return tracks, rows.Err()
}

// RemoveSoloKaraokePlaylistTrack deletes a playlist track by id. Zero rows
// affected surfaces as admin.ErrSoloKaraokePlaylistTrackNotFound. Positions
// of the remaining tracks are deliberately left with a gap rather than
// renumbered — ListSoloKaraokePlaylistTracks orders by position ASC
// regardless of gaps, and the next ReorderSoloKaraokePlaylist call (or the
// next Add's MAX+1) is unaffected by a hole in the sequence.
func (r *PostgresRepo) RemoveSoloKaraokePlaylistTrack(ctx context.Context, trackID string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM solo_karaoke_playlist_tracks WHERE id = $1`, trackID)
	if err != nil {
		return fmt.Errorf("store: remove solo karaoke playlist track: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return admin.ErrSoloKaraokePlaylistTrackNotFound
	}
	return nil
}

// ReorderSoloKaraokePlaylist atomically rewrites every row's position to
// match orderedIDs' index, within one transaction — mirrors RebuildLayout's
// "atomically replace everything" pattern (admin_store.go) rather than a
// sequence of individual position updates, which would risk a transient
// off-by-one/duplicate-position state if read mid-sequence. A trackID not
// found among the existing rows is silently skipped (nothing to reorder);
// an existing row NOT present in orderedIDs keeps its current position
// untouched rather than being deleted — this endpoint only reorders, it
// never removes.
func (r *PostgresRepo) ReorderSoloKaraokePlaylist(ctx context.Context, orderedIDs []string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: reorder solo karaoke playlist: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	for i, id := range orderedIDs {
		if _, err := tx.Exec(ctx,
			`UPDATE solo_karaoke_playlist_tracks SET position = $1 WHERE id = $2`, i, id); err != nil {
			return fmt.Errorf("store: reorder solo karaoke playlist: update id=%s: %w", id, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: reorder solo karaoke playlist: commit: %w", err)
	}
	return nil
}

// GetSoloKaraokeTrack looks up a track by id across BOTH buckets (library
// first, then playlist) for the requery/manual-lyrics endpoints, mirroring
// GetTrack's role for board_tracks. Returns nil, nil if neither bucket has it.
func (r *PostgresRepo) GetSoloKaraokeTrack(ctx context.Context, trackID string) (*admin.SoloKaraokeTrack, error) {
	var t admin.SoloKaraokeTrack
	var status *string
	err := r.pool.QueryRow(ctx,
		`SELECT sk.id, sk.spotify_uri, sk.artist, sk.song, sk.album_art, sk.duration_ms, sk.created_at, tl.status
		   FROM solo_karaoke_library_tracks sk
		   LEFT JOIN track_lyrics tl ON tl.spotify_uri = sk.spotify_uri
		  WHERE sk.id = $1`, trackID).
		Scan(&t.ID, &t.SpotifyURI, &t.Artist, &t.Song, &t.AlbumArt, &t.DurationMs, &t.CreatedAt, &status)
	if err == nil {
		t.LyricsStatus = deref(status)
		return &t, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("store: get solo karaoke track %s (library): %w", trackID, err)
	}

	var t2 admin.SoloKaraokeTrack
	var status2 *string
	err = r.pool.QueryRow(ctx,
		`SELECT sk.id, sk.spotify_uri, sk.artist, sk.song, sk.album_art, sk.duration_ms, sk.position, sk.created_at, tl.status
		   FROM solo_karaoke_playlist_tracks sk
		   LEFT JOIN track_lyrics tl ON tl.spotify_uri = sk.spotify_uri
		  WHERE sk.id = $1`, trackID).
		Scan(&t2.ID, &t2.SpotifyURI, &t2.Artist, &t2.Song, &t2.AlbumArt, &t2.DurationMs, &t2.Position, &t2.CreatedAt, &status2)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("store: get solo karaoke track %s (playlist): %w", trackID, err)
	}
	t2.LyricsStatus = deref(status2)
	return &t2, nil
}
