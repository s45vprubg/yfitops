-- 0003_track_lyrics.sql — track lyric availability + play override.
--
-- Karaoke needs synced lyrics. We probe LRCLIB when a track is added and store
-- the result so the board builder can grey out lyric-less tracks and the engine
-- can skip them by default. An admin can force a lyric-less track playable via
-- lyrics_override.
--
--   has_synced_lyrics: NULL = not yet checked, TRUE/FALSE = probe result.
--   lyrics_override:   admin chose to allow this track even without lyrics.

BEGIN;

ALTER TABLE board_tracks ADD COLUMN IF NOT EXISTS has_synced_lyrics BOOLEAN;
ALTER TABLE board_tracks ADD COLUMN IF NOT EXISTS lyrics_override BOOLEAN NOT NULL DEFAULT FALSE;

COMMIT;
