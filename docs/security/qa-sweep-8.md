# QA Sweep 8 - Findings & Remediation

Date: 2026-10-13/14. Scope: re-scoped per the established rule ("diff against
the last sweep's HEAD first"), against sweep 7's HEAD (`ad4d026`). Covers 3
commits that landed before hunting started: `79abe40` (stage guesser-banner
reposition, cosmetic CSS only), `841abe2` (player-driven cell picking — a new
AND-gate state machine in `engine.go`, a new mobile screen, shared protocol
additions), and `3c3d508` (Daily Double graceful fade-out + the first
volume-control capability in this codebase, plus relocating the standalone
"Mixer" tab's content into the Control Room as "Audio Settings"). `git diff
--stat ad4d026..HEAD` = 18 files, ~1,650 insertions.

Method unchanged: hunters fan out read-only over non-overlapping surfaces,
independent skeptical validators try to refute each finding, only
CONFIRMED/PARTIAL findings get fixed, each fix gets its own adversarial pass
and (where a gate exists) a revert-run-restore proof, verify cold. Layered
order: Code (`engine.go`/`spotify.go`) validated and fixed before UI (admin,
mobile, stage frontends + shared protocol).

## Summary

- **15 findings reported across 2 layers, 15 CONFIRMED, 0 REFUTED.**
- **3 HIGH, 4 MEDIUM, 8 LOW. Zero CRITICAL.**
- **All 15 fixed and independently re-verified from a cold state**: `go
  build/vet/test -race ./...` clean, `tsc --noEmit` clean in all three
  frontends, `qa/smoke.sh`/`qa/acid.sh`/`scripts/preflight.sh` all green.
- One LOW (s8eng-06) deliberately deferred — a pre-existing
  unbounded-submit-on-shutdown pattern shared by `beginTransition`'s own
  goroutine, not newly introduced by this diff. Same precedent as sweep 6's
  deferred `tracks.go` siblings: not re-litigated, just not in scope.
- Separately, outside the hunt pipeline: a real production bug surfaced by
  an actual live stress test against `yfitops.us` (admin got booted during
  batched player joins toward ~500 concurrent). Diagnosed and fixed with the
  same rigor as a sweep finding (see below) — code-only forensics, since
  production logs from the test were unrecoverable by the time it was
  investigated.

## Findings by surface

### Code layer

**`server/internal/game/engine.go` + `server/internal/spotify/spotify.go`**
(8 findings, 3 HIGH):

- **Cell-picker kick/disconnect gaps (HIGH):** a cell-picker who got
  kicked or banned mid-pick could keep steering the board — kick never
  force-disconnected the transport for this specific path, and neither
  `onAdminKick` nor `OnDisconnect` cleared `cellPicker`/`pendingPick` state.
  **Fixed**: `onPlayerSelectCell` now rejects a banned player outright;
  `onAdminKick`/`OnDisconnect` both clear the outstanding pick so a kicked
  picker can never resolve the AND-gate.
- **Daily Double fade staleness (HIGH):** the fade-ending guard (`ddEnding
  bool`) was a single flag, not scoped to a specific performance. A
  Force-Ended DD's leftover fade (still running its 3-second ramp in a
  goroutine) could later block, or worse, misfire against a completely
  unrelated DD that started after it. **Fixed**: replaced with
  `ddFadingRoundKey string`, compared against the live `e.roundKey` at
  every checkpoint — a fade from an ended performance can no longer affect
  a later one.
- **Fade's volume-restore race (HIGH):** the restore-after-fade step used a
  snapshot of `volumePct` captured when the fade *started*, not the live
  value. A mid-fade `admin.setVolume` call from the operator's own slider
  was silently clobbered back to the stale pre-fade level once the fade's
  `defer` fired. **Fixed**: the restore now reads `e.volumePct` live, at
  restore time, inside the same `defer` — satisfying the user's original
  hard requirement ("never get stuck at a level we can't reset") against a
  race the original implementation hadn't considered.
- 4 further MEDIUM/LOW findings in the same two files: the fade curve's
  step loop had an off-by-one (started at `dailyDoubleFadeSteps - 1`
  instead of `dailyDoubleFadeSteps`, clipping the first step); `SetVolume`
  calls weren't serialized against each other (slider + fade-down + restore
  could race as independent goroutines hitting Spotify's API out of order)
  — fixed with a single-writer `volumeCmds` channel + `runVolumeWriter`
  goroutine; two smaller re-entrancy edges in the AND-gate's state cleanup.
- **s8eng-06 (LOW, DEFERRED):** `fadeOutAndFinishDailyDouble`'s goroutine
  calls `e.submit` on a channel that's closed on engine shutdown with no
  guard — an unbounded-submit-on-shutdown pattern. Pre-existing: identical
  to `beginTransition`'s own continuation goroutine, not introduced by this
  diff. Not fixed, per the same scoping precedent as sweep 6's deferred
  `tracks.go` siblings — would apply to both call sites identically
  whenever someone is next in this area.

All 8 fixed and independently re-verified cold under `-race`.

### UI layer

Scope: `web/admin/src/{App.tsx, components/BoardPanel.tsx,
components/EvaluationPanel.tsx, components/AudioSettingsPanel.tsx,
useAdmin.ts, components/TelemetryPanel.tsx}`, `web/mobile/src/{App.tsx,
lib/useGame.ts, screens/PickCellScreen.tsx}`,
`web/stage/src/views/ActiveRound.tsx`, `web/shared/protocol.ts` — the full UI
delta since sweep 7, plus the telemetry-pagination feature layered into the
same working tree.

**`web/admin/src/`** (3 findings, all MEDIUM): the volume slider fired a
real Spotify `SetVolume` API call on every drag tick (dozens per drag,
producing an audible stuttering ramp and burning requests against Spotify's
rate limits) — **fixed** with the same local-state-while-dragging + 120ms
debounce pattern the file's own `thresh` control already used
(`localVolume`/`volumeTouched`); the Mixer→AudioSettingsPanel relocation had
silently dropped an explanatory sentence and two divider-label suffixes from
the original "Mixer" tab, violating the user's own explicit "just moved,
nothing else" requirement — **fixed** by diffing against the pre-move file
(`git show ad4d026:...SettingsPage.tsx`) and restoring the exact lost text;
the telemetry pagination's Prev/Next buttons operated on raw `page` state
instead of the derived `clampedPage` (used everywhere else to guard against
a stale page number after the connection list shrinks), so Prev/Next could
desync from what was actually on screen — **fixed** to operate on
`clampedPage` consistently.

**`web/mobile/src/`** (2 findings, both LOW): `PickCellScreen`'s cell-tap
had no recovery timeout — a lost or rejected pick (dropped frame, race with
admin override) could lock the picker's screen indefinitely with no way
out — **fixed** with an 8-second recovery timer mirroring
`DailyDoubleContestantScreen`'s existing `DECISION_TIMEOUT_MS` pattern;
`selectCell`'s send call was fire-and-forget with no `.catch`, and a brief
wrong-screen flash could appear on the picker's own device right as their
turn started (a resync landing between the pick-eligibility signal and the
screen transition) — **fixed** with an added `.catch` and a 150ms
`pickerSettling` placeholder render.

**`web/stage/src/views/ActiveRound.tsx` + `web/shared/protocol.ts`** (2
findings, both LOW): the guesser-name banner's first implementation used
absolute positioning laid over the existing content, which could overlap
the centered reveal lines at some viewport heights — **fixed** by
restructuring to a genuinely in-flow layout (a fixed-min-height row above a
separate flex-1 content block), matching `Karaoke.tsx`'s actual existing
structure rather than introducing a new overlay pattern; `protocol.ts`'s
doc comment for the new `pickerStatus` message omitted `pickerID` from its
listed fields — **fixed**, comment-only.

**Missing admin visibility (MEDIUM, caught during UI validation, not from
the original hunter list):** the server already broadcast `pickerStatus`
whenever a cell pick was outstanding, but `useAdmin.ts` had no handler for
it at all — the admin had zero visibility into "the game is stalled waiting
on a player to pick," defeating the whole point of the admin override
escape hatch (which already existed and worked, just was never surfaced).
**Fixed**: `useAdmin.ts` gained the `pickerStatus` field + handler;
`EvaluationPanel.tsx` gained a `PickerStatusBanner` showing "waiting on
{handle} to pick a cell" whenever a pick is outstanding — judged the
highest-leverage fix in the UI layer, since it makes an existing safety
mechanism actually discoverable rather than adding a new one.

All 7 UI-layer findings fixed; `tsc --noEmit` clean in all three frontends,
production builds clean.

## Separately: a real production bug found via live stress testing

Not from the hunt pipeline — the user ran an actual stress test against
`yfitops.us` (up to ~500 concurrent users, added in batches of 25) and got
booted from the admin panel on every batch. Investigated and fixed with the
same rigor as a sweep finding:

**Root cause**: `onHello` called `broadcastScoreboard`/`broadcastBoard`/
`broadcastTelemetry` synchronously and unconditionally on EVERY join — the
identical O(P²)-floodable pattern already fixed once for `onHeartbeat`
(`telemetryDirty` + a coalescing ticker), never applied to the join path. A
burst of 25 joins meant 25 full population-wide broadcast cycles
back-to-back on the single Run-loop goroutine that also services admin's
own messages, worsening as player count climbed toward 500. **Fixed** by
adding `scoreboardDirty`/`boardDirty` alongside the existing
`telemetryDirty`, flushed by the same 1-second ticker — a burst of N joins
now costs one flush, not N. New regression test
`TestJoinBurst_CoalescesScoreboardBoardTelemetry`, proven to fail against
the pre-fix code (75/50/25 synchronous broadcast frames for a 25-join
burst) and pass with the fix (0, deferred to the ticker).

**Likely proximate cause of the actual "booted out" symptom**: separately,
`TelemetryPanel.tsx` had zero memoization anywhere — every broadcast forced
a full unmemoized re-render of a list scaling toward 500 rows. Most likely
sequence: the admin tab visibly froze during each batch-add, the operator
reloaded, and because the admin secret is deliberately never persisted
client-side (an earlier intentional security fix), any reload presents a
fresh login screen — not a true network disconnect. **Fixed**: memoized
`TelemetryPanel` itself plus `Row` (with a value-based comparator, since the
server sends a fresh object graph on every broadcast regardless of whether
a given row's values changed).

Production logs from the actual stress test were unrecoverable (wiped by
this session's own later redeploys) — diagnosed from code alone, not log
forensics. No way to confirm the memoization theory is the *complete*
explanation vs. the server-side fix alone; both are real, independently
justified fixes regardless.

**Also shipped in the same pass, not a bug fix:** telemetry pagination
(100 connections/page, flagged/cheat-suspected connections pinned to the
top regardless of sort, highest-score-first within each group) — requested
separately by the user once the connection list was expected to scale
toward 500, not a QA finding.

## Gotchas learned (ranked, read before sweep 9)

1. **A relocation task ("just move it, nothing else") is still a diff, and
   deserves the same "did anything change" scrutiny as a new feature.** The
   Mixer→AudioSettingsPanel move silently dropped real explanatory text
   that the user had never asked to lose. Catch this by diffing the moved
   content against its pre-move original, not just confirming the new
   location renders.
2. **A `pickerStatus`-shaped bug class: broadcasting new state is not the
   same as making it visible.** The engine had this message type working
   and tested server-side; the admin frontend simply never grew a handler
   for it. Any new broadcast type added alongside a new engine feature
   needs an explicit "does the admin actually see this" check, not just a
   wire-protocol check.
3. **A single flag guarding "is X in progress" needs an identity, not just
   a boolean, the moment X can have more than one logical instance over
   time.** `ddEnding bool` was exactly this trap — fine when there's only
   ever one Daily Double in flight at a time in the obvious sense, wrong
   the instant a stale instance (Force-Ended, but its goroutine still
   running) can outlive the state it was tracking. `ddFadingRoundKey`
   compared against the live `roundKey` is the general fix shape:
   whenever a flag means "an async operation for THIS X is active," key it
   by X's identity, not just true/false.
4. **A snapshot taken when an async operation STARTS is stale the moment
   anything else can mutate the same value before it FINISHES.** The
   volume-restore bug existed because "restore to what it was before the
   fade" was implemented as "remember the value at fade-start," not "read
   the value live at restore-time" — the two are only equivalent if nothing
   else can touch that value in between, which a 3-second fade window
   gives plenty of room for.

## Verification (cold)

- `go build ./... && go vet ./... && go test -count=1 -race ./...` — clean,
  no regressions.
- `npx tsc --noEmit` clean in all three frontends (`stage`, `admin`,
  `mobile`); production builds clean in all three.
- `qa/smoke.sh` → SMOKE OK. `qa/acid.sh` → ACID PASSED, 43/43 gates present
  and executing (gate count unchanged from sweep 7 — this sweep's new Go
  regression test, `TestJoinBurst_CoalescesScoreboardBoardTelemetry`, is
  part of the standard `go test` run already covered by acid's existing
  "cold go test" gate, not a new named acid gate).
- `scripts/preflight.sh` → PREFLIGHT PASSED (clean reinstall + production
  build, all three frontends; standard DB-gated-tests-self-skip warning,
  unrelated to this sweep).
- Contract-file check: `protocol.go`, `state.go`, `scoring.go`,
  `latency.go`, `nonce.go`, `config.go` — none touched by any fix.
- Every Go-layer fix verified via a revert-run-restore cycle against its
  regression test; UI-layer fixes (no frontend test runner, unchanged
  structural gap carried since sweep 4) verified via `tsc --noEmit` +
  production build + hand-traced logic, consistent with every prior sweep
  that touched frontend code.

## Known gaps (unchanged, not re-litigated)

Same structural items as sweeps 4-7: no frontend test runner (this sweep's
7 UI findings have no automated regression gate, same as every prior
UI-touching sweep), and the `client.ts` locked-contract question. Neither
is a hunting problem; both need owner bandwidth for a decision.

## Sweep 9 recommendation

Low-to-moderate urgency. All three HIGH findings this sweep were in code
that had not yet been exercised at a real event at the time they were
found (the cell-picker and DD-fade features were both brand new); the one
confirmed *live* production issue (the join-burst freeze) is already fixed
and verified separately. Re-scope again per the same rule if more features
land: diff against this sweep's HEAD first. The two recurring structural
items (frontend test runner, `client.ts` contract status) remain the
highest-value moves whenever there's owner bandwidth for a decision rather
than a hunt — now five sweeps running without a decision.
