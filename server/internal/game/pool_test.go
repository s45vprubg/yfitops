package game

import (
	"encoding/json"
	"testing"

	"github.com/s45vprubg/yfitops/server/internal/protocol"
)

// pool_test.go pins the QA-sweep4 fix for s4-se-1 / s4-se-new-2 / s4-se-new-3:
// the pool we put on the wire (or show the admin) must be the LIVE, partial-
// aware pool, so the number the stage projects at 60fps is the number
// gradeCorrect actually pays (§5, BUILD_CONTRACT rule 6). Every case here
// FAILS with the old row-derived pools.

// poolElapsedMs is the guess time every scenario grades at: 10s past the
// (re-anchored) trackStart, i.e. well into the decay ramp.
const poolElapsedMs = int64(10000)

// lastStageTrackStart returns the most recent trackStart broadcast to the stage.
func lastStageTrackStart(t *testing.T, b *fakeBcast) protocol.TrackStartData {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := len(b.frames) - 1; i >= 0; i-- {
		if f := b.frames[i]; f.role == protocol.RoleStage && f.env.Type == protocol.SMsgTrackStart {
			var d protocol.TrackStartData
			if err := json.Unmarshal(f.env.Data, &d); err != nil {
				t.Fatalf("unmarshal trackStart: %v", err)
			}
			return d
		}
	}
	t.Fatal("no trackStart broadcast to stage")
	return protocol.TrackStartData{}
}

// lastTrackStartTo returns the most recent trackStart sent directly to a connID
// (the reconnect path).
func lastTrackStartTo(t *testing.T, b *fakeBcast, connID string) protocol.TrackStartData {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := len(b.frames) - 1; i >= 0; i-- {
		if f := b.frames[i]; f.connID == connID && f.env.Type == protocol.SMsgTrackStart {
			var d protocol.TrackStartData
			if err := json.Unmarshal(f.env.Data, &d); err != nil {
				t.Fatalf("unmarshal trackStart: %v", err)
			}
			return d
		}
	}
	t.Fatalf("no trackStart sent to %s", connID)
	return protocol.TrackStartData{}
}

// lastAdminView returns the most recent adminView broadcast.
func lastAdminView(t *testing.T, b *fakeBcast) protocol.AdminViewData {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := len(b.frames) - 1; i >= 0; i-- {
		if f := b.frames[i]; f.role == protocol.RoleAdmin && f.env.Type == protocol.SMsgAdminView {
			var d protocol.AdminViewData
			if err := json.Unmarshal(f.env.Data, &d); err != nil {
				t.Fatalf("unmarshal adminView: %v", err)
			}
			return d
		}
	}
	t.Fatal("no adminView broadcast")
	return protocol.AdminViewData{}
}

