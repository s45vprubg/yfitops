# QA Sweep 4 — Findings & Remediation

Date: 2026-08-27. Scope: the post-sweep-3 delta (~1100 lines), not a re-litigation
of sweeps 1–3. Sweeps 1–3 converged on 2026-07-25; re-running the same surfaces
was the wrong move. The delta turned out to contain a brand-new transport —
`server/internal/transport/ws.go`, a cleartext WebSocket door into the same hub —
that had never been swept at all, plus a tech-debt audit's worth of uncommitted
changes that had themselves never been reviewed.

Method unchanged: hunters fan out read-only over non-overlapping surfaces →
independent skeptical validators try to *refute* each finding → only CONFIRMED /
PARTIAL findings get fixed → each fix gets its own adversarial pass → verify cold.

## Summary

- **10 confirmed findings fixed** (3 high, 4 medium, 3 low), plus 2 documentation
  fixes for dead code that *looked* like live defects.
- **4 findings REFUTED** — including two where the hunter's premise was simply
  wrong, one where a "bug" was a documented product decision, and one where the
  *proposed fix* would have been worse than the bug.
- **1 regression introduced by a fix, caught before commit** (the dev LAN
  fallback would have started 404ing).
- `qa/acid.sh` did not exist before this sweep. It does now, and it is the
  ratchet: 27 named locked regression tests, cold build/vet/test, an
  actually-executed check so a skipped gate cannot pass as a present one, and
  smoke. Sweep 4 added 13 of the 27.

## The central finding: `/ws` is a second door, and half the guarantees were on the doorframe

`ws.go` bridges plain WebSocket connections into the **same** `Hub` and the same
engine as WebTransport. That framing is what made the rest of the sweep tractable:
every transport-layer guarantee had to be re-asked, and the answer split cleanly
along *where the guarantee is enforced*.

Inherited correctly, because they live in the **engine**: §4A client
sanitization, §4B server arrival stamping, §4D nonce, the inbound frame size cap,
and client-IP capture.

Silently void, because they lived in the **WebTransport branch of `server.go`**:

### HIGH — s4-ws-c1: the session limiter did not exist on `/ws`
`handleSession` (`server.go:148`) acquires the global `YFI_MAX_SESSIONS` and
per-IP `YFI_MAX_SESSIONS_PER_IP` caps before the upgrade and releases on every
exit. `WSHandler.ServeHTTP` acquired nothing. While the route was mounted, the
flood cap closed in sweep 3 was worth nothing — a single host could open
unbounded `/ws` sessions.

Fixed: acquire *before* the upgrade (a flood is refused with a 503 without paying
for a handshake), one `defer release` covering every exit path, sharing the single
limiter instance with `/wt` — the cap is a ceiling on the process, not per
transport. Locked by `TestWSHandlerHonorsSessionLimiter`.

### HIGH — s4-ws-c2: zombie connections that could still buzz
`ws.go:49` called `ws.hub.add(connID, rw)`. The WebTransport path calls
`s.hub.add(connID, stream, streamCloser(stream))`. The third argument is what
lets a *hub-side* drop tear down the socket: `enqueue`'s overflow branch calls
`conn.stop()`, which closes the stream only if a closer was registered, which
unblocks the parked read loop and drives `OnDisconnect`. Without it, an
overflowing `/ws` player was removed from the hub but kept an open socket and a
live read loop — a dropped player who could still submit buzzes.

The existing gate for exactly this bug class, `TestConnDropClosesStreamOnOverflow`
(sweep 2), registers a bare `io.Writer` with the hub and so was *structurally
incapable* of catching a real call site omitting the closer. Worth remembering:
the gate was green and the bug was in the argument list of the code the gate was
written to protect.

Fixed with `closerFunc(func() error { return c.CloseNow() })`, and `hub.go` now
documents the invariant. See "the fix that was worse than the bug" below.

### MEDIUM — s4-ws-c3: no idle timeout
QUIC reaps dead peers at `MaxIdleTimeout=30s`. `/ws` had no read deadline, no
ping/pong, and no idle reaper, so a phone that walked into a tunnel held a
goroutine, a hub slot, a socket and (after the fix above) a limiter slot
indefinitely. An idle peer produces no read error, so the read loop simply parks
forever.

Fixed with a 30s inactivity reaper mirroring the QUIC value, reset on every
successful read *and* write, mutex-guarded because both the read loop and the
hub's writer goroutine reset it, and unarmable after `stop()`. The mobile client
heartbeats every 2s, so 30s is ~15 missed heartbeats: a quiet-but-healthy
spectator can never trip it. Locked by `TestWSHandlerIdleTimeout`.

