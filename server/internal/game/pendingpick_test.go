package game

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/s45vprubg/yfitops/server/internal/protocol"
)

// pendingpick_test.go covers two additions from the same change:
//   - smsgPendingPick (CONTRACT-QUESTION, engine.go): stage/admin learn which
//     cell the picker has queued for the next round, and learn again when it
//     clears at the exact moment the next round starts (beginTransition).
//   - revealCfg.TransitionDelayMs (reveal.go/engine.go): the "Next track in
//     N" countdown is now a live admin knob instead of the old package-level
//     TransitionDelay var.

// pendingPicksTo returns, in order, every pendingPickData broadcast to role.
func pendingPicksTo(t *testing.T, b *fakeBcast, role protocol.Role) []pendingPickData {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []pendingPickData
	for _, f := range b.frames {
		if f.role == role && f.env.Type == smsgPendingPick {
			var d pendingPickData
			if err := json.Unmarshal(f.env.Data, &d); err != nil {
				t.Fatalf("unmarshal pendingPick: %v", err)
			}
			out = append(out, d)
		}
	}
	return out
}

// TestPendingPick_BroadcastOnSetAndClear verifies both halves of the
// contract: a real {row,col} the instant the picker's choice registers
// (even before the OTHER AND-gate half has fired), and a cleared zero value
// the instant beginTransition's countdown continuation actually starts the
// next round — to BOTH stage and admin, never mobile.
func TestPendingPick_BroadcastOnSetAndClear(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	withFastTransition(t, h, 20)
	h.joinAdmin("admin")
	h.join("c1", "fp1", "a")

	h.selectCell("admin", 1, 1)
	n := h.gate.Current()
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: n}, nowMs())
	h.sync(func() {})
	h.grade("admin", protocol.VerdictCorrect)
	if h.state() != protocol.StateKaraoke {
		t.Fatalf("state = %s, want KARAOKE", h.state())
	}

	// Pick registers BEFORE the vote AND-gate half fires — must still
	// broadcast the real cell immediately (s8uim-style: don't wait for the
	// transition itself to tell stage/admin what's queued).
	h.playerSelectCell("c1", 5, 2)
	if h.state() != protocol.StateKaraoke {
		t.Fatalf("state = %s, want still KARAOKE (vote hasn't passed yet)", h.state())
	}
	for _, role := range []protocol.Role{protocol.RoleStage, protocol.RoleAdmin} {
		picks := pendingPicksTo(t, h.bcast, role)
		if len(picks) == 0 {
			t.Fatalf("%s got no pendingPick broadcast after the picker chose", role)
		}
		got := picks[len(picks)-1]
		if got.Row != 5 || got.Col != 2 {
			t.Errorf("%s last pendingPick = %+v, want {Row:5 Col:2}", role, got)
		}
	}
	// Mobile must never receive this frame — it's stage/admin only.
	for _, f := range h.bcast.mobileFrames() {
		if f.env.Type == smsgPendingPick {
			t.Fatal("SANITIZATION: mobile received smsgPendingPick")
		}
	}

	// Satisfy the other AND-gate half — this fires beginTransition directly
	// since pendingPick is already set.
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgVote, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
	if h.state() != protocol.StateTransition {
		t.Fatalf("state = %s, want TRANSITION", h.state())
	}

	// Wait out the (shrunk) countdown; the continuation clears pendingPick
	// and must broadcast the zero value to stage/admin at that exact point.
	if !waitUntil(t, time.Second, func() bool { return h.state() == protocol.StateRoundActive }) {
		t.Fatalf("state = %s after waiting out the transition, want ROUND_ACTIVE (picked cell should start)", h.state())
	}
	for _, role := range []protocol.Role{protocol.RoleStage, protocol.RoleAdmin} {
		picks := pendingPicksTo(t, h.bcast, role)
		got := picks[len(picks)-1]
		if got.Row != 0 || got.Col != 0 {
			t.Errorf("%s last pendingPick after transition = %+v, want cleared {Row:0 Col:0}", role, got)
		}
		if len(picks) < 2 {
			t.Errorf("%s only saw %d pendingPick broadcast(s), want at least 2 (set then cleared)", role, len(picks))
		}
	}
}

// TestRevealCfg_TransitionDelayMsRoundTrip verifies the admin can read and
// write the new knob through the same round-trip every other reveal-timing
// knob uses (onAdminSetRevealCfg -> revealCfgData echo), that it clamps, and
// that it actually changes beginTransition's real delay (not just the
// stored struct field) — proven by driving an actual transition through a
// shrunk value and observing it land well inside the OLD 3s default.
func TestRevealCfg_TransitionDelayMsRoundTrip(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")

	// Round-trip: set -> echoed back via revealCfgData.
	cfg, _ := json.Marshal(adminSetRevealCfgData{TransitionDelayMs: ptr(1500)})
	h.bcast.reset()
	h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: cmsgAdminSetRevealCfg, Data: cfg, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
	var got int
	h.sync(func() { got = h.e.revealCfgData().TransitionDelayMs })
	if got != 1500 {
		t.Fatalf("revealCfgData().TransitionDelayMs = %d, want 1500", got)
	}
	gotEcho := false
	h.bcast.mu.Lock()
	for _, f := range h.bcast.frames {
		if f.role == protocol.RoleAdmin && f.env.Type == smsgAdminRevealCfg {
			var d adminRevealCfgData
			if err := json.Unmarshal(f.env.Data, &d); err == nil && d.TransitionDelayMs == 1500 {
				gotEcho = true
			}
		}
	}
	h.bcast.mu.Unlock()
	if !gotEcho {
		t.Fatal("admin did not receive adminRevealCfg echo with TransitionDelayMs=1500")
	}

	// Clamp: below min and above max both snap into [1000, 10000].
	cfgLow, _ := json.Marshal(adminSetRevealCfgData{TransitionDelayMs: ptr(1)})
	h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: cmsgAdminSetRevealCfg, Data: cfgLow, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
	h.sync(func() { got = h.e.revealCfg.TransitionDelayMs })
	if got != 1000 {
		t.Fatalf("TransitionDelayMs clamp-low = %d, want 1000", got)
	}
	cfgHigh, _ := json.Marshal(adminSetRevealCfgData{TransitionDelayMs: ptr(999999)})
	h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: cmsgAdminSetRevealCfg, Data: cfgHigh, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
	h.sync(func() { got = h.e.revealCfg.TransitionDelayMs })
	if got != 10000 {
		t.Fatalf("TransitionDelayMs clamp-high = %d, want 10000", got)
	}

	// Non-vacuous: drive an actual transition with the knob shrunk well
	// below the old hardcoded 3s default, and confirm it lands fast. If
	// beginTransition still read the old package var/hardcoded 3s, this
	// would time out.
	withFastTransition(t, h, 15)
	h.join("c1", "fp1", "a")
	h.selectCell("admin", 1, 1)
	n := h.gate.Current()
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: n}, nowMs())
	h.sync(func() {})
	h.grade("admin", protocol.VerdictCorrect)
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgVote, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
	h.playerSelectCell("c1", 5, 2)
	if h.state() != protocol.StateTransition {
		t.Fatalf("state = %s, want TRANSITION", h.state())
	}
	if !waitUntil(t, 200*time.Millisecond, func() bool { return h.state() != protocol.StateTransition }) {
		t.Fatal("transition did not clear within 200ms of a 15ms configured delay — beginTransition is not reading revealCfg.TransitionDelayMs")
	}
}
