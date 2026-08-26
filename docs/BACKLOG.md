# Backlog

Feature work requested but not yet designed or scheduled. Items here are
**not** specified — each needs a decision pass before implementation. Shipped
work goes in `docs/CHANGELOG.md`; QA findings live in `qa/HANDOFF.md`.

Added 2026-08-25.

---

## 1. Verify (and likely fix) the play/pause buttons

**Status:** needs investigation first — treat "broken" as a hypothesis, not a
diagnosis, until reproduced.

Confirm the play/pause controls behave as intended in every state they can be
hit from: before a track starts, mid-track, while buzzed/frozen, during reveal,
during karaoke, and after a round ends. Both the admin-initiated path and the
automatic pause-on-buzz path.

Relevant surfaces (starting points, not a diagnosis): `web/admin/src/components/
GameControls.tsx` and `EvaluationPanel.tsx` on the control side;
`web/stage/src/audio/spotify.ts` + `mock.ts` behind
`web/stage/src/audio/types.ts` on the playback side; `server/internal/game/
engine.go` + `ports.go` for the server-side transitions.

Constraint: hard rule §9 — Go never plays audio, and on buzz the server sends
pause **directly to stage**. Any fix must keep audio decisions on the stage and
must not route audio through the mobile clients.

## 2. Round winner picks the next cell from their own device

**Status:** design needed. Discussed only at a high level.

Let the player who won the most points select the next category / point value on
their phone, instead of the admin picking every cell.

Open questions to settle first:
- "Wins the most points" — the last round's winner, or the current overall
  leader? Tie-break rule?
- Does the admin retain an override / veto, and what happens on a timeout or if
  the winner has disconnected?
- **Security:** this is the first feature that lets a mobile client *drive* game
  state. The mobile client is assumed fully hostile (see `CLAUDE.md`), so cell
  selection has to be server-authorized against the identity the server itself
  computed as the winner — never a client-asserted "I am the winner" claim. It
  also needs nonce coverage (§4D) and must not leak unplayed cell metadata to
  the phone beyond what §4A already permits.

## 3. Allow adding lyric-less songs behind a confirmation

**Status:** small, mostly specified.

Today lyric-less tracks are greyed out in the builder with a per-track play
override. The ask: allow adding a song with no synced lyrics after an explicit
confirm dialog, rather than blocking it.

Decide: does confirming set the existing `lyrics_override` flag, or do we need a
distinct "knowingly added without lyrics" state? What does karaoke show for such
a track — skip the karaoke phase, or show a placeholder? Use the in-app modal
pattern, not a native `confirm()`.

## 4. Leaderboard visibility

**Status:** discussion needed — scope unclear.

Scoreboards currently render in three places (admin split panel, stage corner
overlay, mobile idle screen). Clarify what should change: who can see scores and
when, whether players see only their own score vs. everyone's, whether it can be
hidden/revealed for dramatic effect, and whether that is an admin toggle or a
fixed rule.

## 5. Admin-editable play points

**Status:** design needed — **touches a fixed contract.**

Let the admin edit a cell's point value from the admin screen.

The blocker to resolve first: point values are not stored anywhere: they are
*derived* from the grid row by `RowMultiplier` / `MaxPointsForRow` in
`server/internal/game/scoring.go`, which is a **locked contract file** (Row1=100
… Row5=200, plus the time-decay curve). The stage reimplements the same formula
in JS and must stay bit-identical at the floor. So this needs either a per-cell
point override persisted alongside the layout and threaded through both the
server and the stage's mirrored formula, or a contract version bump. Per
`CLAUDE.md`, raise it as a `// CONTRACT-QUESTION:` in the calling code and get
owner sign-off before touching `scoring.go`.