### Also: `ws.go` had no test file at all
`grep -rln WSHandler server/` found only `main.go` and `ws.go`. A new transport
carrying the same protocol as the primary one shipped with zero coverage. It now
has four tests including a framing round-trip.

### Reviewed and deliberately KEPT: `OriginPatterns: []string{"*"}`
The library default requires the `Origin` host to equal the request `Host`, which
breaks the precise case this fallback exists for: a phone loading the PWA from the
Vite dev server on one `host:port` while `/ws` is on another. Auth is enforced at
the hello layer, and the WebTransport twin is deliberately equally open with the
same reasoning. Documented in place rather than "fixed" — this is intent, not a
defect.

## Configuration gates

### HIGH — s4-ws-x2: the prod gates failed open on `YFI_ENV` casing
Both gates in `main.go` compared `os.Getenv("YFI_ENV")` against the literal
`"prod"`. `Prod`, `PROD`, `production`, and `prod ` with a trailing space all read
as dev and disarmed them silently. On a gate whose entire job is to stop a
misconfigured event server, a case-sensitive string compare is the whole
vulnerability.

Fixed two ways. Normalization (`EqualFold` + `TrimSpace`, accepting both `prod`
and `production`), and — the part that actually matters — **inversion**: the
default-secret refusal now fires whenever `YFI_ENV` is anything *other than* dev,
so an unrecognized value fails closed instead of open. Unset/empty stays dev by
design (compose defaults to it, `dev-up.sh` runs bare). The resolved mode is now
the first line of the boot log, so a misconfigured server no longer looks
identical to a correct one.

### Product decision (owner-approved): `/ws` decoupled from `YFI_ENV`
Surfaced rather than decided unilaterally, because it is a posture question, not a
bug. Keying `/ws` off `YFI_ENV` made the gate **mutually exclusive with its own
use case**: the operator who needs the LAN fallback had to stay out of prod, which
also disarmed the default-secret guard. The variable meant to harden the
deployment was pushing people into the weaker configuration.

Approved resolution: a separate explicit acknowledgement. `/ws` now requires
**both** `YFI_DEV_WS=1` and `YFI_INSECURE_TRANSPORT=1` and ignores `YFI_ENV`
entirely. `YFI_DEV_WS` alone mounts nothing and logs a notice naming the missing
var. Registering `/ws` in prod is permitted and logs a warning naming the exact
exposure — no encryption, no cert pinning, the admin/stage secret readable and
replayable on the wire — on every boot. The matrix is a pure function
(`decideWS`) so it is unit-testable.

Proven against the real binary across five cold boots, not just in unit tests:

| env | result |
|---|---|
| `YFI_DEV_WS=1 YFI_INSECURE_TRANSPORT=1` | `/ws` → **101**, dev registration log |
| `YFI_DEV_WS=1` alone | `/ws` → **404** + notice naming `YFI_INSECURE_TRANSPORT` |
| both + `YFI_ENV=PROD`, real secrets | `/ws` → **101** + loud prod warning |
| `YFI_ENV=Prod`, dev-default secrets | **refuses to boot** (this is the casing fix) |
| `YFI_ENV=` empty | boots as dev, `/ws` → **101** |

## Game correctness

### MEDIUM — s4-engine: the stage projected points the server would not pay
One root cause, four symptoms. Four sites derived the scoring pool from the board
**row** rather than the live post-partial / post-halve state:
`trackStartEnvelope`, `halvePoints`, `gradePartial`'s broadcast, and
`adminViewEnvelope`. A row-5 partial left the stage counting down at 60fps from a
ceiling of 140 while `gradeCorrect` paid 70, and the control room's grading
readout showed 190 against that same 70. On a projector in front of a room, the
number being wrong *is* the bug.

All four now read one accessor pair, `roundPool` / `livePool`, which is also the
single place `pointFactor` is applied to a *displayed* pool.

**No payout change**, deliberately — the award semantics were an owner decision
and they stand. `gradeCorrect`'s arithmetic is untouched, independently verified:
`CurrentPoints(row, e)` is identical to
`currentPointsFromPool(MaxPointsForRow(row), BaseValue, e)` in all three branches
of the §7 curve, so routing the non-partial path through the pool form changes
nothing. `pointFactor` is still applied exactly once, at award time, never inside
the accessor — folding it in would have double-applied it.

Residual, documented in `livePool` rather than hidden: because both ends of the
pool are floored independently, an odd bonus (rows 2 and 4 with no partial) can
project **1 point low**. That is as close as an integer wire pool gets, and it
errs low rather than overstating what will be paid.

Locked by four `TestPool_*` gates covering projected-equals-awarded, the admin
readout, a mid-round reconnect, and the never-invert clamp.

