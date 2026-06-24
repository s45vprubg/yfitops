# Adversarial Review Handoff — yfitops V2

This document hands the security/adversarial review off to a dedicated,
ungated security agent. It exists because the build was done by a fleet of
construction agents whose own review is inherently sympathetic — an independent
adversary should try to *break* the threat model, not confirm it.

The design doc (`design_doc.md`) frames this as a **zero-trust client** system
running at a **hacker conference**, so the attacker is assumed to be skilled,
local, and motivated (memory interception, replay, signal spoofing, script
automation). Review accordingly: assume the mobile client is fully hostile and
controlled by the attacker.

## What to load first

1. `design_doc.md` §4 (Adversarial Threat Modeling) and §9 (Admin security) —
   the contract the system claims to meet.
2. `docs/BUILD_CONTRACT.md` — the fixed interfaces and the hard rules.
3. The code map below.

## Code map (review targets, by risk)

| Area | File | Why it's security-critical |
| --- | --- | --- |
| **Client sanitization (§4A)** | `server/internal/game/engine.go` (broadcast helpers, `sendFullSync`, `revealTo`, `dispatchAdmin`) | The whole "client is blind" guarantee. Any path that sends artist/song/URI/lyrics to a mobile-role conn is a critical breach. |
| Role/audience gating | `server/internal/transport/hub.go` (`Broadcast`, `roleOf`, `SetRole`), `server/internal/protocol/state.go` (`TrustedReveal`) | If a mobile conn can be promoted to stage/admin, sanitization collapses. |
| **Buzz atomicity (§3.4)** | `server/internal/store/lock_redis.go`, engine `onBuzz` | First-writer-wins. Look for races, TOCTOU, or a second winner. |
| **Server arrival authority (§4B)** | `server/internal/transport/server.go` (`serveStream` arrival stamp), engine `onBuzz` | Client timestamps must never affect ordering. Confirm `arrivalUnixMs` is the only ordering input. |
| **Latency comp cap (§4C)** | `server/internal/anticheat/latency.go` | The 50ms cap must hold; a forged high RTT must NOT buy advantage. |
| **Nonce / replay (§4D)** | `server/internal/anticheat/nonce.go`, engine nonce checks | Stale-nonce actions must be dropped. Is the nonce actually bumped on EVERY transition? Can a client predict/forge a future nonce token? |
| **Admin auth (§9)** | engine `onHello` (admin secret check), `dispatch` (role gate on `admin.*`) | Secret comparison should be constant-time-ish; every admin action must re-check role. Look for an admin action reachable without the secret. |
| Input handling | `server/internal/transport/framing.go` (length-prefix), engine JSON unmarshal | Hostile frames: giant length prefix (there's a 1MiB cap — verify), malformed JSON, unknown types, oversized handles, unicode/emoji handles, negative numbers in award/threshold/stars. |
| Join token (§3.1) | `config.go` (`JoinSecret`), **NOT YET WIRED** | The rotating QR join token is specified but the engine does not currently validate `joinToken` on Hello. **This is a known gap — see below.** |
| Spotify OAuth | `server/internal/spotify/spotify.go`, `main.go` callback | Token storage, refresh logic, the OAuth `state` param (currently a constant "yfitops" — CSRF-weak, flag it). |

## Known gaps / honest limitations (do not re-report as novel — but DO assess severity)

These are already known. Confirm them, rate them, and propose fixes — but they
are not "discoveries":

1. **Join token not enforced.** `HelloData.JoinToken` and `cfg.JoinSecret`
   exist, but `onHello` does not validate the rotating QR token (§3.1 soft
   geo-enforcement). Anyone who can reach the WebTransport port can join. Assess
   impact for the conference threat model and propose the HMAC-rotation check.
2. **Admin secret comparison** — verify whether it's constant-time
   (`hmac.Equal`/`subtle.ConstantTimeCompare`) or a plain `==` (timing oracle).
3. **OAuth `state` is a fixed string** in `main.go` (`audio.AuthURL("yfitops")`)
   — no per-session CSRF binding on the Spotify callback.
4. **Self-signed cert + `InsecureSkipVerify`** is used in the E2E test and the
   dev cert flow. Confirm production guidance (real cert) is documented and that
   `InsecureSkipVerify` appears ONLY in test code, never in shipped client/server.
5. **Spotify playback path is unrun** — no Premium creds in this environment.
   Review the code for correctness; runtime behavior is unverified by design.
6. **WebTransport browser clients are unrun headlessly** — the Go E2E test
   exercises the server path with a Go WebTransport client, but the actual
   React clients in a real Chromium are not automatically tested.

## Attacks worth specifically attempting

- Connect as `mobile`, then send a `hello` claiming `role: admin` WITHOUT the
  secret, or with a wrong secret — confirm you are not promoted and cannot issue
  `admin.*`. Then try sending `admin.grade` directly on a mobile conn.
- Connect as `mobile` and send `resync` / any message that triggers
  `sendFullSync` — confirm you receive ONLY the sanitized state flag, never
  board/reveal/trackStart.
- Race many `buzz` frames with valid nonces — confirm exactly one
  `buzzResult{won:true}`.
- Replay an old `buzz` frame with a stale nonce after a state transition —
  confirm it's dropped.
- Send `admin.award` with a huge or negative `delta`; `admin.setThresh` with
  out-of-range percent; `rate` with stars outside 1-5 — confirm clamping/validation.
- Forge heartbeats with absurd `clientTime` — confirm they never affect buzz
  ordering or scoring.
- Oversized / malformed frames against `framing.go` — confirm the 1MiB cap and
  that a bad frame doesn't tear down the connection or panic.

## How to run things

- Unit + E2E (no infra): `cd server && go test ./...`
- Full incl. live Redis/Postgres: `./scripts/integration-test.sh` (needs Docker).
- Boot the server locally: `cd server && go run ./cmd/gameserver` (falls back to
  in-memory if Redis/PG absent; HTTP on :8777, WebTransport on :4433).

## Deliverable expected from the security agent

A findings report: each finding with severity (crit/high/med/low), the file:line,
a concrete repro or argument, and a proposed fix. Separate **confirmed exploits**
from **hardening suggestions**. Where a finding contradicts a design-doc claim,
cite the section it violates.
