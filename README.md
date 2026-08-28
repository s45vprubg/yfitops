# yfitops — Name That Spotify V2

A real-time, ultra-low-latency multiplayer music guessing game built for a
hacker-conference environment. A central **Stage** screen projects the music,
synced lyrics, and a Jeopardy-style board; attendees join from their phones and
use a **Mobile** web app as a pure physical buzzer; a host drives everything
from an **Admin** Control Room. The whole system is built defensively around a
zero-trust client model and server-authoritative anti-cheat.

The full design and threat model live in [`design_doc.md`](./design_doc.md);
the spec every build agent worked against is
[`docs/BUILD_CONTRACT.md`](./docs/BUILD_CONTRACT.md).

## Architecture

| Layer            | Tech                                   | Role |
| ---------------- | -------------------------------------- | ---- |
| Backend engine   | Go 1.26                                | Concurrent buzz/scoring/state engine. |
| Network          | WebTransport over QUIC (HTTP/3, UDP)   | No head-of-line blocking on congested venue Wi-Fi; survives IP migration. |
| Real-time cache  | Redis                                  | Atomic single-winner buzz lock (`SET NX`). |
| Persistence      | PostgreSQL                             | Audit log, scores, persistent track/board curation. |
| Frontends        | React 19 + Vite + Tailwind (PWA)       | Stage, Mobile buzzer, Admin control room. |
| Audio            | Spotify Web Playback SDK + Web API     | Stage tab is the "Virtual Device"; Go routes play/pause to it. Go never plays audio itself. |
| Lyrics           | LRCLIB                                  | Synced timestamped lyrics for the karaoke phase. |

Key design constraints (see design_doc §4, §9):

- **Client sanitization (§4A):** mobile clients never receive track title,
  artist, URI, or lyrics — only sanitized `GameState` flags. Reveal/lyrics data
  goes to Stage/Admin only.
- **Server arrival authority (§4B):** buzz ordering uses the server's arrival
  clock; client timestamps are only used for RTT heartbeats.
- **Atomic buzz (§4 / §3.4):** the first buzz to win the Redis lock flips state;
  everyone else gets `buzzResult{won:false}`.
- **Audio isolation (§9):** on buzz, Go sends a `pause` directly to the Stage
  over WebTransport (~20ms) instead of waiting on a Spotify round-trip.

## Repo layout

```
server/                 Go backend (module github.com/s45vprubg/yfitops/server)
  cmd/gameserver/       main entrypoint
  internal/             protocol, game engine, anticheat, transport, store,
                        spotify, lyrics, config
  test/                 integration tests
web/
  shared/               TS protocol types + scoring mirror + transport client
  stage/                presentation screen (React + Vite + Tailwind)
  mobile/               buzzer PWA
  admin/                control room
deploy/                 Dockerfile.server, docker-compose.yml, .env.example,
                        Makefile, migrations/
scripts/                helper scripts
reference/legacy-core/  legacy banovik/NameThatSpotify for logic reference
```

## Running the backend

Requires Docker (with Compose). From the repo root:

```bash
cd deploy
cp .env.example .env          # then edit secrets / Spotify creds AND set YFI_ENV=prod for a real event (arms the default-secret + cleartext-/ws guards)
make up                       # build + start postgres, redis, gameserver
make logs                     # tail logs
make down                     # stop
```

Or without the Makefile: `docker compose up --build` from `deploy/`.

This brings up three services:

- **postgres** (`postgres:16-alpine`) — runs every `deploy/migrations/*.sql` in
  lexical order on first boot (the whole directory is mounted as an init dir).
  To (re)apply the schema to an existing volume: `make migrate`.
- **redis** (`redis:7-alpine`) — the atomic buzz lock.
- **gameserver** — the Go engine, listening on:
  - **`:4433/udp`** — WebTransport / QUIC / HTTP3 (note: UDP).
  - **`:8777/tcp`** — plain HTTP: `/healthz`, Spotify OAuth
    (`/auth/spotify`, `/auth/spotify/callback`), `/cert-hash`, and the Admin
    REST API (`/api/*` — board/track CRUD, Spotify search proxy).

The server degrades gracefully: if Redis or Postgres are unreachable at boot it
falls back to in-memory implementations and a seeded sample board, logging the
mode of each subsystem (no silent degradation). When Postgres is available, the
engine starts with no board — use the Board Builder to create and load one.

All env vars are documented in [`deploy/.env.example`](./deploy/.env.example)
and read by `server/internal/config/config.go`.

### Before a real event: set `YFI_ENV=prod`

`YFI_ENV` defaults to `dev`, which downgrades the **default-secret boot guard**
in `server/cmd/gameserver/main.go` to a warning. Set to anything other than
`dev` and the server *refuses to start* while `ADMIN_SECRET`,
`YFI_NONCE_SECRET`, or `YFI_JOIN_SECRET` still hold the dev defaults shipped in
`.env.example`. Casing and surrounding whitespace don't matter — `prod`,
`Prod`, `production`, ` PROD ` all arm it.

Copying `.env.example` alone therefore leaves you on `changeme-admin` with the
guard disarmed — anyone who reaches the port can claim admin and stage. For an
attendee-facing deployment, set real secrets **and** `YFI_ENV=prod` in
`deploy/.env`. The guard lives in `main.go` rather than `config.go` because
`config.go` is a locked contract file.

### The cleartext `/ws` fallback is a separate, explicit decision

