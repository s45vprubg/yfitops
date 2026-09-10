# QA Sweep 6 - Findings & Remediation

Date: 2026-09-09/10. Scope: the redesigned Daily Double feature and the admin
manual-score-edit feature, both landed together in commit f4d1ac6 on `main`
(merged with upstream's QA sweeps 4/5 in 284fab0). This is a re-scoped sweep
per sweep 5's own recommendation: "if features land again, re-scope per sweep
4's rule: diff against this sweep's HEAD first." Sweeps 1-5 are untouched by
this sweep; nothing in their surfaces was re-hunted.

Method unchanged: hunters fan out read-only over non-overlapping surfaces,
independent skeptical validators try to refute each finding, only CONFIRMED or
PARTIAL findings get fixed, each fix gets its own adversarial pass, verify cold.
Layered order: server engine and admin API validated before the two frontend
hunts were fixed, per the code-before-UI rule.

## Summary

- **18 findings reported across 4 surfaces, 18 CONFIRMED or PARTIAL, 0
  REFUTED.** No hunter overreached badly enough to get a finding thrown out
  outright, though two were meaningfully downgraded by their validator (see
  below).
- **4 HIGH, 6 MEDIUM, 8 LOW. Zero CRITICAL.** The security-critical surface
  (the deliberate section 4A exception letting the Daily Double contestant's
  own phone see 5 song titles) was independently re-verified twice, once by
  the frontend hunter and again by an independent validator who traced the
  transport layer directly (`Hub.SendTo` only reaches the contestant's own
  connection IDs). No leak found either time.
- **Two severities were correctly downgraded by adversarial validation:**
  dd-api-2 (cross-board delete not scoped by board_id) was reported as
  MEDIUM/security by its hunter but downgraded to LOW by its validator, who
  pointed out this app's actual trust model is a single shared admin secret
  gating the entire admin API with no per-board tenancy anywhere, so this is a
  correctness/fat-finger gap, not a privilege escalation. dd-ui-admin-4
  (ScorePanel's per-row menus can multi-open) was downgraded from MEDIUM to LOW
  after the validator confirmed the mechanism but found the actual outcome is
  cosmetic clutter, not a functional break.
- **The highest-value finding was dd-ui-ms-1**: a WebTransport reconnect
  during the Daily Double accept/song-pick phase silently wiped the
  contestant's own live offer and left them soft-locked (disabled buttons, no
  legal action) until a page reload or an admin Force Skip. The validator
  found the real failure mode was worse than the hunter's original
  description (a hard soft-lock, not just a re-drawable double-offer),
  because the contestant screen's local `decided` state survives the resync
  with no remount.
- `qa/acid.sh` grew from **32 to 41 gates**. All 9 new ones are server-side
  (7 engine, 2 admin API... actually 3 admin API, see table) named Go tests,
  each proven to fail against the pre-fix code and pass after. The 9
  frontend-only findings (6 admin UI, 3 mobile/stage UI) have no equivalent
  gate, per the same standing gap sweeps 4 and 5 already documented: no
  frontend in this repo has a test runner. They were verified by `tsc
  --noEmit`, and by hand-tracing the exact React state/effect logic for each
  claimed scenario, not by an automated regression test.

## Findings by surface

### Server engine (`server/internal/game/engine.go`, `model.go`)

| id | severity | summary | fix | gate |
|---|---|---|---|---|
| dd-eng-1 | HIGH | `startDailyDoublePerformance` inherited the prior round's `roundKey`, so a still-in-flight lyric fetch from the round immediately before a Daily Double could leak into it. | Mint a fresh `roundKey` for the DD performance, mirroring `startTrack`. | `TestQARegression_DailyDoubleFreshRoundKey` |
| dd-eng-2 | HIGH | `onDailyDoubleDecision`'s accept branch had no re-entry guard. A repeated or replayed `{accept:true}` (the nonce doesn't change between accept and choose) drew a second batch from the no-replacement bucket and discarded the first, already-sent batch. | If candidates were already drawn for this offer, resend the same batch instead of drawing again. | `TestQARegression_DailyDoubleAcceptIsIdempotent` |
| dd-eng-3 | MEDIUM | `beginDailyDoubleOffer`'s `lastScorer` fast path never checked `Banned`, unlike its own fallback (random online player) path. A banned player could become the Daily Double contestant. | Check `nil`/`Banned` on the `lastScorer` fast path too. | `TestQARegression_DailyDoubleBannedLastScorerExcluded` |
| dd-eng-4 | LOW | If `cellAt` returned nil in `startDailyDoublePerformance` (reachable via an admin `ReloadBoard` mid-offer, which has no state guard at all), `e.curRow` kept whatever a prior round left it at, mispricing the payout. | Treat a vanished cell as a hard cancel via `declineDailyDouble`, not a stale-state proceed. | `TestQARegression_DailyDoubleAbortsWhenCellVanishes` |
| dd-eng-5 | MEDIUM | `admin.skipDailyDouble` unconditionally called `declineDailyDouble`, which no-ops during a live performance (`e.ddOffer == nil`), contradicting `endRound`'s own doc comment claiming the two actions are equivalent. | New shared `skipDailyDouble()` helper used by both `endRound` and the dedicated admin action; finishes a live performance, declines a pending offer, no-ops otherwise. | `TestQARegression_DailyDoubleSkipDuringLivePerformance` |
| dd-eng-6 | MEDIUM | `ResetToLobby` ("New Game") never restored `Board.DDBucket`. Repeated New Game cycles on the same board permanently depleted the bucket within one running server process (a real restart or board reload does restore it from Postgres). | Snapshot the bucket at board-load time; `ResetToLobby` restores from the snapshot. | `TestQARegression_ResetToLobbyRestoresDDBucket` |

