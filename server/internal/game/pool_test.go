package game

import (
	"encoding/json"
	"testing"

	"github.com/s45vprubg/yfitops/server/internal/protocol"
)

// pool_test.go pins the QA-sweep4 fix for s4-se-1 / s4-se-new-1 / s4-se-new-2 /
// s4-se-new-3: the pool we put on the wire (or show the admin) must be the LIVE
// pool — partial-aware and pointFactor-aware — so the number the stage projects
// at 60fps is the number gradeCorrect actually pays (§5, BUILD_CONTRACT rule 6).
// Every case here FAILS with the old row-derived pools (95/140/190 projected
// against 70/70/140 awarded).

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

// forceHalveGate fully reveals the artist and ticks once, which trips the
// one-field-first gate (revealTick -> halvePoints, pointFactor 0.5).
func (h *harness) forceHalveGate(t *testing.T) {
	t.Helper()
	h.sync(func() {
		h.e.rc.artistRevealed = len(h.e.rc.artistOrder)
		h.e.revealTick(h.e.rc.roundKey)
		if !h.e.rc.fieldHalved || h.e.pointFactor != 0.5 {
			t.Fatalf("halve gate did not fire: fieldHalved=%v pointFactor=%v", h.e.rc.fieldHalved, h.e.pointFactor)
		}
	})
}

// TestPool_ProjectedMatchesAwarded drives the real engine through each ordering
// of the two pool reducers (partial, one-field-first halve) on row 5 (max 200,
// base 100) and asserts that the stage's last trackStart pool projects EXACTLY
// the points gradeCorrect then awards.
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
			// s4-se-1: partial takes 50 (pool 150/50), then the halve gate
			// fires. Old code broadcast the ROW halved (100/50) -> projected 95.
			name: "partial then halve",
			arrange: func(t *testing.T, h *harness) {
				h.buzzMobile("c1")
				h.gradePartialKind("admin", "artist")
				h.forceHalveGate(t)
			},
			wantMax: 75, wantBase: 25, wantProjected: 70, wantAwarded: 70,
		},
		{
			// s4-se-new-1(i): halve gate first, then a partial. Old code
			// broadcast the UN-halved post-partial pool (150/50) -> projected
			// 140, and the displayed pool visibly jumped UP after the partial.
			name: "halve then partial",
			arrange: func(t *testing.T, h *harness) {
				h.forceHalveGate(t)
				if ts := lastStageTrackStart(t, h.bcast); ts.MaxPoints != 100 || ts.BasePoints != 50 {
					t.Fatalf("halved row pool = {%d,%d}, want {100,50}", ts.MaxPoints, ts.BasePoints)
				}
				h.buzzMobile("c1")
				h.gradePartialKind("admin", "song")
			},
			wantMax: 75, wantBase: 25, wantProjected: 70, wantAwarded: 70,
		},
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

// TestPool_LivePoolNeverInverts: livePool must never return base > max, because
// web/shared/scoring.ts computes bonus = max - base and would ramp UP from a
// negative bonus. Not reachable today (a post-partial remaining is >= 50 = the
// post-partial base, and both ends scale by the same factor) — this pins the
// clamp so a future reducer cannot leak an inverted pool onto the wire.
func TestPool_LivePoolNeverInverts(t *testing.T) {
	cases := []struct {
		name    string
		row     int
		partial pendingPartial
		factor  float64
		wantMax int
		wantBse int
	}{
		{"row 1 fresh", 1, pendingPartial{}, 1.0, 100, 100},
		{"row 5 fresh", 5, pendingPartial{}, 1.0, 200, 100},
		{"row 5 halved", 5, pendingPartial{}, 0.5, 100, 50},
		{"row 5 post-partial", 5, pendingPartial{active: true, remaining: 150}, 1.0, 150, 50},
		{"row 5 post-partial halved", 5, pendingPartial{active: true, remaining: 150}, 0.5, 75, 25},
		{"drained pool clamps base", 5, pendingPartial{active: true, remaining: 0}, 1.0, 0, 0},
		{"drained pool clamps base, halved", 5, pendingPartial{active: true, remaining: 10}, 0.5, 5, 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := &Engine{curRow: c.row, partial: c.partial, pointFactor: c.factor}
			maxP, baseP := e.livePool()
			if maxP != c.wantMax || baseP != c.wantBse {
				t.Errorf("livePool() = (%d, %d), want (%d, %d)", maxP, baseP, c.wantMax, c.wantBse)
			}
			if baseP > maxP {
				t.Errorf("inverted pool: base %d > max %d", baseP, maxP)
			}
		})
	}
}
