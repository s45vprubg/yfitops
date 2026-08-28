package game

import (
	"math/rand"
	"testing"

	"github.com/s45vprubg/yfitops/server/internal/protocol"
)

// buzz_fairness_test.go — QA sweep 4 locking gate for s4-ws-new-x1.
//
// arrivalMs has 1ms granularity, so two phones on the same conference Wi-Fi tie
// as a matter of routine. resolveBuzzWindow used to break those ties with
// `a.playerID < b.playerID`, which handed EVERY tie to the lexicographically
// smallest player ID for the entire game — a systematic advantage derived from a
// field that is not supposed to influence ordering at all. The fix pre-shuffles
// contenders and sorts STABLY on arrivalMs alone.
//
// Both tests below were proven RED against the pre-fix code before being
// committed:
//   - TieBreakIsRandom: "over 40 trials with IDENTICAL arrivalMs the winner was
//     always the same player ("fp1" won 40, "fp2" won 0)". Note the winning ID is
//     literally the client-supplied device fingerprint, so this was directly
//     farmable by a hostile phone, not merely a theoretical skew.
//   - EarliestArrivalStillWins guards the other direction: the shuffle must not
//     reorder genuinely different arrivals (§4B).
//
// Measured limitation, stated rather than implied: neither test detects a swap of
// sort.SliceStable back to sort.Slice. An unstable sort over already-shuffled
// input yields a different arbitrary permutation, not an ID-biased one, so it
// stays green — the reason to keep SliceStable is that it makes e.rng the defined
// and seedable source of the tie-break, which is a design property, not something
// these gates can enforce.
//
// QA sweep 5 added two more gates below (still with the same limitation, stated
// once above rather than repeated per-test):
//   - FairnessHoldsAtFiveWayTie: the n=2 gates above can't see a subtly biased
//     shuffle — at n=2 there just aren't enough buckets for a skew to show up as
//     anything other than "one side won more," which 40 trials already tolerates
//     as noise. n=5 gives the bias five buckets to hide from and a much larger
//     seed count to catch it in.
//   - IneligiblePlayersNeverBecomeContendersOrWin: guards eligibility filtering
//     against the shuffle itself — a shuffle that accidentally admitted a
//     banned/already-guessed player as a contender (or let them win) would be a
//     game-integrity bug, and nothing above locks against it.

// buzzTiedTrial runs one full buzz window with both players stamped at the SAME
// server arrival time, with the engine's rng seeded by `seed`. It returns the
// winning playerID plus the two contenders' IDs.
func buzzTiedTrial(t *testing.T, seed int64, arrivalSkewMs int64) (winner, idA, idB string) {
	t.Helper()
	h := newHarness(t)
	// Large window so the resolution AfterFunc never fires mid-test; we resolve
	// explicitly below.
	h.e.buzzWindowMs = 60_000
	defer h.run()()
	h.joinAdmin("admin")
	h.joinStage("stage")
	idA = h.join("c1", "fp1", "alice")
	idB = h.join("c2", "fp2", "bob")

	// Per-trial rng, so the tie-break has a different draw each trial. The engine
	// loop is the only reader (resolveBuzzWindow), and sync runs on that loop.
	h.sync(func() { h.e.rng = rand.New(rand.NewSource(seed)) })

	h.selectCell("admin", 1, 1)
	if h.state() != protocol.StateRoundActive {
		t.Fatalf("state = %s, want ROUND_ACTIVE", h.state())
	}

	nonce := h.gate.Current()
	arrival := nowMs()
	var rk string
	h.sync(func() { rk = h.e.roundKey })

	// c1/alice is stamped at `arrival`; c2/bob at arrival+arrivalSkewMs. Enqueue
	// order is deliberately held constant across trials so that any variation in
	// the winner can only come from the tie-break.
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: nonce}, arrival)
	h.e.OnMessage("c2", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: nonce}, arrival+arrivalSkewMs)
	h.sync(func() {})

	var n int
	h.sync(func() { n = len(h.e.buzzContenders) })
	if n != 2 {
		t.Fatalf("contenders = %d, want 2 (both buzzes must land in the open window)", n)
	}

	h.sync(func() { h.e.resolveBuzzWindow(rk) })
	h.sync(func() { winner = h.e.buzzWinner })
	if winner == "" {
		t.Fatalf("no buzz winner after resolution")
	}
	return winner, idA, idB
}

