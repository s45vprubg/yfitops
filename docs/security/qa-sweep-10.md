# QA Sweep 10 - Findings & Remediation

Date: 2026-10-17. Scope: diff against sweep 9's HEAD (`2d2ef02`) through HEAD
— one large commit (`aa43dc8`, "Add Solo Karaoke mode": 32 files, ~3,489
insertions, a second independent playback-only game mode mutually exclusive
with trivia via a server-side mode-lock) plus a small in-progress addition
(a "Reset to Defaults" button in the Game Settings form). `aa43dc8` had
already been pushed and deployed to production before this sweep started —
build/vet/test/tsc/preflight were independently verified clean and the
mode-lock's bidirectionality and the new migration's safety were spot-checked
before deploying, but this sweep was the first real adversarial read of the
~3,489 inserted lines.

Method unchanged: hunters fan out read-only over non-overlapping surfaces
(engine core, admin API/store, admin frontend, mobile/stage + §4A audit),
every finding independently re-verified by hand-tracing the actual code
(and, for the two highest-value fixes, by writing a regression test
*first* and watching it fail against the pre-fix code), fixes scoped to
confirmed findings, verified cold.

## Summary

- **20 findings across 4 surfaces: 19 CONFIRMED, 1 PARTIALLY REFUTED.**
- **1 high confirmed-but-fix-redirected (see below), 3 high, 10 medium, 6 low.**
- **16 fixed and verified cold. 3 verification-only/no-bug (correctly
  documented negative results, not defects). 1 finding's suggested fix was
  tried, found NOT to work, and replaced with the real fix at a different
  layer** — the sweep's adversarial-fix-judging working exactly as intended.
- §4A sanitization (mobile never receives track title/artist/URI/lyrics)
  was independently confirmed clean from **three separate angles**
  (mobile/stage hunter tracing every client-side handler, admin-UI hunter
  tracing every broadcast's role scoping, and my own direct grep of every
  `Broadcast`/`BroadcastAll` call site in solo_karaoke.go/engine.go) — crown
  jewel held.

## Findings by surface

### Engine core — `server/internal/game/{engine.go,solo_karaoke.go,solo_karaoke_test.go}`

7 findings, 6 fixed, 1 fix redirected to the real root cause elsewhere.

- **s10eng-01 (HIGH):** seeking while paused (`onAdminSoloKaraokeSeek`)
  resumed real playback via `loadAndPlayTrack` but never cleared
  `e.soloKaraoke.paused`, desyncing the in-memory model from actual device
  state — Pause became dead for the rest of the track, Resume would
  audibly jump backward, and a reconnecting stage would get told to pause
  a device that was actually playing. **Fixed**: if the session was paused
  before the seek, immediately re-pause at the new position so the model
  and the device agree. `TestSoloKaraoke_SeekWhilePaused_StaysPaused` added.
- **s10eng-02 (MEDIUM, fix redirected):** a duplicate/retransmitted
  TrackEnded signal could double-advance the queue. The suggested
  server-side fix (a token comparison in `soloKaraokeAdvance`) was
  implemented, tested, and **found not to work** — proven by the very
  regression test written for it (it double-advanced to the third track
  instead of stopping at the second), because by the time a second
  sequential call runs, the engine's own state has already moved on to the
  next track, so there is nothing left to compare against that still
  identifies the *stale* signal. Investigated further and found the real
  root cause client-side: `web/stage/src/audio/spotify.ts`'s `ingest()`
  reported `trackEnded` as a **level** (true on every sample while
  paused-at-position-0) rather than an **edge** (true once, on the
  transition into that state) — the Spotify SDK can fire
  `player_state_changed` more than once during a single track transition.
  **Fixed** there instead, with `endedLevel` tracking the current state
  separately from the edge-triggered outward signal. No Go regression test
  (this project has no frontend test runner, a standing gap across prior
  sweeps); verified via `tsc --noEmit` and hand-trace.
- **s10eng-03 (MEDIUM):** deleting the currently-playing track from the
  Playlist mid-song while `repeat="one"` ended the entire Solo Karaoke
  session on the next natural TrackEnded instead of falling back to a
  normal next-track pick. **Fixed**: fall through to
  `soloKaraokeStepForward()` (already queue-exhaustion-safe) instead of
  calling `soloKaraokeDeactivate()` directly.
  `TestSoloKaraoke_DeleteCurrentTrack_RepeatOne_FallsBackInsteadOfEnding` added.
- **s10eng-04 (LOW, deferred):** the stage-reconnect lyrics resend
  broadcasts to every `RoleStage` connection instead of the one that
  reconnected. Harmless in the common single-stage-connection deployment;
  deferred as cosmetic.
