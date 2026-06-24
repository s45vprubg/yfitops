-- 0001_init.sql — yfitops V2 initial schema (design_doc §2 Postgres, §11).
--
-- Two concerns share this DB:
--   1. Ephemeral game sessions + their players/scores/events (audit layer).
--      Live state lives in the engine/Redis; Postgres is the durable record.
--   2. Persistent track curation: tracks + board cells outlive any one session
--      so a curated board can be reused.
--
-- Players are keyed by device_fingerprint within a session so an attendee who
-- drops and rejoins resumes their exact score (§3.2).

BEGIN;

-- One game instance (§3, §11 game_sessions). Audit-persisted.
CREATE TABLE IF NOT EXISTS game_sessions (
    id                  TEXT PRIMARY KEY,
    created_at          BIGINT NOT NULL,          -- unix ms
    skip_threshold_pct  INT    NOT NULL DEFAULT 70,
    state               TEXT   NOT NULL DEFAULT ''
);

-- Ephemeral attendee sessions (§3.2). Keyed by (session, device_fingerprint)
-- so a reconnecting device resumes its score. id is the engine-issued playerID.
CREATE TABLE IF NOT EXISTS players (
    id                  TEXT PRIMARY KEY,
    session_id          TEXT NOT NULL REFERENCES game_sessions(id) ON DELETE CASCADE,
    device_fingerprint  TEXT NOT NULL,
    handle              TEXT NOT NULL DEFAULT '',
    score               INT  NOT NULL DEFAULT 0,
    banned              BOOLEAN NOT NULL DEFAULT FALSE,
    UNIQUE (session_id, device_fingerprint)
);
CREATE INDEX IF NOT EXISTS idx_players_session ON players(session_id);

-- Persistent curated songs (§11 track curation). Outlive sessions; referenced
-- by board cells. spotify_uri is the routing target for the stage device (§9).
CREATE TABLE IF NOT EXISTS tracks (
    id           TEXT PRIMARY KEY,
    spotify_uri  TEXT NOT NULL,
    artist       TEXT NOT NULL,
    song         TEXT NOT NULL,
    album_art    TEXT NOT NULL DEFAULT '',
    duration_ms  BIGINT NOT NULL DEFAULT 0
);

-- The Jeopardy grid for a session (§7). One row per (session, row, col) cell;
-- its track pool is the set of board_cell_tracks rows. The cell greys out only
-- when every track in its pool is played.
CREATE TABLE IF NOT EXISTS board_cells (
    session_id    TEXT NOT NULL REFERENCES game_sessions(id) ON DELETE CASCADE,
    row           INT  NOT NULL,
    col           INT  NOT NULL,
    category      TEXT NOT NULL DEFAULT '',
    daily_double  BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (session_id, row, col)
);

-- Pool membership: which tracks live in which cell, plus per-cell played flag
-- (a track can sit in cells across sessions, so "played" is cell-scoped, §7).
CREATE TABLE IF NOT EXISTS board_cell_tracks (
    session_id   TEXT NOT NULL,
    row          INT  NOT NULL,
    col          INT  NOT NULL,
    track_id     TEXT NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
    pos          INT  NOT NULL DEFAULT 0,           -- ordering within the pool
    played       BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (session_id, row, col, track_id),
    FOREIGN KEY (session_id, row, col)
        REFERENCES board_cells(session_id, row, col) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_bct_cell
    ON board_cell_tracks(session_id, row, col);

-- Final scores per player (SaveScore). Upsert keyed by (session, player).
CREATE TABLE IF NOT EXISTS score_log (
    session_id  TEXT NOT NULL REFERENCES game_sessions(id) ON DELETE CASCADE,
    player_id   TEXT NOT NULL,
    handle      TEXT NOT NULL DEFAULT '',
    score       INT  NOT NULL DEFAULT 0,
    updated_at  BIGINT NOT NULL DEFAULT 0,          -- unix ms
    PRIMARY KEY (session_id, player_id)
);
CREATE INDEX IF NOT EXISTS idx_score_log_score ON score_log(score DESC);

-- Append-only event audit (LogEvent). detail is jsonb so arbitrary structured
-- context (buzz, grade, kick, etc.) is queryable later.
CREATE TABLE IF NOT EXISTS event_log (
    id          BIGSERIAL PRIMARY KEY,
    session_id  TEXT NOT NULL REFERENCES game_sessions(id) ON DELETE CASCADE,
    kind        TEXT NOT NULL,
    detail      JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at  BIGINT NOT NULL                       -- unix ms
);
CREATE INDEX IF NOT EXISTS idx_event_log_session ON event_log(session_id);

COMMIT;
