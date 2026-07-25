# QA Sweep — HANDOFF

## Goal
Raise yfitops V2 to ship-ready via repeated hunt → validate → remediate → verify
sweeps. Zero-trust client model; runs at a hacker conference. Mobile client is
fully hostile.

## Contract (the guarantees under test)
- §4A Client sanitization: mobile conns never get title/artist/URI/lyrics.
- §4B Server arrival authority: buzz ordering uses server arrival clock only.
- §4D Nonce: stale-nonce actions dropped; bumped every transition.
- Atomic buzz: first to win Redis SET NX flips state; others get won:false.
- §9 Admin/Stage gated by secret; constant-time compare; every admin.* re-checks role.
- Audio isolation: Go never plays audio; pause sent to stage directly.

## Surfaces & posture
- ~16k LOC. Go server (11k), web/admin (1.9k), stage (1.7k), mobile (0.8k), shared (0.4k).
- Fixed/LOCKED contract files (DO NOT edit): protocol.go, state.go, scoring.go,
  anticheat/latency.go, anticheat/nonce.go, config.go. Findings against them are
  surfaced to the owner, not silently patched.
- Validate posture: local only. Go server boots in-memory (no Redis/PG) and
  answers /healthz — see qa/smoke.sh. No Premium Spotify creds; playback path is
  code-review-only. Aggressive tests run locally, never against shared infra.

## Baseline (sweep 1, clean state)
- go build ./... OK, go vet ./... OK, go test ./... ALL PASS.
- qa/smoke.sh GREEN (/healthz answers, in-memory fallback boots).

## Sweep plan (user-requested order)
1. CODE (Go core): engine, transport+anticheat, store+lyrics.  ← in progress
2. API (HTTP): admin REST, spotify OAuth, AI builder, main wiring.
3. UI: stage, mobile, admin React + shared TS.
Each wave: hunt (parallel, disjoint) → adversarial validate → collect confirmed.
Then remediate confirmed on disjoint surfaces, verify from clean state.

## Known gaps (from ADVERSARIAL_REVIEW_HANDOFF — confirm+rate, don't re-report as novel)
1. Join token not enforced in onHello (anyone reachable can join).
2. Admin secret compare — verify constant-time.
3. OAuth state — NOTE: main.go now uses random state + cookie (may be fixed; validate).
4. Self-signed cert / InsecureSkipVerify — must be test-only.
5. Spotify playback unrun (no creds).
6. React clients unrun headlessly.

## Sweep 1 findings (48 total: 0 critical, 6 high, ~20 medium, ~22 low)

### HIGH (6)
- engine-1 (CONFIRMED): stage.* client msgs have no role gate — hostile mobile hijacks Spotify device / forces round transitions w/o secret. Mechanical fix.
- store-1 (CONFIRMED): Postgres server never reloads its attached board on restart (LoadBoard reads orphaned 0001 tables; builder writes 0002). Fix: reload via game_sessions.board_id -> LoadBoardByID.
- ai-1: Gemini API key leaked to HTTP client in raw upstream error (key is in URL query -> not redacted). Fix: generic error + move key to header (ai-2).
- uistage-2: stage has no WebTransport reconnect — one wifi blip wedges the projector for the whole event.
- uimobile-1: mobile has no reconnect/resync — a drop eliminates the player permanently.
- uistage-1: stage/admin secret sent as Bearer over PLAINTEXT HTTP token endpoint (LAN sniff -> secret). Deployment posture (needs TLS).