- **s10eng-05 (MEDIUM):** `soloKaraokeNowPlayingData` (the full-sync
  payload) had no `Paused` field, so a reconnecting admin tab had no
  authoritative play/pause signal until the next reactive position tick.
  **Fixed**: added `Paused bool`, populated from `e.soloKaraoke.paused`.
  Mirrored in `web/shared/protocol.ts`. `TestSoloKaraoke_NowPlaying_IncludesPaused` added.
- **s10eng-06 (MEDIUM):** test coverage gaps for the above — now closed by
  the new regression tests added alongside each fix.
- **s10eng-07 (MEDIUM):** a failed `audio.Play` was only ever logged
  server-side; the session stayed "active" claiming to play a track that
  never started, with no TrackEnded ever arriving to auto-advance away
  from it. **Fixed**: `loadAndPlayTrack` now returns the error (trivia's
  own call site keeps discarding it, unchanged behavior); Solo Karaoke's
  two call sites (`soloKaraokePlayAt`, the seek handler) broadcast a new
  admin-only `smsgSoloKaraokeError` message on failure, wired through to a
  dismissable notice in the admin UI. `TestSoloKaraoke_PlayFailure_SurfacesError` added.

### Admin API + store — `server/internal/{admin/{handler.go,solokaraoke.go,solokaraoke_types.go,store.go,handler_test.go},store/solo_karaoke_store.go}`

4 findings, 3 fixed, 1 partially refuted.

- **s10api-01 (HIGH):** `ReorderSoloKaraokePlaylist` only updated rows
  present in the client-supplied `orderedIDs`; any row NOT included kept
  its old `position`, which could silently collide with a position freshly
  assigned to a different row (no UNIQUE constraint on `position`) — same
  bug class as this project's own sweep-1 `store-3` finding. Cross-confirmed
  independently by the admin-UI hunter as s10ui1-03 (a concurrent-admin-tab
  trigger for the identical bug). **Fixed**: the function now reads the
  full current id set inside the same transaction and produces a total
  order — known ids from `orderedIDs` first, then every remaining row
  appended after — so it can never leave two rows at the same position
  regardless of how incomplete the input list is.
- **s10api-02 (MEDIUM):** `AddSoloKaraokePlaylistTrack` computed
  `MAX(position)+1` with no locking; two concurrent adds under Postgres's
  default READ COMMITTED isolation could both compute the same max and
  collide. **Fixed**: both this function and the reorder above now take a
  shared `pg_advisory_xact_lock`, serializing every position-assigning
  operation against every other one.
- **s10api-03 (MEDIUM, PARTIALLY REFUTED):** reported as "zero test
  coverage" for the 10 new HTTP handlers, based on grepping only
  `handler_test.go`. On closer check, substantial coverage already exists
  in a sibling file, `solokaraoke_test.go` (7 tests covering the soft
  lyrics-gate, duplicate-409, not-found-404, position assignment, and
  engine-reload sync) — the hunter's grep was scoped to the wrong file.
  The one real gap (no auth-gating test for any of the 10 routes) and one
  partial gap (the soft-gate test only covered the Library bucket, not
  Playlist) were genuine and are now fixed:
  `TestSoloKaraokeRoutes_RequireAuth` (table-driven over all 10 routes) and
  `TestAddSoloKaraokeTrack_SucceedsWithNoLyrics` (both buckets) added.
- **s10api-04 (LOW):** `reorderSoloKaraokePlaylist` had no cap on the
  client-supplied id list length. **Fixed**: rejects >1000 entries with 400
  before calling the store (the store-level fix above also naturally bounds
  actual DB work to the real row count regardless of input size).

### Admin frontend — `web/admin/src/components/{GameControls.tsx,SoloKaraokePage.tsx,soloKaraoke/Player.tsx,AudioSettingsPanel.tsx}`

5 findings, 4 fixed, 1 verification-only (no bug).

- **s10ui1-01 (MEDIUM):** Solo Karaoke's Prev/Play/Next transport buttons
  had no in-flight guard, reintroducing the exact double-click bug class
  `GameControls.tsx`'s Mark Drop/Jump to Drop buttons were already hardened
  against (`uia7-3`) elsewhere in this same file. **Fixed**: the same
  short-timeout boolean-guard pattern, extracted as a small `useTransportGuard`
  hook and applied to all three buttons.
- **s10ui1-02 (HIGH):** `handleStartGame`/`handleResetGame` swallowed the
  server's HTTP rejection with an empty catch block whose comment ("state
  broadcast will update UI") is false for every error path — `StartGame()`
  returns its error before any broadcast fires. The new Solo-Karaoke-active
  rejection now ran through this same already-broken path: a stale/not-yet-
  synced `modeLock` on one admin tab could click Start Game, get rejected,
  and see nothing happen. **Fixed**: both handlers now surface the error
  message via a dismissable banner.
