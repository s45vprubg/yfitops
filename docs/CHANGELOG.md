# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased] — 2026-06-29

### Added — Server-authoritative streaming letter reveal (stage + mobile)
- The decrypt reveal (Artist/Song revealed letter-by-letter) is now driven by
  the server and streamed to BOTH the projector and the player phones in the
  same broadcast, so a phone can never learn a letter before the projector
  shows it. Previously the stage held the full answer and animated locally, and
  mobile got nothing (§4A). New security invariant: mobile still never receives
  the trusted `SMsgReveal`/lyrics/adminView/board/trackStart — only a masked
  frame carrying letters already shown on the stage.
- `server/internal/game/reveal.go` (new): `revealClock` (count-based, pause = do
  not advance), deterministic per-round reveal order, per-char mask builder,
  the `maskedReveal` CONTRACT-QUESTION message + payload, and the live-tunable
  `revealConfig` knobs (interval, phase-1 delay, alternate artist/song).
- `server/internal/game/engine.go`: reveal clock lifecycle (arm at `startTrack`,
  phase-1 one-shot + self-rescheduling letter ticker via `submit`, pause while
  not ROUND_ACTIVE, finalize at `enterKaraoke`/`enterDailyDouble`, force-field
  on `gradePartial`, teardown on end/transition/reset); `broadcastMask` fans one
  identical envelope to stage+mobile+admin; `sendFullSync` resyncs a
  reconnecting client to the current mask. Live knob handler
  `onAdminSetRevealCfg` (admin-gated, clamped, applies NEXT round) + echo.
- `server/cmd/gameserver/main.go`: `YFI_REVEAL_INTERVAL_MS` / `_PHASE1_MS` /
  `_ALTERNATE` seed the knob defaults (config.go is locked).
- `web/shared/protocol.ts`: `maskedReveal` + `MaskedRevealData`,
  `admin.setRevealCfg` + `adminRevealCfg` + payload interfaces.
- `web/stage`: `ActiveRound` renders from the server mask (revealed letters in
  the locked color, hidden slots as cosmetic noise); local `computeFrame` reveal
  driver retired (`glyphAt` kept for noise).
- `web/mobile`: new `RevealStrip` renders the same mask (§4A carve-out in
  `useGame`); shown under the buzzer during a round and at karaoke.
- `web/admin`: collapsible "Reveal settings" panel (interval + noise-delay
  sliders, alternate toggle) seeded from the server echo; changes apply next
  round.
- Tests: `server/internal/game/reveal_test.go` (co-broadcast, alternation,
  pause-on-buzz, karaoke finalize, knob apply-next-round + role gate); the §4A
  e2e guard (`e2e_webtransport_test.go`) rewritten to a co-visibility invariant
  (every mobile mask byte-matches a stage mask; no trusted frame to mobile).

### Fixed — Stage (projector) audio silently dead after autoplay block
- Root cause: the activation overlay added in the prior release had two
  silent-failure holes. (1) `SpotifyAudioPlayer.activate()` swallowed a failed
  `activateElement()` and `useGame` marked `audioActivated: true` regardless, so
  the overlay dismissed even when the hidden `<audio>` element was never
  unlocked. (2) The `autoplay_failed` SDK event only `console.warn`ed. Net
  effect: Spotify transferred playback to the stage's virtual device but the
  browser kept the element muted — no sound and no tab media indicator, with no
  way to recover.
- `web/stage/src/audio/spotify.ts`: `activate()` now returns whether the element
  actually unlocked and replays any play() that arrived while locked;
  `autoplay_failed` notifies a new `onAutoplayBlocked` subscription; play()
  remembers the pending track so activation resumes it immediately.
- `web/stage/src/audio/types.ts` + `mock.ts`: `activate()` returns `boolean`;
  added optional `onAutoplayBlocked`.
- `web/stage/src/net/useGame.ts`: only dismiss the overlay when activation
  succeeds; re-show it on `onAutoplayBlocked` so the operator can re-enable.

