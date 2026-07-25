# QA Sweep 2 — Findings & Remediation

Date: 2026-07-25. Scope: adversarial audit of sweep 1's own fixes (the changed
surfaces) plus a fresh-eyes pass and a live aggressive-security probe against a
running local stack (Postgres + Redis + gameserver). Method unchanged: parallel
read-only hunters -> independent skeptical validators (default-refute) ->
file-disjoint fixers -> verify from a clean state, this time including live
Postgres and a running server.

## Why this sweep existed

Core principle 6: the nastiest bugs are introduced BY fixes. Sweep 1 shipped 40
changes; this sweep hunted them. The result validates the principle — 7 of the 9
confirmed findings were residual defects inside sweep-1 fixes. The big, scary
rewrites held; the small "safe" fixes grew the new bugs.

## Summary

- 9 confirmed findings (0 critical, 0 high, 4 medium, 5 low). 0 refuted by the
  hunters' own filtering; every candidate that reached validation was CONFIRMED
  or PARTIAL (1 downgrade). The live security probe found nothing exploitable.
- All 9 fixed and verified. Full `preflight.sh` passed; Go suite green under
  `-race`; three staging items verified against live Postgres / a running server.

## What HELD (audited sound — the important half of an audit)

- **engine-2 buzz-window rewrite** (the riskiest sweep-1 change): sound across all
  eight sub-cases — stale-timer roundKey guard, window teardown on every exit,
  no lock leak, synchronous path parity, no reveal/scoring skew.
- **transport-1/2 teardown + QUIC limits**: `CancelRead` genuinely fires
  (verified against vendored webtransport-go), OnDisconnect exactly once,
  double-close safe, `-race` clean; MaxIncomingStreams=2 correct per-session.
- **adminapi-2/ai-5 RebuildLayout tx**, **store-1 LoadBoard**, **adminapi-4 body
  cap**, **adminapi-1 goroutine drain**, **ai-1..4 Gemini hardening**, admin auth
  wrapping, client frame cap (3-4x headroom over worst-case legit frame),
  ErrorBoundary, admin-secret de-persistence: all audited sound.
- **Live security probe** (authenticated, against 127.0.0.1): auth rejects every
  bad-token variant (constant-time content compare), no info leak on forced
  errors, SQLi inert (parameterized), body cap enforced (~2.2 MB rejected), OAuth
  state correct, AI builder 503s cleanly. Real boards untouched.

## Confirmed findings, fixed

Medium:
- **s2-engine-1** — `sanitizeHandle` (the player-1 fix) stripped only control
  chars; bidi/format/zero-width runes (U+202E, U+200B, U+FEFF, isolates,
  leading combining marks) survived and could still scramble the stage render.
  Now strips Cf/Cs/Co and leading marks; empty result preserves the prior handle.
- **s2-engine-2** — `checkDailyDoubleComplete` (the engine-3 fix) compared
  `len(ratings) >= len(ratingPool)` while kick/disconnect deleted from ratingPool
  only, so the daily double could finish before an eligible rater voted and a
  departed player's stars skewed the bonus. Fixed by also deleting from
  `e.ratings` on kick/disconnect so the sets stay in sync.
- **s2-store-001** — the spotify-1 single-flight short-circuit defeated the 401
  forced-refresh retry for ~58 of a token's ~60 minutes: a server-revoked token
  was reused instead of re-fetched. Added a forced-refresh entry that bypasses
  the skew short-circuit while still serializing on refreshMu (and reusing a
  token another goroutine already refreshed). New test proves the 401 path
  re-POSTs on a locally-live-but-dead token.
- **s2-ui-01 / s2-ui-02** — one root cause in both reconnect hooks: the prior
  GameClient was never `.close()`d on reconnect and the shared onState handler
  didn't check client identity, so a superseded client's late `onState(false)`
  leaked the old client and could tear down / reconnect on top of a healthy one.
  Fixed with close-on-replace plus a per-client identity guard in both frontends.

Low:
- **s2-store-002** — played-state was never persisted, so a mid-game restart
  re-offered consumed tracks. Fixed properly (owner chose to fix, not defer):
  migration 0005 adds `board_layout_cell_tracks.played`; the engine persists on
  consume via a `consumeTrack()` helper and clears on new game; LoadBoardByID
  restores it. Verified end-to-end against live Postgres.
- **s2-store-003** — `Search()` dropped the album release year (playlist import
  kept it), so search-added tracks lost the year hint. One-line map fix.
- **s2-admin-2** — admin auth used `subtle.ConstantTimeCompare` on raw bytes,
  which short-circuits on length mismatch and leaks the secret length via timing.
  Now compares fixed-size SHA-256 digests (secret digest precomputed once).
  Practically unexploitable, but honors the CLAUDE.md constant-time mandate.
- **s2-admin-3** — searchSpotify/importPlaylist still returned raw upstream error
  text at 502 (the adminapi-6 pass missed the BadGateway paths). Now log + generic.
- **s2-ui-03** (downgraded medium->low) — only `connect()` was try/caught; the
  post-connect hello/resync/startHeartbeat could throw an unhandled rejection.
  Wrapped to close + scheduleReconnect on failure. (The "permanent wedge" was
  overstated: readLoop/wt.closed usually surface onState(false) and recover.)
- **s2-ui-04** — the stage board rendered server rows/cols unclamped; a corrupt
  huge-dimension frame is a synchronous OOM the ErrorBoundary can't catch. Clamped
  to a sane max (12). Server is the only, trusted frame source, hence low.

## Latent / not-live (fixed anyway, noted for honesty)

- **s2-admin-1** — placeTrack dropped `pos` on a chunked/empty body. The shipped
  browser client always sets Content-Length, so pos is honored in production; only
  a re-chunking proxy or non-browser client triggers it. Fixed (decode
  unconditionally, tolerate EOF) so it's correct regardless.

## Verification

- `scripts/preflight.sh` passed (Go build/vet/test + clean reinstall & production
  build of stage, mobile, admin). Full Go suite green under `-race`.
- **Live Postgres** (migration 0005 applied to the running stack):
  `TestStaging_PlayedStatePersistsAcrossReload` proves played survives a reload
  and ClearPlayed resets it; `TestStaging_RebuildLayout_AtomicRollback` still
  green. Both proven non-vacuous (revert -> red). Ran on throwaway boards, cleaned
  up; the six real boards untouched.
- Gameserver rebuilt and redeployed with sweep-2 code; healthz answers and the
  attached board still restores against the live schema.
- New regression tests, each confirmed fails-when-reverted: the handle-sanitizer
  and daily-double set-sync (game), the 401 forced-refresh (spotify), the auth
  digest compare + chunked-body pos (admin), and the two staging tests above.

## Still deferred (unchanged from sweep 1)

- **Per-IP / global QUIC session cap** — `MaxIncomingStreams` only bounds streams
  within a session; an attacker can still open many sessions from one IP. Needs
  new shared state on the server. Re-confirmed exploitable as-deployed; carry to a
  future sweep or accept for the LAN-party threat model.

## Next sweep

Optional. Sweep 2 found only medium/low residuals and converged cleanly; a
sweep 3 would mainly re-audit sweep-2's own fixes (the forced-refresh signature,
the played-state persistence, the reconnect identity guards) and finally take on
the deferred per-IP session cap. Recommend a sweep 3 only if the per-IP flood
defense is wanted before the event, or after further feature work.