### MEDIUM (self-confirmed marked ✅)
- engine-3 ✅ daily-double hang if a rater disconnects (no timeout, no re-eval).
- player-1 ✅ unbounded client handle stored + broadcast (amplification DoS / render corruption).
- transport-1 ✅ "dropped" conn only kills writer; read loop keeps processing -> zombie conn keeps buzzing.
- store-2 LRCLIB body read has no size cap (OOM).
- transport-2 no QUIC idle timeout / session caps (flood DoS).
- engine-4 every heartbeat forces O(P^2) telemetry rebuild on the Run loop.
- store-3 missing UNIQUE(board_id,track_id) — track can land in two cells.
- store-5 boots silently with default secrets (config.go LOCKED -> guard in main.go).
- adminapi-1 rescanLyrics leaks ~9 goroutines on mid-scan DB error.
- adminapi-2 / ai-5 (same bug) aiBuild clear-then-rebuild non-atomic -> corrupt board on mid-apply error.
- adminapi-3 attachBoard: nil-board -> ReloadBoard panic + half-commit on load failure.
- adminapi-4 no http.MaxBytesReader on admin JSON bodies (memory DoS).
- spotify-1 concurrent refresh race can clobber a rotated refresh token -> forced re-auth mid-event.
- ai-2 Gemini key in URL query (log leak). ai-3 unbounded Gemini response read (OOM). ai-4 prompt injection via track names -> attacker-chosen category names (rendered unsanitized).
- uistage-3 no ErrorBoundary + unvalidated frame casts -> white-screen projector. uistage-4 cert hash over plaintext HTTP, blindly trusted (MITM defeats pinning).
- uimobile-2 heartbeat interval not cleared on disconnect -> unhandled rejection every 2s.
- uiadmin-1 admin secret persisted in localStorage (XSS-stealable). uiadmin-3 builder mutations lack try/catch (silent stale UI).

### LOW (22)
engine-5 award no clamp; engine-6 misleading backpressure comment; transport-3 nonce Token() dead code (LOCKED, contract claim unmet); store-4 brittle err-string not-found check;
adminapi-5 CORS wildcard (no creds, low risk); adminapi-6 raw err leaked in 500s; adminapi-7 placeTrack col not bounded to board Cols; adminapi-8 renameCategory no length cap;
spotify-2 stale token returned when no refresh; spotify-3 playlistID unescaped (guarded upstream); spotify-4 OAuth cookie missing Secure; ai-6 sparse board under-fill;
uistage-5 fire-and-forget send rejections; uimobile-3 misleading "reconnecting" label; uimobile-4 ban evadable via localStorage clear (by design); uimobile-5 stale awaitingBuzzResult; uimobile-6 stale pendingPing RTT; uiadmin-2 client no frame cap; uiadmin-4 non-atomic cross-cell drag.