### Fixed — Playlist import 403 (Feb-2026 Spotify Web API migration)
- Root cause: the `GET /playlists/{id}/tracks` endpoint was deprecated in
  Spotify's February 2026 Web API migration and now returns 403 for
  Development-Mode custom OAuth clients (search and catalog reads still work,
  which is what made it look like a scope/quota problem). The fix is the
  replacement endpoint, not a permissions change.
- `server/internal/spotify/search.go`: Switched `GetPlaylistTracks` to
  `GET /playlists/{id}/items`, which requires a `market` (now `from_token`) and
  nests the track under `items[].item` instead of `items[].track`. Also calls
  `ValidToken` up front so a cold-started server mints an access token from a
  restored refresh token before fetching (the old 401-retry path never fired
  with an empty token).
- `server/internal/spotify/spotify.go`: Added `playlist-read-private` /
  `playlist-read-collaborative` scopes (the new endpoint still needs them),
  plus `RefreshToken()`/`RestoreRefreshToken()` accessors and logging of the
  scopes Spotify actually grants at OAuth (ground-truth debugging).
- `server/cmd/gameserver/main.go`: Persist the Spotify refresh token to
  `certs/spotify_refresh_token` (gitignored) on OAuth callback and restore it
  on boot, so a dev-server restart no longer forces a re-auth. Overridable via
  `YFI_SPOTIFY_TOKEN_FILE`.
- `server/internal/spotify/integration_test.go`: New `spotify_integration`
  build-tagged live harness (token refresh / search / playlist import) that
  pinpoints which capability breaks when Spotify changes something again. Never
  runs in the default suite or preflight.

### Changed — dev-up.sh runs on real Postgres/Redis
- `scripts/dev-up.sh` + `deploy/docker-compose.dev.yml`: The dev launcher now
  starts Postgres/Redis (ports published to loopback), applies migrations, and
  wires the host-side gameserver to them — matching the deployed server. It
  fails loudly if the backend falls back to in-memory (which silently drops the
  board-management API and 404s board creation).

### Added — New Game reset
- `server/internal/game/engine.go`: `ResetToLobby()` clears round state, resets
  all scores to 0, resets track Played flags, unloads board, transitions to LOBBY.
- `server/internal/admin/`: REST endpoint `POST /api/game/reset` calls ResetToLobby.
- `web/admin`: TopBar shows "New Game" button when state is GAME_OVER.

### Added — Spotify Web Playback SDK audio activation
- `web/stage/src/audio/spotify.ts`: calls `activateElement()` to unlock browser
  autoplay policy; listens for `autoplay_failed` event.
- `web/stage/src/App.tsx`: one-time "Enable Audio" overlay appears when Spotify
  SDK connects; a click activates the player and dismisses the overlay.

### Added — Partial reveal on stage
- `server/internal/game/engine.go`: `gradePartial` now sends a `partialReveal`
  message to the stage indicating which field (artist/song) was correctly guessed.
- `web/stage`: on `partialReveal`, the guessed field is displayed in full while
  the other continues its cycling animation.

### Added — Track-end auto-ends round
- `server/internal/game/engine.go`: `onStagePlayerState` now handles `trackEnded`
  during ROUND_ACTIVE (in addition to KARAOKE), calling `endRound()` so a song
  that finishes without anyone buzzing returns to the board automatically.

### Changed — Admin UI state awareness overhaul
- `web/admin/src/components/TopBar.tsx`: single game action button (Start/End/New);
  Start Game disabled until Spotify connected; Pause/Play merged into one toggle
  button; board selector disabled during active game.
- `web/admin/src/components/BoardPanel.tsx`: cells only clickable when state is
  BOARD/KARAOKE/TRANSITION and Spotify connected; track counts enlarged.
- `web/admin/src/components/EvaluationPanel.tsx`: grade buttons only active during
  ADJUDICATE; artist/song fields enlarged with softer color.
- `web/admin/src/App.tsx`: mid-game Spotify disconnect warning banner.

### Changed — Stage animation rewrite (decrypt.ts)
- Phase 1 (0–5s): 20 random characters cycling at ~5fps.
- Phase 2 (5s): snaps to exact answer length, spaces shown.
- Phase 3 (5s+): one random character revealed every 2s. Spaces free.
- Animation freezes when a player buzzes and resumes on grade.
- Server now sends reveal data to stage at track start (stage is trusted).

