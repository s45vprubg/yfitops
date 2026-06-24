# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased] — 2026-06-24

### Added — Spotify token refresh for long games
- `server/internal/spotify/spotify.go`: capture `expires_in`; new `ValidToken()`
  returns a non-expired access token, refreshing via the stored refresh token
  inside a 2-minute skew window. Injectable clock for tests.
- `server/internal/admin/`: `SpotifySearcher.ValidToken` + `RegisterSpotifyToken`
  mounts `GET /api/spotify/token` (admin-Bearer gated) independently of Postgres
  so the Stage can fetch tokens in in-memory dev mode too.
- `web/stage`: `SpotifyAudioPlayer` now takes an async token provider; the SDK's
  `getOAuthToken` fetches a fresh token from `/api/spotify/token` on every call,
  so audio survives the ~1h Spotify access-token TTL across a multi-hour game.
- `web/stage/src/config.ts`: `fetchSpotifyToken()` helper; JOIN_URL default fixed
  to the mobile dev port (8780).

### Security — OAuth state CSRF protection
- `server/cmd/gameserver/main.go`: `/auth/spotify` now mints a random state in a
  short-lived HttpOnly cookie and the callback verifies it (constant-time),
  replacing the constant `"yfitops"` state.

### Changed — dev launcher
- `scripts/dev-up.sh`: sources `deploy/.env` for Spotify creds, uses 127.0.0.1
  (Spotify loopback requirement), passes `VITE_STAGE_SECRET` + Spotify env to
  the backend so real audio is testable via the launcher.

### Fixed — E2E test follows stage-secret gating
- `server/test/e2e_webtransport_test.go`: the stage role is now gated by the
  shared secret (a trusted client that receives reveal data should be), so the
  E2E test's stage Hello now sends `AdminSecret`. No production code changed.

## [Unreleased] — 2026-06-23

### Added — Track Management & Board Builder

- **DB migration** (`deploy/migrations/0002_boards.sql`): Independent `boards` table, board-scoped `board_tracks` library (dedup per board via UNIQUE constraint), `board_layout_cells` and `board_layout_cell_tracks` for grid composition. `game_sessions.board_id` FK for attaching boards at game time.
- **Spotify Search** (`server/internal/spotify/search.go`): `Search()` and `GetPlaylistTracks()` methods on the existing Spotify client. No new OAuth scopes required.
- **Admin REST API** (`server/internal/admin/`): New package with auth middleware (constant-time Bearer compare), top-level CORS handler wrapping the HTTP mux, and handlers for board CRUD, track import, layout management, Spotify search proxy, and playlist bulk import. Registered on the existing HTTP mux at `/api/*`.
- **Engine ReloadBoard** (`server/internal/game/engine.go`): Thread-safe board hot-reload via the command channel. Used when attaching a board to a live session.
- **Admin Frontend — Board Builder**: New "Board Builder" tab in the admin UI with drag-and-drop (via @hello-pangea/dnd). Features: board create/delete, Spotify search + playlist import, holding area, visual 5xN grid with droppable cells, category management, auto-save.
- **Admin Frontend — Control Room board loader**: "Load board…" dropdown in the Control Room TopBar lets the admin attach a built board to the live session. Board appears immediately in the Queuing panel via WebTransport broadcast. Shows "No board loaded" message when no board is attached.
- **Admin Frontend — Login persistence**: Admin secret saved to `localStorage` on successful authentication; auto-restores on page reload. Cleared on logout.
- **Admin Frontend — REST client** (`web/admin/src/useAdminApi.ts`): Typed fetch wrapper for all admin endpoints.
- **No sample board in Postgres mode**: When Postgres is connected, the engine starts with no board (sample board only used in in-memory fallback mode). Directs user to the Board Builder.

**Security:** REST endpoints gated by `Authorization: Bearer <ADMIN_SECRET>` with `crypto/subtle.ConstantTimeCompare`. Track metadata never reaches mobile clients (isolated from WebTransport broadcast path).

**No fixed contract files modified.**

### Changed — localhost to 127.0.0.1 migration
- `deploy/.env.example`: Spotify redirect URI default updated to `http://127.0.0.1:8777/auth/spotify/callback`
- `deploy/docker-compose.yml`: fallback Spotify redirect URI updated to 127.0.0.1
- `web/stage/src/config.ts`: fallback host changed from "localhost" to "127.0.0.1"
- `web/admin/src/config.ts`: fallback host changed from "localhost" to "127.0.0.1"
- `web/mobile/src/lib/env.ts`: fallback host changed from "localhost" to "127.0.0.1"
- `server/internal/spotify/spotify_test.go`: test assertions updated to match 127.0.0.1 redirect URI

**Reason:** Spotify does not allow `localhost` as a redirect URI; requires `127.0.0.1`.

**Note:** `server/internal/config/config.go` is a fixed contract and was NOT modified.
The override lives in `deploy/.env` (user-managed, gitignored).

### Added — Sample board fallback for empty Postgres
- `server/cmd/gameserver/main.go`: After connecting to Postgres, checks if a board
  exists for the session. If not, injects `store.SampleBoard()` into the engine so
  the game is playable without manual board curation.

### Added — Spotify OAuth token push via WebTransport
- `server/internal/spotify/spotify.go`: Added `ExchangeToken()` method that returns
  the access token alongside storing it server-side.
- `server/internal/game/engine.go`: Added `PushSpotifyToken(token)` method that
  broadcasts the token to all Stage-role connections via a `"spotifyToken"` message.
- `server/cmd/gameserver/main.go`: OAuth callback now calls `PushSpotifyToken`
  instead of redirecting to the Stage URL.
- `web/stage/src/net/useGame.ts`: Listens for `"spotifyToken"` server message and
  hot-swaps to SpotifyAudioPlayer when received.
- `web/stage/src/audio/index.ts`: Exports `SpotifyAudioPlayer` class for direct use.
- `web/stage/src/components/SpotifyBanner.tsx`: Removed "Connect Spotify" button
  (status-only now).
- `web/shared/protocol.ts`: Added `"spotifyToken"` to `ServerMsgType` union.

**CONTRACT-QUESTION:** `"spotifyToken"` is a new server message type defined outside
`protocol.go` (in `engine.go` as a local constant) because `protocol.go` is a fixed
contract. If accepted, it should be moved into the fixed contract on a version bump.

### Added — Stage role authentication
- `server/internal/game/engine.go`: `onHello` now gates Stage role with the same
  `ADMIN_SECRET` check as Admin role.
- `web/stage/src/config.ts`: Added `STAGE_SECRET` config (reads `VITE_STAGE_SECRET`).
- `web/stage/src/net/useGame.ts`: Sends `adminSecret` field in the hello message.

### Changed — Spotify Connect moved to Admin
- `web/admin/src/components/TopBar.tsx`: Added "Connect Spotify" button that opens
  the OAuth flow in a new tab.
- `web/stage/src/components/SpotifyBanner.tsx`: Removed the connect button; shows
  status only.

### Changed — Skip threshold slider range
- `web/admin/src/components/TopBar.tsx`: Changed slider min from 50 to 0.
