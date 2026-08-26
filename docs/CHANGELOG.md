# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased] — 2026-07-01

### Added
- **AI board builder (Gemini).** "✨ Build with AI" groups a board's track
  library into ~6 themed Jeopardy categories and lays out the board. New
  `server/internal/ai` Gemini client (`gemini-3.1-flash-lite`, JSON response),
  `admin.Categorizer` + `POST /api/boards/{id}/ai-build`, admin UI button.
  Optional — 503s without `GEMINI_API_KEY`. (`GEMINI_API_KEY` / `GEMINI_MODEL`
  in `.env.example`.)
- **Anti-cheat telemetry.** Capture each connection's client IP at the transport
  and surface per-player signals to the control room via `smsgCheatReport`
  (shared-IP, multi-connection flags). Telemetry panel shows IP + flag chips.
- **Reveal length-gate + char-drop morph.** Hide the real answer length behind a
  fixed-width block until a knob-set time, then drop characters down to the true
  length over a morph window and flash a "correct length" color.
- **Auto-lockout + auto-karaoke.** When few enough letters remain hidden,
  buzzing closes and points zero; on full reveal the round auto-enters karaoke.
  Both knob-configurable.
- **Pre-reveal hints.** Release year is surfaced as a hint (genre wired but
  unavailable on the Spotify dev tier — see Changed), shown under the points,
  timed by mixer knobs.
- **Scoreboard everywhere** (admin split panel, stage corner overlay, mobile
  idle screen) and **lyric-availability** (grey out lyric-less tracks, per-track
  play override, background "Check lyrics" rescan; migration 0003).
- **In-app modals** replacing native browser confirm/prompt (admin).
- **Settings "Mixer" tab** — a full-page mixing desk consolidating every live
  knob (skip threshold + all reveal timing).

### Changed
- **Lyrics prefetched + singleflight-cached** at track start so karaoke shows
  them instantly (no loading spinner unless reveal is hit almost immediately);
  smoothed the karaoke scroll (transform-based, stable line boxes, Apple
  Music-style focal scroll).
- **Reveal defaults** retuned (block 15s / letters 10s / interval 3s / morph 3s).
- **Admin control room reorganized:** game controls folded into the Evaluation
  panel header (top bar dropped), board loader moved into the Board panel, board
  rendered as a proper compact Jeopardy grid, YFITOPS brand + Spotify status +
  lock icon in the top nav.
- **Stage audio** activation reworked into a one-time "Start Stage" gate.
- **Playlist import** preserves Spotify order and returns fast (lyrics probed in
  the background); genre fetch removed (Spotify strips artist genres for
  Development-Mode apps).
- **Points** halve when one field reveals ahead of the other.
- Migrations 0003 (track lyrics) and 0004 (year + genre); `make migrate` +
  `dev-up.sh` apply them.

### Fixed
- **Server crash** (nil-deref) on Play/Pause with no track loaded; added
  panic-recovery around the engine command loop so a handler panic can't take
  down every connection.
- **New Game → Start Game** failed because reset unloaded the board; reset now
  keeps it (regression test added).
- Stage played ~0.5s of the previous song on cell select; telemetry never
  refreshed; board didn't show a cell exhausted until after a click; the reveal
  froze the engine during the (now async) lyric fetch; long reveal text
  overflowed the screen; a single-column board's tiles ballooned.
- Karaoke winner banner no longer defaults to the scoreboard leader — shows the
  real winner or "nobody :(".

### Security & robustness — QA sweep 1 (2026-07-25)
Full report: `docs/security/qa-sweep-1.md`. 40 validated findings fixed across
code, API, and UI layers; preflight passed. Highlights:
- **Auth:** `stage.*` client messages now require the stage/admin role (a hostile
  mobile could previously hijack the Spotify device / force round transitions).
- **Board persistence:** a Postgres-backed server no longer boots boardless on
  restart — the attached board is restored via `game_sessions.board_id` (was
  reading orphaned migration-0001 tables). Verify in staging.
- **Buzz fairness (§4B/§4C):** winner is now the minimum latency-compensated
  arrival time via a short collection window (`Config.BuzzWindowMs`, default
  40ms), not goroutine-enqueue order. Single-winner guarantee preserved.
