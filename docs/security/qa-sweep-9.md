# QA Sweep 9 - Findings & Remediation

Date: 2026-10-16/17. Scope: diff against sweep 8's HEAD (`282fbe9`) plus the
working tree at the time: 3 commits (`a5c302f` lyrics-tooling scoping fix +
Daily Double bucket drag-out, `422476a` LRCLIB near-duplicate fallback,
`64dbe15` bulk retry widened to `not_found`) plus the from-scratch rewrite
of `AudioSettingsPanel.tsx` (Mixer sliders -> Volume + a draft-until-Save
Game Settings form) and the admin-specific favicon swap.

Method unchanged from prior sweeps: hunters fan out read-only over
non-overlapping surfaces, each finding independently re-verified against
the real code/behavior (reproduced locally wherever possible — a live
revert-run-restore cycle for the two highest-value regression tests), fixes
scoped to confirmed findings only, verified cold.

## Summary

- **16 findings across 3 surfaces: 16 CONFIRMED, 0 REFUTED.**
- **1 CRITICAL, 3 HIGH, 7 MEDIUM, 5 LOW.**
- **13 fixed and verified cold. 3 deliberately deferred** (reasoning below;
  none are data-integrity risks post-fix, all pre-existing or narrowly
  scoped UX/efficiency gaps).
- The critical finding was in code from earlier *this session* (the
  AudioSettingsPanel rewrite), caught before it ever shipped to production.

## Findings by surface

### Code layer — `server/internal/lyrics/lyrics.go`, `server/internal/admin/tracks.go`

6 findings, 5 fixed, 1 deferred.

- **s9code-01 (HIGH):** the new `/api/search` fallback fired immediately
  after a not-found `/api/get` response with zero pacing, doubling the
  burst rate against LRCLIB on every not-found probe — directly working
  against this project's own repeated real-world experience of LRCLIB
  rate-limiting under load. **Fixed**: a 300ms pacing delay (`fallbackProbeDelay`)
  before the fallback request, context-cancellation-aware.