// buzzMobile makes connID buzz with a fresh nonce.
func (h *harness) buzzMobile(connID string) {
	h.e.OnMessage(connID, protocol.RoleMobile,
		protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
}

// gradePartialKind grades PARTIAL naming the field that was correct.
func (h *harness) gradePartialKind(adminConn, kind string) {
	d, _ := json.Marshal(protocol.AdminGradeData{Verdict: protocol.VerdictPartial, PartialKind: kind})
	h.e.OnMessage(adminConn, protocol.RoleAdmin,
		protocol.ClientEnvelope{Type: protocol.CMsgAdminGrade, Data: d, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
}

// TestPool_ProjectedMatchesAwarded drives the real engine through the partial
// pool reducer on row 5 (max 200, base 100) and asserts that the stage's last
// trackStart pool projects EXACTLY the points gradeCorrect then awards.
func TestPool_ProjectedMatchesAwarded(t *testing.T) {
	cases := []struct {
		name string
		// arrange runs everything before the final guesser buzzes. conns are
		// mobile connIDs c1..c3; the runner buzzes c3 and grades it CORRECT.
		arrange       func(t *testing.T, h *harness)
		wantMax       int
		wantBase      int
		wantProjected int
		wantAwarded   int
	}{
		{
			// s4-se-new-2: a partial, then any INCORRECT guess. gradeIncorrect
			// -> resumeAudio -> trackStartEnvelope used to hand the FULL row
			// pool back (200/100) -> projected 190 against an award of 140.
			name: "partial then incorrect (resumeAudio)",
			arrange: func(t *testing.T, h *harness) {
				h.buzzMobile("c1")
				h.gradePartialKind("admin", "artist")
				h.buzzMobile("c2")
				h.grade("admin", protocol.VerdictIncorrect)
			},
			wantMax: 150, wantBase: 50, wantProjected: 140, wantAwarded: 140,
		},
		{
			// A fully correct guess after a partial must pay the FULL
			// (unhalved) live pool: the one-field-first halve gate was
			// removed (it silently paid 50% of a fully correct guess,
			// contradicting the "fully correct earns all available points"
			// rule) and must not resurface here.
			name: "partial then correct",
			arrange: func(t *testing.T, h *harness) {
				h.buzzMobile("c1")
				h.gradePartialKind("admin", "artist")
			},
			wantMax: 150, wantBase: 50, wantProjected: 140, wantAwarded: 140,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			defer h.run()()
			h.joinAdmin("admin")
			h.joinStage("stage")
			h.join("c1", "fp1", "alice")
			h.join("c2", "fp2", "bob")
			p3 := h.join("c3", "fp3", "carol")
			h.selectCell("admin", 5, 1) // row 5: max 200, base 100
			h.sync(func() { h.e.trackStartMs = nowMs() })

			c.arrange(t, h)

			ts := lastStageTrackStart(t, h.bcast)
			if ts.MaxPoints != c.wantMax || ts.BasePoints != c.wantBase {
				t.Errorf("stage pool = {max:%d base:%d}, want {max:%d base:%d}",
					ts.MaxPoints, ts.BasePoints, c.wantMax, c.wantBase)
			}
			if ts.BasePoints > ts.MaxPoints {
				t.Errorf("inverted pool on the wire {max:%d base:%d}: web/shared/scoring.ts would decay upward from a negative bonus",
					ts.MaxPoints, ts.BasePoints)
			}
			// What the stage projects: same formula, mirrored in web/shared/scoring.ts.
			projected := currentPointsFromPool(ts.MaxPoints, ts.BasePoints, poolElapsedMs)
			if projected != c.wantProjected {
				t.Errorf("projected = %d, want %d", projected, c.wantProjected)
			}

			// Final guesser buzzes 10s into the (re-anchored) track, graded CORRECT.
			h.sync(func() { h.e.trackStartMs = nowMs() - poolElapsedMs })
			h.buzzMobile("c3")
			h.grade("admin", protocol.VerdictCorrect)
			awarded := h.score(p3)
			if awarded != c.wantAwarded {
				t.Errorf("awarded = %d, want %d (payout must not change)", awarded, c.wantAwarded)
			}
			if projected != awarded {
				t.Errorf("stage projected %d but engine awarded %d", projected, awarded)
			}
		})
	}
}

// TestPool_StageReconnectKeepsReducedPool: a stage/admin reconnecting mid-round
// gets trackStart via SendTo. Old code sent the row pool, so a reconnect handed
// the crowd back the points a partial had taken (200/100 -> projected 190).
func TestPool_StageReconnectKeepsReducedPool(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.joinStage("stage")
	h.join("c1", "fp1", "alice")
	p2 := h.join("c2", "fp2", "bob")
	h.selectCell("admin", 5, 1)
	h.sync(func() { h.e.trackStartMs = nowMs() })

	h.buzzMobile("c1")
	h.gradePartialKind("admin", "artist")

	// A second stage connects mid-round (the reconnect path).
	h.joinStage("stage2")
	ts := lastTrackStartTo(t, h.bcast, "stage2")
	if ts.MaxPoints != 150 || ts.BasePoints != 50 {
		t.Errorf("reconnect pool = {max:%d base:%d}, want {max:150 base:50}", ts.MaxPoints, ts.BasePoints)
	}
	projected := currentPointsFromPool(ts.MaxPoints, ts.BasePoints, poolElapsedMs)
	if projected != 140 {
		t.Errorf("reconnected stage projects %d, want 140", projected)
	}

	h.sync(func() { h.e.trackStartMs = nowMs() - poolElapsedMs })
	h.buzzMobile("c2")
	h.grade("admin", protocol.VerdictCorrect)
	if awarded := h.score(p2); awarded != projected {
		t.Errorf("reconnected stage projected %d but engine awarded %d", projected, awarded)
	}
}

// TestPool_AdminReadoutMatchesAward: s4-se-new-3. adminView.currentPoints is
// what the human grader reads before clicking CORRECT; it used to be
// CurrentPoints(row, pausedAt) = 190 while the grade paid 140.
func TestPool_AdminReadoutMatchesAward(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.joinStage("stage")
	h.join("c1", "fp1", "alice")
	p2 := h.join("c2", "fp2", "bob")
	h.selectCell("admin", 5, 1)
	h.sync(func() { h.e.trackStartMs = nowMs() })

	h.buzzMobile("c1")
	h.gradePartialKind("admin", "artist")

	// Next guesser buzzes 10s in; the admin readout is broadcast on that buzz.
	h.sync(func() { h.e.trackStartMs = nowMs() - poolElapsedMs })
	h.buzzMobile("c2")
	av := lastAdminView(t, h.bcast)
	if av.CurrentPoints != 140 {
		t.Errorf("admin currentPoints = %d, want 140", av.CurrentPoints)
	}
	h.grade("admin", protocol.VerdictCorrect)
	if awarded := h.score(p2); awarded != av.CurrentPoints {
		t.Errorf("admin showed %d but the grade paid %d", av.CurrentPoints, awarded)
	}
}

// TestPool_LivePoolNeverInverts: roundPool must never return base > max,
// because web/shared/scoring.ts computes bonus = max - base and would ramp UP
// from a negative bonus. Not reachable today (a post-partial remaining is
// always >= 50 = the post-partial base) — this pins the clamp so a future
// reducer cannot leak an inverted pool onto the wire.
func TestPool_LivePoolNeverInverts(t *testing.T) {
	cases := []struct {
		name    string
		row     int
		partial pendingPartial
		wantMax int
		wantBse int
	}{
		{"row 1 fresh", 1, pendingPartial{}, 100, 100},
		{"row 5 fresh", 5, pendingPartial{}, 200, 100},
		{"row 5 post-partial", 5, pendingPartial{active: true, remaining: 150}, 150, 50},
		{"drained pool clamps base", 5, pendingPartial{active: true, remaining: 0}, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := &Engine{curRow: c.row, partial: c.partial}
			maxP, baseP := e.roundPool()
			if maxP != c.wantMax || baseP != c.wantBse {
				t.Errorf("roundPool() = (%d, %d), want (%d, %d)", maxP, baseP, c.wantMax, c.wantBse)
			}
			if baseP > maxP {
				t.Errorf("inverted pool: base %d > max %d", baseP, maxP)
			}
		})
	}
}

// TestPool_AsymmetricRevealDoesNotHalve: s11-engine-delta-01. The one-field-
// first halve gate (pointFactor) was removed from revealTick because it
// silently paid half of a fully-correct guess, contradicting "fully correct
// earns all available points." This drives the exact asymmetric-reveal
// condition that used to trip it (artist fully revealed, song still mostly
// hidden) and pins that the pool, and the final payout, are unaffected.
func TestPool_AsymmetricRevealDoesNotHalve(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.joinStage("stage")
	p1 := h.join("c1", "fp1", "alice")
	h.selectCell("admin", 5, 1) // row 5: max 200, base 100
	h.sync(func() { h.e.trackStartMs = nowMs() })

	h.sync(func() {
		h.e.rc.artistRevealed = len(h.e.rc.artistOrder)
		h.e.revealTick(h.e.rc.roundKey)
	})

	// The lockout gate must NOT have fired: the song is still almost fully
	// hidden, so remainingHidden() is far above LockoutChars.
	h.sync(func() {
		if h.e.revealLocked {
			t.Fatalf("lockout gate fired on a one-sided reveal; song still mostly hidden")
		}
	})

	ts := lastStageTrackStart(t, h.bcast)
	if ts.MaxPoints != 200 || ts.BasePoints != 100 {
		t.Errorf("pool after one-sided reveal = {max:%d base:%d}, want {max:200 base:100} (no halving)",
			ts.MaxPoints, ts.BasePoints)
	}

	h.sync(func() { h.e.trackStartMs = nowMs() - poolElapsedMs })
	h.buzzMobile("c1")
	h.grade("admin", protocol.VerdictCorrect)
	wantAwarded := currentPointsFromPool(200, 100, poolElapsedMs)
	if awarded := h.score(p1); awarded != wantAwarded {
		t.Errorf("awarded = %d, want %d (full pool, no halving)", awarded, wantAwarded)
	}
}

// TestPool_LockoutGateStillFiresWithoutHalveGate: s11-engine-delta-01. The
// lockout gate sits in the same function as the deleted halve gate; confirm
// it still fires on its own, unrelated trigger condition (both fields nearly
// fully revealed) now that its neighbor is gone.
func TestPool_LockoutGateStillFiresWithoutHalveGate(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.joinStage("stage")
	h.join("c1", "fp1", "alice")
	h.selectCell("admin", 5, 1)
	h.sync(func() { h.e.trackStartMs = nowMs() })

	h.sync(func() {
		h.e.rc.artistRevealed = len(h.e.rc.artistOrder)
		h.e.rc.songRevealed = len(h.e.rc.songOrder)
		h.e.revealTick(h.e.rc.roundKey)
	})

	h.sync(func() {
		if !h.e.revealLocked {
			t.Fatalf("lockout gate did not fire with both fields fully revealed")
		}
	})

	ts := lastStageTrackStart(t, h.bcast)
	if ts.MaxPoints != 0 || ts.BasePoints != 0 {
		t.Errorf("post-lockout pool = {max:%d base:%d}, want {0,0}", ts.MaxPoints, ts.BasePoints)
	}
}
