# yfitops V2 — Stage (Central Presentation Screen)

The single, projected display at the venue. It is the **trusted client**
(design_doc §4A): it receives reveal, lyrics, and board payloads that mobile
clients never see. Everything it shows is driven by backend `GameState`.

It also hosts the audio layer: this browser tab becomes a Spotify **Virtual
Device** via the Web Playback SDK, and the backend routes playback to it.

## Run

```bash
npm install
npm run dev      # vite dev server (default http://localhost:5173)
npm run build    # tsc -b && vite build  -> dist/
npm run preview  # serve the production build
npm run typecheck
```

Open the dev server in a browser, ideally fullscreen on the projector. The page
connects to the backend over WebTransport as role `stage`.

> **Note on TypeScript version:** pinned to `~5.6` on purpose. The shared
> contract `web/shared/client.ts` is fixed (we may not edit it) and does not
> compile under the stricter `Uint8Array<ArrayBuffer>` invariance introduced in
> TS 5.7+. 5.6 type-checks the whole tree (app + shared) cleanly.

## Environment variables

All optional. Defaults derive from `window.location.hostname`, so a plain
`npm run dev` against a co-located backend usually needs no config.

| Var             | Default                       | Purpose                                              |
| --------------- | ----------------------------- | ---------------------------------------------------- |
| `VITE_WT_URL`   | `https://<host>:4433/wt`      | WebTransport endpoint (role `stage`)                 |
| `VITE_HTTP_URL` | `http://<host>:8777`          | Plain HTTP: `/cert-hash` + `/auth/spotify` (OAuth)   |
| `VITE_JOIN_URL` | `http://<host>:5173`          | Mobile buzzer URL encoded into the QR codes          |

Copy `.env.example` to `.env.local` to override.

### Dev cert pinning

For self-signed dev certs the stage fetches the SHA-256 cert hash from
`${VITE_HTTP_URL}/cert-hash` and passes it to the WebTransport API via
`serverCertificateHashes` (accepts hex or base64). If that endpoint is
unreachable it connects without pinning (the production CA case). If the backend
is entirely absent the screen still runs in offline/demo mode.

## Spotify integration & demo mode (read this)

**Honest limitation:** the Spotify Web Playback SDK cannot authenticate or play
audio without a real **Spotify Premium** account and a valid OAuth token. We do
not pretend otherwise.

The audio layer is therefore isolated behind a single interface
(`src/audio/types.ts → AudioPlayer`) with two implementations:

- **`SpotifyAudioPlayer`** (`src/audio/spotify.ts`) — loads
  `https://sdk.scdn.co/spotify-player.js`, registers this tab as the
  "yfitops Stage" Virtual Device, and reports its `device_id` to the backend.
- **`MockAudioPlayer`** (`src/audio/mock.ts`) — a virtual playhead. No real
  audio, but it advances position so the karaoke lyric highlighter and the
  play/pause/resume commands still behave correctly for demos.

**Selection is automatic** (`src/audio/index.ts`): if an OAuth access token is
present (captured from the redirect hash/query or `localStorage`), you get the
Spotify player; otherwise the mock. **Nothing else in the app knows or cares
which one is active** — animations, the point timer, the board, lyric rendering,
and the whole view router work identically in both modes.

### OAuth flow

1. The banner (top-left) shows **"Spotify not connected — demo mode"** with a
   **Connect Spotify** button.
2. Clicking it redirects to `${VITE_HTTP_URL}/auth/spotify`. The backend runs the
   OAuth dance and redirects back to the stage with `#access_token=…`.
3. On reload the token is captured (and scrubbed from the address bar), the SDK
   initializes, the device registers, and we send `stage.deviceReady`
   (`CMsgStageDeviceReady { spotifyDeviceID }`) to the backend.
4. The backend then routes Web API playback to that device, and sends
   `audio` commands (`SMsgAudio` play/pause/resume) over WebTransport — including
   the latency-critical **local pause-on-buzz (~20ms)** that bypasses the Spotify
   API round-trip (§9). We report `player_state_changed` back via
   `stage.playerState` (`CMsgStagePlayerState { positionMs, paused, trackEnded }`).

If auth fails, the banner says so and the screen falls back to demo mode.

## Views (driven by `GameState`)

- **Lobby** — massive centered QR (the join URL) + a rolling marquee of joined
  handles from the scoreboard payload.
- **Board** — clean 5×5 Jeopardy grid; categories on top, point values in cells;
  exhausted cells faded.
- **Active Round** — the showpiece. Two giant lines (Artist + Song) run the
  decryption animation; a huge ticking point timer above them.
- **Reveal & Karaoke** — winner flash; split screen with the revealed track on
  top and real-time-highlighted synced lyrics below.
- **Game Over** — final leaderboard.

A small QR + join URL is anchored in the corner of every non-lobby view for late
joiners (§8A).

## How the decryption animation works (§5)

`src/anim/decrypt.ts` is **pure logic**: `computeFrame({ elapsedMs, targetLen,
target?, seed })` returns the exact text to display for a given moment. It is
deterministic (a stable hash drives glyph selection and the reveal order), which
makes it reproducible and flicker-stable per character slot.

Three phases keyed off `elapsedMs` (time since `trackStart.startTime`):

1. **Phase 1 (0–1400ms)** — rapid randomized glyph cycling. The noise **length
   is intentionally decoupled** from the real string.
2. **Phase 2 (1400–2600ms)** — masked underscores at the **exact** target length
   (`artistLen` / `songLen` from `trackStart`), preserving word geometry. This is
   all we can show before the reveal payload arrives, since the true strings only
   come in `reveal`.
3. **Phase 3 (2600ms+ , once `reveal` arrives)** — Wheel-of-Fortune progressive
   reveal: true characters pop in at pseudo-random positions over
   `REVEAL_INTERVAL_MS` steps while unrevealed slots keep cycling.

`src/views/ActiveRound.tsx` drives **both** the artist and song lines plus the
point timer from **one** `requestAnimationFrame` loop (never `setInterval`, per
§5, to avoid stutter). The loop writes `textContent` directly (no React
re-render per frame) and reads its inputs from a ref so streaming messages never
restart it.

## How the deterministic point timer works (§5)

The backend sends `trackStart { maxPoints, basePoints, startTime, … }` **once**.
The same rAF loop computes, every frame:

```
elapsed = Date.now() - trackStart.startTime
points  = currentPoints(row, elapsed)   // from web/shared/scoring.ts
```

`currentPoints` is the **floor-identical mirror** of the Go backend's scoring
(`math.Floor`, linear decay holding max for 5s then decaying to the 100 base by
60s), so the projected number matches the authoritative score.

- **Latency masking:** the moment a player buzzes (`state → LOCKED_OUT`), the
  timer **freezes and dims instantly**. The server's authoritative arrival time
  differs slightly from the local visual clock; freezing hides that discrepancy.
- **Partial recalibration:** a partial guess makes the backend send a fresh
  `trackStart` (new `startTime` / lower ceiling). That re-anchors `elapsed` and
  un-freezes the timer, which resumes ticking from the new ceiling.

## Layout

```
src/
  audio/        AudioPlayer interface + Spotify SDK impl + mock + factory
  anim/         decrypt.ts — pure 3-phase decryption frame logic
  components/   CornerJoin (persistent QR), SpotifyBanner
  net/          GameClient wiring (useGame), cert-hash fetch
  views/        Lobby, Board, ActiveRound, Karaoke
  config.ts     env-overridable endpoints
  App.tsx       GameState -> view router
```