- **s9code-02 (MEDIUM):** the fallback treated a 429/503 from `/api/search`
  identically to "no near-duplicate exists" — no `Retry-After` parsing, no
  backoff, silently indistinguishable from a genuine absence. **Fixed**:
  split the raw request into `searchOnce` (mirrors `probeOnce`'s shape) and
  give the fallback the same one-retry-after-`Retry-After` treatment as the
  primary call.
- **s9code-03 (HIGH, DEFERRED):** widening the bulk retry to `not_found`
  means a permanently-dead row gets re-probed on every single click of
  "Retry unresolved," forever, with no cooldown — wasteful against the same
  external dependency. **Deferred**: the complete fix (tracking a
  last-probed timestamp, which `track_lyrics.checked_at` already has but
  isn't yet threaded through `admin.LyricsRecord`/`GetLyricsStatusesForURIs`)
  is a small schema-adjacent plumbing change, not a one-line fix, and the
  actual risk is bounded (admin-triggered, capped at
  `maxRescanTracksPerRequest`=100 per click, against a free best-effort
  public API we don't pay for or hold an SLA with) rather than a
  data-integrity or production-breaking issue. Worth doing in a future
  pass if LRCLIB rate-limiting becomes a bigger problem than it already is.
- **s9code-04 (MEDIUM):** `ListTracks` orders by `created_at`, so a board
  that accumulated more `not_found` rows than the 100-track cap, ordered
  before a handful of still-`pending` ones, would have the `not_found`
  backlog permanently consume the entire budget on every call — the
  `Truncated` flag's own "click again to continue" promise is false in
  this configuration, since re-running processes the identical deterministic
  prefix forever. **Fixed**: two explicit passes, `pending` always claims
  the cap first, `not_found` only spends what's left.
  `TestRequeryPendingLyrics_PendingNeverStarvedByNotFoundBacklog` added,
  proven to fail against the pre-fix single-pass code via an explicit
  revert-run-restore cycle, then pass with the fix restored.
- **s9code-05 (LOW):** no test proved `searchForSyncedWithinTolerance`
  picks the *closest* in-tolerance candidate rather than just the first
  one — a regression swapping `<=` for an unconditional first-match
  assignment would have passed every existing test. **Fixed**: added
  `TestProbe_FallbackPicksClosestOfMultipleCandidates` (farther candidate
  deliberately listed first in the fake response, to actually exercise the
  comparison).
- **s9code-06 (MEDIUM):** the fallback's central safety contract ("any
  failure here is swallowed, never turns a clean not-found into a new kind
  of error") was completely untested — no test sent a malformed/non-200
  `/api/search` response. **Fixed**: added
  `TestProbe_FallbackFailureModesNeverPropagateAsErrors` (500 and malformed
  JSON) and `TestProbe_FallbackRetriesOnceAfterRateLimit` (doubles as the
  s9code-02 regression gate).

### UI layer 1 — lyrics tooling scoping + Daily Double bucket drag-out

4 findings (`web/admin/src/components/{BoardBuilderPage,builder/BuilderGrid,
builder/DailyDoubleBucket,builder/HoldingArea}.tsx`), all fixed.

- **s9ui1-01 (MEDIUM):** the Daily Double bucket's new ✕ button, now
  sitting inside a `<Draggable>` row with `dragHandleProps` spread across
  the whole row, lacked the `e.stopPropagation()` every sibling button in
  `TrackCard.tsx` already uses for the identical hazard — a slight pointer
  drift on click could engage a drag instead of (or alongside) the delete.
  **Fixed**: added `stopPropagation`.
- **s9ui1-02 (LOW):** dragging a bucket track directly onto a grid cell
  matched no branch in `handleDragEnd` — silent no-op, the row visibly
  snaps back with zero explanation. **Fixed**: explicit branch with a clear
  error ("drag to the Track Library first").
- **s9ui1-03 (MEDIUM):** the reverse-drag's async window (and, the hunter
  noted, the pre-existing forward branch's identical shape) had no guard
  against two overlapping drags of the same track before `ddBucket` state
  caught up — `@hello-pangea/dnd` releases its drag session on mouseup
  without waiting for the async handler to settle. **Fixed**: a `movingIds`
  in-flight-tracking guard applied to the top of `handleDragEnd`, covering
  every branch symmetrically (not just the two Daily Double ones), since
  the same unguarded gap existed on all of them.
- **s9ui1-04 (LOW):** lifting `requeryError` up to `BoardBuilderPage` moved
  its display from inline-in-context to a bar below the entire 3-column
  grid, which could land outside the viewport for a requery triggered deep
  in a scrolled `BuilderGrid` cell. **Fixed**: `position: sticky` to the
  bottom of the viewport, plus a dismiss button (needed once it's sticky).

### UI layer 2 — `AudioSettingsPanel.tsx` (new Game Settings form)

6 findings, 4 fixed, 2 deferred.

- **s9ui2-01 (CRITICAL):** "Save Settings" unconditionally pushed the
  component's local skip-vote-threshold draft to the server on every save —
  including a save triggered by editing a completely unrelated field. Since
  that draft is seeded from a hardcoded default (`useState(50)`) and has
  *no* server readback at all, and since `AudioSettingsPanel` lives inside
  `App.tsx`'s `{page === "control" && ...}` conditional (confirmed: this
  genuinely unmounts/remounts on every Control Room <-> Board Builder tab
  switch), any save after a tab-switch-and-back would silently reset the
  real threshold back to 50% with zero operator intent — directly
  undercutting the project's own stated anti-abuse rationale for the 50%
  floor. **Fixed**: a `threshTouched` ref, set only by the threshold
  field's own `onChange` — `Save` now only sends `setThresh` if that
  specific control was actually touched, restoring exactly the safety
  property the pre-rewrite slider had (it only ever fired from its own
  handler, never bundled with unrelated fields).
- **s9ui2-02 / s9ui2-03 (HIGH):** none of the six seconds-based number
  inputs could have a decimal typed via keyboard, and the threshold field
  (non-zero min=50) couldn't be freely retyped — both caused by clamping
  on every keystroke instead of on commit. **Fixed**: `SettingRow`
  redesigned to hold its own raw text while focused, parsing/clamping/
  step-snapping only on blur or Enter (Escape reverts).
- **s9ui2-04 (MEDIUM, DEFERRED):** the entire Game Settings draft is
  component-local state with no persistence outside the mounted instance,
  so switching tabs and back silently discards any unsaved edits with no
  warning — undercutting the feature's "safe draft" framing a second way.
  **Deferred**: the real danger (silently corrupting the *live* value) is
  already closed by the s9ui2-01 fix; what's left is losing your own
  in-progress edit, the same risk as closing a browser tab on any unsaved
  web form. The proper fix (lift state to a parent that survives the page
  switch, or a navigation-confirm guard) is a larger refactor than this
  sweep's scope justified for a UX-only residual risk.
- **s9ui2-05 (LOW, DEFERRED):** skip-vote threshold has no server readback
  at all — pre-existing since before this rewrite (the old slider had the
  identical gap/comment). Not newly introduced; now less consequential
  given s9ui2-01's fix closes the actual clobber risk. Deferred alongside
  s9code-03 as a "would need real protocol/engine work" item.
- **s9ui2-06 (LOW):** `lockoutChars` (conceptually a whole-number letter
  count) could show a confusing decimal draft like "5.7 chars" until Save
  silently rounded it. **Fixed** as part of the same `SettingRow` redesign:
  every field snaps to its nearest `step` on commit, not just at Save.

## Verification (cold)

- `go build ./... && go vet ./... && go test -count=1 -race ./...` — clean,
  no regressions, including the race detector.
- `npx tsc --noEmit` + production build clean in `web/admin` (the only
  frontend touched this sweep).
- `qa/smoke.sh` -> SMOKE OK. `RACE=1 qa/acid.sh` -> ACID PASSED.
- `scripts/preflight.sh` -> PREFLIGHT PASSED (clean reinstall + production
  build, all three frontends; standard DB-gated-tests-self-skip warning,
  unrelated to this sweep).
- Both new Go-layer regression tests
  (`TestRequeryPendingLyrics_PendingNeverStarvedByNotFoundBacklog` and, by
  extension, the fallback suite) proven to fail against the pre-fix code
  via an explicit revert-run-restore cycle, byte-for-byte restored after.
- UI-layer fixes (no frontend test runner, unchanged structural gap since
  sweep 4) verified via `tsc --noEmit` + production build + hand-traced
  React state/effect logic — same posture as every prior sweep touching
  frontend code.

## Known gaps (unchanged, not re-litigated)

Same structural items as sweeps 4-8: no frontend test runner, the
`client.ts` locked-contract question. Plus this sweep's own 3 deferred
items (s9code-03, s9ui2-04, s9ui2-05) — none are data-integrity risks,
all documented above with explicit reasoning.

## Sweep 10 recommendation

Low urgency on a hunt basis — this sweep's one critical finding was caught
before shipping, not in production, and the remaining deferred items are
bounded efficiency/UX gaps, not correctness bugs. The two deferred
protocol-adjacent items (threshold server readback, not_found retry
cooldown) are worth bundling into one small feature pass if/when there's
owner bandwidth, since both would benefit from the same kind of change
(threading one more field through the existing admin-sync broadcast).