### Changed — Stage view routing
- ADJUDICATE state now stays on ActiveRound (timer frozen, "{handle} is
  guessing…") instead of switching to the Karaoke/reveal view prematurely.
- Karaoke view shows "now guessing" during ADJUDICATE, "winner" during KARAOKE.

### Changed — Admin Reveal enters karaoke
- `server/internal/game/engine.go`: admin "Reveal" now enters karaoke mode (shows
  lyrics, disables guessing, marks track played) instead of just showing the text.

### Changed — Timer resume after incorrect/partial grade
- `server/internal/game/engine.go`: `resumeAudio()` re-broadcasts `trackStart`
  with re-anchored time so the stage timer unfreezes correctly.
- `web/stage`: state handler also unfreezes timer on ROUND_ACTIVE transition.

### Changed — Mobile buzz result messaging
- `web/mobile/src/screens/BuzzScreen.tsx`: judged-out message changed from
  "Incorrect — you're out this round" (red) to "Good job — sit tight for the
  next one" (amber) for a more neutral tone.

### Changed — Lyrics cleared on new track
- `web/stage/src/net/useGame.ts`: `trackStart` with a new startTime clears
  stale lyrics and partial-reveal flags, preventing bleed between tracks.

### Fixed — Spotify OAuth scopes
- `server/internal/spotify/spotify.go`: added `user-read-email` and
  `user-read-private` scopes as required by the Web Playback SDK (documented in
  Spotify's "Building a Spotify Player" how-to).

### Fixed — OAuth cookie domain mismatch
- `web/admin/src/config.ts`, `web/stage/src/config.ts`: normalize `localhost` →
  `127.0.0.1` so cookies set on the IP match the Spotify callback redirect.

### Fixed — Stage audio commands hitting dead player
- `web/stage/src/net/useGame.ts`: audio message handler now reads `audioRef.current`
  instead of capturing the original player in a closure, so pause/resume/play
  reach the active Spotify player after hot-swap.

## [Unreleased] — 2026-06-24

### Fixed — admin no longer logs out on operational errors
- `web/admin`: only auth errors (forbidden/banned/unauthorized) de-authenticate;
  operational errors like "busy: round in progress" now show a dismissable
  notice toast and keep the session. Previously any error bounced the admin to
  the login screen.

### Fixed — Spotify connects even if the stage joins after OAuth
- `server/internal/game/engine.go`: the engine remembers Spotify is
  authenticated (`spotifyAuthed`) and re-signals a stage that connects later via
  full-sync, so the stage initializes the SDK and fetches the live token. Fixes
  the case where the token broadcast hit no listening stage.
- `web/stage`: spotifyToken handler accepts an empty-token "go fetch it" signal
  and is idempotent (push + full-sync can't double-init).

### Changed — clearer status surfaces
- `web/admin` TopBar: polls `/api/spotify/token` and shows "● Spotify connected"
  vs "Connect Spotify" instead of a static button.
- `web/stage`: audio badge moved top-right, restyled as a "LIVE / tunes" label.

### Changed — launcher robustness + honest messaging
- `scripts/dev-up.sh`: kills any prior dev-up run, pre-clears all ports (so a
  leftover process can't silently kill a new server via --strictPort), logs each
  service to scripts/_work/logs/, verifies each frontend with ✅/❌, and prints
  accurate Spotify status (creds-loaded ≠ authenticated) instead of a hardcoded
  "demo mode" line.

### Added — Preflight gate
- `scripts/preflight.sh`: the "are we actually runnable?" check — Go
  build/vet/test plus a CLEAN reinstall + production build of every frontend.
  The clean build is what catches a dependency referenced in code but missing
  from node_modules (the @hello-pangea/dnd break). Verified it fails loudly on
  that exact bug class. Now the enforced Definition of Done (CLAUDE.md).

### Fixed — launcher installs newly-added deps
- `scripts/dev-up.sh`: always run `npm install` (was: only when node_modules
  absent), so deps added since the last run — e.g. after a merge — actually get
  installed instead of erroring at page load. Fails loudly if install fails.

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
