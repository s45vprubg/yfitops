# Changelog

All notable changes to this project will be documented in this file.

## Upgrading past 5c7c623 (2026-08-25)

On an existing Postgres volume, run `cd deploy && make migrate` **before**
starting the new server: `make up` does not apply migrations, and `PlaceTrack` /
`RebuildLayout` now infer `ON CONFLICT (board_id, track_id)`, which errors
(`42P10`) without migration 0006's index. `scripts/dev-up.sh` migrates on its
own. 0006 dedups existing rows, so a board that currently has one track in two
cells will lose the duplicate cell. Pre-built frontend `dist/` bundles must be
rebuilt for the breaking `GET /api/spotify/token` change; `npm run dev` is fine.
Migration 0007 (2026-08-27) adds `boards.daily_double_count` (default 2) and
`board_dd_bucket_tracks` for the redesigned Daily Double — no data migration,
purely additive.

Migration 0008 (2026-10-13) adds `track_lyrics` (the new global lyrics cache)
and **drops `board_tracks.has_synced_lyrics`** — the lyric-availability signal
is now the join against `track_lyrics.status`, not a per-row boolean. Run
`cd deploy && make migrate` before starting the new server/gameserver image.
No backfill: every song's lyrics are re-resolved from scratch the first time
each is probed (via "Check lyrics," board-add, or playlist import) under the
new cache; nothing is lost, but a board with previously-known-good lyrics
will show them as not-yet-resolved until the next probe.

Migration 0009 (2026-10-13) adds `track_drop_point` (purely additive, no
column drops) for the new Mark/Jump Drop Point feature. Run
`cd deploy && make migrate` before starting the new server/gameserver image.
No backfill: every song's drop point starts unmarked until an admin marks it.

## [Unreleased] — 2026-10-17

### Removed — Points silently halved on a fully-correct guess (one-field-first gate)
- `server/internal/game/engine.go` / `reveal.go`: Removed the "one field
  revealed ahead of the other" halve gate — if the artist's or song's hidden
  letters dropped to the lockout threshold while the other field still had
  more hidden, the server quietly scaled a subsequent **fully correct**
  guess's payout by 0.5 (`pointFactor`/`halvePoints`, `revealTick`'s
  one-field-first check). This contradicted the scoring rule that a fully
  correct guess always earns 100% of the live decayed pool, and was reported
  as "points look halved" — it has nothing to do with who selected the cell
  (admin vs. the round winner both go through the identical `selectCell` →
  `startTrack` path); it simply correlated with longer-running rounds, which
  gave the gate more time to fire before anyone buzzed. `roundPool()` (now
  the single pool accessor, replacing `livePool()`) is unaffected — the
  partial-guess floor-drop-to-50 behavior is unchanged. The separate
  lockout gate (zeroes points and closes buzzing once both fields are nearly
  fully revealed) is a distinct mechanic and was not touched.
- `server/internal/game/pool_test.go`: removed the halve-gate test cases and
  the `forceHalveGate` helper; added a `"partial then correct"` case pinning
  that a fully correct guess after a partial still pays the full
  (unhalved) live pool.

### Fixed — Backgrounded tab could permanently miss its "pick a cell" prompt
- `web/mobile/src/lib/useGame.ts`, `web/stage/src/net/useGame.ts`,
  `web/admin/src/useAdmin.ts`: the server tells a client "you're the
  picker" via `pickerStatus`/`pickableBoard`, broadcast once at the moment
  of grading. If that connection wasn't live at that exact instant
  (background-tab timer throttling, a brief idle-timeout drop, or the
  browser suspending a hidden tab), the message was lost permanently — the
  player fell through to a legitimate-looking `VoteScreen` with no
  indication anything was wrong, and nothing re-requested the missed state.
  The server already replays `pickerStatus`/`pickableBoard` correctly on an
  explicit `resync`, but nothing anywhere listened for `visibilitychange`.
  All three frontends now send `{t: "resync"}` the moment a backgrounded
  tab becomes visible again (if still connected); mobile and stage
  additionally cancel a stalled reconnect-backoff timer and retry
  immediately rather than waiting out a possibly-throttled delay. Admin has
  no auto-reconnect by design (a drop requires manually re-entering the
  secret), so only the resync-while-connected half applies there.

## [Unreleased] — 2026-10-13

