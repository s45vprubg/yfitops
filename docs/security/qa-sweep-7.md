# QA Sweep 7 - Findings & Remediation

Date: 2026-10-13. Scope: re-scoped per sweeps 4/5/6's own rule ("diff against
this sweep's HEAD first" when features land again). Everything since sweep
6's commit (`feb7458`): the lyrics subsystem redesign (persisted LRC cache
replacing a live-probe boolean), the stage-auth rework (stage used to
auto-authenticate on page load via a build-time-embedded secret; now gated
behind a runtime login identical to admin's, plus a new public landing page
and an unauthenticated player-count endpoint), the admin playback scrubber +
manual drop-point marking, and one small Daily Double decline-button fix
that landed on top. 48 files, ~3,100 insertions. Sweeps 1-6 are untouched;
nothing in their surfaces was re-hunted.

Method unchanged: hunters fan out read-only over non-overlapping surfaces,
independent skeptical validators try to refute each finding (with real
reproduction wherever possible — timing tests, live-Postgres repro, SQL
tracing), only CONFIRMED/PARTIAL findings get fixed, each fix gets its own
adversarial pass and a revert-run-restore proof, verify cold. Layered order:
Code (engine/lyrics-client/store) validated and fixed before API (admin
handlers/deploy-infra), before UI (stage/admin/mobile frontends).

## Summary

- **24 findings reported across 8 surfaces, 24 CONFIRMED, 0 REFUTED.**
- **5 HIGH, 8 MEDIUM, 11 LOW. Zero CRITICAL.**
- **All 24 fixed and independently re-verified from a cold state**, including
  running the previously-self-skipping DB-gated test suite for real against
  a live local Postgres/Redis (found already running from this sweep's own
  work) — all 8 staging tests, including 2 new sweep-7 regression gates,
  executed and passed, not just "present."
- Three severities were corrected by adversarial validation, two upward: a
  lyrics-clobber finding (str7-003) was upgraded from MEDIUM to HIGH when its
  validator found the real exposure is wider than the hunter reported (reachable
  via an automatic background batch after every playlist import, not just a
  manual rescan click); the HIGH blocking-call finding (eng7-1) was confirmed
  exactly as reported via an empirical timing proof (2.003s blocked vs. 61µs
  for the properly-async sibling path). Three were corrected downward
  (str7-002, lyr7-001, uim7-1) on reachability/impact grounds.
- The hunters' own suggested SQL fixes for the lyrics-clobber findings had a
  latent bug (a `CASE WHEN` with both branches evaluating to the same value)
  and a nullability mismatch (`<>` vs `IS DISTINCT FROM`); both were caught
  and corrected by the validator before anything shipped — exactly the
  "judge the fix as hard as the finding" discipline this methodology exists
  for.

## Findings by surface

### Code layer

**`server/internal/game/engine.go`+`ports.go`** (4 findings, 1 HIGH):
- eng7-1 (HIGH): the new drop-point feature's `GetDropPoint`/`SetDropPoint`
  calls ran synchronously on the engine's single-threaded command loop with
  `context.Background()` and no timeout — unlike the lyrics-store lookup
  added in the same diff, which was deliberately moved to a goroutine. A
  slow/unreachable store could stall buzz processing and every admin action
  game-wide. **Fixed**: moved both calls to goroutines posting back via the
  engine's existing submit mechanism, with a bounded timeout, mirroring
  `lyricFetch`'s shape exactly. While implementing, the fixer caught and
  fixed an additional real bug: `refreshCurDropPoint()` was being called
  before `e.roundKey` was reminted in `startTrack`/`startDailyDoublePerformance`,
  which would have made its staleness guard permanently false, silently
  dropping every async result.