func TestQARegression_BuzzTieBreakIsRandomNotPlayerID(t *testing.T) {
	const trials = 40
	wins := map[string]int{}
	var idA, idB string
	for seed := int64(0); seed < trials; seed++ {
		w, a, b := buzzTiedTrial(t, seed, 0 /* exactly tied */)
		idA, idB = a, b
		wins[w]++
	}

	if idA == idB {
		t.Fatalf("harness bug: both players got playerID %q", idA)
	}
	// Sanity: nobody outside the two contenders won.
	for id := range wins {
		if id != idA && id != idB {
			t.Fatalf("unexpected winner %q (contenders %q, %q)", id, idA, idB)
		}
	}

	// The real assertion. Pre-fix, the lexicographically smaller ID took all 40.
	if wins[idA] == 0 || wins[idB] == 0 {
		low, high := idA, idB
		if high < low {
			low, high = high, low
		}
		t.Fatalf("tie-break is not random: over %d trials with IDENTICAL arrivalMs the winner was always the same player (%q won %d, %q won %d).\n"+
			"This is the s4-ws-new-x1 regression — most likely the playerID tie-break is back in resolveBuzzWindow, or the pre-shuffle is gone. "+
			"The lexicographically smaller ID is %q.",
			trials, idA, wins[idA], idB, wins[idB], low)
	}
}

func TestQARegression_BuzzEarliestArrivalStillWinsAfterShuffle(t *testing.T) {
	// A 1ms genuine gap is the smallest arrival difference the server can observe,
	// and it must decide the round every single time — the fairness shuffle only
	// applies WITHIN an equal-arrival group (§4B: server arrival is the sole
	// ordering input). 40 seeds, so a shuffle that leaked into the ordering shows
	// up rather than hiding behind one lucky draw.
	for seed := int64(0); seed < 40; seed++ {
		w, idA, _ := buzzTiedTrial(t, seed, 1 /* alice arrives 1ms earlier */)
		if w != idA {
			t.Fatalf("seed %d: winner = %q, want alice (%q) — a 1ms-earlier server arrival must always win; the tie-break shuffle must not reorder unequal arrivals", seed, w, idA)
		}
	}
}

// buzzFiveWayTiedTrial is buzzTiedTrial widened to 5 contenders, all stamped at
// the SAME server arrival time, with the engine's rng seeded by `seed`. It
// returns the winning playerID plus all 5 contenders' IDs (join order:
// alice, bob, carol, dave, erin).
func buzzFiveWayTiedTrial(t *testing.T, seed int64) (winner string, ids []string) {
	t.Helper()
	h := newHarness(t)
	h.e.buzzWindowMs = 60_000
	defer h.run()()
	h.joinAdmin("admin")
	h.joinStage("stage")

	conns := []string{"c1", "c2", "c3", "c4", "c5"}
	fps := []string{"fp1", "fp2", "fp3", "fp4", "fp5"}
	handles := []string{"alice", "bob", "carol", "dave", "erin"}
	ids = make([]string, len(conns))
	for i := range conns {
		ids[i] = h.join(conns[i], fps[i], handles[i])
	}

	h.sync(func() { h.e.rng = rand.New(rand.NewSource(seed)) })

	h.selectCell("admin", 1, 1)
	if h.state() != protocol.StateRoundActive {
		t.Fatalf("state = %s, want ROUND_ACTIVE", h.state())
	}

	nonce := h.gate.Current()
	arrival := nowMs()
	var rk string
	h.sync(func() { rk = h.e.roundKey })

	// All 5 buzz at the identical arrival time, enqueue order held constant
	// across trials — same rationale as buzzTiedTrial.
	for _, c := range conns {
		h.e.OnMessage(c, protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: nonce}, arrival)
	}
	h.sync(func() {})

	var n int
	h.sync(func() { n = len(h.e.buzzContenders) })
	if n != 5 {
		t.Fatalf("contenders = %d, want 5 (all 5 buzzes must land in the open window)", n)
	}

	h.sync(func() { h.e.resolveBuzzWindow(rk) })
	h.sync(func() { winner = h.e.buzzWinner })
	if winner == "" {
		t.Fatalf("no buzz winner after resolution")
	}
	return winner, ids
}

// TestQARegression_BuzzFairnessHoldsAtFiveWayTie is the n=5 widening of
// TestQARegression_BuzzTieBreakIsRandomNotPlayerID. At n=2 a subtly biased
// shuffle is nearly invisible — there are only two buckets, and "60/40" reads as
// noise. At n=5 a bias has five buckets to hide from and 400 seeds to be caught
// in: a real hunter run of this exact scenario over 400 trials observed a spread
// of 71-90 wins per contender with all 5 winning at least once. This test
// asserts only the robust claim (every contender wins at least once); it
// deliberately does NOT assert a specific count or a tight distribution, since
// that would make the gate flaky on rng/impl details unrelated to fairness.
func TestQARegression_BuzzFairnessHoldsAtFiveWayTie(t *testing.T) {
	const trials = 400
	wins := map[string]int{}
	var ids []string
	for seed := int64(0); seed < trials; seed++ {
		w, trialIDs := buzzFiveWayTiedTrial(t, seed)
		ids = trialIDs
		wins[w]++
	}

	for _, id := range ids {
		if wins[id] == 0 {
			t.Fatalf("tie-break is not fair at n=5: player %q never won across %d trials (wins: %v) — "+
				"this is the s4-ws-new-x1 regression surfacing at a scale the n=2 gates can't see, "+
				"most likely the playerID tie-break is back or the shuffle is biased", id, trials, wins)
		}
	}
}