### HIGH — s4-ws-x1: buzz ties went to the lowest player ID, and the ID is client-supplied
`resolveBuzzWindow` ordered contenders by `(arrivalMs, playerID)`. `arrivalMs` has
1ms granularity, so two phones on the same conference Wi-Fi tie **as a matter of
routine** — the tie-break was the common path, not a corner case. Every one of
those ties went to the lexicographically smallest player ID, for the whole game.

The player ID is the client-supplied device fingerprint. The red test output says
it plainly: `"fp1" won 40, "fp2" won 0`. A hostile phone picks a low fingerprint
and wins every contested buzz in every round, from a field that is not supposed to
influence ordering at all. This is the same *shape* as sweep 3's forged-RTT
finding — a client-controlled value leaking into the ordering — one layer further
down, in the tie-break rather than the sort key.

Owner-approved resolution: random tie-break. Contenders are pre-shuffled from the
engine's injectable `rng`, then sorted with `sort.SliceStable` on `arrivalMs`
alone. `SliceStable` is load-bearing — the unstable `sort.Slice` throws the
shuffle away and substitutes its own pivot-dependent permutation.

Honest limitation, stated in the test file rather than implied: the gates do
**not** detect a swap back to `sort.Slice`. An unstable sort over shuffled input
yields a different arbitrary permutation, not an ID-biased one, so it stays green.
The reason to keep `SliceStable` is that it makes the rng the *defined and
seedable* source of the tie-break — a design property, not something these tests
can enforce. `TestQARegression_BuzzEarliestArrivalStillWinsAfterShuffle` guards
the other direction: a genuine 1ms gap must still decide the round every time
(§4B).

## Client

### MEDIUM — mobile leaked a live QUIC session per reconnect attempt
`useGame.ts`'s connect-failure catch dropped `clientRef.current` without closing
it, unlike its post-hello sibling. `connectWT` awaits `wt.ready` *before* creating
the stream, so anything throwing after that point left an **open** session with no
remaining reference — and, once the limiter fix above landed, burned a per-IP slot
on every backoff retry. The two failure paths handling the same object differently
was the tell for which one was the considered decision.

### MEDIUM — a single teardown fired `onState(false)` twice
Three paths called `handleClose()` and then re-entered it via the transport's own
close event: `feedWS`'s overflow close, `readLoopWT`'s reader cancel, and a
pre-open `onerror` that also rejected `connect()`. A single-fire `down` latch
makes each teardown exactly one signal. The pre-open case is latched only pre-open
— an error on a live socket must still let `onclose` report the disconnect, or the
reconnect loop wedges.

### LOW — a text frame vanished with no error
`new Uint8Array(ev.data as ArrayBuffer)` on a string yields a **zero-length**
array. No throw, no teardown, the frame simply disappeared; a probe confirmed
`dispatched: []`. The framing is binary-only, so a non-`ArrayBuffer` frame now
closes with `1003`. Hardening rather than a live defect: this server only writes
binary frames, but a proxy or a hand-rolled dev server might not.

### MEDIUM — the stage's audio overlay could be dismissed for the wrong player
The auto-dismiss was guarded by `!disposed`, which only means the mount effect is
alive. `initSpotify()` destroys and replaces `audioRef.current` at runtime, so a
slow `activate()` resolving from the *destroyed* player could clear the overlay
for a successor that was never activated — a silent projector with no way to
re-arm. Both call sites now require `audioRef.current === p`.

### LOW — stage audio could die silently
`GET /api/spotify/token`'s error branch had no logging, unlike its siblings
`searchSpotify` and `importPlaylist`. It is the only signal that stage audio is
about to fail, and it produced silence; the stage just quietly reported "not
connected". Still returns `200` with `connected:false`, because the Web Playback
SDK's `getOAuthToken` treats a non-200 as fatal.

## Findings that were NOT defects

Recorded because "not every finding is a defect" was load-bearing four times this
sweep, and because the next sweep should not re-litigate them.

- **`web/shared/client.ts` is not a locked contract file.** A hunter reported a
  locked-contract violation. Neither `CLAUDE.md` nor `docs/BUILD_CONTRACT.md`
  lists any `web/` file. REFUTED. The real (unfixed) issue is that the file's own
  header prose and `qa/HANDOFF.md` both call it a fixed contract while the two
  authoritative lists do not — **the docs contradict each other, and which one is
  right is an owner call.** Flagged, not decided.
- **`math.Floor` vs bare `int()` in `currentPointsFromPool` was not a live bug.**
  A validator refuted any *reachable* disagreement: the single Go call site passes
  `maxP = e.partial.remaining` and `baseP = min(BaseValue-PartialPoints, maxP)`,
  both clamped at ≥0, and the default branch is a convex combination of
  non-negatives. Kept anyway, because §7 specifies floor and the JS mirror uses
  `Math.floor` — all three copies should read the same. Behavior-preserving, not a
  fix.
