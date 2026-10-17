-- 0010_solo_karaoke.sql — standalone data for Solo Karaoke mode: a second,
-- independent game mode (straight playback, no guessing/scoring) reachable
-- as a third admin tab.
--
-- Two standalone tables, NOT FK'd to boards — the trivia grid's board
-- metadata (cols, daily_double_count, layout cells) is irrelevant here; Solo
-- Karaoke has exactly one global Library (an unordered holding pool, mirrors
-- the Board Builder's unplaced-track holding area) and exactly one global
-- Playlist (an ordered queue the engine actually plays from, mirrors the
-- Daily Double bucket's "independent curated pool" shape but adds a
-- position column since playback order matters here and didn't there).
--
-- Lyrics gating is intentionally soft for both tables (unlike board_tracks'
-- optional override or board_dd_bucket_tracks' hard reject): a track lacking
-- resolved lyrics may still be added to either bucket; the lyrics status is
-- only ever used by the admin UI to flag it, never to block the add. Lyrics
-- themselves still live in the shared global track_lyrics cache (0008),
-- joined by spotify_uri exactly like the other two track tables.

BEGIN;

CREATE TABLE IF NOT EXISTS solo_karaoke_library_tracks (
    id           TEXT PRIMARY KEY,
    spotify_uri  TEXT NOT NULL UNIQUE,
    artist       TEXT NOT NULL,
    song         TEXT NOT NULL,
    album_art    TEXT NOT NULL DEFAULT '',
    duration_ms  BIGINT NOT NULL DEFAULT 0,
    created_at   BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS solo_karaoke_playlist_tracks (
    id           TEXT PRIMARY KEY,
    spotify_uri  TEXT NOT NULL UNIQUE,
    artist       TEXT NOT NULL,
    song         TEXT NOT NULL,
    album_art    TEXT NOT NULL DEFAULT '',
    duration_ms  BIGINT NOT NULL DEFAULT 0,
    position     INT NOT NULL,
    created_at   BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_solo_karaoke_playlist_position ON solo_karaoke_playlist_tracks(position);

COMMIT;