// buzzEligibilityTrial runs a 4-player tied buzz window where two players are
// eligible and two are not: connC/handle carol is Banned, connD/handle dave has
// already guessed this track (GuessedThisTrack). Both states are set directly on
// the registry via h.sync, matching the pattern engine_test.go already uses for
// RTTMs/Score (see TestBuzz_SingleWinner and friends) — there is no harness
// setter for either flag, so this reaches into the same registry map the rest of
// the test file already reaches into.
//
// It returns the winner, the two eligible IDs, the two ineligible IDs, and the
// number of contenders resolveBuzzWindow actually saw (onBuzz is expected to
// reject the ineligible pair at buzz time, before they ever become contenders).
func buzzEligibilityTrial(t *testing.T, seed int64) (winner string, eligibleA, eligibleB, bannedID, guessedID string, contenders int) {
	t.Helper()
	h := newHarness(t)
	h.e.buzzWindowMs = 60_000
	defer h.run()()
	h.joinAdmin("admin")
	h.joinStage("stage")

	eligibleA = h.join("c1", "fp1", "alice")
	eligibleB = h.join("c2", "fp2", "bob")
	bannedID = h.join("c3", "fp3", "carol")
	guessedID = h.join("c4", "fp4", "dave")

	h.sync(func() { h.e.reg.players[bannedID].Banned = true })
	h.sync(func() { h.e.rng = rand.New(rand.NewSource(seed)) })

	h.selectCell("admin", 1, 1)
	if h.state() != protocol.StateRoundActive {
		t.Fatalf("state = %s, want ROUND_ACTIVE", h.state())
	}

	// selectCell starts a fresh round, which resets GuessedThisTrack for
	// everyone (§3.4 "new track => everyone may guess again") — so this must be
	// set AFTER the round starts, not before, or selectCell wipes it back to
	// false and dave becomes a legitimate contender.
	h.sync(func() { h.e.reg.players[guessedID].GuessedThisTrack = true })

	nonce := h.gate.Current()
	arrival := nowMs()
	var rk string
	h.sync(func() { rk = h.e.roundKey })

	// All 4 buzz at the identical arrival time; the banned/guessed pair should be
	// rejected by onBuzz's eligibility checks (§897-901 in engine.go) and never
	// reach e.buzzContenders at all.
	for _, c := range []string{"c1", "c2", "c3", "c4"} {
		h.e.OnMessage(c, protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: nonce}, arrival)
	}
	h.sync(func() {})

	h.sync(func() { contenders = len(h.e.buzzContenders) })

	h.sync(func() { h.e.resolveBuzzWindow(rk) })
	h.sync(func() { winner = h.e.buzzWinner })
	if winner == "" {
		t.Fatalf("no buzz winner after resolution")
	}
	return winner, eligibleA, eligibleB, bannedID, guessedID, contenders
}

// TestQARegression_IneligiblePlayersNeverBecomeContendersOrWin guards §4B
// eligibility against the fairness shuffle: a shuffle bug that accidentally
// admitted a banned or already-guessed player as a contender (or let one win)
// would be a game-integrity bug, and nothing before this test locked against it.
// 100 seeds, so a shuffle that leaked past the eligibility check shows up rather
// than hiding behind one lucky draw.
func TestQARegression_IneligiblePlayersNeverBecomeContendersOrWin(t *testing.T) {
	const trials = 100
	for seed := int64(0); seed < trials; seed++ {
		w, eligibleA, eligibleB, bannedID, guessedID, contenders := buzzEligibilityTrial(t, seed)

		if contenders != 2 {
			t.Fatalf("seed %d: contenders = %d, want 2 — the banned and already-guessed players must be rejected by onBuzz before ever becoming contenders", seed, contenders)
		}
		if w == bannedID {
			t.Fatalf("seed %d: banned player %q won the buzz — eligibility check did not survive the shuffle", seed, bannedID)
		}
		if w == guessedID {
			t.Fatalf("seed %d: already-guessed player %q won the buzz — eligibility check did not survive the shuffle", seed, guessedID)
		}
		if w != eligibleA && w != eligibleB {
			t.Fatalf("seed %d: winner = %q, want one of the two eligible contenders (%q, %q)", seed, w, eligibleA, eligibleB)
		}
	}
}
