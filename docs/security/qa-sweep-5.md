# QA Sweep 5 — Findings & Remediation

Date: 2026-08-28. Scope: **sweep 4's own fixes**, plus the surfaces they touched.
Sweep 4 shipped 10 fixes into transport, the engine, the boot gates and three
frontends; the harness rule is that a fix is a new change and gets its own
adversarial pass, because most of the nastiest bugs in practice are introduced
BY fixes. Sweep 4 itself produced two examples (a proposed closer that would
have frozen the game, and a gate decoupling that broke the dev LAN flow), so
this sweep took them as the target rather than re-hunting sweeps 1–3.

Method unchanged: hunters fan out read-only over non-overlapping surfaces →
independent skeptical validators try to *refute* each finding → only CONFIRMED /
PARTIAL findings get fixed → each fix gets its own adversarial pass → verify cold.

## Summary

- **8 findings reported, 7 CONFIRMED, 1 REFUTED.** Every confirmed finding came
  back at LOW or MEDIUM. Nothing in sweep 4's delta was critical or high.
- **The one HIGH was downgraded to MEDIUM by its validator — and its stated
  mechanism was wrong.** The validator refuted the hunter's reachability claim
  and then found a *different, real* path to the same defect. That is the sweep's
  most useful single result.
- **The engine surface came back clean on a redo**, with sweep 4's two
  game-correctness fixes independently re-proven by revert-and-watch-it-fail.
- `qa/acid.sh` grew from **27 to 32 gates**. Two lock new fixes; three lock
  coverage holes sweep 4 left behind.
- **Three agents failed silently** (idled without reporting). One had written a
  valid deliverable, one had written an empty placeholder, one had written its
  verdict. Handled per the watchdog rule — see "Process" below.

## The one that mattered: a fix that could close the healthy client

### MEDIUM — s5-ui-01: two `establish()` attempts can run concurrently in mobile

