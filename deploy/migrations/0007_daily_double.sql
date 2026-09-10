-- 0007_daily_double.sql — the redesigned Daily Double (§7 sidenote): an
-- admin-configurable count of cells assigned at random each Start Game (never
-- persisted per-cell — see server/internal/game/engine.go assignDailyDoubles),
-- and a standalone curated song bucket per board that a Daily Double draws its
-- 5 choices from, independent of the grid's category tracks.
--
-- board_layout_cells.daily_double (0002_boards.sql) is now dead: the engine
-- overrides it in memory every Start Game rather than trusting the persisted
-- value. Left in place rather than dropped — lower-risk, separate cleanup.

BEGIN;

ALTER TABLE boards ADD COLUMN IF NOT EXISTS daily_double_count INT NOT NULL DEFAULT 2 CHECK (daily_double_count >= 0);

CREATE TABLE IF NOT EXISTS board_dd_bucket_tracks (
    id           TEXT PRIMARY KEY,
    board_id     TEXT NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
    spotify_uri  TEXT NOT NULL,
    artist       TEXT NOT NULL,
    song         TEXT NOT NULL,
    album_art    TEXT NOT NULL DEFAULT '',
    duration_ms  BIGINT NOT NULL DEFAULT 0,
    created_at   BIGINT NOT NULL,
    UNIQUE (board_id, spotify_uri)
);
CREATE INDEX IF NOT EXISTS idx_dd_bucket_board ON board_dd_bucket_tracks(board_id);

COMMIT;
