# QA Sweep 12 - Findings & Remediation

Date: 2026-10-22. Scope: diff against sweep 11's actual HEAD (`849f694`,
which already bundles sweep 11's own remediation) through the current HEAD —
11 commits covering the full batch of owner-reported fixes/features from
this session: End Game not stopping playback, the DD-decay question (no
code change needed, DD already shared the decay path), clearing the stale
DD result banner, the admin-reconnect pause/trusted-reveal resync fix, the
"nobody :(" misattribution fix, making Daily Double end only via admin or
the track finishing, a dev-cert expiry fix, a deploy Makefile fix, kick
messaging, genre-hint removal, the stage leaderboard/QR-overlap layout fix,
and the configurable transition delay + next-cell highlight + admin "queued"
toast batch. `git diff --stat 849f694..HEAD` = 24 files, 1083 insertions,
236 deletions.

Per the established re-scoping rule: hunt the delta, not the whole codebase.
Two hunters fan out read-only over non-overlapping surfaces (server engine
delta vs. frontend delta), each finding independently re-verified by
tracing the actual code and reproducing the described scenario against the
harness, fixes scoped to confirmed findings only, verified cold.

## Summary

- **8 findings across 2 surfaces: 8 CONFIRMED, 0 REFUTED. 0 HIGH, 7 MEDIUM,
  1 LOW.**
- **6 fixed directly. 2 (s12fe-01, s12fe-02) resolved as a side effect of
  one of the 6 — their root cause was identical to a server-side finding
  (s12-engine-01); no frontend change was needed once the server stopped
  skipping the broadcast.**
- Baseline confirmed clean before any hunting: `go build ./... && go vet
  ./... && go test ./...` and `RACE=1 qa/acid.sh` both green on the
  as-implemented working tree.

## Findings by surface

### Server engine delta — `server/internal/game/engine.go`

4 findings, all CONFIRMED, all fixed.

- **s12-engine-01 (MEDIUM)**: `smsgPendingPick`'s new clear-broadcast
  (`broadcastPendingPick(pendingPickData{})`) was only ever wired into
  `beginTransition`'s continuation, not the two OTHER pre-existing sites
  that silently zero `e.pendingPick`: `selectCell`'s admin-override reset
  and `onAdminKick`'s cell-picker-kick branch (a third site, `OnDisconnect`,
  had already been fixed earlier in this same session before this sweep's
  hunt ran). An admin override or a kick left stage's cell highlight and
  admin's "queued for next round" toast stuck pointing at a pick that was
  no longer pending — on the public stage projector in the override/kick
  case — until an unrelated future pick happened to overwrite it. **Fixed**:
  added the identical `e.broadcastPendingPick(pendingPickData{})` call to
  both remaining sites, mirroring `beginTransition`'s own pattern. Two
  existing tests (`TestPlayerPicker_AdminOverrideClearsPending`,
  `TestOnAdminKick_ClearsCellPicker`) extended with a
  `broadcastHasClearedPendingPick` assertion. Proven non-vacuous: reverting
  both new calls made both tests fail red with the exact expected message,
  then restored clean (`git diff` byte-identical).