- **s10ui1-03:** the same bug as s10api-01, found independently from the
  frontend trigger angle (two admin tabs, one with stale local state).
  Closed by s10api-01's server-side fix, which makes the outcome
  collision-free regardless of client staleness.
- **s10ui1-04 (LOW):** Play wasn't disabled when the Playlist was empty
  (the server-side rejection already correctly surfaced via the existing
  WS error path, so this was UX polish, not a silent failure). **Fixed**:
  Play now also disables on an empty queue, with an explanatory title.
- **s10ui1-05:** verified, not a bug — "Reset to Defaults" correctly marks
  the threshold as touched before resetting it, so a subsequent Save can't
  silently skip it (the exact failure class sweep 9's s9ui2-01 was).

### Mobile + stage + shared protocol — §4A audit, `web/stage/src/views/SoloKaraokeView.tsx`, `web/mobile/src/App.tsx`

4 findings, 2 fixed (cosmetic), 2 documented clean/deferred.

- **s10ui2-01:** documented clean result — mobile registers exactly one
  Solo Karaoke handler (`soloKaraokeActive`, a bare boolean) and never
  reads any field from `soloKaraokeNowPlaying`/`soloKaraokeLyricsStatus`;
  `GameClient.dispatch` silently no-ops on any message type with no
  registered listener. Independently confirmed server-side (see Summary).
- **s10ui2-02 (LOW):** a dead `albumArt` field was passed to `Karaoke`,
  which never reads it (the album art is rendered by `SoloKaraokeView`'s
  own overlay). **Fixed**: dropped from the `reveal` object, comment added.
- **s10ui2-03 (LOW, deferred):** a brand-new stage connection landing
  while Solo Karaoke is already active briefly renders Lobby/GameOver for
  one round trip before the full-sync broadcasts arrive. Self-heals,
  architecturally consistent with existing first-connect flashes elsewhere
  in the app; deferred.
- **s10ui2-04 (LOW):** mobile's Solo Karaoke `IdleScreen` call site omits
  `scoreboard`/`ddResult` props, unlike every other call site — deliberate
  (no scoring in this mode) but worth a comment so a future "fix" doesn't
  reintroduce stale pre-mode-lock data. **Fixed**: comment added, no
  functional change.

## Verification (cold)

- `go build ./... && go vet ./... && go test -count=1 -race ./...` — clean,
  no regressions, including the race detector.
- `npx tsc --noEmit` clean in all three frontends (`stage`, `admin`, `mobile`).
- `qa/smoke.sh` -> SMOKE OK. `RACE=1 qa/acid.sh` -> ACID PASSED.
- `scripts/preflight.sh` -> PREFLIGHT PASSED (clean reinstall + production
  build, all three frontends; standard DB-gated-tests-self-skip warning).
- Both of the new engine-layer regression tests for the two highest-value
  fixes (`TestRequeryPendingLyrics_PendingNeverStarvedByNotFoundBacklog`-style
  rigor: `TestSoloKaraoke_SeekWhilePaused_StaysPaused`,
  `TestSoloKaraoke_DeleteCurrentTrack_RepeatOne_FallsBackInsteadOfEnding`)
  plus every other new test, all proven passing from a cold build; the
  s10eng-02 fix attempt was proven to FAIL against its own first version
  (the adversarial self-check working as designed) before being redirected
  to the real fix.
- The new store-layer fixes (advisory lock + full-renumber reorder) were
  **not** verified against a real live Postgres instance in this sweep —
  no existing staging test covers `solo_karaoke_store.go`, and this sweep's
  posture kept dynamic validation read-only against production. The SQL
  pattern (`pg_advisory_xact_lock` + a read-then-renumber transaction)
  mirrors established, already-staging-verified patterns elsewhere in this
  codebase (`RebuildLayout`), but this specific new code has only been
  verified by hand-trace + the Go build/test suite against the in-memory
  mock store. Recommend a staging-verification pass against real Postgres
  before this sees heavy concurrent-admin-tab use at an event.

## Known gaps (unchanged, not re-litigated)

Same structural items as sweeps 4-9: no frontend test runner, the
`client.ts` locked-contract question. Plus this sweep's two deferred items
(s10eng-04, s10ui2-03) and the one flagged-but-not-yet-staging-verified
item above.

## Sweep 11 recommendation

Moderate value if Solo Karaoke sees real concurrent-admin-tab use at an
event: the store-layer fix (s10api-01/02) is the one piece of this sweep
not yet verified against real Postgres under real concurrency, and that's
exactly the scenario the fix exists for. Otherwise low urgency — this
sweep's three HIGH findings are all fixed and verified, and the one
redirected fix (s10eng-02) is a good example of why this project's own
"judge the fix as hard as the finding" rule exists: the first attempt
looked reasonable on paper and failed the moment it was actually tested.