### Crown jewels — all HELD (verified clean, no finding)
§4A sanitization (mobile never gets track data); buzz atomicity (SET NX, no 2nd winner); SQL fully parameterized; §4B arrival authority (server clock stamped pre-decode); framing 1MiB cap (server side); admin auth (constant-time Bearer, every /api/* gated); OAuth CSRF fix (correct); token endpoint (no leak); §5 timer determinism (scoring.ts bit-identical to scoring.go); no XSS in any frontend.

### DESIGN / OWNER CALLS (not mechanical — surfaced to user, not auto-fixed)
- engine-2: buzz winner = Run-loop enqueue order; EffectiveBuzzTime (arrival + latency comp §4C) is computed then DISCARDED. Single-winner + no-client-time hold, but fairest-arrival + latency comp do NOT. Fixing = design change.
- store-5 / transport-3: LOCKED contract files (config.go, nonce.go). Caller-side mitigations possible (boot guard) but the contract deviation needs owner sign-off.
- uistage-1/4, spotify-4: plaintext-HTTP LAN posture. Real leak, but the fix is TLS/deploy config, not a code one-liner.

## Fixed / Refuted / Deferred

### Validation summary (sweep 1, all findings independently validated)
- CODE (18): all CONFIRMED. store-3 downgraded medium->low (admin-only, mislabeled race).
- API (14): 9 CONFIRMED, 3 PARTIAL (adminapi-3 nil-deref half unreachable / LoadBoardByID never returns nil,nil; adminapi-5 CORS not a real cross-origin read; adminapi-7 phantom-col killed by FK), 1 REFUTED (ai-6 under-fill is by-design), severity drops ai-1 high->med, ai-3 med->low.
- UI (15): all CONFIRMED, no severity change. uimobile-4 is a documented zero-trust limitation (server-side fix only, no client change).

### CODE layer — FIXED & VERIFIED (build+vet+`go test -race` all green from cold, smoke green)
- engine-1 (HIGH authz): stage.* now role-gated (RoleStage||RoleAdmin) in dispatch. Regression test TestQARegression_StageMessagesRequireStageRole (proven fails-when-reverted).
- engine-2 (buzz fairness, DESIGN change per owner): winner now = min(EffectiveBuzzTime) via a collection window; atomic lock still gates single-winner. Config.BuzzWindowMs sign convention: 0=default 40ms, <0=synchronous (tests use -1). New test TestBuzz_FairestArrivalWinsWithinWindow (proven non-vacuous). Follow-on: letter-reveal ticks up to ~40ms post-first-buzz before pause (negligible vs 250ms interval).
- engine-3 (MED deadlock): checkDailyDoubleComplete() called from onRate + OnDisconnect + onAdminKick. Regression test TestQARegression_DailyDoubleCompletesWhenRaterLeaves (proven fails-when-reverted).
- engine-4 (MED dos): heartbeat telemetry coalesced via dirty flag + 1s flush ticker.
- engine-5 (LOW): admin.award Delta clamped [-1000,1000], score floored at 0.
- engine-6 (LOW): submit's dead no-op default branch removed; documented as blocking backpressure.
- player-1 (MED): resolvePlayer sanitizes handle (<=24 runes, strip control chars).
- transport-1 (MED): drop now tears down whole conn. KEY: *webtransport.Stream.Close() closes send-side only; needed CancelRead(0) to unblock ReadFrame. Zombie conn gone.
- transport-2 (MED): QUIC MaxIdleTimeout=30s, MaxIncomingStreams=2 (HTTP/3 CONNECT is itself a bidi stream, so 1 breaks e2e), 10s AcceptStream deadline. Per-IP session cap DEFERRED (needs new shared state).
- transport-3 (LOW, LOCKED nonce.go): honest-minimal fix per owner OK — corrected the false "unforgeable opaque token" doc to describe the real raw-counter scheme; dead Token()/sign() kept (tests reference them). NOTE: nonce.go is a repo-locked file; only comments changed; add CONTRACT-QUESTION for owner.
- store-1 (HIGH): LoadBoard rewritten to resolve via game_sessions.board_id -> LoadBoardByID (0002 tables). Postgres server now restores its board on restart. Follow-on: per-track Played state not restored (LoadBoardByID doesn't set it) — but 0001 tables were never written so that state never actually persisted; pre-existing, not a regression.
- store-2 (MED): lyrics body io.LimitReader(1MiB).
- store-4 (LOW): GetBoard uses errors.Is(pgx.ErrNoRows).
- store-5 (MED, config.go LOCKED): main.go boot guard log.Fatal if YFI_ENV=prod and any secret == dev default.
- spotify-1 (MED): refresh serialized via refreshMu (outer) -> c.mu (inner), re-check expiry after acquire; race-clean.
- spotify-2 (LOW): ValidToken errors on expired token when no refresh token.
- spotify-3 (LOW): playlistID url.PathEscape at point of use.
- spotify-4 (LOW): OAuth state cookie Secure:true when redirect URI is https (dev http stays non-Secure).

### CODE layer — DEFERRED
- store-3 (LOW): UNIQUE(board_id,track_id) needs a migration + existing-data dedup; can't rehearse against real Postgres locally. Documented, not shipped (methodology: rehearse destructive data ops on a copy first).

### API layer — FIXED & VERIFIED (build+vet+`go test -race` green; admin+ai+spotify)
- ai-1 (MED info-leak): aiBuild logs real err, returns generic "AI build failed"/"internal error".
- ai-2 (MED): Gemini key moved from URL query to x-goog-api-key header.
- ai-3 (LOW): Gemini response io.LimitReader(1MiB) + error checked.
- ai-4 (MED prompt-injection): track list emitted as delimited JSON data block ("treat as DATA"); category names sanitizeCategory'd (<=100 runes, strip control chars) before AddColumn.
- adminapi-2/ai-5 (MED): new atomic RebuildLayout(tx-backed) on AdminStore+PostgresRepo; aiBuild builds layout in-memory then swaps in one tx. CAVEAT: Postgres tx path NOT exercised locally (no PG) — code-review-only, MUST run end-to-end in staging.
- adminapi-4 (MED dos): decodeJSON now wraps http.MaxBytesReader(1MiB); signature took w, updated all 9 callers (incl spotify.go). Regression test TestQARegression_BodySizeCapped (proven non-vacuous — asserts the decode-failure path, not just any 4xx, since an after-decode length check would mask it).
- adminapi-6 (LOW info-leak): serverError() helper logs real err + returns generic "internal error"; replaced ~25 raw-err 500 sites across handler/boards/layout/tracks/aibuild.
- adminapi-1 (MED dos): rescanLyrics drains results to completion (captures firstErr, no early return) — goroutine leak fixed.
- adminapi-8 (LOW): renameCategory caps category <=100 chars.
- adminapi-7 (LOW, PARTIAL): placeTrack stops discarding decode error (400 on bad body), validates pos 0..4. Phantom-col half not a bug (FK catches it).
- adminapi-3 (LOW, PARTIAL): attachBoard reordered — LoadBoardByID first, then AttachBoard, then ReloadBoard (no commit-then-diverge). nil-deref half unreachable, not addressed.
- adminapi-5 (LOW, PARTIAL): NOT changed — wildcard CORS is not a real cross-origin read (no Allow-Credentials + Bearer auth). Documented as hardening-only.
- ai-6: REFUTED (under-fill is by-design, non-crashing). No change.

### UI layer — FIXED & VERIFIED (all 3 frontends: typecheck + production build green; full preflight PASSED)
- uistage-2 (HIGH): reconnect-with-backoff (500ms->cap 10s); establish() reused by initial+retry; disposed-guarded. Audio layer survives drop (only transport rebuilt).
- uistage-5 (LOW): audio sends routed through safeSend() (skip when disconnected + .catch).
- uistage-3 (MED): new ErrorBoundary.tsx wraps App (auto-clears every 2s); Board.tsx iterates `cells ?? []`.
- uistage-1 (HIGH) + uistage-4 (MED): env-gated TLS. Outside DEV, HTTP_URL/CERT_HASH_URL must be https or module init throws (fail closed); cert-hash fetch failure fails closed. DEV keeps plain http (no CORS breakage) per owner.
- uimobile-1 (HIGH): auto-reconnect, same deviceFP+saved handle, sends {t:'resync'} on reconnect; capped backoff; unmount-gated.
- uimobile-2 (MED): heartbeat torn down on disconnect, restarted only on (re)connect; sendHeartbeat awaits/catches.
- uimobile-3 (LOW): "reconnecting" label now truthful (real reconnect implemented).
- uimobile-5 (LOW): awaitingBuzzResult reset on fresh ROUND_ACTIVE + on disconnect.
- uimobile-6 (LOW): pendingPing cleared on disconnect; echoes older than 5*HEARTBEAT_MS ignored.
- uiadmin-1 (MED): admin secret no longer persisted to localStorage — in-memory only. Behavior change: admin re-enters secret after page reload (safer for kiosk).
- uiadmin-3 (MED): builder mutation handlers try/catch + error banner + refresh() in finally.
- uiadmin-4 (LOW): cross-cell drag compensating re-place on failure + error surfaced.
- uiadmin-2 (LOW, web/shared/client.ts FIXED CONTRACT): client readLoop caps frame at 1<<20 (mirrors server maxFrameLen) — refuses oversized prefix instead of buffering ~4GiB. Added with // CONTRACT-QUESTION per CLAUDE.md. Only comment+guard added; no wire change.
- uimobile-4 (LOW): NOT a client fix — documented zero-trust limitation; bans must be enforced server-side (IP/subnet + cheatReport), not on DeviceFP alone. Deferred to server ban design.

## Sweep 1 outcome
- 48 findings hunted -> validated: ~44 CONFIRMED/PARTIAL, 1 REFUTED (ai-6), a few severity-adjusted.
- FIXED & VERIFIED: 40 (all criticals=0, all 6 highs, all confirmed mediums, most lows).
- DEFERRED: store-3 (needs PG migration+dedup rehearsal), uimobile-4 (server ban design), adminapi-5 (CORS hardening only, not a real vuln).
- VERIFY POSTURE: `scripts/preflight.sh` PASSED (Go build/vet/test + clean reinstall & prod build of stage/mobile/admin). smoke.sh green cold. Full Go suite green under `-race` (4.3s isolated). 4 new regression tests, each proven fails-when-reverted: TestQARegression_StageMessagesRequireStageRole, _DailyDoubleCompletesWhenRaterLeaves, _BodySizeCapped, TestBuzz_FairestArrivalWinsWithinWindow.

## STAGING VERIFICATION — DONE (2026-07-25, against local docker-compose stack: Postgres+Redis+gameserver)
All 5 verified against real infra. New tests added; DB-backed ones self-skip without YFI_TEST_DSN.
1. store-1 — VERIFIED. Restarted the Postgres-backed gameserver cold: boot log loaded the attached
   board (no "no board attached" warning). Proved the old path would fail: 0001 `board_cells` has 0
   rows for the session, while the new `game_sessions.board_id`->0002 resolution finds 10 cells.
2. adminapi-2/ai-5 — VERIFIED against live Postgres. New `TestStaging_RebuildLayout_AtomicRollback`
   (internal/store, YFI_TEST_DSN-gated, ran in a golang:1.26 container on the compose network): a good
   rebuild commits (10 cells, 2 placements); a rebuild with a bad track_id (FK violation mid-tx) rolls
   back ENTIRELY — board unchanged, category still original "Cat1". Ran on a throwaway board, cleaned up.
3. transport-1 — VERIFIED. New deterministic unit tests `TestConnDropClosesStreamOnOverflow` +
   `TestConnStopIsIdempotent` (internal/transport): send-queue overflow now closes the stream
   (CancelRead+Close path), proven fails-when-reverted (writer-only stop -> ZOMBIE). transport-2's
   "legit sessions accepted with MaxIncomingStreams=2" is covered by the e2e suite over real quic-go.
4. TLS env-gate — VERIFIED with a correction. The fail-closed guard is RUNTIME (fires on page load in
   the browser), NOT build-time: a prod build with cleartext VITE_HTTP_URL still compiles; it throws
   when loaded. Guard string confirmed baked into the prod bundle; prod default is https. Predicate
   verified: prod+http -> refused, prod+https -> OK, dev+http -> OK (local dev unbroken).
5. spotify-1 — VERIFIED under `-race`. New `TestValidToken_ConcurrentRefreshSingleFlight`: 20
   concurrent callers on an expired token trigger exactly 1 grant (max in-flight concurrency 1),
   rotated refresh token stored intact. Proven fails-when-reverted (20 grants without refreshMu).
   Tested with a fake token endpoint — did NOT hit real Spotify, so no live token was risked.

## Open contract questions for owner
- transport-3 (nonce.go LOCKED): only the doc comment was corrected + kept dead Token()/sign() (tests reference). Repo rule says never edit locked files; owner authorized this doc-only edit for the sweep. Consider removing Token()/sign() on a version bump or wiring the opaque token if §4D unpredictability is actually wanted.
- store-5 (config.go LOCKED): boot guard added in main.go instead of editing config.go. Consider making config.go error on default secrets in prod.
- uiadmin-2 (client.ts FIXED CONTRACT): frame cap added with CONTRACT-QUESTION; ratify on version bump.
- engine-2: buzz fairness is now min(EffectiveBuzzTime) via a 40ms window (owner-approved design change). Confirm 40ms window is acceptable vs instant-pause-on-buzz (letter reveal ticks up to 40ms post-first-buzz).

## Next sweep (sweep 2) recommendation
Warranted. Sweep 1 fixes are themselves new code (principle 6) — especially the engine-2 buzz-window rewrite, the transport teardown, and RebuildLayout — and deserve their own adversarial hunt. Plus: the deferred store-3 migration, and a dedicated aggressive-security pass against a locally-deployed stack (Redis+PG+TLS) that this sweep could only code-review.

---

# SWEEP 2 (2026-07-25) — DONE

Audited sweep 1's own fixes + live aggressive-security probe against the running local stack. Findings/verdicts in qa/findings-sweep2/ and qa/validation-sweep2/. Full report: docs/security/qa-sweep-2.md.

## Outcome
- 9 confirmed (0 crit/high, 4 med, 5 low); 7 were residual bugs INSIDE sweep-1 fixes. Live security probe: nothing exploitable. All 9 FIXED & VERIFIED.
- The big sweep-1 rewrites (buzz window, transport teardown, RebuildLayout tx, LoadBoard) all audited SOUND. The small "safe" fixes (handle sanitizer, reconnect, error-suppression, single-flight) grew the residuals.

## Fixed & verified
- s2-engine-1 (med): sanitizeHandle now strips Cf/Cs/Co + leading combining marks (bidi/zero-width). Was IsControl-only.
- s2-engine-2 (med): daily-double kick/disconnect now delete from e.ratings too (sets stay in sync) — no premature finish / skewed bonus.
- s2-store-001 (med): spotify refresh() got a force+failedToken path so 401 retries actually re-POST; single-flight preserved. New test TestPlay401ForcesRefreshOnLocallyLiveToken.
- s2-ui-01/02 (med, one root cause): reconnect now closes the superseded GameClient + per-client identity guard on onState — kills leak/reconnect-storm.
- s2-store-002 (low, OWNER CHOSE FIX): migration 0005 adds board_layout_cell_tracks.played; engine consumeTrack() persists, ClearPlayed on new game, LoadBoardByID restores. VERIFIED on live Postgres (TestStaging_PlayedStatePersistsAcrossReload, non-vacuous).
- s2-store-003 (low): Search() now maps release year.
- s2-admin-2 (low): auth compares SHA-256 digests (no length-timing leak); secret digest precomputed once.
- s2-admin-3 (low): spotify 502 paths log + generic message (adminapi-6 gap closed).
- s2-ui-03 (low, downgraded from med): post-connect hello/resync/heartbeat wrapped in try/catch -> close + scheduleReconnect.
- s2-ui-04 (low): stage Board clamps rows/cols to MAX 12 (corrupt-frame OOM guard).
- s2-admin-1 (low, latent): placeTrack decodes unconditionally, tolerates EOF (pos honored on chunked bodies). Not live via the browser client.

## Verification
- preflight.sh PASSED. Go suite green under -race (isolated ~4s).
- Live Postgres (migration 0005 applied to running stack): played-state + RebuildLayout staging tests green, both proven fails-when-reverted. Throwaway boards cleaned up; 6 real boards intact.
- Gameserver rebuilt+redeployed with sweep-2 code; healthz ok, board still restores.
- New regression tests all proven non-vacuous.

## Deploy notes learned
- deploy/Makefile migrate target updated to include 0005. Live DB migrated. A fresh deploy must run `make migrate` (init mount only auto-runs 0001).
- DB-backed Go tests are YFI_TEST_DSN-gated (self-skip without it); run them in a golang:1.26 container on docker network `deploy_default` since PG isn't published to host.

## Still deferred
- store-3 UNIQUE(board_id,track_id): still deferred (migration + dedup).

## Per-IP QUIC session cap — FIXED (2026-07-25, post-sweep-2, pre-sweep-3)
The sweep-1/2 deferred flood vector is closed. New server/internal/transport/limiter.go: sessionLimiter caps concurrent WebTransport sessions globally (YFI_MAX_SESSIONS, default 2000) and per client IP (YFI_MAX_SESSIONS_PER_IP, default 128); 0 disables a dimension. Wired into handleSession: acquire BEFORE Upgrade (reject flood cheaply with 503), defer release on every exit. Defaults deliberately GENEROUS so a NAT'd venue (many players, one public IP) + reconnect churn are never locked out — per-IP targets a single abusive source, global is the real ceiling.
- Verified: limiter unit tests (per-IP cap, global cap, 0-disables, prune/floor, concurrent-exact) under -race; live e2e TestE2E_PerIPSessionCapRejectsExcess opens real WebTransport sessions, confirms the N+1 from one IP is rejected pre-upgrade and a freed slot re-admits — proven non-vacuous (bypass -> admitted -> red). e2e suite still green (legit multi-session-from-127.0.0.1 admitted under default cap => no false-reject).
- Deployed live (2000/128 confirmed in container env). .env.example + docker-compose.yml document the vars.

---

# SWEEP 3 (2026-07-25) — DONE

Convergence sweep: audited sweep-2 fixes + the new session limiter, fresh-eyes crown-jewel pass. Findings/verdicts in qa/findings-sweep3/. Report: docs/security/qa-sweep-3.md.

## Outcome: 2 findings (1 high, 1 low), both FIXED & VERIFIED. Crown jewels all HELD.

- Per-IP QUIC session cap (the long-deferred item): FIXED before the sweep proper. limiter.go (global YFI_MAX_SESSIONS=2000 + per-IP YFI_MAX_SESSIONS_PER_IP=128, generous for NAT'd venue, 0 disables). Wired in handleSession (acquire pre-upgrade, defer release). Unit tests + live e2e TestE2E_PerIPSessionCapRejectsExcess (non-vacuous). Deployed. .env.example + compose + Makefile updated.
- s3-fe (HIGH): buzz ordering used client-reported RTT via EffectiveBuzzTime -> a forged high RTT bought a ~50ms head start and won every contested buzz. Latent until sweep-1's engine-2 wired EffectiveBuzzTime into selection. FIX (owner decision): arrival-only ordering — RTT dropped from ordering entirely (QUIC SmoothedRTT unreachable in webtransport-go; echo-measurement still gameable by pong-delay; RTT comp can't be both fair and unforgeable on a hostile client). Regression test TestBuzz_EarliestArrivalWinsAndForgedRTTBuysNothing (forged RTT loses to earlier arrival), non-vacuous.
- s3-sanitize (LOW): sweep-2 sanitizeHandle stripped U+200D ZWJ, shattering emoji sequences. FIX: exempt ZWJ, keep bidi/zero-width strip. Test TestQARegression_SanitizeHandle, non-vacuous.

## Audited SOUND
Session limiter (pairing/panic-path/TOCTOU/reject-clean/no-XFF), daily-double ratings sync, played persistence, spotify forced-refresh single-flight, digest auth. Crown jewels: §4A sanitization (every mobile frame traced clean), §4B arrival authority (now the SOLE ordering input), §4D nonce coverage, atomic single-winner, scoring, deadlock escapes, pickTrack nil-safety — all HOLD.

## Verification
preflight PASSED; Go suite green under -race; gameserver redeployed with s3 code, healthz ok. Both new regression tests proven fails-when-reverted.

## Still deferred
- store-3 UNIQUE(board_id,track_id): migration + dedup, low, admin-only. Unchanged.

## Convergence verdict
CONVERGED. Sweep 3 = 1 high (a latent activation, closed) + 1 low cosmetic; crown jewels held. A sweep 4 is low-yield unless new features land. Accepted residual risk: the deferred low store-3, and inherent zero-trust limits already documented (DeviceFP ban evasion; RTT comp dropped as unforgeable-vs-fair impossible).

## Resume checklist
- Re-read this file. Check qa/findings/*.json and qa/validation/*.json.
- Run qa/smoke.sh + `cd server && go test ./...` for a clean baseline before edits.
