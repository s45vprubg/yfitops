# QA Sweep 11 - Findings & Remediation

Date: 2026-10-17. Scope: diff against sweep 10's HEAD (`d8e5147`) through the
uncommitted working tree — a small, two-feature delta from this same session,
not yet committed: (1) removal of a scoring mechanic (the "one-field-first"
reveal gate that silently halved a fully-correct guess's payout, which
contradicted the owner's spec and was the real cause of a reported "points
look halved when the admin picks a cell" bug), and (2) a fix for a separate
reported bug (a backgrounded `/play` tab could permanently miss its one-shot
"you're the picker, pick a cell" broadcast) via a new `visibilitychange`
listener in all three frontends that resyncs or accelerates a stalled
reconnect on tab refocus. Plus a trivial, unrelated CSS change (stage
leaderboard/QR moved from the bottom corners to the top corners). `git diff
--stat HEAD` = 10 files, 154 insertions, 166 deletions.

Per the project's established re-scoping rule (sweeps 4/5/6/7/8/9/10): hunt
the delta, not the whole codebase, since the last sweep's HEAD was fully
committed/pushed/deployed. Method unchanged: two hunters fan out read-only
over non-overlapping surfaces (server scoring delta vs. frontend resync +
CSS delta), every finding independently re-verified by hand-tracing the
actual code, high-severity claims re-verified a second time by the
orchestrator directly (not just trusted from the hunter's report), fixes
scoped to confirmed findings, verified cold with a prove-the-gate-can-fail
probe on the one new test that could carry one.

## Summary

- **5 findings across 2 surfaces: 5 CONFIRMED, 0 REFUTED. 1 HIGH-reported
  (downgraded to MEDIUM on independent verification — not server-exploitable),
  1 MEDIUM, 1 MEDIUM, 2 LOW.**
- **4 fixed and verified cold. 1 LOW deferred** (a narrow, currently-inert
  handshake-timing race with no reachable bad outcome — no crash, no spam, no
  double-connect).
- Baseline confirmed clean before any hunting: `RACE=1 qa/acid.sh` → ACID
  PASSED (43/43 gates, cold build/vet/test + `-race`, smoke green) on the
  as-implemented working tree.

## Findings by surface

### Server scoring delta — `server/internal/game/{engine.go,reveal.go,pool_test.go}`

2 findings, both CONFIRMED, both fixed.

- **s11-engine-delta-01 (MEDIUM)**: deleting the one-field-first gate also
  deleted the only test that ever drove `revealTick` with an asymmetric
  artist/song reveal (`forceHalveGate`). The new `"partial then correct"`
  case only exercises the `gradePartial` reduced-pool path, not asymmetric
  reveal — a future reintroduction of one-field-first halving (a bad merge
  resurrecting `pointFactor`, or a differently-named replacement) would pass
  `go test` undetected. **Fixed**: two new tests,
  `TestPool_AsymmetricRevealDoesNotHalve` (fully reveals the artist, confirms
  the pool and the final payout are unaffected, and that the separate
  lockout gate does NOT spuriously fire) and
  `TestPool_LockoutGateStillFiresWithoutHalveGate` (fully reveals both
  fields, confirms the lockout gate still fires on its own, unrelated
  trigger condition now that its neighbor is gone). Proven non-vacuous: a
  temporary `pts = pts / 2` probe inserted into `gradeCorrect` made
  `TestPool_AsymmetricRevealDoesNotHalve` fail red (`awarded = 95, want
  190`), then the probe was removed and the diff confirmed byte-clean via
  `git diff`.
- **s11-engine-delta-02 (LOW)**: `gofmt -l` flagged both `engine.go` (a
  stray double blank line left by the `halvePoints()` deletion) and
  `reveal.go` (struct-alignment drift from removing the `fieldHalved` field)
  — cosmetic only, not caught by build/vet/test/preflight/acid since none
  run `gofmt -l`. **Fixed**: `gofmt -w` on both files; re-ran `gofmt -l`
  across the whole Go tree afterward — only 4 pre-existing, out-of-scope
  files remain flagged (`hub.go`, `ws.go`, `middleware.go`, `gemini.go`, none
  touched by this delta), left alone.

Hunt priorities that came back explicitly clean (verified, not assumed, no
finding filed): removal correctness (zero dangling references to
`pointFactor`/`livePool`/`fieldHalved`/`halvePoints`/`artistRemaining`/
`songRemaining` anywhere in the Go tree), Daily Double isolation
(`startDailyDoublePerformance`'s remaining `e.partial` reset reads clean on
its own), and the lockout gate's isolation (its trigger condition and
control flow are byte-for-byte unaffected by the deleted gate sitting next
to it, since the deleted block had no early return).

### Frontend resync + CSS delta — `web/mobile/src/lib/useGame.ts`, `web/stage/src/net/useGame.ts`, `web/admin/src/useAdmin.ts`, `web/stage/src/{App.tsx,components/CornerJoin.tsx,components/ScoreOverlay.tsx}`

3 findings, all CONFIRMED, 2 fixed, 1 deferred.

- **s11fe-01 (reported HIGH, independently re-verified and downgraded to
  MEDIUM)**: the new visibility-triggered resync, by design, can re-deliver
  the CURRENT `"state"` message with no real transition (a resync mid-round
  repeats `"ROUND_ACTIVE"` while the client's own previous local state is
  already `"ROUND_ACTIVE"`). Mobile's `"state"` handler used to infer "this
  must be a fresh round" from "previous local state wasn't ADJUDICATE" —
  true for a genuine BOARD/TRANSITION→ROUND_ACTIVE transition, but *also*
  true for this same-value repeat, which wiped `judgedThisRound`/
  `buzzedAndLost`/`lastVerdict` for a player already locked out this round,
  reachable by nothing more than backgrounding and refocusing a tab mid-round
  after being judged. Severity correction: independently traced
  `addContender` (`engine.go:1524`) and confirmed the server enforces
  `GuessedThisTrack` completely independently of client UI state, so this
  cannot actually let a locked-out player re-buzz and win — it is a
  confusing/incorrect UI state reset (their lockout banner would wrongly
  disappear), not a fairness exploit. Also independently confirmed stage's
  own `"state"` handler does NOT share this bug class (its branches are
  idempotent on a same-value repeat) — mobile-only. **Fixed**: the
  "fresh round" branch now requires `v.state !== "ROUND_ACTIVE"`, so a
  same-value repeat falls through and leaves the judged/locked-out flags
  untouched, while every genuine transition still resets them exactly as
  before.
- **s11fe-02 (MEDIUM)**: same root cause, same file — `pickerSettling` and
  its 150ms settle-timer were unconditionally re-armed on every `"state"`
  message naming BOARD/KARAOKE, including a resync repeat of the SAME
  phase, causing a visible loading-flash on every tab refocus during
  board/karaoke phases even though nothing had changed. **Fixed**: both the
  `pickerSettling` flag and the timer rearm are now gated on an actual
  phase change (`v.state !== d.state`), captured via the same previous-state
  snapshot the s11fe-01 fix already needed.
- **s11fe-03 (LOW, deferred)**: a narrow, currently-inert race where
  `clientRef.current`/`connectedRef.current` can read truthy slightly before
  the client's stream writer actually exists, causing a swallowed/no-op
  resync send during handshake. No crash, no spam, no double-connect — not
  fixed this sweep.

Also checked and found no live issue (no finding filed): the existing
reconnect-overlap guards (`establishingRef`, `establishing`) correctly cover
the new `visibilitychange` call paths in mobile and stage — no double-connect
reachable. Admin's `sendAction("resync")` safely no-ops in every pre-auth/
mid-handshake state. The CSS corner-move changes were independently
re-verified (not just trusted from the implementing session's own claim):
grepped every `fixed`/`absolute` positioned element in `web/stage/src` —
no live collision with the connection dot's new `bottom-4 left-4`, the
leaderboard's `top-4 left-4`, or the QR box's `top-5 right-5`; the one
near-collision candidate (`SpotifyBanner.tsx`, also `fixed top-4 right-4`)
is dead code, never imported or rendered anywhere, so it has no runtime
effect.

## Verification (cold)

- `RACE=1 qa/acid.sh` → **ACID PASSED** (43/43 gates, cold build/vet/test and
  `-race` all green, smoke green) — both before hunting (baseline) and after
  all fixes landed.
- `go test ./internal/game/... -run 'TestPool_AsymmetricRevealDoesNotHalve|TestPool_LockoutGateStillFiresWithoutHalveGate'`
  — both new tests pass; the first proven non-vacuous via a revert-probe
  (temporary `pts/2` in `gradeCorrect` → red → reverted → confirmed clean via
  `git diff`).
- `scripts/preflight.sh` → **PREFLIGHT PASSED** (clean npm install + prod
  build, all 3 frontends; backend build/vet/test clean). One transient `go
  test` failure (`TestDailyDoubleFade_RestoreStillAttemptedWhenSetVolumeErrors`)
  on the first preflight run was investigated and ruled a flake, not a
  regression: 5/5 isolated reruns and 3/3 full-package reruns passed clean
  immediately after, and a second full `preflight.sh` run from a clean state
  passed with no failures at all. No code related to Daily Double fade/volume
  was touched by this sweep's delta.
- Frontend fixes have no automated regression gate (no frontend test runner
  in this repo — a structural gap documented in every prior sweep since
  sweep 4, not re-raised as novel here); verified via the production build
  (`preflight.sh`, which runs `tsc` as part of the Vite build) plus
  hand-traced React state/effect logic for both the original bug and the fix,
  same posture as every prior sweep that touched frontend code.

## Still open after sweep 11

Unchanged from sweep 10, not re-litigated: no frontend test runner (now 8
sweeps running), the `client.ts` locked-contract question, s9code-03
(not_found retry cooldown), s9ui2-04/s9ui2-05 (Game Settings draft
persistence + threshold server readback), s10eng-04/s10ui2-03 (both
cosmetic, deferred), and the store-layer concurrency fix (s10api-01/02)
still unverified against real Postgres under genuine concurrent load. New
from this sweep: s11fe-03 (inert handshake-timing race, deferred above).

## Resume checklist for whoever picks this up next

- This sweep's fixes (both the pre-existing session delta and sweep 11's own
  4 fixes on top of it) are verified but **not yet committed/pushed/deployed**
  as of the end of this sweep — confirm current `git status` before assuming
  otherwise.
- The visibility-triggered resync fix is a genuinely new client behavior
  (not previously present anywhere in this codebase) — if a future sweep
  touches any of the three `useGame.ts`/`useAdmin.ts` files again, re-check
  that any OTHER state-reset logic keyed on "previous state" (not just the
  one found this sweep) is resync-repeat-safe, per the s11fe-01/02 pattern.

## Sweep 12 recommendation

Low urgency on a hunt basis — this was a small, well-scoped delta and came
back clean on four of five hunt priorities. Re-scope again per the
established rule if more features land: diff against this sweep's HEAD
first.
