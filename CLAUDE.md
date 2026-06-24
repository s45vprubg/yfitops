# yfitops V2 — Project Rules

## Contracts (MUST READ before any code change)

Before modifying any code in this repo, read and internalize:
- `docs/BUILD_CONTRACT.md` — Fixed contracts, hard rules, definition of done.
- `docs/ADVERSARIAL_REVIEW_HANDOFF.md` — Security model and known gaps.

## Fixed Contract Files (DO NOT MODIFY)

The following files are locked. Never edit them. If they seem wrong, add a
`// CONTRACT-QUESTION:` comment in the calling code instead:

- `server/internal/protocol/protocol.go`
- `server/internal/protocol/state.go`
- `server/internal/game/scoring.go`
- `server/internal/anticheat/latency.go`
- `server/internal/anticheat/nonce.go`
- `server/internal/config/config.go`

## Hard Rules

1. **Client sanitization (§4A):** Mobile clients NEVER receive track title,
   artist, URI, or lyrics. Only `GameState` flags + sanitized payloads.
   Trusted reveal data goes to stage/admin only via `TrustedReveal(role)`.
2. **Server arrival authority (§4B):** Buzz ordering uses server arrival clock
   only. Client timestamps are for RTT heartbeats only.
3. **Nonce (§4D):** Actions carrying a stale nonce are dropped.
4. **Atomic buzz:** First buzz wins the Redis lock; all others get
   `buzzResult{won:false}`.
5. **Audio isolation (§9):** Go never plays audio. On buzz, Go sends pause
   directly to stage over WebTransport.
6. **Deterministic timer (§5):** Server sends `trackStart` once; stage computes
   decay with the same formula as `scoring.go`.

## Adding New Message Types

Do NOT add new message types to `protocol.go` — it is a fixed contract.
If a new message type is needed:
1. Define it as a local constant in the package that uses it (e.g., engine.go).
2. Add a `// CONTRACT-QUESTION:` comment explaining what it is and why.
3. Document it in `docs/CHANGELOG.md`.
4. Discuss with the project owner — it can be moved into the fixed contract
   on a version bump if accepted.

## Security Model

- This runs at a hacker conference. Assume the mobile client is fully hostile.
- Stage and Admin roles MUST be gated by a secret.
- Never send sensitive data (tokens, secrets, track metadata) to mobile clients.
- Admin secret comparison should use constant-time comparison.

## Definition of Done

Run `scripts/preflight.sh` and get a green "PREFLIGHT PASSED" before claiming
any change is done. It is the gate, and it must pass — not a `go test` alone.
It does a CLEAN frontend reinstall + production build, which is the only thing
that catches a dependency referenced in code but missing from node_modules /
package.json (a dev server hides this until you load the page).

The gate enforces:
- Go: `go build ./...`, `go vet`, and `go test ./...` clean.
- Each frontend (stage, mobile, admin): clean `npm install` + `npm run build`.

Also required (not all machine-checkable):
- Unit tests for non-trivial logic; table-driven.
- No fixed contract files modified.
- Changes documented in `docs/CHANGELOG.md`.

## Dev Environment

- Backend: `cd deploy && make up` (Docker Compose: postgres, redis, gameserver)
- Frontends: `cd web/{stage,mobile,admin} && npm install && npm run dev`
- Stage requires `VITE_STAGE_SECRET` env var matching `ADMIN_SECRET`
- WebTransport requires Chromium (Chrome/Edge)
- Spotify is optional; system runs in demo mode without credentials