- eng7-2 (MEDIUM): `positionMs` on `admin.seek`/`admin.setDropPoint` had no
  bounds check anywhere in the chain (confirmed by checking both the real
  Spotify adapter and the test fake — neither validated it). **Fixed**:
  `seekTo` clamps to `[0, durationMs]`; `onAdminSetDropPoint` rejects
  out-of-range input outright (since that value is persisted forever and
  protected by the never-clobber-manual rule, silently "correcting" it would
  be worse than rejecting it).
- eng7-3 (MEDIUM) / eng7-4 (LOW): three silent no-op failure paths and one
  overloaded error code in the drop-point handlers gave the admin operator
  no signal when a mark didn't take or why "Jump to Drop" failed. **Fixed**:
  distinct `sendError` codes per actual cause.

**`server/internal/lyrics/lyrics.go`** (2 findings, both LOW): a `Retry-After`
value large enough to overflow an `int64` multiply produced a negative
`time.Duration` that bypassed the retry clamp (needs an implausible
~10-digit value from LRCLIB to trigger — real but low-reachability); one
error-wrap inconsistency with zero functional impact. **Fixed**: the bound
check now happens pre-multiply in seconds, not post-multiply on a
possibly-overflowed duration; the error wrap matches the file's convention.

**`server/internal/store/admin_store.go`** (3 findings, 2 HIGH): this is
the sweep's most important finding cluster. `UpsertLyricsNotFound` and
`UpsertLyricsResolved` were missing the same never-clobber-manual /
never-downgrade-resolved protection that `SetDropPoint` (added in the same
diff) already had correctly — meaning the exact "transient error silently
destroys good data" bug class this whole lyrics redesign was built to fix
had regrown in two of its own three new functions. str7-001 (HIGH): a
confirmed-good `resolved` row could be flipped to `not_found` by a stray
requery. str7-003 (HIGH, upgraded from MEDIUM on validation): a manually-
entered lyric could be silently overwritten by an automated probe racing it
— and the validator found this is reachable not just via a manual bulk-
rescan click but via an invisible background batch that runs after every
playlist import. str7-002 (LOW): a confirmed `not_found` row could be
demoted back to `pending` on a transient error, contradicting migration
0008's own documented invariant — real but gameplay-inert, since the engine
treats `pending`/`not_found` identically. **Fixed**: all three upsert
functions now carry the correct `WHERE`/`CASE WHEN` protection, verified
against real Postgres (the fixer started the local dev stack specifically to
test this live) with a full revert-run-restore cycle for all four
clobber-direction combinations plus the first-write happy path.

### API layer

**`server/internal/admin/{tracks,dailydouble,handler,spotify}.go`** (4
findings, 1 HIGH): api7-001 (HIGH): `rescanLyrics`'s probe loop never
checked request cancellation — a client disconnect mid-rescan kept the
loop running and persisted bogus `pending`+"context canceled" rows onto
tracks that were never actually attempted, confirmed via a live repro.
api7-002 (MEDIUM): no cap on tracks-per-rescan, and no HTTP server timeout
anywhere in this repo to catch an unbounded one. api7-003 (LOW, downgraded
on validation to match the already-accepted sweep-6 `dd-api-2` precedent —
this app's trust model has no per-board tenancy to escalate against, fixed
anyway since it was cheap): `requeryLyrics`/`setLyricsManual` didn't check
the URL's board id against the track's real board. api7-004 (LOW): two
different 409 cases distinguishable only by free text. **Fixed**: context
checked every loop iteration (and the inter-probe delay itself is now
interruptible); a 100-track cap with an explicit truncation signal in the
response; board-ownership checks added; the two 409s split into 412
(precondition: lyrics not ready) vs. 409 (actual duplicate).

**Deploy/infra** (2 findings, both low-stakes): a documentation gap (the
deploy skill didn't explain that `/cert-hash` is intentionally disabled in
prod, or why that's safe — independently re-verified safe: prod uses a real
CA-signed cert through the normal TLS trust chain, pinning was only ever
load-bearing for the dev self-signed cert) and one stale comment referencing
a column migration 0008 already dropped. Both fixed. Notably, this hunt also
confirmed clean: the new public player-count endpoint leaks nothing beyond a
bare integer; both new migrations are properly idempotent; no stale
`VITE_STAGE_SECRET` references remain anywhere in the repo.

