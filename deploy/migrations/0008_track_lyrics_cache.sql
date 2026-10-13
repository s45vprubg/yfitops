-- 0008_track_lyrics_cache.sql — store resolved lyric text once per song.
--
-- 0003_track_lyrics.sql's has_synced_lyrics boolean on board_tracks was
-- derived from a live, un-cached LRCLIB probe and re-derived (overwritten) on
-- every "Check lyrics" rescan with no comparison against the prior value — a
-- transient LRCLIB error during a rescan collapsed to "no lyrics" and
-- silently clobbered a track that previously WAS known to have lyrics.
--
-- This table stores the actual LRC lyric text once per song, keyed by
-- spotify_uri (the only identity shared by board_tracks and the independent
-- board_dd_bucket_tracks table), so:
--   - a song fetched/entered once is never re-fetched on any other board or
--     in the Daily Double bucket,
--   - "resolved" is a terminal state the bulk rescan never touches again,
--   - "not_found" (LRCLIB confirmed no lyrics) is distinguished from
--     "pending" (never attempted, or last attempt hit a transient error) so a
--     network blip can never overwrite a confirmed-good or confirmed-absent
--     result.
--
--   status: 'pending' (default; no attempt yet, or last attempt errored —
--           last_error set in the latter case) | 'not_found' (LRCLIB
--           confirmed absent) | 'resolved' (lrc_text present).
--   source: 'lrclib' | 'manual', set only when status = 'resolved'.

BEGIN;

CREATE TABLE IF NOT EXISTS track_lyrics (
    spotify_uri TEXT PRIMARY KEY,
    lrc_text    TEXT,
    status      TEXT NOT NULL DEFAULT 'pending',
    source      TEXT,
    last_error  TEXT,
    checked_at  TIMESTAMPTZ,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE board_tracks DROP COLUMN IF EXISTS has_synced_lyrics;

COMMIT;
