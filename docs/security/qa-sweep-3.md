# QA Sweep 3 — Findings & Remediation

Date: 2026-07-25. Scope: a narrowing convergence sweep — adversarially audit
sweep 2's own fixes plus the newly-added per-IP session limiter, and a
fresh-eyes pass on the crown-jewel invariants. Method unchanged.

## Preamble: the per-IP QUIC session cap (closed before this sweep)

The flood vector deferred through sweeps 1-2 (`MaxIncomingStreams` bounds streams
only within a session, so one host could open unbounded sessions) is now fixed.
`server/internal/transport/limiter.go` adds a `sessionLimiter` capping concurrent
WebTransport sessions globally (`YFI_MAX_SESSIONS`, default 2000) and per client
IP (`YFI_MAX_SESSIONS_PER_IP`, default 128); 0 disables a dimension. Wired into
`handleSession`: acquire before the WebTransport upgrade (reject a flood cheaply
with 503), `defer release` on every exit. Defaults are deliberately generous so a
NAT'd venue where many players share one public IP is never locked out. Verified
by unit tests (per-IP cap, global cap, 0-disables, prune/floor, concurrent-exact)
under `-race` and a live e2e (`TestE2E_PerIPSessionCapRejectsExcess`) that opens
real sessions and confirms the N+1 from one IP is rejected pre-upgrade and a freed
slot re-admits — proven fails-when-reverted. Deployed live.

## Summary

- 2 confirmed findings (1 high, 1 low). Both fixed and verified.
- Strong convergence signal: the fresh-eyes crown-jewel pass confirmed §4A
  sanitization, §4B arrival authority, §4D nonce coverage, atomic single-winner,
  scoring, deadlock escapes, and pickTrack all HOLD. The audit of sweep 2's fixes
  and the new limiter found only ONE low residual. The single high was surfaced
  not by the fix-audit but by fresh eyes, and — notably — it was activated by a
  sweep-1 fix rather than being newly written.

## Findings

### HIGH — s3-fe: client-controlled RTT poisoned buzz ordering
Buzz ordering used `EffectiveBuzzTime(arrivalMs, p.RTTMs)`, where `p.RTTMs` was
blended from the client's own heartbeat (`hb.RTTMs`). Because effective time is
`arrivalMs - min(RTT/2, 50ms)`, a HIGHER reported RTT yields an EARLIER effective
time. A hostile mobile forging `{rttMs: 100}` earned a permanent ~50ms head start
and won essentially every contested buzz inside the 40ms collection window,
directly violating the handoff's own invariant that a forged RTT must buy no
advantage. The server never independently measured RTT.

This became live only because sweep 1's engine-2 fix wired `EffectiveBuzzTime`
into winner selection (it was previously computed and discarded). The fix was
correct per §4B/§4C, but the contract assumed a trustworthy RTT the server never
had.

Investigated the robust fixes and hit two hard constraints: QUIC's own
`SmoothedRTT` is unreachable (webtransport-go does not expose the underlying
`*quic.Conn`), and any echo-based server measurement is still gameable by a
client that delays its pong to inflate measured RTT (and §4C rewards higher RTT).
Any RTT compensation is fundamentally gameable under a zero-trust-client model.

**Fix (owner decision): arrival-only ordering.** Buzzes are ordered by the
server-stamped `arrivalMs` alone (tie-break by playerID); the client's RTT is
ignored for ordering. `EffectiveBuzzTime` is no longer applied. On a same-LAN
venue real RTT spread is tiny (<10ms), so the fairness cost is negligible and the
exploit is fully closed with an unforgeable input. The collection-window
single-winner machinery (atomic lock, eligibility re-check, teardown guards) is
unchanged. Regression test `TestBuzz_EarliestArrivalWinsAndForgedRTTBuysNothing`:
the later-arriving player forges RTT 100 and still LOSES to the earlier real
arrival; proven fails-when-reverted (re-adding RTT to ordering flips the winner).

### LOW — s3-sanitize: emoji ZWJ handles shattered by the sweep-2 sanitizer
Sweep 2's `sanitizeHandle` broadened the strip to all `unicode.Cf`, which
includes U+200D ZERO WIDTH JOINER — required to bind legitimate emoji ZWJ
sequences (👨‍👩‍👧, 👨‍💻). Stripping it silently split a valid emoji handle into
its component glyphs — a cosmetic regression from the sweep-2 fix, no security
impact. **Fix:** exempt U+200D from the strip (it carries no bidi/reorder risk);
the valuable removal of bidi overrides / zero-width spaces / BOM stays. Regression
test `TestQARegression_SanitizeHandle` asserts both halves (bidi stripped, ZWJ
emoji preserved); proven fails-when-reverted.

## Invariants confirmed HOLD (fresh-eyes crown-jewel pass)

- §4A sanitization — every mobile-reaching frame traced clean (lockout, buzz
  result, vote state, scoreboard, masked reveal); the mask path emits only
  revealed chars + lengths, and the noise phase hides even the length; fullSync
  to a reconnecting mobile carries no trusted fields.
- §4B arrival authority — `arrivalUnixMs` is server-stamped pre-decode and (after
  this sweep) the SOLE ordering input; client time never influences ordering.
- §4D nonce — every state-mutating client action validates the nonce.
- Atomic single-winner — one winner via the lock, `buzzWinner` set only on the
  Run loop.
- Scoring, state-machine deadlock escapes, pickTrack nil-safety — all hold.

## Audited SOUND (sweep-2 fixes + limiter)

The session limiter (acquire/release pairing airtight incl. the panic path, no
TOCTOU, reject-before-upgrade clean, correctly not consulting X-Forwarded-For
since it is direct QUIC with no L7 proxy), daily-double ratings/pool sync, played
persistence, spotify forced-refresh single-flight, and the digest auth were all
audited and found sound.

## Verification

- `scripts/preflight.sh` passed. Full Go suite green under `-race`. Gameserver
  rebuilt and redeployed with sweep-3 code; healthz answers.
- Both new regression tests proven non-vacuous.

## Still deferred

- store-3 `UNIQUE(board_id, track_id)` — migration + existing-row dedup; unchanged
  from sweep 1, low, admin-only.

## Convergence

Sweep 3 found 1 high (a latent activation, now closed) + 1 low cosmetic, and the
crown jewels all held. The codebase is converged: a sweep 4 would be
low-yield unless new features land. Residual accepted risk is limited to the
deferred low store-3 item and the inherent zero-trust-client limitations already
documented (DeviceFP ban evasion; RTT compensation cannot be both fair and
unforgeable, hence dropped).