- **Reconnect:** stage and mobile clients reconnect with backoff after a
  WebTransport drop instead of wedging until a manual reload; mobile resyncs with
  the same device fingerprint. Stage gains an ErrorBoundary so a malformed frame
  can't white-screen the projector.
- **AI builder:** atomic `RebuildLayout` (single transaction) so a mid-build
  failure can't corrupt a board; Gemini key moved to a header; upstream errors no
  longer echoed to clients; category names sanitized; prompt uses a delimited
  data block against track-name injection. Tx path pending staging verification.
- **DoS hardening:** admin JSON body cap (`MaxBytesReader`), bounded LRCLIB/Gemini
  response reads, QUIC idle timeout + stream cap, full teardown of dropped
  connections (was leaving zombie read loops), heartbeat telemetry coalescing,
  client frame-length cap mirroring the server.
- **Secrets/TLS:** admin secret kept in-memory only (no localStorage); OAuth
  state cookie `Secure` when https; stage refuses non-https endpoints outside dev;
  boot guard aborts if a dev-default secret is used with `YFI_ENV=prod`.
- Locked-contract files touched only via caller-side guards or documented
  `// CONTRACT-QUESTION` comments; see report for owner sign-off items.

### Security & robustness — QA sweep 2 (2026-07-25)
Full report: `docs/security/qa-sweep-2.md`. An adversarial audit of sweep 1's own
fixes (plus a live security probe against a local Postgres+Redis stack). 9
findings fixed (0 crit/high, 4 med, 5 low); 7 were residual bugs inside sweep-1
fixes. Preflight passed; three items verified against live Postgres / a running
server. Highlights:
- **Handle sanitizer** now strips bidi/format/zero-width runes (U+202E etc.), not
  just control chars — closes the scoreboard render-injection the sweep-1 fix left open.
- **Daily double** no longer finishes early or skews the bonus when a rater is
  kicked/disconnects (ratings/ratingPool kept in sync).
