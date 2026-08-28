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