### UI layer

**`web/stage/src/`** (2 findings, 1 HIGH — the sweep's other headline
finding): uis7-001 (HIGH): the Spotify SDK's `connect()` fired on login
submission in parallel with, not after, the server's secret validation —
and the token-fetch fallback treated an auth rejection (401) identically to
a network failure, falling back to a locally-cached Spotify OAuth token.
Net effect: entering a *wrong* admin secret could still register a real
Spotify Connect device against the production account (bounded to roughly
the cached token's remaining lifetime, not indefinite — the WebTransport
game-control channel itself stayed correctly gated throughout, confirmed by
independently tracing `onHello`). This is the same bug class the whole
stage-auth rework was built to close, just resurfacing on the audio-SDK side
instead of the control channel. **Fixed**: Spotify connect now only fires
from inside the `welcome` handler (i.e. strictly after the server confirms
the secret), and the token-fetch fallback now distinguishes "unauthorized"
(fail hard, no fallback) from "unavailable" (network blip — the existing
reconnect-resilience fallback is preserved for an already-authenticated
session). uis7-002 (LOW): cosmetic tab-title fix.

**`web/admin/src/`** (5 findings, all MEDIUM/LOW): an unclamped "Mark Drop
Point" value that could trigger the server's own out-of-range rejection; no
optimistic UI update on seek (bar shows stale position until the next
broadcast); no double-click guard on two new buttons unlike their siblings
in the same diff; a requery failure that silently showed nothing despite a
comment claiming otherwise; and stale previous-track scrubber data briefly
showing after a round transition. All five fixed, `tsc --noEmit` and
production build clean.

**`web/mobile/src/screens/DailyDoubleContestantScreen.tsx`** (2 findings,
both LOW): a stale-closure double-tap guard (confirmed pre-existing, not a
regression) that could in theory let both Accept and Decline fire from a
true simultaneous tap, which — as a derivative — could let a raced offer
message briefly flip the UI back to the picker after a decline. **Fixed**
with one `useRef`-based change: refs are written synchronously, unlike
closure-captured state, so the second of two simultaneous taps now always
observes the first's decision and no-ops. Resolved both findings at once.

## Verification (cold)

- `go build ./... && go vet ./... && go test -count=1 -race ./...` — clean,
  no regressions, including under the race detector.
- `npx tsc --noEmit` clean in all three frontends (`stage`, `admin`,
  `mobile`); production builds clean in all three.
- `qa/smoke.sh` → SMOKE OK. `qa/acid.sh` → ACID PASSED, 43/43 gates present
  and executing (up from 41 before this sweep).
- `scripts/preflight.sh` → PREFLIGHT PASSED.
- All 8 DB-gated staging tests (including 2 new sweep-7 regression gates)
  run and PASS for real against a live local Postgres/Redis — not just
  self-skip-and-call-it-done. This closes the "DB-gated tests unverified"
  caveat every `preflight.sh` run in this sweep otherwise carried.
- Every fix's regression test was proven to actually fail against the
  pre-fix code via an explicit revert-run-restore cycle, confirmed by the
  fixer and spot-checked independently.

## Known gaps (unchanged, not re-litigated)

Same two structural items as sweeps 4-6: no frontend test runner (verification
here relied on `tsc --noEmit` + production builds + hand-traced logic, same
as every prior sweep that touched frontend code), and the `client.ts`
locked-contract question. Neither is a hunting problem; both need owner
bandwidth for a decision.

## Sweep 8 recommendation

Low-to-moderate urgency. The two HIGH frontend/backend findings (stage audio
auth-ordering, lyrics-clobber protection gaps) were real and are now fixed
and verified; nothing here was caught live at an event. Re-scope again per
the same rule if more features land: diff against this sweep's HEAD first.