WebTransport needs a secure context, so a phone on a plain-HTTP LAN origin
can't use it at all. `/ws` carries the same framing over unencrypted WebSocket
for exactly that case, and it takes **two** vars — `YFI_DEV_WS=1` **and**
`YFI_INSECURE_TRANSPORT=1`. Setting only the first mounts nothing and logs a
notice naming the missing one.

It is deliberately **independent of `YFI_ENV`**. Keying it off `YFI_ENV` made
the gate mutually exclusive with its own use case: an operator who needed the
LAN fallback had to stay out of prod, which also disarmed the secret guard
above — the flag meant to harden the deployment pushed people into the weaker
configuration. Registering `/ws` with `YFI_ENV=prod` is now allowed and logs a
loud warning on every boot.

Understand what you're turning on: `/ws` has **no encryption and no cert
pinning**. Anyone on the same network can read, forge and replay frames,
including the admin/stage secret in the hello. This runs at a hacker
conference; assume they will. Use it only on a network you trust — the real
answer is TLS + WebTransport. `scripts/dev-up.sh` and
`deploy/docker-compose.dev.yml` set both vars for you.

## Running the frontends

Each app under `web/` is an independent Vite project (Node 24 recommended). For
each of `stage`, `mobile`, `admin`:

```bash
cd web/stage   # or web/mobile, web/admin
npm install
npm run dev     # Vite dev server
npm run build   # production build
```

- **stage** (port 5174) — the central presentation screen. Hosts the Spotify
  Web Playback SDK and shows the board, timer, reveal, and karaoke lyrics.
  Requires `VITE_STAGE_SECRET` matching `ADMIN_SECRET`.
- **mobile** (port 5173) — the buzzer PWA attendees load via the lobby QR code.
- **admin** (port 8779) — the host Control Room (grading, overrides, kick/ban,
  skip threshold) and Board Builder (track import, drag-and-drop grid). Gated
  by `ADMIN_SECRET`. Login persists across page reloads via localStorage.

The apps share types and the transport client from `web/shared/` (a TS mirror of
the Go protocol and scoring contracts).

## Board Builder — creating game boards

The Board Builder (in the Admin UI) is how you curate the Jeopardy-style grid:

1. **Create a board** — give it a name.
2. **Add tracks** — search Spotify by artist/song, or paste a playlist URI to
   bulk-import. Tracks are stored per-board (same song on different boards is
   fine).
3. **Build the grid** — add columns (categories), then drag-and-drop tracks from
   the holding area into cells. Always 5 rows (scoring is fixed), up to 8
   columns.
4. **Load into the game** — in the Control Room, select a board from the "Load
   board…" dropdown. The engine hot-reloads and the board appears in the Queuing
   panel immediately.
5. **Start a round** — click a cell in the Queuing panel to begin playing a
   track from that cell.

Boards auto-save on every action. Deleting a board cascade-deletes all its
tracks and layout.

## Spotify (real audio) — required setup

Real audio playback needs a **Spotify Premium** account on the Stage device and
an app registered at <https://developer.spotify.com/dashboard>:

1. Create an app, copy its Client ID / Secret into `deploy/.env`
   (`SPOTIFY_CLIENT_ID`, `SPOTIFY_CLIENT_SECRET`).
2. Add the redirect URI verbatim to the app's allowlist — default
   `http://127.0.0.1:8777/auth/spotify/callback` (note: Spotify requires
   `127.0.0.1`, not `localhost`).
3. From the Admin Control Room, click "Connect Spotify" — this opens the OAuth
   flow. The token is pushed to the Stage via WebTransport automatically.

**Demo / mock fallback:** without Spotify credentials the system still runs end
to end — the engine, buzzing, scoring, state machine, and a seeded sample board
all work — but there is **no real audio**. This is the intended way to develop
and demo the gameplay loop without a Premium account.

## WebTransport self-signed cert (dev flow)

WebTransport over QUIC requires TLS. In dev the server **auto-generates a
self-signed cert** on first boot (`transport.GenerateSelfSigned`) into the
`/certs` volume — a writable cert dir is all you need.

Browsers won't trust that cert by default, so WebTransport clients connect using
`serverCertificateHashes`. The server publishes the cert's base64 SHA-256 at:

```
http://localhost:8777/cert-hash
```

The web transport client (`web/shared/client.ts`) fetches that hash and pins it
when opening the WebTransport session. The cert is persisted in the `certs`
volume so the hash stays stable across restarts (use `make clean` to wipe it).

## Known limitations / not runtime-verified

Honest accounting of what is and isn't proven:

- **Verified:** `docker compose config` parses cleanly, including the critical
  `4433:4433/udp` mapping and the `migrations/` init-bind. The Go server image
  builds from `deploy/Dockerfile.server` (pure Go, static, CGO off). Go unit
  tests cover the fixed contracts (scoring, anticheat, nonce gate).
- **Not runtime-verified (needs manual checking):**
  - A full `docker compose up` with live gameplay end-to-end has **not** been
    run here. Bring it up and watch the gameserver logs for the data-layer mode
    lines.
  - **Real Spotify playback** is untestable without Premium credentials and the
    OAuth flow above — only the mock/no-audio path has been exercised.
  - **WebTransport requires a Chromium-based browser** (Chrome/Edge). Firefox
    and Safari support is incomplete; the self-signed `serverCertificateHashes`
    flow is Chromium-specific. There is no fallback transport.
  - The self-signed cert flow is a **dev convenience**. A real deployment needs
    a proper cert and a reverse proxy / SSL termination story that preserves
    HTTP/3 + UDP (design_doc §11) — not configured here.
