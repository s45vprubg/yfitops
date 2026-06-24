-- 0002_boards.sql — Independent named boards with board-scoped track libraries.
--
-- Boards are curated independently of game sessions. A board is attached to a
-- session at game time via game_sessions.board_id. Tracks belong to a single
-- board (deduped per-board by spotify_uri). Deleting a board cascade-deletes
-- its tracks, layout cells, and cell-track placements.

BEGIN;

-- Independent named boards.
CREATE TABLE IF NOT EXISTS boards (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL DEFAULT '',
    cols        INT  NOT NULL DEFAULT 1 CHECK (cols >= 1 AND cols <= 8),
    created_at  BIGINT NOT NULL,
    updated_at  BIGINT NOT NULL
);

-- Board-scoped track library. Each track belongs to exactly one board.
-- UNIQUE(board_id, spotify_uri) enforces per-board dedup.
CREATE TABLE IF NOT EXISTS board_tracks (
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
CREATE INDEX IF NOT EXISTS idx_board_tracks_board ON board_tracks(board_id);

-- Board layout cells. Always 5 rows (scoring contract), 1-8 columns.
CREATE TABLE IF NOT EXISTS board_layout_cells (
    board_id      TEXT NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
    row           INT  NOT NULL CHECK (row >= 1 AND row <= 5),
    col           INT  NOT NULL CHECK (col >= 1 AND col <= 8),
    category      TEXT NOT NULL DEFAULT '',
    daily_double  BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (board_id, row, col)
);

-- Track placement within cells. A track can only be in one cell per board.
CREATE TABLE IF NOT EXISTS board_layout_cell_tracks (
    board_id     TEXT NOT NULL,
    row          INT  NOT NULL,
    col          INT  NOT NULL,
    track_id     TEXT NOT NULL REFERENCES board_tracks(id) ON DELETE CASCADE,
    pos          INT  NOT NULL DEFAULT 0,
    PRIMARY KEY (board_id, row, col, track_id),
    FOREIGN KEY (board_id, row, col)
        REFERENCES board_layout_cells(board_id, row, col) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_blct_cell
    ON board_layout_cell_tracks(board_id, row, col);

-- Link boards to game sessions. Nullable for backward compatibility.
ALTER TABLE game_sessions ADD COLUMN IF NOT EXISTS board_id TEXT REFERENCES boards(id) ON DELETE SET NULL;

COMMIT;
