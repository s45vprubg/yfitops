-- 0006_unique_placement.sql — enforce "a track lives in at most ONE cell per board".
--
-- QA sweep 1 finding store-3, deferred through sweeps 2 and 3 because it needs a
-- data migration and could not be rehearsed against real Postgres at the time.
--
-- 0002_boards.sql documents the invariant in a comment ("A track can only be in
-- one cell per board") but never enforced it: the primary key is
-- (board_id, row, col, track_id), so the SAME track in TWO different cells of the
-- same board is two distinct, perfectly legal rows. PlaceTrack's upsert targeted
-- that same PK, so placing an already-placed track into a different cell inserted
-- a duplicate instead of moving it. A duplicated track then occupies two cells,
-- and UnplacedTracks (which tests `track_id NOT IN (placed)`) still reports it as
-- placed — so the board silently plays one song twice.
--
-- Two steps, both idempotent: scripts/dev-up.sh reapplies every migration on each
-- launch, so re-running this must be a no-op.

BEGIN;

-- Step 1 — dedup existing data, keeping exactly one row per (board_id, track_id).
--
-- Tiebreak order is deliberate:
--   played DESC  — if any copy was already played, KEEP that one. Keeping an
--                  unplayed copy instead would resurrect the track and let the
--                  game serve it a second time, which is the exact bug being
--                  fixed. Losing a played flag is unrecoverable at game time.
--   "row", col   — deterministic: the topmost-leftmost cell wins, so the result
--                  does not depend on physical row order.
--   pos          — final tiebreak within a cell.
--
-- No-op when there are no duplicates.
DELETE FROM board_layout_cell_tracks blct
      USING (
        SELECT board_id, "row", col, track_id,
               row_number() OVER (
                 PARTITION BY board_id, track_id
                 ORDER BY played DESC, "row", col, pos
               ) AS rn
          FROM board_layout_cell_tracks
      ) dup
      WHERE blct.board_id = dup.board_id
        AND blct."row"    = dup."row"
        AND blct.col      = dup.col
        AND blct.track_id = dup.track_id
        AND dup.rn > 1;

-- Step 2 — enforce it going forward. A unique INDEX (not a table constraint) so
-- IF NOT EXISTS makes re-application safe; it still serves as an ON CONFLICT
-- inference target, which PlaceTrack now relies on to MOVE a track rather than
-- duplicate it.
CREATE UNIQUE INDEX IF NOT EXISTS uq_blct_board_track
    ON board_layout_cell_tracks (board_id, track_id);

COMMIT;
