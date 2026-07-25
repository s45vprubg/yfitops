# QA Sweep 1 — Findings & Remediation

Date: 2026-07-25. Scope: full codebase (Go server ~11k LOC, three React frontends,
shared TS). Method: parallel read-only hunters -> independent adversarial validators
(default-refute) -> file-disjoint fixers -> verify from a clean state. Layered order:
code -> API -> UI, each validated before the next.

## Summary

- 48 candidate findings hunted (0 critical, 6 high, ~20 medium, ~22 low).
- After adversarial validation: ~44 CONFIRMED or PARTIAL, 1 REFUTED, several severity-adjusted.
- 40 fixed and verified. 3 deferred with rationale. Zero criticals existed; all 6 highs fixed.
- Definition-of-done gate `scripts/preflight.sh` PASSED (Go build/vet/test + clean reinstall and
  production build of stage, mobile, admin). Full Go suite green under the race detector.
- 4 new regression tests, each proven to fail when its fix is reverted (non-vacuous).

The crown jewels held: mobile sanitization (no track metadata to mobile), buzz single-winner
atomicity, parameterized SQL, server arrival authority, admin constant-time auth, timer determinism.
No XSS was found in any frontend.

## Highs (all fixed)

- **engine-1** — `stage.*` client messages had no role gate; a hostile mobile could hijack the
  Spotify playback device or force round transitions without the stage/admin secret. Now gated to
  RoleStage/RoleAdmin. Regression test proves a mobile is rejected and a stage conn accepted.
- **store-1** — a Postgres-backed server booted with no board on every restart: `LoadBoard` read
  orphaned migration-0001 tables that nothing ever writes, ignoring the persisted
  `game_sessions.board_id`. Rewritten to resolve the board via the persisted link (reuses
  `LoadBoardByID` on the real 0002 tables). Must be confirmed against real Postgres in staging.
- **uistage-2 / uimobile-1** — neither the stage projector nor the mobile buzzer reconnected after
  a WebTransport drop; one Wi-Fi blip permanently wedged them. Both now reconnect with capped
  backoff. Mobile re-sends the same device fingerprint + saved handle and issues a resync.
- **uistage-1** — the stage sent the shared admin secret as a Bearer token over plaintext HTTP.
  Now env-gated: outside dev the client refuses a non-https endpoint (fails closed). Dev stays http.

## Notable validator corrections (why independent validation mattered)

- **ai-6 REFUTED** — the AI builder "under-fill" is documented, intended, non-crashing behavior with
  correct over-fill/hallucination guards. Not a defect.
- **adminapi-3 / adminapi-7 PARTIAL** — the scary halves were unreachable: `LoadBoardByID` never
  returns `(nil,nil)` (no nil-deref), and a foreign-key constraint already rejects phantom-column
  placements (no silent corruption). Only the real remainders were fixed.
- **adminapi-5 PARTIAL** — the wildcard CORS is not a real cross-origin read: auth is a Bearer header
  and `Allow-Credentials` is unset, so a drive-by page can neither forge the header nor read
  responses. Left as documented hardening, not changed.
- **ai-1 high->medium, ai-3 medium->low** — both are gated behind the admin secret / require a TLS
  MITM of Google, lowering real exposure.

## A green we did not earn (caught and fixed)

The first `adminapi-4` (body-size cap) regression test asserted only "a 4xx was returned." It passed
even with the fix reverted, because `createBoard` validates name length after decoding, so an
oversized body is rejected either way. The assertion was tightened to require the decode-failure
message ("invalid JSON") that only appears when `MaxBytesReader` aborts the read. It now fails when
reverted, with the failure output showing the pre-fix "name too long" path.

## Fixed (by layer)

Code: engine-1..6, player-1, transport-1..3, store-1/2/4/5, spotify-1..4, plus engine-2 (buzz
fairness design change). API: ai-1..5, adminapi-1/2/3(partial)/4/6/7(partial)/8. UI: uistage-1..5,
uimobile-1/2/3/5/6, uiadmin-1/2/3/4. See `qa/HANDOFF.md` for the per-finding fix and file list.

