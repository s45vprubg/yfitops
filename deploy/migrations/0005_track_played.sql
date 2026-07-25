-- 0005_track_played.sql — persist per-placement "played" (consumed) state.
--
-- The engine marks a track Played=true once it's been graded/revealed/consumed
-- (§7). That live state was never persisted, so a mid-game server restart
-- rebuilt every placement Played=false and re-offered already-consumed tracks.
-- We store the flag on the placement row (board_id, row, col, track_id) so a
-- reload via LoadBoardByID restores exactly what has been consumed. New Game
-- clears it back to FALSE for the whole board.

BEGIN;

ALTER TABLE board_layout_cell_tracks ADD COLUMN IF NOT EXISTS played BOOLEAN NOT NULL DEFAULT FALSE;

COMMIT;