### Added
- **Lyrics subsystem rewrite: a real, deduped, admin-correctable cache.**
  Previously the admin "Check lyrics" rescan re-probed and overwrote every
  track's lyric-availability flag unconditionally — a transient LRCLIB
  error (timeout/429) was indistinguishable from "no lyrics" and silently
  clobbered tracks that were already known-good, which is why a rescan could
  lose more lyrics than it found. Lyrics are now stored as actual LRC text,
  once per song (keyed by Spotify URI, shared by every board and the Daily
  Double bucket a song appears on — never re-fetched twice), in a new
  `track_lyrics` table with a `pending`/`not_found`/`resolved` status that a
  transient error can never downgrade out of `resolved`.
  - **Bulk "Check lyrics" only probes never-attempted songs** — known-good
    and known-absent songs are left alone, so repeated clicks can't re-spend
    LRCLIB calls or re-risk a false negative on songs that already have an
    answer.
  - **New per-track "Requery"** (↻) retries a song that previously came back
    `not_found` or errored, independent of the bulk scan.
  - **New manual lyrics entry** (✎): paste raw LRC text for a song auto-fetch
    can't find; stored in the same cache a successful fetch would populate,
    so it drives the identical karaoke overlay.
  - **Daily Double now has a hard lyrics requirement.** The bucket previously
    had no lyrics field at all and silently allowed anything in; adding a
    song to the bucket is now rejected (409) unless it already has resolved
    lyrics — there is no override for Daily Double, unlike the main grid.
  - **Play-time lookup is DB-first.** The engine checks the resolved-lyrics
    cache before ever calling LRCLIB during a live round, removing the
    runtime dependency on LRCLIB for any song already resolved during board
    building (previously every play re-fetched live, with only an
    in-process, restart-wiped cache).
  - **LRCLIB client fixes**, per LRCLIB's published API docs
    (`https://lrclib.net/docs`): sends a required descriptive `User-Agent`
    (previously the Go default, against LRCLIB's documented requirement);
    classifies `404` as confirmed-absent vs. `429`/`503` as retryable
    (one retry, honoring `Retry-After`) vs. other failures as a hard error,
    instead of collapsing every non-success into "no lyrics"; batch probes
    (rescan, playlist import) now run sequentially with a ~300ms delay
    between requests per LRCLIB's documented batch-scan guidance, replacing
    an 8-way concurrent worker pool that could rate-limit itself.
- **Stage is now a public landing page + a login-gated real stage.**
  Previously `web/stage` was served at the site root and auto-authenticated
  as the trusted `stage` role the instant the page loaded — a build-time
  `VITE_STAGE_SECRET` was embedded in the public JS bundle and sent with
  zero human interaction, so anyone who loaded the page (or just read the
  bundle in devtools) got a live connection that could control game
  audio/playback. The site root (`/`) now renders a static, unauthenticated
  `PublicLanding` (QR code + a live "N jacked in" count from a new
  unauthenticated `/api/public/player-count` endpoint, carrying nothing but
  a bare integer). The real stage moved to `/stage` and now shows a runtime
  login screen — same `ADMIN_SECRET` password as `web/admin`, entered by a
  human each session, never baked into the build — mirroring `web/admin`'s
  existing `Login.tsx`/idle-until-login pattern exactly. One Vite build
  serves both routes (asset URLs are absolute, not path-relative), split by
  a client-side `window.location.pathname` check, no router needed.
  Opportunistically fixed the admin/stage secret comparison
  (`game.Engine.onHello`) to use `crypto/subtle.ConstantTimeCompare` instead
  of a plain `!=`, per this project's own security rules.
- **Admin playback scrubber.** The control room now shows a live, clickable
  progress bar for the current track and can seek to any position. Stage
  already reported live position/pause state on every `player_state_changed`
  event (`StagePlayerStateData`) but the server only read `trackEnded` and
  silently discarded `positionMs`/`paused` — now relayed to admin only via a
  new `playbackPosition` broadcast, with duration from the already-known
  per-track metadata. Seeking reuses the existing, unmodified `AudioData`
  broadcast (`{action:"play", trackURI, positionMs}` — stage's handler
  already supported this shape, no stage-side changes needed) via a new
  `admin.seek` command. Deliberately does **not** re-anchor the scoring decay
  clock (`trackStartMs`/`pausedAtMs`) the way pause/resume's existing
  re-anchor does — a seek can jump past `scoring.go`'s 60s decay window, and
  letting decay follow would zero out the bonus just because the admin cued
  to a later part of a song. Karaoke lyrics need no new code at all: they
  already recompute from the audio player's live position on every render.
- **Manual drop-point marking.** "Mark Drop" captures the scrubber's current
  live position as a song's "best part" (chorus/drop); "Jump to Drop" seeks
  there (minus a ~4s lead-in) via the same `seekTo` path the scrubber uses —
  same deliberate decay-independence, no new engine logic duplicated. Stored
  once per song in a new `track_drop_point` table (keyed by Spotify URI,
  mirroring `track_lyrics`'s dedup-across-boards shape) with a `source`
  column ("manual" today) so a future automated guess (YouTube "most
  replayed" heatmap, lyrics `[Chorus]`-tag cross-referencing — discussed,
  not built) can populate the same column without any schema/API change.
  The one rule that matters, learned from the lyrics-cache clobber bug: an
  automated write can never overwrite an existing human-set mark —
  `SetDropPoint`'s upsert enforces this in SQL, not left to callers to
  remember. The admin scrubber feed now also carries `hasDropPoint`/
  `dropPointMs` so the UI can show a marker on the bar and disable "Jump to
  Drop" when nothing's marked, without a separate round-trip.
- **Player-driven cell picking.** Whoever most recently scored (the same
  `lastScorer`/`cellPicker` concept Daily Double already used — including a
  DD payout, so a DD winner becomes the next picker too, no special case)
  can now pick the next board cell from their own phone instead of the
  admin always picking. During `KARAOKE` this is one half of an AND-gate
  with the existing skip-vote/track-end mechanism (`karaokeConditionMet`):
  whichever of "vote passed or song ended" and "picker picked" resolves
  second fires the existing 3s `TRANSITION` countdown, which now starts the
  picked cell directly instead of landing on `BOARD` and waiting for the
  admin. A Daily Double resolves straight to `BOARD` with no karaoke/vote
  phase at all, so there the performer picks immediately, same as
  `admin.select` already does. Admin keeps every existing power — a manual
  `admin.select` always wins immediately and clears any pending player pick,
  exactly as it already silently did before this feature existed; no
  scorer yet (round ended with nobody correct, or the very first pick of
  the game) falls back to admin-only, unchanged. New local message types
  (`player.selectCell`, `pickerStatus`, `pickableBoard`) mirror the Daily
  Double offer/choose pattern (one connection scoped by playerID, a
  sanitized "who's up" status broadcast to everyone else) — board
  category/points/exhausted data is not track metadata, so sending it to
  the picker's phone needs no §4A exception the way Daily Double's song
  titles did. New `web/mobile` screen `PickCellScreen.tsx`.
- **Daily Double now ends with a graceful 3s fade, plus an admin volume
  control.** A performance used to call `audio.Pause()` the instant the
  last rating landed — an audible hard cut. `checkDailyDoubleComplete` now
  routes through a shared `fadeOutAndFinishDailyDouble()` that steps
  playback volume down over 3 seconds before pausing, then restores it
  afterward — guaranteed via a `defer`, so the volume can never get stuck
  faded down even if the round ends some other way (Force End Round/
  `admin.skipDailyDouble`) while a fade is mid-flight; that race is guarded
  by a new `e.ddEnding` flag so the two paths can't double-award. New admin
  button "End Daily Double" (`admin.endDailyDouble`) ends a performance
  early with the identical fade, as an alternative to the instant Force End
  Round/skip kill switches, which stay unchanged on purpose. This also
  needed real new capability: `AudioDevice` gained `SetVolume`, implemented
  against Spotify's volume endpoint, and a persistent admin volume slider
  (under the Board panel) that applies immediately and is the level the
  fade restores to — not just a one-off mechanism for this feature.

- **Solo Karaoke mode — a second, independent game mode.** Straight
  playback, no guessing/scoring, reachable as a third "Karaoke" admin tab.
  Admin builds a Library (holding pool) and a Playlist (ordered queue) from
  the exact same Spotify search/playlist-import and lyrics infrastructure
  the Board Builder already uses, then the engine plays the Playlist back
  with full transport control (play/pause/resume/stop/next/prev/seek/
  shuffle/repeat/volume), lyrics synced on the stage screen exactly like
  trivia's existing karaoke phase.
  - **Mutually exclusive with trivia via a server-side mode-lock**, not a
    client navigation hook (admin can have several `/admin` tabs open at
    once, so "leaving a tab" isn't a reliable release signal): starting
    trivia (`StartGame`) is rejected while Solo Karaoke is active;
    starting/playing Solo Karaoke is rejected while a trivia game is active
    (state not `LOBBY`/`GAME_OVER`). Every open admin tab reflects the same
    lock via a new `modeLock` broadcast (`"none" | "trivia" |
    "solo_karaoke"`), including on `sendFullSync` for a newly-opened tab.
  - **Auto-advance on track end** (not stop-and-wait), with `repeat="one"`
    (replay the current track), `repeat="all"` (wrap to the start), and
    `shuffle` (a fresh random pick per advance, excluding the current
    track — not a persisted permutation). Queue exhaustion with no repeat
    deactivates the mode and releases the lock automatically.
  - **Lyrics gating is soft** for both the Library and Playlist buckets
    (unlike Daily Double's hard 412 reject): a track lacking resolved
    lyrics may still be added; status is reported for the UI to flag it,
    never to block the add.
  - **Sanitization (§4A):** the new `soloKaraokeNowPlaying` broadcast
    (full track metadata) goes out via `Broadcast(RoleStage, ...)`/
    `Broadcast(RoleAdmin, ...)` only, never `BroadcastAll`. The only Solo
    Karaoke signal mobile ever receives is the bare boolean
    `soloKaraokeActive`, carrying zero track metadata — mobile shows an
    idle "sing along at the stage" screen.
  - New DB migration `deploy/migrations/0010_solo_karaoke.sql`:
    `solo_karaoke_library_tracks` / `solo_karaoke_playlist_tracks`, two
    standalone global tables (not board-scoped, no FK to `boards`).
  - New files: `server/internal/admin/solokaraoke_types.go`,
    `server/internal/admin/solokaraoke.go` (REST routes:
    `GET/POST /api/solo-karaoke/library`, `DELETE .../library/{trackId}`,
    `GET/POST /api/solo-karaoke/playlist`, `DELETE .../playlist/{trackId}`,
    `PUT .../playlist/reorder`, `POST .../import-playlist`,
    `POST .../tracks/{trackId}/requery-lyrics`,
    `PUT .../tracks/{trackId}/lyrics`),
    `server/internal/store/solo_karaoke_store.go` (Postgres impl),
    `server/internal/game/solo_karaoke.go` (engine mode logic + new local
    message types), `server/internal/game/solo_karaoke_test.go`,
    `server/internal/admin/solokaraoke_test.go`.
  - Modified: `server/internal/admin/store.go` (`AdminStore` interface
    gains the Library/Playlist methods; new `SoloKaraokeReloader`
    interface so the admin handler keeps the engine's live in-memory queue
    in sync with Postgres after every Playlist mutation),
    `server/internal/admin/handler.go` (route registration +
    `SetSoloKaraokeReloader`), `server/cmd/gameserver/main.go` (wires
    `adminHandler.SetSoloKaraokeReloader(eng)`),
    `server/internal/game/engine.go` (`startTrack`'s playback primitive
    extracted into a shared `loadAndPlayTrack` helper — pure extraction,
    trivia behavior unchanged; new `soloKaraoke` state field; `StartGame`
    mode-lock gate; `admin.endGame` now also broadcasts the released
    lock; new dispatch cases for the nine `admin.soloKaraoke.*` commands;
    `sendFullSync`/`onStagePlayerState`/`broadcastPlaybackPosition` each
    gained a Solo Karaoke branch).
  - New local `CONTRACT-QUESTION` message types (not in the fixed
    `protocol.go`, per project rules — mirrored in `web/shared/protocol.ts`):
    client `admin.soloKaraoke.play/pause/resume/stop/next/prev/seek/
    setShuffle/setRepeat`; server `soloKaraokeNowPlaying`/`modeLock`/
    `soloKaraokeActive`/`soloKaraokeLyricsStatus`. `admin.soloKaraoke.
    setVolume` is deliberately NOT one of these — it reuses the existing
    `admin.setVolume` unmodified (one physical volume knob shared by both
    modes).
  - **No fixed-contract files were modified** (`protocol/protocol.go`,
    `protocol/state.go`, `game/scoring.go`, `anticheat/latency.go`,
    `anticheat/nonce.go`, `config/config.go` — untouched).
  - **Admin frontend — new "Karaoke" tab.** Library (search/import holding
    pool) + Playlist (ordered, drag-reorderable queue) + Player
    (play/pause/resume/stop/next/prev/seek/shuffle/repeat/volume +
    full-metadata now-playing display — admin isn't §4A-sanitization-gated).
    New `SoloKaraokePage.tsx`: a single `DragDropContext` (modeled on
    `BoardBuilderPage.tsx`'s `handleDragEnd`) with two droppables
    (`skLibrary`/`skPlaylist`). Cross-bucket drag is the same non-atomic
    move+compensating-rollback shape as the Daily Double bucket; a
    same-droppable drag within the Playlist is a genuine reorder (the
    Playlist is ordered by `position`, unlike Daily Double's unordered
    bucket), handled by a local optimistic reorder + a single
    `PUT .../playlist/reorder` call with the full ordered id list.
  - New `components/soloKaraoke/{LibraryBucket,PlaylistBucket,Player,
    SoloKaraokeLyricsModal}.tsx`. `LibraryBucket` adapts
    `HoldingArea.tsx`/`SpotifySearch.tsx`/`ImportPlaylist.tsx`;
    `PlaylistBucket` adapts `DailyDoubleBucket.tsx` (search-add kept, but
    lyrics gating is soft here, not a hard 412 reject); `Player` reuses
    `GameControls.tsx`'s `Scrubber` (now exported) wired to
    `admin.soloKaraoke.seek`, and the existing `admin.setVolume`
    action/control — no new volume action was added, per the one-physical-
    device design. `SoloKaraokeLyricsModal` adapts
    `builder/LyricsEditorModal.tsx` for the non-board-scoped
    `PUT /api/solo-karaoke/tracks/{id}/lyrics` route.
  - `builder/TrackCard.tsx` generalized to a generic
    `TrackCard<T extends LyricsFlaggedTrack>` so it's reusable for
    `SoloKaraokeTrack` (no `lyricsOverride` field — soft gating, so the
    status badge renders non-interactively when no `onToggleOverride` is
    passed) as well as the existing board-scoped `TrackData`. Generic
    rather than a single widened parameter type, to avoid a
    `strictFunctionTypes` contravariance error on the `onOpenManualLyrics`
    callback prop at the existing Board Builder call sites.
  - `useAdminApi.ts` gained the `SoloKaraokeTrack` type + the ten
    `/api/solo-karaoke/...` REST methods. `useAdmin.ts` gained `modeLock`/
    `soloKaraokeNowPlaying` state + listeners, and the nine
    `admin.soloKaraoke.*` action senders.
  - `GameControls.tsx`'s "Start Game" button is now disabled (with an
    explanatory title) while `modeLock === "solo_karaoke"`; the Player's
    own "Play" button is disabled while a trivia game is active
    (`gameState` not `LOBBY`/`GAME_OVER`) — the mirror image of the same
    lock. Both read the server's `modeLock` broadcast, never local/per-tab
    state, since multiple admin tabs can be open at once. A rejected
    `admin.soloKaraoke.play` (server error code `modeLocked`) surfaces
    through the existing `admin.error` -> `state.notice` banner — no new
    error-display mechanism was added.
  - `App.tsx`: new `"karaoke"` page + nav entry. The transient-notice and
    connection-lost banners (previously rendered only inside the Control
    Room page branch) now render regardless of which tab is open, since a
    rejected Solo Karaoke action can occur while the Karaoke tab is active.
  - **Stage frontend.** `Karaoke.tsx` (trivia's existing post-round
    lyric-sync view) gained one optional `hideBanner` prop to suppress the
    winner/guesser banner — its lyric-sync rAF loop, auto-scroll, and line
    rendering are reused verbatim, the same way `DailyDoublePerformance.tsx`
    already reuses it. New `SoloKaraokeView.tsx` renders
    `<Karaoke ... hideBanner />` plus an album-art thumbnail and track-index
    indicator, driven by new `useGame.ts` listeners for
    `soloKaraokeActive`/`soloKaraokeNowPlaying`/`soloKaraokeLyricsStatus`.
    `App.tsx` renders `SoloKaraokeView` ahead of the trivia `GameState`
    switch when `soloKaraokeActive` is true (the mode-lock keeps `GameState`
    pinned on `LOBBY`/`GAME_OVER` for the whole session, so it can't be a
    case inside that switch). The existing `audio`/`state` handlers are
    reused completely unmodified. `sendFullSync` (engine.go) also now
    re-sends the current track's lyrics on stage reconnect, in addition to
    the audio replay — otherwise a stage refresh mid-song showed blank
    lyrics until the next track change.
  - **Mobile frontend.** The ONLY Solo Karaoke signal mobile ever receives
    is the bare boolean `soloKaraokeActive` — zero track metadata, per
    §4A. `App.tsx` short-circuits to an idle screen when it's true, ahead
    of (and without touching) the existing trivia `GameState` switch.
    `IdleScreen.tsx` gained an optional `soloKaraoke` prop rendering fixed
    copy ("Solo Karaoke mode — sing along at the stage!") in place of its
    normal state-keyed lookup.
  - Verified: `scripts/preflight.sh` passes (Go build/vet/test + all three
    frontends' clean install + production build). DB-gated tests
    (unique-placement/RebuildLayout-rollback/played-state-persistence/
    integration store guards) self-skip without `YFI_TEST_DSN` — pre-existing
    behavior, unrelated to this feature, not a regression. Remaining:
    a manual pass against real Spotify Premium credentials + a real
    Chromium stage tab, and a two-admin-tab mode-lock check, have not yet
    been run (see docs/ADVERSARIAL_REVIEW_HANDOFF.md's "unrun by design"
    category).

### Changed
- `server/internal/admin.Track`/`DailyDoubleTrack`: `HasSyncedLyrics *bool`
  replaced by `LyricsStatus string` ("" / "pending" / "not_found" /
  "resolved"); `web/admin`'s `TrackData`/`DailyDoubleTrackData` mirror this.
- `game.NewEngine` takes a new `LyricsTextStore` parameter (nilable — nil
  preserves the exact pre-cache always-live-fetch behavior, e.g. without
  Postgres), and now also a `DropPointStore` parameter (same nilable
  contract, disables Mark/Jump Drop Point without Postgres).

### Fixed — QA sweep 8 (code + UI layers), plus a live stress-test finding
Full report: `docs/security/qa-sweep-8.md`. 15 findings across the
cell-picker and Daily Double fade/volume features (3 high, 4 medium, 8 low),
all confirmed, all fixed and verified cold. Highlights: a kicked/banned
cell-picker could keep steering the board (kick never force-disconnects the
transport); the Daily Double fade's end-guard wasn't scoped per-performance,
so a Force-Ended DD's leftover fade could misfire against a later, unrelated
one; the fade's volume restore used a stale snapshot instead of live state,
so a mid-fade `admin.setVolume` got silently clobbered; a mobile player's
cell pick had no recovery timeout, so a lost/rejected pick could lock their
screen and stall karaoke for everyone with zero visibility anywhere that the
game was stuck (admin now sees a "waiting on {handle}" banner). Also fixed,
separately, a real bug found via an actual stress test against production:
joining players in bursts was flooding the game loop with unthrottled
per-join broadcasts (the identical O(P^2) pattern already fixed once for
heartbeats, never applied to joins) and the admin telemetry list had no
memoization despite scaling toward 500 rows — together, likely why the
admin tab was freezing and getting reloaded during the test. Telemetry list
also gained pagination (100/page, flagged connections pinned to the top).

### Added — bulk "retry pending lyrics" + visible rescan results (2026-10-14)
"Check lyrics" silently discarded the `{attempted, resolved, truncated}`
result the server already returned on every click, so there was no way to
tell "nothing left to check" from "still working" — every click looked
identical, which is what made a long first-time scan of a freshly-populated
board feel like it was re-scanning everything from scratch on every press.
The admin board builder now shows that result after each click. Also added
a bulk "Retry pending (N)" action (`POST /api/boards/{id}/requery-pending-
lyrics`) so a batch of LRCLIB rate-limit failures can be retried in one
click instead of one ↻ per track.

### Fixed — lyrics tooling and Daily Double bucket were scoped to the wrong track list (2026-10-16)
The "N without lyrics" count, bulk retry, per-track requery, and manual
lyrics entry all only ever looked at the unplaced Track Library list — once
a board was built out and most tracks were placed in cells, this made the
tooling blind to most of the board (a built board could show "0 missing"
while a dozen placed tracks sat stuck pending). All four now operate over
every track on the board regardless of placement; manual lyrics entry (✎)
and per-track retry (↻) are also now available on tracks already placed in
a cell, not just the library. Separately, Daily Double bucket tracks were
one-way: dragging a track in converted it permanently, and the only way out
was the ✕ button, which deleted the song outright rather than returning it
anywhere. Bucket tracks are now draggable back out to the Track Library
(reversing the same move, same non-atomic compensate-on-failure handling as
the forward direction); ✕ remains a true permanent delete, now labeled as
such.

### Added — LRCLIB near-duplicate fallback for lyrics matching (2026-10-16)
A real chunk of tracks coming back `not_found` turned out to have synced
lyrics on LRCLIB after all — LRCLIB carries duplicate submissions of the
same recording that report a duration a second or so apart (rounding,
re-encodes), and `/api/get`'s own disambiguation among those duplicates can
return the one submission with no synced lyrics over a near-identical one
that has them. When `/api/get` finds nothing synced, the prober now also
checks `/api/search` for a same-artist/track submission within ±1s of the
real duration and uses that if found. Deliberately narrow: a genuinely
different edit/remix (verified by hand — compilation/radio edits of the
same songs differ by tens of seconds, not ~1s) is never picked up by this,
so it can't desync a karaoke overlay against a shorter/different cut.

### Changed — Mixer sliders replaced with a Volume + Game Settings form (2026-10-16)
The old "Mixer" fader rack was never an audio equalizer — it was always
game-reveal-timing knobs (skip threshold, hint delays, letter-reveal
timing) laid out as vertical sliders, which made the actual values hard to
read and, after an earlier move to this panel, confusing to find. Replaced
with two clearly separate sections: a **Volume** slider (unchanged, live)
and a **Game Settings** form — every knob is now a labeled number input or
checkbox with a plain-English label and a unit in seconds, and nothing
applies live: edits are a pure draft until **Save Settings** is pressed.
Confirmed (via Spotify's own docs and the DRM constraints on their Web
Playback SDK) that a real bass/treble audio EQ isn't achievable with this
architecture, so Audio Settings stops at Volume.

### Fixed — QA sweep 9 (16 findings, 1 critical) (2026-10-16/17)
Full report: `docs/security/qa-sweep-9.md`. Headline fix: the new Game
Settings form's Save button was unconditionally pushing a hardcoded
default skip-vote-threshold value to the server on every save — including
saves of totally unrelated fields — because that control has no
server-side readback and the panel genuinely remounts (resetting its
default) every time the admin switches between the Control Room and Board
Builder tabs. Caught and fixed before this ever reached production. Also:
decimal values couldn't be typed into any of the new number inputs
(clamped on every keystroke instead of on commit); the LRCLIB
near-duplicate fallback could burst-request LRCLIB and silently swallow
its own rate limiting; the bulk "Retry unresolved" action could let a
backlog of permanently-dead `not_found` tracks starve out the genuinely
recoverable `pending` ones; and a few Daily Double bucket drag-and-drop
polish fixes (stopPropagation on the delete button, a clear error instead
of a silent no-op when dropping on the grid, a race guard against
double-dragging the same track).

### Fixed — admin bundle shipped without its production backend URL (2026-10-17)
A local-only verification build (for an unrelated favicon change) got
deployed without the `VITE_HTTP_URL=https://yfitops.us` build flag, so the
shipped bundle fell back to its default (`http://<hostname>:8777`,
bypassing nginx/TLS). Every HTTP-driven call from the admin panel (Spotify
status/token, Board Builder's REST endpoints) was silently blocked by the
browser's mixed-content policy; Control Room's live game state (over
WebTransport, a separate connection) was unaffected. Rebuilt and redeployed
with the correct flag.

### Fixed — admin login no longer eats a guaranteed-404 request every time (2026-10-17)
The server deliberately never registers `/cert-hash` once running a real CA
cert (pinning via `serverCertificateHashes` requires a <=14-day cert, which
a real CA cert fails) — only the dev self-signed cert ever needs it. Every
admin login was firing a guaranteed 404 against it anyway: harmless (the
client already caught a non-OK response and used `undefined`, the correct
value either way) but noisy. The client now skips the request entirely
once talking to an `https://` backend.

### Added — Solo Karaoke mode: a second, independent playback-only game mode (2026-10-17)
Straight playback, no guessing/scoring, reachable as a third "Karaoke"
admin tab. Admin builds a Library + ordered Playlist from the same Spotify
search/import and lyrics infrastructure the Board Builder uses, then the
engine plays it back with full transport control (play/pause/resume/
next/prev/seek/shuffle/repeat). Lyrics sync on stage and audio routes
through the existing Spotify device exactly like trivia's karaoke phase.
Mutually exclusive with the trivia game via a server-side mode-lock —
starting one is rejected while the other is active, and every open admin
tab reflects the lock via a broadcast. New standalone migration
(`0010_solo_karaoke.sql`, two tables, no FK into existing boards).

### Fixed — QA sweep 10 (20 findings, 3 high) (2026-10-17)
Full report: `docs/security/qa-sweep-10.md`. Headline fixes: seeking while
Solo Karaoke was paused silently resumed real playback without updating
the paused flag, desyncing Pause/Resume and stage-reconnect behavior for
the rest of the track; the Playlist's `position` column had no protection
against concurrent admin tabs or a stale reorder list, risking duplicate
positions and nondeterministic play order (closed with a shared Postgres
advisory lock plus a reorder that always produces a total order regardless
of how incomplete the input list is); `handleStartGame`/`handleResetGame`
silently swallowed every HTTP rejection from the server, including the new
mode-lock rejection, so a stale admin tab's Start Game click could do
nothing with zero feedback. Also: a failed Solo Karaoke playback attempt
used to be logged server-side only, with no admin-visible signal that the
session was stalled on a dead track; deleting the currently-playing track
mid-session under repeat-one ended the whole session instead of advancing;
a duplicate TrackEnded signal (confirmed caused by the Spotify SDK firing
player_state_changed more than once per track transition) could skip an
extra song — fixed at the actual source (stage's playback-state ingestion)
after a first server-side fix attempt was proven not to work by its own
regression test.

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

### Added — Admin can manually adjust a player's score from the scoreboard (2026-08-27)
- A ⋮ menu on each row of the admin scoreboard (`ScorePanel.tsx`) opens "Edit
  points", prompting for a signed point delta (e.g. `50` or `-25`) and sending
  the existing `admin.award` message for that player.
- No server changes: `admin.award`/`AdminAwardData` already existed
  (`protocol.go`, `engine.go` `onAdminAward`), already clamps the delta to
  ±1000, floors the resulting score at 0, persists it, and broadcasts the
  scoreboard to stage/admin/mobile.
- Removed the free-standing "Award Points" form (player dropdown + delta input)
  from `EvaluationPanel.tsx` now that the same action is available per-row on
  the scoreboard, avoiding two UIs for one action.

### Added — Daily Double redesigned end-to-end (2026-08-27)
Replaces the old, never-triggered "flag a cell + grade correct" design with
the mechanic from the feature spec: admin-configurable Daily Double cells,
randomly balanced across categories, that activate on a cell's **second**
selection and hand the contestant a real song choice instead of a buzz round.

- **Assignment**: `boards.daily_double_count` (default 2, admin-editable in
  the builder next to the board picker) is randomly assigned across eligible
  (≥2-track) cells each Start Game — `Engine.assignDailyDoubles`, balanced
  round-robin across categories, degrading gracefully if too few cells
  qualify. Never persisted per-cell; `board_layout_cells.daily_double`
  (migration 0002) is now dead — left in place, not dropped.
- **Trigger**: `selectCell` tracks per-cell visit counts; a flagged cell's 1st
  pick plays a normal round, its 2nd pick (`Cell.DDTriggerVisit`) skips
  straight to the offer instead of a track — no buzzing, no guessing. Nothing
  is consumed from the cell's own pool.
- **Contestant**: the last player to earn *gameplay* points (`Engine.award`'s
  new `lastScorer`, distinct from the per-round `roundWinner`) — falls back to
  a random connected player if nobody has scored yet.
- **Flow**: accept/decline → (if accepted) up to 5 songs drawn without
  replacement from a new standalone per-board bucket
  (`board_dd_bucket_tracks`, admin-curated via a drag-and-drop "Daily Double
  Bucket" panel in the builder — same `@hello-pangea/dnd` context as the grid;
  dropping a track there is a MOVE, deleting it from `board_tracks` so it can
  never reappear on the Jeopardy board) → contestant picks one → full
  metadata + synced lyrics on stage (no masking — it's a performance, not a
  guess) → every other online player rates 1-5★ → payout =
  `MaxPointsForRow(row) × DailyDoubleMultiplier(avg)` (floored at the 1★
  multiplier if nobody rates) → straight back to `BOARD` (no karaoke
  afterward). The performance always plays out fully (to a natural song-end or
  an admin Force End Round) even with zero raters online — an empty rating
  pool no longer short-circuits straight to a pause the instant the song
  starts.
- **Decline / stalls**: declining (or an admin `admin.skipDailyDouble` during
  the offer/pick sub-phase) permanently clears the original cell's flag and
  arms a new random cell with ≥1 unplayed track to fire on its very next
  pick; cancels outright if none exists. The existing "Force End Round" button
  now also finishes an in-progress performance (same as a natural song-end).
- **`// CONTRACT-QUESTION` §4A exception (owner-approved)**: the 5 song
  choices (title/artist) are sent to the contestant's own phone only, as a
  single just-in-time push at the moment they accept — never prefetched, never
  broadcast, held only in transient component state on the client (never
  localStorage/sessionStorage). No other mobile client ever receives track
  metadata for a Daily Double. See the `smsgDailyDoubleOffer` comment in
  `engine.go`.
- New local (non-fixed-contract) message types in `engine.go`/`protocol.ts`:
  `dailyDouble.decision`, `dailyDouble.choose`, `admin.skipDailyDouble`,
  `dailyDouble.performer` (sanitized status, all roles), `dailyDouble.result`
  (sanitized payout, all roles), `dailyDouble.offer` (contestant only).
- New UI: mobile gets `DailyDoubleContestantScreen`/`DailyDoubleWaitingScreen`
  and a re-labeled `DailyDoubleScreen` (rating, not a confidence wager); stage
  gets `DailyDoublePerformance` (reuses `Karaoke`'s lyric-sync engine) and
  `DailyDoubleWaiting`; admin gets a `DailyDoubleOfferCard` with the Force Skip
  button and the drag-and-drop Daily Double Bucket panel in the board builder.
- Known limitation: no reconnect-resync mid-offer/song-pick for the
  contestant specifically (the sanitized "who's up" status does resync; the
  actual 5 choices do not) — an admin Force Skip is the recovery path if a
  contestant's phone drops mid-decision (their disconnect already
  auto-declines, so this is mostly moot).
### Fixed — `YFI_ENV` is documented and the default-secret guard is now visible in dev (2026-08-27)
From a tech-debt audit (DEBT-001). Two production safety gates in
`cmd/gameserver/main.go` — the dev-default-secret boot refusal and the cleartext
`/ws` refusal — are armed only by `YFI_ENV=prod`. That variable appeared in
**none** of `deploy/.env.example`, `deploy/docker-compose.yml`,
`docker-compose.dev.yml`, `README.md`, or `docs/BUILD_CONTRACT.md`, while
`docker-compose.yml` defaults `ADMIN_SECRET` to `changeme-admin`. Following the
documented setup (`cp .env.example .env && make up`) therefore produced a server
running on the published default secret with both guards inert, and looking
identical at boot to a correctly configured one.
- `deploy/.env.example` gains a DEPLOYMENT MODE section documenting `YFI_ENV`
  and exactly what each gate does, plus the two other previously undocumented
  vars: `YFI_DEV_WS` and `YFI_SPOTIFY_TOKEN_FILE`.
- `docker-compose.yml` sets `YFI_ENV: ${YFI_ENV:-dev}` explicitly, so the knob
  is discoverable in the file that runs the server. Default kept at `dev` on
  purpose: defaulting compose to `prod` would make `make up` fatal on an
  unedited `.env` and break the documented local playtest loop. Flipping that
  default is an owner call, not a doc fix.
- `main.go` now runs the same default-secret check in dev and logs a `WARNING`
  naming each defaulted secret. Never fatal — dropping a live event server over
  a dev default is worse than running with one — but a misconfigured server is
  no longer silent. Extracted as `defaultedSecrets(cfg)`; the default literals
  stay duplicated from `config.go` because that file is a locked contract and
  cannot be asked which of its values are defaults.
- `README.md` gains a "Before a real event: set `YFI_ENV=prod`" subsection
  spelling out both gates, and its stale claim that Postgres runs "`0001_init.sql`
  and `0002_boards.sql`" on first boot is corrected — the whole `migrations/`
  directory is mounted as an init dir, so every `*.sql` runs in lexical order.

### Fixed — `integration-test.sh` ran against a stale schema and skipped the store tests (2026-08-27)
From the same audit (DEBT-006). The only automated DB harness in the repo applied
**only** `0001_init.sql`, so migrations 0002–0006 were absent, and it exported
`YFI_TEST_PG` while `internal/store`'s staging tests gate on `YFI_TEST_DSN`. Net
effect: every Postgres test written during QA sweeps 1–3 self-skipped here — the
regression guard for the store-3 unique-placement fix among them — while the run
still reported green. Per `qa/HANDOFF.md` these were being run by hand in a
`golang:1.26` container instead.
- Migrations are now globbed in lexical order with `ON_ERROR_STOP=1` and a clear
  per-file failure message, matching what `deploy/Makefile`'s `migrate` target
  already does — the same hardcoded-list bug that silently skipped 0006 there.
- Both `YFI_TEST_PG` and `YFI_TEST_DSN` are exported from one `PG_DSN`, with a
  comment on why both exist. Unifying the two names is left as a follow-up.

### Added — the post-partial decay curve is pinned by tests (2026-08-27)
From the same audit (DEBT-005). `currentPointsFromPool` is the third copy of the
§7 decay curve: it lives in `game/engine.go` rather than the locked `scoring.go`,
and `web/shared/scoring.ts` mirrors it for the stage's 60fps point timer
(`views/ActiveRound.tsx`). Neither copy had a test, so a drift between them would
surface as the projected number disagreeing with the score actually awarded.
- Three table tests in `game/scoring_test.go`. The load-bearing one asserts the
  pool form *equals* `CurrentPoints` for every row across 0–70s when fed the
  row-derived ceiling and standard base — that is what catches one copy of the
  formula being edited without the other. The others cover the shifted pool a
  partial leaves behind (`max=150, base=50`, the real values from a row-5
  partial), the collapsed `max == base` pool from a row-1 partial, and
  monotonicity plus the floor.
- Proven non-vacuous: swapping `math.Floor` for `math.Round` fails both the
  equivalence test (row 2, t=5125ms) and the fractional vector (140 → 141).
- `engine.go` now spells the rounding `math.Floor` instead of a bare `int()`
  truncation. Behavior-preserving — verified by running the new vectors against
  the original truncation, which passes, since the two agree for the
  non-negative pools this can be called with (`PartialAward` clamps at 0) — but
  §7 specifies floor and the JS mirror uses `Math.floor`, so all three copies
  now read the same.
- `scoring.ts` gains a `CONTRACT-QUESTION` pointing at the pinned vectors and
  noting this belongs in `scoring.go` on a contract version bump. No behavior
  change on the TS side; reproducing the vectors there needs a frontend test
  runner, which the repo does not yet have.

### Security — QA sweep 4: the `/ws` fallback was a second, unguarded door into the hub (2026-08-27)
`transport/ws.go` landed after sweep 3 and had never been swept. It is a parallel
transport into the **same** `Hub` and engine, so every guarantee had to be
re-asked. The ones enforced in the engine (§4A sanitization, §4B arrival stamp,
§4D nonce, frame cap, IP capture) were inherited correctly. The ones that lived
in the WebTransport *branch* of `server.go` were silently void on `/ws`:
- **Session limiter bypass (high).** `handleSession` acquires the global
  (`YFI_MAX_SESSIONS`) and per-IP (`YFI_MAX_SESSIONS_PER_IP`) caps; `ServeHTTP`
  acquired nothing, so one host could open unbounded `/ws` sessions and the cap
  meant nothing while the route was up. Now acquires *before* the upgrade (a
  flood is turned away cheaply) with exactly one release per exit path, sharing
  the one limiter instance with `/wt` — the cap is a ceiling on the process, not
  per-transport. Locked by `TestWSHandlerHonorsSessionLimiter`.
- **Zombie connections (high).** `hub.add(connID, rw)` omitted the third
  `streamCloser` argument that the WebTransport path passes. A hub-side drop
  (`enqueue` overflow → `conn.stop()`) removed the conn from the hub but left the
  socket open and the read loop running — a dropped player who could still buzz.
  `drop_test.go`'s existing gate registered a bare writer and so was structurally
  incapable of catching it. Now passes `CloseNow`, and `hub.go` documents the
  invariant that a `streamCloser` **must not block**: `stop()` runs inline on the
  engine's single broadcast goroutine, and nhooyr's `Close(code, reason)` waits
  ~5s for a close reply from a peer that by construction is not reading — using
  it here would have frozen the entire game. (That was a proposed fix, caught and
  measured before it shipped.) Locked by `TestWSHandlerDropTearsDownSocket`.
- **No idle timeout (medium).** QUIC reaps dead peers at
  `MaxIdleTimeout=30s`; `/ws` had no read deadline, ping/pong, or idle reaper, so
  a phone that vanished into a tunnel held a goroutine, a hub slot, a socket and a
  limiter slot forever. Now a 30s inactivity reaper mirroring the QUIC value, reset
  on every successful read *and* write. Locked by `TestWSHandlerIdleTimeout`.
- `ws.go` had **no test file at all** before this sweep; it now has four,
  including a framing round-trip.

`OriginPatterns: []string{"*"}` was reviewed and **kept**: the default requires
Origin host == request Host, which breaks the exact case the fallback exists for
(a phone loading the PWA from the Vite dev server on a different host:port).
Auth is at the hello layer, and the WebTransport twin is deliberately equally
open. Documented in place rather than "fixed".

### Security — QA sweep 4: prod gates failed open on `YFI_ENV` casing (2026-08-27)
Both gates added in the DEBT-001 entry above compared `os.Getenv("YFI_ENV")` to
the literal `"prod"`, so `Prod`, `PROD`, `production`, or a trailing space all
read as dev and silently disarmed them. Now normalized (`EqualFold` + `TrimSpace`,
accepting `prod` and `production`) and, more importantly, inverted: the
default-secret refusal fires whenever `YFI_ENV` is anything **other** than dev, so
a typo now fails *closed*. Unset/empty stays dev — compose defaults to it and
`dev-up.sh` runs bare. The resolved mode is logged on the first line of boot.

### Changed — the cleartext `/ws` fallback is now an explicit opt-in, independent of `YFI_ENV` (2026-08-27)
Supersedes the "cleartext transport refusal" gate described in the DEBT-001 entry
above. Keying `/ws` off `YFI_ENV` made the gate mutually exclusive with its own
use case: an operator who needed the LAN fallback had to stay out of prod, which
also disarmed the default-secret guard — the flag meant to harden the deployment
pushed people into the weaker configuration.
- `/ws` now requires **both** `YFI_DEV_WS=1` and `YFI_INSECURE_TRANSPORT=1`, and
  ignores `YFI_ENV`. `YFI_DEV_WS` alone mounts nothing and logs a notice naming
  the missing acknowledgement. Registering it with `YFI_ENV=prod` is allowed and
  logs a loud warning naming the exact exposure (no encryption, no cert pinning,
  the admin/stage secret is readable on the wire) on every boot.
- The matrix is a pure function (`decideWS`) so it is unit-testable
  (`TestDecideWS`), and was additionally proven against the real binary across
  five live boots: both opt-ins → `101`; `YFI_DEV_WS` alone → `404` + notice;
  both + `YFI_ENV=PROD` → `101` + prod warning; `YFI_ENV=Prod` with dev secrets →
  refuses to boot; `YFI_ENV=` empty → still dev.
- **Breaking for the dev flow:** `scripts/dev-up.sh` and
  `docker-compose.dev.yml` set only `YFI_DEV_WS=1` and would have started
  404ing the LAN fallback. Both now set the pair. If you have a local override
  that sets `YFI_DEV_WS` by hand, add `YFI_INSECURE_TRANSPORT=1`.

### Fixed — QA sweep 4: the stage projected points the server would not pay (2026-08-27)
One root cause, four symptoms. Four sites derived the scoring pool from the board
**row** instead of the live post-partial / post-halve state, so the stage's 60fps
timer counted down from a ceiling that no longer existed: a row-5 partial
projected 140 while `gradeCorrect` paid 70, and the control room's grading readout
showed 190 against the same 70. `trackStartEnvelope`, `halvePoints`,
`gradePartial`'s broadcast and `adminViewEnvelope` now all read one private
accessor pair (`roundPool` / `livePool`), which is also the single place
`pointFactor` is applied to a displayed pool.
- **No payout change** — deliberately. `gradeCorrect`'s arithmetic is unchanged
  (independently verified: `CurrentPoints(row, e)` is identical to
  `currentPointsFromPool(MaxPointsForRow(row), BaseValue, e)` in all three
  branches of the curve), and `pointFactor` is still applied exactly once, at
  award time and never inside the pool accessor. Only the projected and displayed
  numbers moved, onto the awarded one.
- Residual, documented in `livePool`: because both ends of the pool are floored
  independently, an odd bonus (rows 2 and 4 with no partial) can project **1
  point low**. That is the closest an integer wire pool can get, and it errs low
  rather than overstating.
- Locked by `TestPool_ProjectedMatchesAwarded`,
  `TestPool_AdminReadoutMatchesAward`, `TestPool_StageReconnectKeepsReducedPool`,
  `TestPool_LivePoolNeverInverts`.

### Changed — buzz ties are now broken randomly, not by player ID (2026-08-27)
`resolveBuzzWindow` ordered contenders by `(arrivalMs, playerID)`. `arrivalMs` has
1ms granularity, so two phones on the same conference Wi-Fi tie as a matter of
routine — this was the common case, not a corner case — and every one of those
ties went to the lexicographically smallest player ID for the entire game. The
player ID *is* the client-supplied device fingerprint, so a hostile phone could
simply pick a low one and win every tie in every round.
Contenders are now pre-shuffled from the engine's injectable `rng` and sorted
with `sort.SliceStable` on `arrivalMs` alone. `SliceStable` is load-bearing: the
unstable `sort.Slice` discards the shuffle and substitutes its own
pivot-dependent permutation, which makes fairness an accident of the sort
implementation rather than a property that can be seeded and tested.
Proven red before green — over 40 trials with identical arrival stamps the
pre-fix code gave the low ID 40 wins to 0. Locked by
`TestQARegression_BuzzTieBreakIsRandomNotPlayerID`, with
`TestQARegression_BuzzEarliestArrivalStillWinsAfterShuffle` guarding the other
direction (a genuine 1ms gap must still decide the round every time — §4B).

### Fixed — QA sweep 4: client-side connection and audio leaks (2026-08-27)
- **Mobile leaked a live QUIC session per reconnect attempt.** `useGame.ts`'s
  connect-failure path dropped `clientRef.current` without closing it. `connectWT`
  awaits `wt.ready` *before* creating the stream, so anything that threw after
  that left an open session with no remaining reference — and burned a per-IP
  limiter slot on every backoff retry. Now closes the captured client, matching
  the post-hello path.
- **`GameClient` reported a single teardown twice.** Three paths fired
  `handleClose()` and then re-entered it via the transport's own close event
  (`feedWS`'s overflow close, `readLoopWT`'s cancel, and a pre-open `onerror`
  that also rejected `connect()`). A single-fire `down` latch makes each teardown
  exactly one `onState(false)`.
- **A text frame vanished silently.** `new Uint8Array(ev.data as ArrayBuffer)` on
  a string yields a *zero-length* array with no throw — the frame disappeared with
  no error and no teardown. The framing is binary-only; a non-`ArrayBuffer` frame
  now closes with `1003`.
- **The stage's audio overlay could be dismissed for the wrong player.** The
  auto-dismiss was guarded only by `!disposed`, but `initSpotify()` destroys and
  replaces `audioRef.current` at runtime, so a slow `activate()` from the dead
  player could clear the overlay for a successor that was never activated —
  leaving a silent projector with no way to re-arm. Both call sites now check
  player identity.

### Fixed — QA sweep 4: silent failure paths and misleading dead code (2026-08-27)
- `GET /api/spotify/token`'s error branch had no logging, unlike its siblings.
  It is the only signal that stage audio is about to die, and it was silent; the
  stage just quietly reported "not connected". Still returns `200` with
  `connected:false` (the SDK's `getOAuthToken` treats a non-200 as fatal).
- `web/stage/src/anim/decrypt.ts`'s `PHASE1_MS=5000` / `REVEAL_INTERVAL_MS=2000`
  disagree with `reveal.go`'s `10000`/`3000`, which looked like a live
  client/server drift. It is not: the reveal is server-driven and only `glyphAt`
  is imported — `computeFrame` and both constants have no callers. Documented as
  dead rather than "synced", which would only have made dead code look
  authoritative.
- `config.Config.BoardRows` / `BoardCols` are read from `YFI_BOARD_ROWS` /
  `YFI_BOARD_COLS` and then used by **nothing**; real board dimensions come from
  Postgres via the admin layout API. `SampleBoard`'s `5, 5` is not a duplicate of
  them and must not be "fixed" by wiring them in. Marked with a
  `CONTRACT-QUESTION` at both ends (`config.go` is locked) and documented as inert
  in `.env.example`.

### Changed — QA sweep artifacts are no longer committed (2026-08-27)
`qa/findings*/` and `qa/validation*/` (24 JSON files) are per-agent sweep output:
machine generated and only meaningful next to the run that produced them. They
are now gitignored and untracked, while staying on disk so `qa/HANDOFF.md`'s
resume checklist can still read them. `qa/HANDOFF.md` and `qa/smoke.sh` remain
tracked — the narrative and the tooling are what outlive a sweep.

### Fixed — QA sweep 5: a reconnect fix could close the healthy connection (2026-08-28)
Full report: `docs/security/qa-sweep-5.md`. Sweep 5 audited **sweep 4's own
fixes**. 7 findings confirmed, all low/medium, 1 refuted; nothing critical or
high survived validation. The two highest-severity reports were both downgraded
with their stated mechanisms refuted — and in both cases the validator then found
a *different, real* path to the same defect.
- **`web/mobile/src/useGame.ts`**: sweep 4's connect-failure `close()` operated on
  the shared `clientRef`, so two concurrent `establish()` calls could close the
  healthy client and leak the failed one — re-creating the session leak it was
  written to kill. The reachable path is not a double-tap (the join button is
  disabled while connecting and `conn:"connecting"` is patched synchronously);
  it is a pending backoff timer racing a manual retry, both passing the
  `clientRef.current`-null guard while the manual attempt is parked in
  `await fetchCertHashes()`. Fixed by closing the instance *this* invocation owns
  (`if (clientRef.current === client)`) at every ref-clearing site, plus an
  overlap latch released in a `finally` — the `finally` is load-bearing, since a
  stranded latch would block every future reconnect and end that player's game.
- **`web/admin/src/useAdmin.ts`**: `onState` had no stale-client guard (mobile and
  stage both have one), and `patch({status:"connecting"})` ran *after*
  `await close()`. On a **re-login** — `useAdmin` has no disconnect handler, so
  the dead client is still in the ref and the Login form is re-enabled — that
  await stalls on the broken link with the submit button still live. Patch hoisted
  above the await so the busy gate is synchronous; guard compares the locally
  captured `client`, not the ref (a guard against the ref is a tautology).
  Not extended to `wire()`'s handlers: `welcome` is what admits the operator, and
  a misapplied guard there could lock them out of the control room mid-event.

### Fixed — QA sweep 5: `/ws` teardown hygiene (2026-08-28)
- Deliberate teardown (idle reaper, or a hub drop via `conn.stop()`) left the
  parked read loop returning a `net.ErrClosed`-shaped error that fell through the
  `io.EOF` / normal-close check and logged `read error`. Normal behavior logging
  as a fault is how a real fault gets scrolled past at a live event. Fixed with a
  `localClose` flag set **before** `CloseNow()` in both paths; genuine peer errors
  still log loudly, and the gate asserts that inverse.
- `idleTimer.reset()` used `Timer.Reset`, which cannot retract an `AfterFunc`
  that has already begun running. Real, but a sub-millisecond race against a 30s
  timer with ~15x heartbeat margin. Fixed with a generation counter: a superseded
  callback vetoes itself. It decides under `mu` and calls `onExpire()` after
  releasing it — holding a lock across `CloseNow()` invents a deadlock while
  fixing a benign race. Tradeoff: a fresh timer allocation per `reset()`, safe
  only because §5's deterministic-timer contract means there is no per-tick
  server broadcast. Revisit if one is ever added.
- **Refuted, with a measurement**: concurrent `CloseNow()` was claimed to
  serialize on the library's `closeMu` for the winner's full close duration (~15s
  on the engine's single broadcast goroutine). 8 concurrent `CloseNow()` on one
  real conn peaked at **81µs**. This re-validates sweep 4's choice of `CloseNow()`
  over `Close(code, reason)` and `hub.go`'s "must not block" invariant.

### Fixed — QA sweep 5: docs still claimed a protection sweep 4 removed (2026-08-28)
Sweep 4 decoupled `/ws` from `YFI_ENV`; `web/mobile/.env.example` and a comment in
`useGame.ts` were missed and still claimed the server refuses `/ws` when
`YFI_ENV=prod`. Documentation that overstates a protection is worse than none — a
reader skips the real check. Both now state the actual gate: both `YFI_DEV_WS=1`
and `YFI_INSECURE_TRANSPORT=1` required, no `YFI_ENV` backstop, prod may opt in
and only warns. Also documented at `finishDailyDouble` that its bypass of the new
`roundPool`/`livePool` accessors is **deliberate** (a separate crowd-rating bonus,
never broadcast as a projected decaying pool), so a future sweep does not re-file
it as the fifth site that doesn't use the accessor pair.

### Added — QA sweep 5: coverage for the gaps sweep 4 left (2026-08-28)
`qa/acid.sh` grew from 27 to **32** locked gates. Two lock sweep 5's fixes
(`TestWSHandlerTeardownLogging`, `TestIdleTimerResetVetoesStaleGeneration`);
three lock holes sweep 4 left behind:
- `TestEnvModeWrappers` — sweep 4 tested the pure predicates (`isProdEnv`,
  `isDevEnv`, `decideWS`) and left `modeName()`/`isProd()`/`isDev()`, the
  `os.Getenv` wrappers that actually run at boot, with **zero** coverage — and
  the casing bug lived in precisely that wrapper layer. The `staging` case pins
  the inverted secret guard failing **closed** on an unrecognized value.
- `TestQARegression_BuzzFairnessHoldsAtFiveWayTie` — sweep 4's fairness gates
  only cover n=2, where a biased shuffle is essentially invisible. Asserts every
  one of 5 tied contenders wins at least once over a fixed seed range;
  deliberately does not assert distribution tightness, because a flaky ratchet
  entry is worse than an absent one.
- `TestQARegression_IneligiblePlayersNeverBecomeContendersOrWin` — sweep 4
  changed how contenders are *ordered*; nothing verified the reordering could not
  admit a banned or already-guessed player at all.

The engine surface came back **clean** on a re-hunt, with sweep 4's two
game-correctness fixes independently re-proven by revert-and-watch-it-fail
(`fp1 won 40/40`; projected 190 vs awarded 70). A brute force over the whole
pool domain measured `worst_low=-1, worst_high=0` — the documented 1-point floor
residual, and nothing projects high.

**Known gap, stated rather than dressed up:** the two UI fixes have **no
regression gate**, because no frontend has a test runner. They were proven by
`tsc --noEmit`, a production build, and a line-numbered interleaving trace
instead; no pseudo-gate was added to any script to make the coverage look
present. This is the same gap that blocks pinning the TypeScript copy of the §7
decay curve, and it has now blocked coverage in two consecutive sweeps.

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

### Fixed — Stage: wrong secret could still trigger a real Spotify Connect (uis7-001)
- `web/stage/src/net/useGame.ts`: `createAudioPlayer()`/the Spotify SDK
  `connect()` call previously ran unconditionally at the top of the login
  effect, racing the hello/welcome round trip that actually validates the
  secret server-side. Moved both into a new `startAudio()`, now invoked only
  from the `"welcome"` handler — i.e. strictly after `authStatus` reaches
  `"authed"` — guarded against double-firing across reconnects.
- `web/stage/src/config.ts`: `fetchSpotifyToken()` now returns a tagged
  `SpotifyTokenFetch` result (`{ ok: true, token }` or `{ ok: false, reason:
  "unauthorized" | "unavailable" }`) instead of collapsing every failure to
  `null`, so callers can tell a server-side auth rejection (401/403) apart
  from a genuine network/transport failure.
- `web/stage/src/audio/index.ts`: `liveTokenProvider()` now fails hard (throws,
  so the SDK's `getOAuthToken` callback never receives a token) on an
  `"unauthorized"` result instead of falling back to the locally-cached
  Spotify token — closing the window where a wrong admin secret, submitted on
  a browser with an unexpired cached token from a prior legitimate session,
  could still register a real Spotify Connect device. The cached-token
  fallback is preserved for `"unavailable"` (genuine network blips), so an
  already-authenticated session that loses connectivity mid-game still works.

### Fixed — Stage: tab title didn't vary by route (uis7-002)
- `web/stage/src/main.tsx`: Sets `document.title` based on which route
  mounted (`"yfitops // STAGE"` for `/stage`, `"yfitops"` for the public
  landing page) instead of relying on `index.html`'s single hardcoded title.