- **s12-engine-02 (MEDIUM)**: `admin.endGame`'s new "stop whatever's
  playing" fix paused audio correctly even mid-Daily-Double, but never
  routed through the Daily-Double-aware payout path — every OTHER way a DD
  ends (natural track end, Force End Round, `admin.skipDailyDouble`,
  `admin.endDailyDouble`, even the disconnect/kick deadlock guard) awards
  via `finishDailyDouble()`; this one silently discarded the performer's
  earned payout with no error or indication to the admin. **Fixed**: when
  `e.state == StateDailyDouble && e.curTrack != nil`, call
  `e.finishDailyDouble()` before the generic pause block, which becomes a
  no-op for this case since `finishDailyDouble` already nils `curTrack` (no
  double pause/double award, safe even against an in-flight fade thanks to
  `fadeOutAndFinishDailyDouble`'s own `roundKey`/`curTrack` bail-out). New
  test `TestAdminEndGame_MidDailyDoublePaysOutBeforeStopping` pins the
  payout lands and pause count stays at exactly 1. Proven non-vacuous via a
  temporary `if false && ...` guard (red, then restored clean).
- **s12-engine-03 (MEDIUM)**: the admin-resync fix resends `e.lastPlayback`
  unconditionally, but `e.lastPlayback` was only ever cleared in
  `startTrack` — every OTHER way a track stops left the stale
  position/duration/paused snapshot sitting there for a reconnecting admin
  to be handed as if it were live. **Fixed**: added `e.lastPlayback = nil`
  at the 6 sites that clear/replace `e.curTrack` without an immediately
  following `startTrack`/`startDailyDoublePerformance` call:
  `ResetToLobby`, `beginTransition`'s continuation,
  `startDailyDoublePerformance` (clearing the PRIOR track's stale
  snapshot), `finishDailyDouble`, `endRound`, and `admin.endGame`'s own
  non-DD pause block (missed by the first pass since it never nils
  `curTrack` directly — found and fixed in a second pass after reconciling
  against the hunter's own suggested-fix list). Two new tests:
  `TestAdminReconnect_NoStalePlaybackAfterRoundEnds` (via
  `admin.endRound`) and an extension to `TestAdminEndGame_StopsPlayback`
  (via `admin.endGame`, non-DD). Both proven non-vacuous via revert/red/
  restore.
- **s12-engine-04 (MEDIUM)**: `adminViewEnvelope()`, now resent on every
  admin reconnect, computed `CurrentPoints` from `e.roundPool()`/
  `e.pausedAtMs` even when `e.curTrack` is a live Daily Double performance
  — a figure with zero relation to the real DD payout formula
  (`MaxPointsForRow * DailyDoubleMultiplier(avgStars)`, known only once
  rating is in). A reconnecting admin saw a plausible-looking but entirely
  fabricated "worth N pts" next to the DD track's real artist/song.
  **Fixed**: guarded the whole block on `e.state != StateDailyDouble` (DD's
  own `ddPerformer`/`ddResult` broadcasts already cover this state). New
  test `TestAdminView_BlankDuringDailyDouble` pins the envelope stays blank
  mid-performance. Proven non-vacuous via revert/red/restore.

### Frontend delta — `web/stage`, `web/admin`

4 findings, all CONFIRMED. 2 fixed directly, 2 resolved as a side effect.

- **s12fe-01 (MEDIUM, resolved by s12-engine-01)**: stage's `pendingPick`
  handler (`useGame.ts`) already correctly nulls `pendingPickCell` on a
  zero-value broadcast — the bug was entirely that the server never sent
  one from the admin-override path. Confirmed by code-reading after
  s12-engine-01 landed: no frontend change needed.
- **s12fe-02 (MEDIUM, resolved by s12-engine-01)**: same root cause, traced
  through `EvaluationPanel.tsx`'s `PendingPickToast` — its effect's
  else-branch (fade out + unmount) already fires correctly once
  `pendingPick` goes falsy. Confirmed by code-reading: no frontend change
  needed once the server stopped skipping the clear-broadcast.
- **s12fe-03 (LOW)**: `sendFullSync` never resent the live `e.pendingPick`
  on resync — a reconnecting stage/admin mid-pick (the between-rounds
  AND-gate window) got no highlight/toast at all until the next pick/clear
  fired on its own. **Fixed** (already landed earlier in this session,
  before this sweep's hunt ran; confirmed still correct): `sendFullSync`
  now sends the live `pendingPick` to a reconnecting stage/admin connection
  when `e.pendingPick != nil`.
- **s12fe-04 (LOW)**: the `DAILY_DOUBLE` no-performer fallback Board render
  (`web/stage/src/App.tsx`) omitted `highlightCell`, inconsistent with the
  other two Board render sites added in the same delta. **Fixed**: added
  `highlightCell={view.pendingPickCell ?? undefined}` for consistency.

## Verification (cold)

`go build ./... && go vet ./... && go test ./...` -> all green, both before
remediation and after. `RACE=1 qa/acid.sh` -> ACID PASSED (43/43 gates,
cold build/vet/test + `-race`, smoke green) both before and after.
`scripts/preflight.sh` -> PREFLIGHT PASSED (backend + all 3 frontends build
clean from a cold `npm install`), both before and after. Every new/extended
test proven non-vacuous via a temporary revert-build-red-restore cycle,
confirmed byte-clean via `git diff` after restore.

## Still open after sweep 12

None carried forward — all 8 findings resolved (6 fixed, 2 resolved as a
side effect of another fix).

## Resume checklist

- All of sweep 12's fixes are committed/pushed/deployed as of the end of
  this sweep (see `qa/HANDOFF.md`'s updated commit reference).
- The `e.lastPlayback` clearing pattern (s12-engine-03) is now scattered
  across 6 sites with no shared helper — if a future feature adds another
  way to stop/replace `e.curTrack`, it must remember this clear too. Worth
  factoring into one helper if a 7th site ever comes up.

## Sweep 13 recommendation

Low urgency on a hunt basis. Re-scope again per the established rule if
more features land. The deferred staging-verification recipe (10 DB-gated
tests that self-skip without live Postgres/Redis) remains unexercised this
sweep, as in every prior sweep — not newly introduced risk, just a
standing gap.