- **Spotify 401 retry** now forces a genuine token refresh (the sweep-1
  single-flight short-circuit had made refresh-on-401 inert for most of a token's life).
- **Played-state persistence** (migration 0005): a mid-game server restart no
  longer re-offers already-consumed tracks. Verified end-to-end on live Postgres.
- **Client reconnect** (stage + mobile): the superseded GameClient is now closed
  and late events from it are ignored — kills a reconnect-storm / leak the sweep-1
  reconnect fix introduced.
- **Admin auth** compares fixed-size SHA-256 digests (no secret-length timing
  leak); remaining raw-error 502 leaks and an unclamped stage board render fixed.
- Live probe confirmed auth, injection defenses, body cap, info-leak suppression,
  and OAuth state all hold against a running instance.

### Security & robustness — QA sweep 3 + session cap (2026-07-25)
Full report: `docs/security/qa-sweep-3.md`. A convergence sweep auditing sweep-2's
fixes + the new session cap, plus a fresh-eyes crown-jewel pass. The crown jewels
(§4A/§4B/§4D, atomic buzz, scoring, deadlock escapes) all held.
- **Per-IP QUIC session cap** (the long-deferred flood defense): a `sessionLimiter`
  caps concurrent WebTransport sessions globally (`YFI_MAX_SESSIONS`, default 2000)
  and per client IP (`YFI_MAX_SESSIONS_PER_IP`, default 128); rejected pre-upgrade
  with 503. Generous defaults so a NAT'd venue is never locked out. Verified live.
- **Buzz ordering is now server-arrival-only** (HIGH fix): the latency-compensated
  effective time was derived from the client's self-reported RTT, so a hostile
  mobile forging a high RTT won every contested buzz. RTT compensation is removed
  (it is unforgeable-vs-fair only on a trusted client, which we don't have);
  ordering uses the server-stamped arrival alone. A forged RTT now buys nothing.
- **Emoji handles fixed** (LOW): the sweep-2 handle sanitizer stripped U+200D ZWJ,
  shattering emoji sequences; ZWJ is now preserved while bidi/zero-width injection
  chars stay stripped.
- Still deferred: store-3 `UNIQUE(board_id, track_id)` (migration + dedup, low).

### Added — Dev-only WebSocket fallback for LAN phones (2026-08-25)
WebTransport requires a secure context, so a phone loading the mobile PWA from a
plain-HTTP LAN origin (`http://192.168.x.x`) cannot connect at all — `WebTransport`
is simply undefined there. A WebSocket fallback carries the identical
length-prefixed framing so real devices can be tested without standing up TLS.
- New `server/internal/transport/ws.go` (`NewWSHandler`), mounted on `/ws` from
  `main.go` only when `YFI_DEV_WS=1`. The production compose does not define it
  at all. Two dev paths set it, because they run the server differently:
  `docker-compose.dev.yml` for the containerized gameserver, and
  `scripts/dev-up.sh` (which runs it bare via `go run`, so the compose
  environment never reaches it) where it defaults on as `${YFI_DEV_WS:-1}`.
- `GameClient` (`web/shared/client.ts`) gained an optional `wsUrl`. `connect()`
  prefers WebTransport and only falls back when it is unavailable. The frame
  parser was extracted into a shared `drainFrames()` so **both** transports get
  the 1 MiB frame cap from QA sweep 1 (`uiadmin-2`); it signals an oversized
  frame by returning `null` and each transport tears itself down.
- **Fails closed in production, in two independent places:** the server refuses
  to register `/ws` when `YFI_ENV=prod` (logs a warning rather than exiting — a
  stray dev var should not kill a live event server), and mobile passes `wsUrl`
  only under `import.meta.env.DEV`. In prod the answer is TLS + WebTransport,
  not this.
  - Verified against the actual production bundle: `wsUrl` is `undefined`, so
    `connect()` takes its `else throw` branch and the WebSocket path is
    unreachable. Note the bundle *does* still contain `connectWS()`'s body
    (Rollup cannot tree-shake unused class methods) and the `ws://` URL as an
    unreferenced dead constant. Inert, but present — do not read its absence
    from the bundle as the guarantee; the guarantee is `wsUrl === undefined`
    plus the server-side `YFI_ENV=prod` refusal.
- Verified on a running dev stack (not just unit tests): the dev server logs
  `DEV: WebSocket fallback registered on /ws` and completes a real WebSocket
  handshake (`101` with a correct `Sec-WebSocket-Accept`); a second server
  booted with `YFI_ENV=prod` **and** `YFI_DEV_WS=1` logged the refusal warning,
  still came up healthy, and answered `404` to `/ws` — including to a valid
  upgrade request.
- Resolved the "requires Chromium" open question (researched 2026-08-25 against
  MDN browser-compat-data, the W3C editor's draft, and MDN Baseline):
  WebTransport is **no longer Chromium-only**. Chrome 97, Firefox 114, Safari
  **26.4** — MDN marks it Baseline "newly available" since March 2026. On iOS
  every browser is WebKit, so Safari 26.4 covers *all* iPhone browsers, and
  `serverCertificateHashes` landed in the same versions (Chrome 100, Firefox
  125, Safari 26.4). `CLAUDE.md`'s "WebTransport requires Chromium" note is
  stale.
  - **This does not rescue the LAN-phone case, and that is why the fallback
    still exists.** WebTransport is restricted to *secure contexts*, which is a
    property of the **page origin**, not the transport: on
    `http://192.168.x.x`, `WebTransport` is undefined no matter what the server
    presents. `serverCertificateHashes` only replaces server *name* auth with
    hash auth — it never makes an insecure origin secure.
  - So the venue answer is HTTPS on the mobile PWA origin. With a real cert
    there, use that same cert for `:4433` and drop `serverCertificateHashes`
    entirely: the spec caps hash-authenticated certs at a **two-week** total
    validity period (and forbids RSA keys, requiring ECDSA P-256 — which
    `certgen.go` already satisfies at 13d+1h), so keeping it in production
    would mean a fortnightly cert-and-hash redeploy treadmill.
  - Residual gap, and the only remaining argument for a production fallback:
    iPhones below iOS 26.4 have no WebTransport at all and old hardware cannot
    upgrade to get it. If those must play, the fallback has to be promoted to a
    TLS-backed `wss://` path that is prod-eligible — an explicit decision, not
    a relaxation of the `ws://` gates above.

### Fixed — store-3: a track can no longer occupy two cells on one board (2026-08-25)
`0002_boards.sql` claimed "a track can only be in one cell per board" in a
comment but never enforced it: the primary key is
`(board_id, row, col, track_id)`, so the same track in two different cells was
two perfectly legal rows. `PlaceTrack`'s upsert targeted that PK, which only
deduped a re-place into the *same* cell — placing into a *different* cell
inserted a second row. The board then served the same song twice, while
`UnplacedTracks` still counted the track as placed, so the duplicate was
invisible in the builder UI. Deferred through QA sweeps 1–3 (low, admin-only)
because it needed a destructive data migration that couldn't be rehearsed
without a real Postgres.
- New `deploy/migrations/0006_unique_placement.sql`: dedups existing rows via
  `row_number() OVER (PARTITION BY board_id, track_id)`, then adds
  `uq_blct_board_track (board_id, track_id)`. Both steps are idempotent (it is
  a unique **index**, not a table constraint, so `IF NOT EXISTS` applies while
  still being a valid `ON CONFLICT` inference target). The dedup tiebreak keeps
  an already-`played` copy first — dropping it would let the game serve that
  song again — then `"row", col, pos` for determinism.
- `PlaceTrack` and `RebuildLayout` both now conflict on `(board_id, track_id)`
  instead of the PK, turning a cross-cell re-place into a single-statement
  move. `played` is deliberately absent from `PlaceTrack`'s `SET` list so a
  track keeps its played state when moved. Aligning `RebuildLayout` matters
  because with the index in place its old PK target would have raised a unique
  violation and rolled back an entire board rebuild; its only caller
  (`admin.aiBuild`) already dedups via its `placed` map, so this is defense in
  depth.
- `deploy/Makefile`'s `migrate` target hardcoded 0001–0005 and would have
  silently skipped 0006 (`scripts/dev-up.sh` already globbed). It now globs
  `migrations/*.sql`.
- Verified against the live dev Postgres. The migration reported `DELETE 0` on
  real data, so the dedup was additionally rehearsed inside a rolled-back
  transaction against *injected* duplicates — including a case where the
  `played` copy was not the lowest cell, so the tiebreak order was genuinely
  exercised. New `TestStaging_PlaceTrack_MovesInsteadOfDuplicating`
  (`YFI_TEST_DSN`-gated) asserts the move, the preserved `played` flag, that a
  same-cell re-place still just updates `pos`, and that a raw duplicate
  `INSERT` is rejected by the index. Proven non-vacuous twice: reverting
  `PlaceTrack` alone fails on the unique violation, and reverting it *with the
  index dropped* reproduces the original bug exactly ("track occupies 2
  cells").

### Changed — BREAKING: `GET /api/spotify/token` reports readiness in the body (2026-08-25)
The endpoint now **always** answers `200` with `{"token": string|null, "connected": bool}`.
Previously it returned `503` when Spotify was unconfigured and `409` when OAuth
had not been completed.
- Why: both the admin UI and the stage poll this endpoint for connection state,
  and at the `fetch` layer a non-2xx status is indistinguishable from a network
  or auth failure — "Spotify isn't set up" looked identical to "the request
  died". An explicit `connected` flag separates the two.
- Consumers updated: `web/admin/src/App.tsx` reads `body.connected` instead of
  inferring from `res.ok`; `web/stage/src/config.ts` requires both `connected`
  and a non-empty `token` before returning one.
- Compatibility tests updated for the new contract: `TestSpotifyToken_Serves`
  now asserts `connected:true`, `TestSpotifyToken_NotConfigured` asserts
  `200 {connected:false, token:null}` (proven fails-when-reverted), and a new
  `TestSpotifyToken_RefreshFailed` covers the old `409` path and asserts the
  upstream error text does not leak.
- Verified on a running dev stack with real Spotify creds loaded but OAuth not
  completed — the old `409` path — which now answers
  `200 {"connected":false,"token":null}`. Auth is unchanged: `401` for both a
  missing and a wrong bearer.
- Known trade-off: `503` (never configured) and `409` (configured but token
  refresh failed) both collapse into `connected:false`, so clients can no longer
  distinguish them. The server still logs the real error. No consumer branched
  on `409`, so nothing breaks today — but surfacing a stale-token state in the
  admin UI would need a distinct signal added back.

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
