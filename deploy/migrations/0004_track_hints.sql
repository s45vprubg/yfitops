-- 0004_track_hints.sql — pre-reveal hints (release year + primary genre).
--
-- Surfaced on the stage as gameplay hints (not worth points): genre first, then
-- year, then the letter reveal. Year comes from the Spotify release date; genre
-- from the track's primary artist genres (best-effort — may be empty).

BEGIN;

ALTER TABLE board_tracks ADD COLUMN IF NOT EXISTS year  INT  NOT NULL DEFAULT 0;
ALTER TABLE board_tracks ADD COLUMN IF NOT EXISTS genre TEXT NOT NULL DEFAULT '';

COMMIT;
