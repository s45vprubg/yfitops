# yfitops V2 — Build Contract

This is the shared spec every build agent reads before writing code. The
**contracts are already written and tested** — do not change them; build
against them.

## Repo layout

```
server/                       Go backend (module github.com/s45vprubg/yfitops/server)
  cmd/gameserver/             main entrypoint
  internal/protocol/          FIXED: wire types (protocol.go), state machine (state.go)
  internal/game/              FIXED: scoring.go (+ tests). TO BUILD: engine.go, board.go, player.go
  internal/anticheat/         FIXED: latency.go, nonce.go (+ tests)
  internal/transport/         TO BUILD: WebTransport/QUIC server + connection hub
  internal/store/             TO BUILD: Redis (atomic buzz lock) + Postgres (persistence)
  internal/spotify/           TO BUILD: Web API device-routing client + OAuth
  internal/lyrics/            TO BUILD: LRCLIB client
  internal/config/            FIXED: config.go
  test/                       TO BUILD: integration tests
web/
  shared/                     TO BUILD: protocol TS types (mirror protocol.go), scoring.ts (mirror scoring.go), transport client
  stage/                      TO BUILD: presentation screen (React+Vite+Tailwind)
  mobile/                     TO BUILD: buzzer PWA
  admin/                      TO BUILD: control room
deploy/                       TO BUILD: docker-compose, Dockerfile, migrations
reference/legacy-core/        legacy banovik/NameThatSpotify for logic reference (§10)
```

## Fixed contracts (DO NOT MODIFY)

- `protocol.go` — every message between server and clients. `ClientEnvelope`/
  `ServerEnvelope` with `t` (type) + `d` (data) + `n` (nonce). Roles: stage,
  mobile, admin.
- `state.go` — `GameState` enum is the ONLY state info mobile gets (§4A).
  `TrustedReveal(role)` gates who receives reveal/lyrics/admin payloads.
- `scoring.go` — Base+Bonus linear decay, math.Floor. Row1..5 = 100..200 max.
  Partial = 50 + remaining pool. Daily-double star multiplier.
- `anticheat/latency.go` — `EffectiveBuzzTime = arrival - min(rtt/2, 50ms)`.
- `anticheat/nonce.go` — `NonceGate`: bump on every state transition, reject
  stale nonces (§4D).

## Hard rules from the design doc

1. **§4A Client sanitization**: mobile clients NEVER receive track title,
   artist, URI, or lyrics. Only `GameState` flags + sanitized payloads
   (lockout, buzzResult, voteState). Trusted reveal data goes to stage/admin
   only. The engine serializes per-audience.
2. **§4B Server arrival authority**: buzz ordering uses the server's arrival
   clock only. Client timestamps are used ONLY for RTT heartbeats, never
   ordering.
3. **§4D Nonce**: actions carrying a stale nonce are dropped.
4. **Atomic buzz**: the first buzz to win the Redis lock triggers the state
   switch; all others get `buzzResult{won:false}`. Use Redis SET NX (or a Lua
   script) for the atomic single-winner guarantee.
5. **§9 Audio isolation**: Go never plays audio. On buzz, Go sends `SMsgAudio`
   {pause} directly to the stage over WebTransport (~20ms) to pause locally,
   bypassing Spotify API round-trip. Spotify Web API is used for play/track
   routing to the stage's Virtual Device.
6. **§5 Deterministic timer**: server sends `trackStart{maxPoints, basePoints,
   startTime}` once; stage computes decay at 60fps with the SAME formula as
   scoring.go (mirror it in web/shared/scoring.ts, floor-identical).

## Tech versions present

Go 1.26, Node 24, Docker. No local redis/postgres binaries — use Docker.
WebTransport: use `github.com/quic-go/webtransport-go` + `quic-go`.

## Definition of done per component

- Go code compiles (`go build ./...`) and `go vet` clean.
- Unit tests for any non-trivial logic; table-driven.
- No fixed contract files modified. If a contract seems wrong, leave a
  `// CONTRACT-QUESTION:` comment, don't edit it.
- Frontends: `npm run build` succeeds.