### Admin REST API (`server/internal/admin/dailydouble.go`, `boards.go`, `server/internal/store/admin_store.go`)

| id | severity | summary | fix | gate |
|---|---|---|---|---|
| dd-api-1 | HIGH | `AddDailyDoubleTrack`'s `ON CONFLICT (board_id, spotify_uri) DO NOTHING` silently no-ops on a duplicate, but the handler always returned 201 with a track object whose id was never inserted. Live-confirmed against real Postgres. | Check `RowsAffected()`; return 409 on a real no-op instead of a fabricated 201. | `TestQARegression_AddDailyDoubleTrack_DuplicateSpotifyURI_Returns409` |
| dd-api-2 | LOW (downgraded from hunter's MEDIUM/security) | `RemoveDailyDoubleTrack`'s DELETE was scoped only by track id, not board id, so a track id from board A could be deleted via board B's URL. Live-confirmed exploitable mechanically; downgraded because the app's actual trust model has no per-board tenancy to escalate against. | Scope the DELETE by both id and board_id; 404 on a mismatch. | `TestQARegression_DeleteDailyDoubleTrack_WrongBoard_Returns404` |
| dd-api-3 | LOW | `dailyDoubleCount` had no upper bound beyond `>= 0`. Confirmed no crash/hang risk in `assignDailyDoubles`, but a persisted value that can never be honored is a data-integrity gap. | Reject a PATCH where `dailyDoubleCount` exceeds the board's actual cell count (5 rows x its columns). | `TestQARegression_RenameBoard_DailyDoubleCount_RejectsOversized` |

Deferred, out of scope (pre-existing, not introduced by this feature): `tracks.go`'s
`AddTrack`/`DeleteTrack` have the identical unconditional-201-on-conflict and
unscoped-delete patterns dd-api-1 and dd-api-2 just fixed for their Daily
Double siblings. Same fix would apply. Not touched this sweep to stay scoped
to the new code.

### Admin frontend (`web/admin/src/`)

| id | severity | summary | fix |
|---|---|---|---|
| dd-ui-admin-1 | HIGH | Dragging a track onto the Daily Double bucket is a two-step MOVE (add to bucket, then delete from the grid). If the delete failed after the add succeeded, the track was silently duplicated with no rollback and a generic "Move failed" message. | Attempt a compensating delete of the just-created bucket row on delete failure; if that also fails, name the exact track and point the admin at the bucket's own remove button instead of a generic error. |
| dd-ui-admin-2 | MEDIUM | `handleDragEnd`'s `finally { refresh(); }` did not await the refresh, leaving a stale-render window a second drag could exploit. | `refresh()` now returns its promise; the `finally` awaits it. |
| dd-ui-admin-3 | MEDIUM | `BoardSelector`'s debounced `dailyDoubleCount` save used a single timer ref. Switching boards while a save was pending silently dropped it. | Key the debounce timers by board id in a `Map`, plus a flush-on-unmount effect. |
| dd-ui-admin-4 | LOW (downgraded from hunter's MEDIUM) | `ScorePanel`'s per-row 3-dot menus have no mutual exclusion or outside-click dismissal. Confirmed real, downgraded because the outcome is visual clutter, not broken state. | Lifted menu-open state to the parent, added an outside-click-to-close listener. |
| dd-ui-admin-5 | LOW | "Edit points" silently no-ops on non-numeric or zero input with no feedback. | Added an inline dismissable error on invalid input. |
| dd-ui-admin-6 | MEDIUM | `ddResult` (the Daily Double payout) was captured in admin state but never rendered anywhere. | Added a result banner to `EvaluationPanel`, wired through from `App.tsx`. |

Verified by `npx tsc --noEmit` (clean) and hand-traced React state/effect logic
per fix; no frontend test runner exists in this repo (standing gap, sweeps 4
and 5).

### Mobile + stage frontend (`web/mobile/src/`, `web/stage/src/`)

| id | severity | summary | fix |
|---|---|---|---|
| dd-ui-ms-1 | HIGH | A WebTransport reconnect during the Daily Double accept/song-pick phase caused the server's resync (`sendFullSync` re-sends `dailyDoublePerformer{performing:false}` whenever `e.ddOffer != nil`, including to the contestant reconnecting to their own live offer) to be misread by the client as "a brand new Daily Double is starting," clearing the contestant's own offer. The contestant screen's local `decided` state survives the resync with no remount, so the real failure mode is a hard soft-lock, not a re-drawable double-offer as first described. | Client now compares the broadcast's `playerID` against the previously-known one; only treats a `performing:false` broadcast as genuinely new when the playerID changed. Added an 8-second safety-net timer in the contestant screen that re-enables the buttons if no offer ever arrives, favoring availability over a permanent stall on the remaining ambiguous case (a resync landing between tapping accept and the offer arriving). |
| dd-ui-ms-2 | LOW | The DAILY_DOUBLE routing branch's no-performer fallback omitted the `ddResult` prop and `IdleScreen` had no DAILY_DOUBLE copy entry, unlike the sibling default case. | Added the missing prop and copy entry. |
| dd-ui-ms-3 | LOW | Stage's `isNewTrack` heuristic (artist/song length comparison) also gated clearing the Daily Double result banner; a coincidental length match on the next track could rarely leave it stale. Judged genuinely low-impact; this heuristic predates the feature and governs several other fields. | Pulled `ddResult` clearing out of the `isNewTrack` gate so it clears on every `trackStart` unconditionally, without touching the other fields the same gate still controls. |

Independent section 4A re-check (performed twice, once by the hunter and once
by an independent validator tracing the transport layer): no leak found. The
offer is delivered via `Hub.SendTo` to only the contestant's own connection
IDs; the bytes never reach a bystander's stream regardless of any
hypothetical client-side render bug.

Verified by `npx tsc --noEmit` (both `web/mobile` and `web/stage`, clean) and
hand-traced logic, same standing test-runner gap as the admin frontend.

## Verification (cold)

`bash qa/acid.sh` after all fixes: **ACID PASSED** (41/41 gates present, cold
`go build`/`go vet`, `go test -count=1 ./...` green across every package
including `internal/admin` and `internal/store` which two fixers touched in
parallel, all 9 new named gates showing `--- PASS` in verbose output, smoke
green). `scripts/preflight.sh`: full clean install + production build across
all three frontends, plus the same Go suite. `npx tsc --noEmit` clean in
`web/admin`, `web/mobile`, `web/stage`. Every new Go regression test was
proven to fail against the pre-fix code (via a revert-run-restore cycle) and
pass with the fix restored, byte-for-byte diffed to confirm clean restoration.

## Process notes

- 2 hunters (server layer) ran first and in parallel; both validators for
  that layer ran in parallel with the 2 UI hunters, since UI hunting doesn't
  depend on server-layer fixes being trusted yet, only on not being fixed
  based on unvalidated UI findings. This saved a full round-trip versus a
  strictly serial code-then-API-then-UI pipeline while still validating
  server findings before any server fix landed.
- 4 fixers ran in parallel on file-disjoint surfaces (engine, admin API,
  admin UI, mobile/stage UI) with no merge conflicts, since the hunt/validate
  phase had already partitioned the surface cleanly.
- One validator's repro test file briefly failed `go vet` (declared-and-not-used
  variable) mid-run; it fixed its own scratch test file before finishing, so
  this never reached the orchestrator as a real finding.

## Open items after sweep 6

Carried forward, not re-litigated: everything in sweep 5's "Still open" list
(the `client.ts` locked-contract question, no frontend test runner, uimobile-4
server-side ban enforcement, adminapi-5 CORS hardening, the ignored
`YFI_BOARD_ROWS`/`YFI_BOARD_COLS` env vars, the 3x-duplicated section 7 decay
curve) is unchanged by this sweep.

New from this sweep:
- **`tracks.go`'s `AddTrack`/`DeleteTrack` have the same fabricated-201 and
  unscoped-delete patterns just fixed in their Daily Double siblings**
  (dd-api-1, dd-api-2). Deliberately not fixed here to stay scoped to the new
  code. Worth a small dedicated fix outside a QA sweep, or as sweep 7's
  target if this pattern shows up a third time somewhere.
- **The 8s safety-net timer in `DailyDoubleContestantScreen` (dd-ui-ms-1's
  fix) is a heuristic, not a proof.** It resolves the one ambiguous case the
  playerID-comparison fix cannot (a resync landing between tapping accept and
  the offer's arrival) by favoring availability over a permanent stall. If a
  future sweep wants a more principled fix, the real answer is probably a
  server-side acknowledgement of receipt rather than a client-side timeout.
- **5 pre-existing `TestStaging_*`/store DB-gated tests remain unverified
  against real Postgres in this sweep** (unchanged from sweep 5, not touched
  by the new code): they were not part of this sweep's scope and `preflight.sh`
  already warns about this on every run.

## Sweep 7 recommendation

Low urgency. This sweep found and fixed 4 HIGH and 6 MEDIUM issues in code
that had not shipped yet, none of which are now live in production, and the
security-critical boundary held on two independent checks. The two genuinely
interesting deferred items are the `tracks.go` sibling bugs (a 15-minute fix
whenever someone is next in that file) and the frontend test runner question,
which is now a 3-sweep-running structural gap, not a hunting problem.
