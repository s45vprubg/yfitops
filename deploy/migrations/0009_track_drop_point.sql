-- 0009_track_drop_point.sql — store a song's marked "best part" position
-- once per song, so the control room's "Jump to Drop" button can seek there.
--
-- Mirrors track_lyrics (0008) exactly: one row per song, keyed by
-- spotify_uri (the only identity shared by board_tracks and the independent
-- board_dd_bucket_tracks table) so a mark made once is reused across every
-- board/Daily Double placement of that song.
--
-- source distinguishes a human-set mark ("manual", the only writer today)
-- from a future automated guess (e.g. "youtube_heatmap" / "lyrics_chorus").
-- This is deliberate "piping for automation": a later job can write rows
-- with a different source value through the exact same SetDropPoint method,
-- no schema change needed. The one rule that matters, learned from the
-- track_lyrics rescan-clobber bug: an automated write must never overwrite
-- an existing human-set row. SetDropPoint enforces this itself (see
-- store/admin_store.go), not left to callers to remember.

BEGIN;

CREATE TABLE IF NOT EXISTS track_drop_point (
    spotify_uri   TEXT PRIMARY KEY,
    drop_point_ms BIGINT NOT NULL,
    source        TEXT NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMIT;
