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

// soloKaraokePlaylistPositionLockKey serializes every operation that reads
// or assigns solo_karaoke_playlist_tracks.position — AddSoloKaraokePlaylistTrack
// (MAX(position)+1) and ReorderSoloKaraokePlaylist (full renumber) both take
// this advisory lock first (s10api-01/s10api-02/s10ui1-03). A plain
// INSERT...SELECT MAX(...) does NOT serialize against a concurrent one under
// Postgres's default READ COMMITTED isolation — two near-simultaneous adds
// (or an add racing a reorder) could otherwise both compute the same max and
// collide on position, which has no UNIQUE constraint backing it. The lock
// is released automatically on commit/rollback (pg_advisory_xact_lock).
const soloKaraokePlaylistPositionLockKey = 827364501

// AddSoloKaraokePlaylistTrack appends a song to the end of the Solo Karaoke
// Playlist — position = current max + 1 (0 if the playlist is empty).
func (r *PostgresRepo) AddSoloKaraokePlaylistTrack(ctx context.Context, t *admin.SoloKaraokeTrack) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: add solo karaoke playlist track: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, soloKaraokePlaylistPositionLockKey); err != nil {
		return fmt.Errorf("store: add solo karaoke playlist track: lock: %w", err)
	}

	tag, err := tx.Exec(ctx,
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
	return tx.Commit(ctx)
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

// ReorderSoloKaraokePlaylist atomically renumbers EVERY row to 0..n-1 within
// one transaction, keyed off a server-side read of the current full id set
// rather than trusting orderedIDs to be a complete, current permutation.
//
// The original version only updated rows present in orderedIDs and left any
// row NOT present at its old position — which could silently collide with a
// position just assigned to a different row (s10api-01/s10ui1-03: a second
// admin tab's stale/partial orderedIds list, or a track added by another tab
// between that tab's last refresh and this reorder, both reproduce this).
// There is no UNIQUE constraint on position, so the collision was silent.
//
// Fixed by always producing a total order: known ids from orderedIDs go
// first (in the given order, deduped, dropping any id that doesn't actually
// exist), then every remaining existing row is appended afterward preserving
// its prior relative order — a superset-safe renumber that can never leave
// two rows at the same position, regardless of how incomplete orderedIDs is.
func (r *PostgresRepo) ReorderSoloKaraokePlaylist(ctx context.Context, orderedIDs []string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: reorder solo karaoke playlist: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, soloKaraokePlaylistPositionLockKey); err != nil {
		return fmt.Errorf("store: reorder solo karaoke playlist: lock: %w", err)
	}

	rows, err := tx.Query(ctx, `SELECT id FROM solo_karaoke_playlist_tracks ORDER BY position ASC`)
	if err != nil {
		return fmt.Errorf("store: reorder solo karaoke playlist: read existing: %w", err)
	}
	var existing []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("store: reorder solo karaoke playlist: scan existing: %w", err)
		}
		existing = append(existing, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("store: reorder solo karaoke playlist: rows: %w", err)
	}
	rows.Close()

	existingSet := make(map[string]bool, len(existing))
	for _, id := range existing {
		existingSet[id] = true
	}

	seen := make(map[string]bool, len(orderedIDs))
	final := make([]string, 0, len(existing))
	for _, id := range orderedIDs {
		if !existingSet[id] || seen[id] {
			continue // unknown or duplicate id: silently skipped, same as before
		}
		seen[id] = true
		final = append(final, id)
	}
	for _, id := range existing {
		if !seen[id] {
			final = append(final, id) // not in orderedIDs: appended after, never left at a stale position
		}
	}

	for i, id := range final {
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