- **`decrypt.ts`'s reveal timings do not drift from the server.** `PHASE1_MS=5000`
  / `REVEAL_INTERVAL_MS=2000` against `reveal.go`'s `10000`/`3000` looks like a
  live client/server disagreement. It is not: the reveal is server-driven, only
  `glyphAt` is imported, and `computeFrame` plus both constants have no callers at
  all. Documented as dead — "syncing" the numbers would only have made dead code
  look authoritative.
- **`SampleBoard`'s `5, 5` is not a duplicate of `Config.BoardRows/BoardCols`.**
  Those two fields are read from `YFI_BOARD_ROWS`/`YFI_BOARD_COLS` and then used
  by nothing anywhere in the server; real dimensions come from Postgres via the
  admin layout API. The demo board is 5×5 because `categories` hardcodes five
  column labels and the §7 difficulty curve is defined for rows 1..5. Marked with
  a `CONTRACT-QUESTION` so nobody "fixes" it by wiring the inert vars in, and
  documented as inert in `.env.example`. The vars being accepted-and-ignored is
  real, but the fix is a contract version bump — `config.go` is locked.

## Two things the sweep caught that the sweep itself created

Worth its own section, because both are the reason fixes get judged as hard as
findings.

**A proposed fix that would have frozen the game.** A validator's suggested
`streamCloser` for the zombie-conn fix was nhooyr's `c.Close(code, reason)`. That
performs a WebSocket close handshake and waits ~5s for a reply from a peer that,
by construction in this path, is not reading. `conn.stop()` runs **inline** on the
engine's single broadcast goroutine, so this would have stalled every player's
frames for the handshake timeout — trading a zombie connection for a whole-game
freeze. Caught by a validator who built and *measured* it rather than reasoning
about it. `CloseNow()` shipped instead, and `hub.go` now carries the invariant in
writing: **a `streamCloser` must not block.**

**A fix that broke the dev flow.** Decoupling `/ws` from `YFI_ENV` means
`YFI_DEV_WS=1` alone no longer mounts the route — and `scripts/dev-up.sh` and
`deploy/docker-compose.dev.yml` set exactly that and nothing else. The LAN phone
playtest would have started 404ing with only a notice in a log file to explain it.
Both now set the pair. Found by grepping every consumer of the changed variable
after the fix, which is the check that exists for precisely this.

Also, for the record: a fixer made an unrequested sibling edit, moving the dev
Postgres host port 5432→5433 to resolve a conflict on one machine. It was in no
finding and no brief. Reverted.

## Verification (cold, 2026-08-27)

- `RACE=1 qa/acid.sh` → **ACID PASSED**. 27/27 named gates present, cold
  `go build` / `go vet`, `go test -count=1 ./...` and `go test -count=1 -race
  ./...` both green across all packages, all runnable gates confirmed to actually
  execute, smoke green.
- `scripts/preflight.sh` → **PREFLIGHT PASSED** (clean `npm install` + production
  build of stage, mobile and admin; backend build/vet/test). It also now warns
  loudly that **5 DB-gated tests self-skipped** and are not covered — a reporting
  fix from this sweep, because a green preflight was overstating its own coverage.
- The five-case live `/ws` gate matrix above, against a freshly built binary.
- Every new gate was proven able to FAIL. The buzz gate's red output is quoted in
  its test file. `qa/acid.sh`'s gate-presence check was itself proven able to
  fail before being trusted.

Not verified, and deliberately not claimed: the 5 Postgres-backed
`TestStaging_*` gates, which need `YFI_TEST_DSN` and a real database. They are
presence-checked by `qa/acid.sh` and must be run against Postgres before an
event — see `qa/HANDOFF.md`.

## Still open

- **uimobile-4** (deferred since sweep 1): server-side ban enforcement is a design
  question, not a patch.
- **adminapi-5** (deferred since sweep 1): CORS hardening.
- **s4-ui-01**: whether `web/shared/client.ts` is a locked contract file. The
  authoritative lists say no; the file's own header and `qa/HANDOFF.md` say yes.
  Owner call.
- `YFI_BOARD_ROWS`/`YFI_BOARD_COLS` are accepted and ignored. Wire them up or
  drop them on a contract version bump.
- The §7 decay curve exists in three places (`scoring.go`, `engine.go`,
  `scoring.ts`). The Go pair is pinned by shared vectors; the TS copy cannot be,
  because the frontends have no test runner. Folding
  `currentPointsFromPool` into `scoring.go` on a version bump would remove one
  copy.