### engine-2 (owner-approved design change)
Buzz winner was decided by goroutine-enqueue order; the latency-compensated `EffectiveBuzzTime`
(contract §4B/§4C) was computed then discarded. Now a short collection window (default 40ms,
`Config.BuzzWindowMs`; negative = synchronous for tests) gathers contending buzzes and awards the
minimum effective time, tie-broken by arrival then player id. The atomic lock still guarantees a
single winner. New test proves the lower-latency player wins even when its buzz is processed second.

## Deferred

- **store-3** (low) — add `UNIQUE(board_id, track_id)` so a track can't occupy two cells. Needs a
  migration plus a de-dup of any existing rows; not shippable without rehearsing against a Postgres
  copy, which was not available locally.
- **uimobile-4** (low) — DeviceFP is a resettable localStorage UUID, so a client-side ban is
  evadable. Inherent to the zero-trust model; the fix is server-side ban enforcement on IP/subnet +
  connection heuristics (already partly tracked via cheatReport). Deferred to server ban design.
- **adminapi-5** (low) — CORS wildcard; documented as hardening, no real cross-origin exposure.

## Staging verification — completed (2026-07-25)

All five items were exercised against a real local stack (docker-compose: Postgres + Redis +
gameserver), not just code-reviewed. New tests were added; the DB-backed ones self-skip unless
`YFI_TEST_DSN` is set, so the normal no-infra suite is unchanged.

1. **store-1 — verified.** A cold restart of the Postgres-backed gameserver booted with the attached
   board loaded (no "no board attached" warning). The old path was proven dead: the migration-0001
   `board_cells` table holds 0 rows for the session, while the new `game_sessions.board_id` resolution
   finds the board's 10 layout cells.
2. **adminapi-2 / ai-5 — verified against live Postgres.** New `TestStaging_RebuildLayout_AtomicRollback`
   (ran in a golang container on the compose network): a good rebuild commits; a rebuild whose second
   column references a nonexistent track (foreign-key violation mid-transaction) rolls back entirely —
   the board is left at its prior good state, categories unchanged, not half-wiped. Ran on a throwaway
   board and cleaned up; the six real boards were untouched.
3. **transport-1 — verified.** New deterministic unit tests `TestConnDropClosesStreamOnOverflow` and
   `TestConnStopIsIdempotent`: overflowing a non-reading connection's send queue now closes the stream
   (the `CancelRead` + `Close` teardown), and repeated `stop()` is idempotent. Proven to fail when the
   fix is reverted to writer-only stop (the zombie). transport-2's "legit sessions still accepted with
   `MaxIncomingStreams=2`" is covered by the e2e suite passing over real quic-go.
4. **TLS env-gate — verified, with a correction.** The fail-closed guard is a *runtime* check (it
   throws when the projector loads the page in a production build), not a build-time failure — a
   production build with a cleartext `VITE_HTTP_URL` still compiles and refuses to run when loaded. The
   guard is baked into the production bundle and the production default is https. Predicate confirmed:
   prod + http refuses, prod + https allowed, dev (either) allowed so local http is unbroken.
5. **spotify-1 — verified under the race detector.** New `TestValidToken_ConcurrentRefreshSingleFlight`:
   20 concurrent callers on an expired token trigger exactly one refresh grant (peak in-flight
   concurrency of one), and the rotated refresh token is stored intact. Fails when the `refreshMu`
   guard is reverted (20 grants). Used a fake token endpoint — no real Spotify call, so no live token
   was put at risk.

Each new regression/staging test was confirmed non-vacuous by reverting its fix and observing the red.

## Locked-file notes (owner sign-off)

`nonce.go` (comment-only correction of a false security claim; dead `Token()`/`sign()` kept because
tests reference them), `config.go` (guarded in `main.go` instead of edited), and `web/shared/client.ts`
(frame cap added with a `// CONTRACT-QUESTION`). Each is flagged in `qa/HANDOFF.md` for ratification.

## Next sweep

Recommended. Sweep 1's own fixes are new code and deserve an adversarial pass — especially the
engine-2 buzz-window rewrite, the transport stream teardown, and the untested `RebuildLayout` tx.
Plus a dedicated aggressive-security pass against a locally-deployed stack (Redis + PG + TLS) that
this sweep could only reach by code review.