Sweep 4 fixed a session leak: `useGame.ts`'s connect-failure `catch` dropped
`clientRef.current` without closing it, so every backoff retry leaked a live
QUIC session and (after sweep 4's limiter fix) burned a per-IP slot. The fix
added a `close()` to that catch.

The hunter claimed a double-tap could make two attempts race, so that catch —
operating on the *shared ref* — could close the healthy client and leak the
failed one, re-creating the exact bug it was written to kill.

**The validator refuted that mechanism.** `JoinScreen.tsx:52` disables the
button and `:18` early-returns while `conn === "connecting"`, and `establish()`
patches `conn:"connecting"` **synchronously** at `useGame.ts:280` before any
await — so the re-render lands long before a second tap task could run. The
double-tap cannot happen.

Then it found the path that can. After a *failed* initial connect, `joined` is
still false, so `App.tsx:16` keeps `JoinScreen` mounted with the button
**re-enabled** (`conn === "disconnected"`), while a backoff timer armed at
`useGame.ts:330` is still pending. The timer's only guard is
`if (unmountedRef.current || clientRef.current) return` (`:251`) — and
`clientRef.current` is still **null** while the user's manual `establish()` sits
parked in `await fetchCertHashes()` (`:285`). Both attempts pass the guard at
`:278`. Two concurrent attempts, shared-ref teardown, exactly the predicted
damage via an unpredicted route.

Fixed two ways, because either alone is insufficient:
1. **Identity**, not the ref: the catch closes the instance *this* invocation
   owns and surrenders `clientRef.current` only `if (clientRef.current === client)`.
   The optional call covers `fetchCertHashes()` throwing before construction,
   where there is nothing to close. Applied at every ref-clearing site in the
   function, including the post-hello catch.
2. **An overlap latch** closing the window itself, released in a `finally` as
   the sole exit point.

The `finally` is load-bearing and was specified before the fix was written. The
validator's own `fix_risk` field said it plainly: a latch set without being
cleared on every exit path means the `:278` guard rejects **every future
reconnect** and the player can never rejoin. That is strictly worse than the
bug — a leaked session burns one slot out of a deliberately generous per-IP cap
of 128, whereas a stranded latch ends that player's game. The guard's early
return is placed *before* the latch is set, so it cannot strand.

### LOW — s5-ui-02: admin's `onState` had no stale-client guard

Mobile and stage both ignore state events from a superseded client. `useAdmin`
did not. Again the hunter's reachability was wrong and the validator's was
right: a first login cannot race, because `await clientRef.current?.close()` is
`await undefined` — a microtask — so the status patch and re-render beat any
second click.

A **re-login** can. `useAdmin` has no disconnect handler, so after a drop
`clientRef.current` still points at the dead client, `status` is `"closed"`, and
the Login screen is re-enabled. The operator's submit then sits in a *real*
`await close()` that stalls on the broken link for hundreds of ms to seconds,
and because `patch({status:"connecting"})` had not run yet, the button was still
enabled for an impatient second click.

Fixed by hoisting the patch above the await so the busy gate disables the form
synchronously within the click handler, and by adding the identity guard.
The guard compares the **locally captured `client` const**, not
`clientRef.current` — per the validator's `fix_risk`, a guard written against
the ref is a tautology that silently does nothing while reading as correct.

Deliberately NOT done: the guard was not extended to the `wire()` message
handlers. `welcome` sets `status:"authed"` and is what admits the operator; a
guard misapplied there could lock them out of the control room mid-event, which
is far worse than a corrupted status field. Recorded as a follow-up question,
not silently attempted.

## Transport: teardown hygiene

### LOW — s5-ws-001: an intended teardown logged as a fault
When the idle reaper fires or the hub drops a conn via `conn.stop()`, the socket
is closed deliberately — but the parked read loop then returns a
`net.ErrClosed`-shaped error that fell through the `io.EOF` / normal-close check
and logged `transport/ws: conn %s read error`. Normal behavior logging as a
fault is not cosmetic: at a live event the log is the only signal anyone has,
and a line that cries error on healthy teardown is how a real fault gets
scrolled past.

Fixed with a `localClose` flag set **before** `CloseNow()` in both teardown
paths, so there is no window where the resulting error slips through unflagged.
Genuine peer-side errors still log loudly — and the gate asserts that inverse,
so it cannot pass if someone deletes all the logging.

### LOW — s5-ws-003: `Timer.Reset` cannot retract an in-flight `AfterFunc`
`idleTimer.reset()` could not save a connection from a callback that had already
begun running. Mechanism real, reachability negligible: a sub-millisecond race
against a 30s timer with ~15x heartbeat margin, not hostile-client-exploitable.

Fixed with a generation counter — each `reset()` stops the current timer, bumps
`gen`, and arms a fresh `AfterFunc` closed over the new generation; a superseded
callback sees the mismatch and vetoes itself. The callback decides under `mu`
and calls `onExpire()` **after releasing it**, because holding a lock across
`CloseNow()` is how you invent a deadlock while fixing a benign race.

Tradeoff accepted and recorded: `reset()` now allocates a fresh timer per call
instead of reusing one via `Reset()`. Safe here **specifically** because §5's
deterministic-timer contract means the server sends `trackStart` once and the
stage computes decay locally — there is no per-tick server broadcast to churn
against. Revisit if one is ever added.

### REFUTED — s5-ws-002: concurrent `CloseNow()` does not serialize for seconds
Claimed: concurrent `CloseNow()` calls serialize on the library's `closeMu` for
the winner's full close duration, which on the engine's single broadcast
goroutine would be a whole-game stall. This would have been critical, since
sweep 4 chose `CloseNow()` over `Close(code, reason)` for exactly that reason.

Refuted by reading the vendored library source — the loser never re-touches
`closeMu`, it waits on already-closing channels — and then by **measuring**:
8 concurrent `CloseNow()` on one real conn peaked at **81µs**, not 15s. This
also independently re-validates `hub.go`'s "a streamCloser MUST NOT BLOCK"
invariant comment written in sweep 4.

## Configuration gates

Three CONFIRMED lows, all of them **sweep 4's own misses**, not pre-existing debt.

- **s5-cfg-01 / s5-cfg-02**: two places still claimed the server refuses `/ws`
  when `YFI_ENV=prod`. Sweep 4 *decoupled* `/ws` from `YFI_ENV`, so that is now
  false. `deploy/.env.example` and `README.md` were rewritten for exactly this
  and `web/mobile/.env.example` plus a comment in `useGame.ts` were missed.
  Documentation that overstates a protection is worse than none — a reader skips
  the real check. Both now state the actual gate: both vars required, no
  `YFI_ENV` backstop, prod may opt in and only warns.
- **s5-cfg-03**: `modeName()` / `isProd()` / `isDev()` had **zero** coverage.
  Sweep 4 tested the pure predicates (`isProdEnv`, `isDevEnv`, `decideWS`) and
  left the `os.Getenv`-wrapped wrappers that actually run at boot untested —
  and the casing bug lived in precisely that wrapper layer. Now pinned by
  `TestEnvModeWrappers` over `""`, `dev`, `prod`, `production`, `Prod`, `PROD`,
  `"  prod  "` and `staging`, asserting the mode string as well as the booleans.
  The `staging` case is the important one: it pins `isProd()=false,
  isDev()=false, modeName()="non-dev"`, which is what makes the inverted secret
  guard **fail closed** on an unrecognized value.

## The engine: clean on a redo, with sweep 4's fixes independently re-proven

The first engine hunter returned an empty findings array and could not say
whether that was a real verdict or an unupdated placeholder, so the surface was
treated as **unverified** and re-hunted from scratch. The redo found **zero
defects** and, unlike the first attempt, showed its work per point:

- **A brute force over the whole pool domain** — every `pointFactor`, with and
  without a partial, rows 1–5, elapsed 0–70s in 137ms steps — comparing the
  display path against the award path: `worst_low = -1`, `worst_high = 0`. That
  is exactly the documented 1-point-low floor residual and nothing worse, and
  crucially **no case projects HIGH**. Erring low is the safe direction.
- **Sweep 4's gates genuinely fail.** Reverting the shuffle to `sort.Slice` +
  playerID drove `TestQARegression_BuzzTieBreakIsRandomNotPlayerID` RED at
  `fp1 won 40/40`, the exact figure sweep 4 documented. Reverting
  `trackStartEnvelope` to the row-derived pool drove two `TestPool_*` gates RED
  at projected-190-vs-awarded-70. Sweep 4's central claims are now confirmed by
  someone other than their author.
- `pointFactor` verified applied exactly once, at award time, never inside the
  accessors. `trackStartEnvelope` verified field-identical to the inline
  envelopes it replaced (all 5 `TrackStartData` fields). `e.rng` verified
  non-nil, assigned once, read only from the engine's own goroutine, clean
  under `-race`.

### Not a defect: the daily double bypasses the new accessors
`finishDailyDouble` computes from raw `MaxPointsForRow(e.curRow)` rather than
`roundPool()`/`livePool()`. The hunter correctly declined to file it: it is
pre-existing, untouched by sweep 4, and it is a separate crowd-rating bonus
multiplier that is **never broadcast as a projected decaying pool**, so there is
no display-vs-award divergence for `livePool` to guard. Sweep 4's write-up says
four sites now read one accessor pair, so a future sweep would spot the fifth
that doesn't and re-file it — a comment at `finishDailyDouble` now records that
this is deliberate. Documenting intent is cheaper than validating the same
non-defect twice, which already happened in sweep 4 with the dead
`computeFrame` constants.

## Coverage that was proven and then thrown away

The engine redo verified something valuable with a scratch test it then deleted:
buzz fairness holds at **n=5** (400 trials, each of 5 tied contenders winning
71–90 times) and **ineligible players never become contenders** (banned and
already-guessed players rejected at `onBuzz`, winner always one of the 2
eligible).

Sweep 4's committed gates only cover **n=2**, where a biased shuffle is
essentially invisible. That coverage is now permanent:

- `TestQARegression_BuzzFairnessHoldsAtFiveWayTie` — n=5 all tied, fixed seed
  range 0..399, asserting every contender wins at least once. Deliberately does
  **not** assert win counts or distribution tightness: a flaky entry in a
  ratchet is worse than an absent one, because it trains people to re-run until
  green. Runtime 0.06s.
- `TestQARegression_IneligiblePlayersNeverBecomeContendersOrWin` — n=4 with one
  banned and one already-guessed player, seeds 0..99, asserting `contenders == 2`
  every trial. Sweep 4 changed how contenders are *ordered* and nothing verified
  that reordering could not admit someone who should not be in the pool at all.

A real trap surfaced while writing the second one: `selectCell` resets
`GuessedThisTrack = false` for everyone at round start, so the flag must be set
**after** cell selection or the test silently tests nothing. Documented inline.
That is precisely the failure mode that produces a green-but-empty gate.

## The gate that cannot be built

`s5-ui-01` and `s5-ui-02` have **no regression gate**, and will not get one.
The frontends have no test runner, so `qa/acid.sh` cannot lock them. Both fixes
were proven instead by `npx tsc --noEmit`, a production build via
`scripts/preflight.sh`, and a line-numbered before/after interleaving trace.
No test framework was added and no grep-shaped pseudo-gate was put in any
script to make the coverage *look* present.

This is weaker than every other gate in this sweep and is stated rather than
dressed up. It is the same structural gap that already prevents pinning the
TypeScript copy of the §7 decay curve, and it has now blocked coverage in two
consecutive sweeps. Fixing it is a real decision (add Vitest to three
frontends), not a patch.

## Process: three agents failed silently

Recorded because the watchdog rule earned its place this sweep. Three agents
idled without reporting, and the right response differed for each:

- One hunter had already written a **valid deliverable** (3 findings). Its
  silence cost nothing; the file was validated normally.
- One hunter had written an **empty placeholder** and, when asked, could not say
  whether `[]` was a verdict or an unupdated stub. An empty file is not
  evidence, so the surface was declared unverified and **re-hunted from
  scratch** a model rung up. The redo is the strongest single piece of work in
  this sweep.
- One validator had written its **verdict file** before going quiet; the
  verdicts were read from disk and acted on.

The lesson is not "agents are unreliable" — it is that **deliverables on disk
are the contract, and a report is only a convenience**. Progress was driven off
filesystem evidence throughout. Where a fixer never reported (both UI fixers),
its diff was reviewed and its verification re-run by hand rather than assumed.

Two serialization rules also paid off: `qa/acid.sh` was edited by exactly one
writer (me, in a single pass at the end, with fixers reporting gate *names*
instead) because two fixers in a ratchet file is how an entry silently
disappears; and the `s5-cfg-01` comment fix was deliberately held back and
batched into the mobile fixer's brief, because it lived in the same file another
agent was rewriting.

## Verification (cold, 2026-08-28)

- `RACE=1 qa/acid.sh` → **ACID PASSED**. 32/32 named gates present, cold
  `go build` / `go vet`, `go test -count=1 ./...` **and** `go test -count=1
  -race ./...` green across every package, all runnable gates confirmed to
  actually execute, smoke green. The `-race` run matters this sweep: it covers
  the new mutex-guarded generation counter and the atomic teardown flag.
- `scripts/preflight.sh` → **PREFLIGHT PASSED** (clean `npm install` +
  production build of stage, mobile and admin). Both changed frontends went
  through it, which is the only gate that catches an import resolving in a dev
  server but not in a real build.
- `npx tsc --noEmit` clean in `web/mobile` and `web/admin`.
- Every new gate was proven able to FAIL by reverting its fix and watching RED,
  then restoring byte-for-byte. One inverse gate initially used a client-side
  `CloseNow()`, which surfaces server-side as `io.EOF` and was therefore
  **already silent before the fix** — the probe proved nothing. It was replaced
  with an explicit protocol-error close, which does log pre-fix. That is the
  "prove the check can fail" rule applied to a check on a check.

Not verified, and deliberately not claimed: the 5 Postgres-backed
`TestStaging_*` gates, which need `YFI_TEST_DSN` and a real database. They are
presence-checked only and must be run against Postgres before an event — see
`qa/HANDOFF.md`. `preflight.sh` warns about them by name.

## Still open

Carried forward; none re-litigated this sweep.

- **s4-ui-01**: whether `web/shared/client.ts` is a locked contract file.
  `CLAUDE.md` and `docs/BUILD_CONTRACT.md` list no `web/` file; the file's own
  header and `qa/HANDOFF.md` say it is locked. **The docs contradict each other
  and this is an owner call.** It constrained two fixers this sweep, both of
  whom were told to stop and report rather than edit it. Neither needed to.
- **No test runner in any frontend.** Now demonstrably blocking: it is why
  s5-ui-01/02 have no gate and why the TS copy of the §7 curve cannot be pinned.
- **uimobile-4** (deferred since sweep 1): server-side ban enforcement is a
  design question, not a patch.
- **adminapi-5** (deferred since sweep 1): CORS hardening.
- `YFI_BOARD_ROWS` / `YFI_BOARD_COLS` accepted and ignored; needs a contract
  version bump since `config.go` is locked.
- The §7 decay curve exists in three places (`scoring.go`, `engine.go`,
  `scoring.ts`).
- **New follow-up**: should admin's `wire()` message handlers carry the
  stale-client identity guard? Deliberately not attempted — `welcome` admits the
  operator and a misapplied guard could lock them out of the control room
  mid-event.

## Convergence

Sweep 5 is **not** a convergence. It produced 7 confirmed findings, so the rule
says keep going. But the shape of the result is the real signal: **nothing
critical or high survived validation, the two highest-severity reports were both
downgraded with their stated mechanisms refuted, and the engine surface came
back clean on a rigorous redo.** Sweep 4's fixes held up.

A sweep 6 targeting sweep 5's own fixes would be examining 7 low/medium changes,
five of which are test-only or comment-only. The higher-value targets now are
the two standing structural gaps — the frontend test runner and the
`client.ts` contract question — and both are owner decisions rather than
hunting problems.
