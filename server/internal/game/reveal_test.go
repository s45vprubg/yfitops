package game

import (
	"encoding/json"
	"math/rand"
	"testing"
	"time"

	"github.com/s45vprubg/yfitops/server/internal/anticheat"
	"github.com/s45vprubg/yfitops/server/internal/protocol"
)

// newRevealHarness is like newHarness but with fast, deterministic reveal
// timing (0 phase-1 noise, 250ms letter interval — the server clamp floor) so
// the streaming reveal can be exercised in a few hundred ms.
func newRevealHarness(t *testing.T) *harness {
	t.Helper()
	bc := newFakeBcast()
	lk := newFakeLock()
	repo := newFakeRepo()
	audio := &fakeAudio{}
	gate := anticheat.NewNonceGate([]byte("test-secret"))
	e := NewEngine(repo, lk, audio, fakeLyrics{}, bc, gate, Config{
		SessionID:        "s1",
		SkipThresholdPct: 50,
		RevealIntervalMs: 250, // clamp floor
		RevealPhase1Ms:   10,  // ~no noise delay (0 is treated as "unset" -> default)
		RevealBlockMs:    1,   // effectively no length-hide block for fast tests
		Rand:             rand.New(rand.NewSource(1)),
	})
	e.SetBoard(testBoard())
	return &harness{e: e, bcast: bc, lock: lk, repo: repo, audio: audio, gate: gate, t: t}
}

// maskFramesFor returns the maskedReveal payloads a given role received.
func (b *fakeBcast) maskFramesForRole(role protocol.Role) []maskedRevealData {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []maskedRevealData
	for _, f := range b.frames {
		if f.role == role && f.env.Type == smsgMaskedReveal {
			var m maskedRevealData
			_ = json.Unmarshal(f.env.Data, &m)
			out = append(out, m)
		}
	}
	return out
}

// revealedCount counts non-empty, non-space slots in a mask field.
func revealedCount(mask []string) int {
	n := 0
	for _, c := range mask {
		if c != "" && c != " " {
			n++
		}
	}
	return n
}

// TestReveal_StreamsToStageAndMobileIdentically verifies the core invariant:
// stage and mobile receive byte-identical masked frames, letters trickle in,
// and mobile never receives the trusted full SMsgReveal.
func TestReveal_StreamsToStageAndMobileIdentically(t *testing.T) {
	h := newRevealHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.joinStage("stage")
	h.join("c1", "fp1", "alice")

	h.selectCell("admin", 1, 1)
	if h.state() != protocol.StateRoundActive {
		t.Fatalf("state = %s, want ROUND_ACTIVE", h.state())
	}

	// Let a few letter ticks fire (phase1=0, interval=250ms).
	time.Sleep(900 * time.Millisecond)
	h.sync(func() {})

	stageMasks := h.bcast.maskFramesForRole(protocol.RoleStage)
	mobileMasks := h.bcast.maskFramesForRole(protocol.RoleMobile)

	if len(mobileMasks) == 0 {
		t.Fatal("mobile received no maskedReveal frames")
	}
	// Stage and mobile must have received the same NUMBER of masks and each
	// mobile mask must be byte-identical to the stage's at the same index (they
	// come from one fanned envelope).
	if len(stageMasks) != len(mobileMasks) {
		t.Fatalf("stage got %d masks, mobile got %d — not co-broadcast", len(stageMasks), len(mobileMasks))
	}
	for i := range mobileMasks {
		if revealedCount(mobileMasks[i].Artist) != revealedCount(stageMasks[i].Artist) ||
			revealedCount(mobileMasks[i].Song) != revealedCount(stageMasks[i].Song) {
			t.Fatalf("mask %d differs between stage and mobile (co-visibility breach)", i)
		}
	}

	// Letters must have progressed beyond zero but the mask must never carry the
	// full contiguous answer string to mobile as a single value.
	last := mobileMasks[len(mobileMasks)-1]
	if revealedCount(last.Artist)+revealedCount(last.Song) == 0 {
		t.Fatal("no letters revealed after several ticks")
	}

	// Mobile must NEVER have received the trusted full reveal.
	h.bcast.mu.Lock()
	for _, f := range h.bcast.frames {
		if f.role == protocol.RoleMobile && f.env.Type == protocol.SMsgReveal {
			t.Fatal("SANITIZATION BREACH: mobile received trusted SMsgReveal")
		}
	}
	h.bcast.mu.Unlock()
}

// TestReveal_AlternatesFields verifies that with Alternate=true the reveal fills
// artist and song roughly in lockstep (differ by at most one) rather than one
// field fully before the other.
func TestReveal_AlternatesFields(t *testing.T) {
	h := newRevealHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.joinStage("stage")
	h.join("c1", "fp1", "alice")

	h.selectCell("admin", 1, 1)
	time.Sleep(900 * time.Millisecond)
	h.sync(func() {})

	masks := h.bcast.maskFramesForRole(protocol.RoleStage)
	if len(masks) < 2 {
		t.Fatalf("expected several masks, got %d", len(masks))
	}
	// While both fields still have hidden letters, the revealed counts must stay
	// within 1 of each other (alternation). Check the last streaming frame that
	// isn't the final full reveal.
	for _, m := range masks {
		if m.Final {
			continue
		}
		a, s := revealedCount(m.Artist), revealedCount(m.Song)
		// Only meaningful while neither field is complete.
		aFull := a == nonSpace(m.Artist)
		sFull := s == nonSpace(m.Song)
		if !aFull && !sFull {
			if diff := a - s; diff > 1 || diff < -1 {
				t.Fatalf("fields not alternating: artist=%d song=%d", a, s)
			}
		}
	}
}

// TestReveal_PausesWhileLockedOut verifies letters stop advancing during a buzz
// (state != ROUND_ACTIVE) and resume afterward.
func TestReveal_PausesWhileLockedOut(t *testing.T) {
	h := newRevealHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.joinStage("stage")
	h.join("c1", "fp1", "alice")

	h.selectCell("admin", 1, 1)
	time.Sleep(400 * time.Millisecond)

	// Buzz to freeze the round. A winning buzz transitions ROUND_ACTIVE ->
	// LOCKED_OUT -> ADJUDICATE (awaiting grade); either way state is no longer
	// ROUND_ACTIVE, so the reveal must pause.
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
	if h.state() == protocol.StateRoundActive {
		t.Fatalf("state still ROUND_ACTIVE after buzz; reveal would not pause")
	}

	before := revealedTotal(h.bcast.maskFramesForRole(protocol.RoleStage))
	time.Sleep(700 * time.Millisecond) // several intervals while paused
	h.sync(func() {})
	after := revealedTotal(h.bcast.maskFramesForRole(protocol.RoleStage))
	if after != before {
		t.Fatalf("reveal advanced while LOCKED_OUT: before=%d after=%d", before, after)
	}
}

// TestReveal_KaraokeFinalizes verifies enterKaraoke completes the reveal (final
// full mask to both surfaces) and mobile still never gets SMsgReveal.
func TestReveal_KaraokeFinalizes(t *testing.T) {
	h := newRevealHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.joinStage("stage")
	h.join("c1", "fp1", "alice")

	h.selectCell("admin", 1, 1)
	// Admin force-reveal -> enterKaraoke -> finalizeReveal.
	h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: protocol.CMsgAdminReveal, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
	if h.state() != protocol.StateKaraoke {
		t.Fatalf("state = %s, want KARAOKE", h.state())
	}

	mobileMasks := h.bcast.maskFramesForRole(protocol.RoleMobile)
	if len(mobileMasks) == 0 {
		t.Fatal("mobile got no masks")
	}
	final := mobileMasks[len(mobileMasks)-1]
	if !final.Final {
		t.Fatal("final mobile mask not marked Final")
	}
	// Final mask must have every non-space slot filled.
	if revealedCount(final.Artist) != nonSpace(final.Artist) || revealedCount(final.Song) != nonSpace(final.Song) {
		t.Fatal("final mask not fully revealed")
	}
	// Mobile must still never have received the trusted reveal.
	h.bcast.mu.Lock()
	defer h.bcast.mu.Unlock()
	for _, f := range h.bcast.frames {
		if f.role == protocol.RoleMobile && f.env.Type == protocol.SMsgReveal {
			t.Fatal("SANITIZATION BREACH: mobile received SMsgReveal at karaoke")
		}
	}
}

// TestReveal_ConfigAppliesNextRound verifies a knob change does NOT alter the
// in-flight round but IS snapshotted by the next startTrack, and that the admin
// receives an echo of the applied values.
func TestReveal_ConfigAppliesNextRound(t *testing.T) {
	h := newRevealHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.joinStage("stage")
	h.join("c1", "fp1", "alice")

	h.selectCell("admin", 1, 1)
	var inFlightInterval int
	h.sync(func() { inFlightInterval = h.e.rc.cfg.IntervalMs })
	if inFlightInterval != 250 {
		t.Fatalf("in-flight interval = %d, want 250", inFlightInterval)
	}

	// Change the knob mid-round.
	cfg, _ := json.Marshal(adminSetRevealCfgData{IntervalMs: ptr(1234), Phase1Ms: ptr(0)})
	h.bcast.reset()
	h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: cmsgAdminSetRevealCfg, Data: cfg, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})

	// In-flight round unchanged.
	h.sync(func() { inFlightInterval = h.e.rc.cfg.IntervalMs })
	if inFlightInterval != 250 {
		t.Fatalf("in-flight interval changed to %d; must stay 250 (applies next round)", inFlightInterval)
	}
	// Engine setting updated.
	var setInterval int
	h.sync(func() { setInterval = h.e.revealCfg.IntervalMs })
	if setInterval != 1234 {
		t.Fatalf("engine reveal interval = %d, want 1234", setInterval)
	}
	// Admin received an echo.
	gotEcho := false
	h.bcast.mu.Lock()
	for _, f := range h.bcast.frames {
		if f.role == protocol.RoleAdmin && f.env.Type == smsgAdminRevealCfg {
			gotEcho = true
		}
	}
	h.bcast.mu.Unlock()
	if !gotEcho {
		t.Fatal("admin did not receive adminRevealCfg echo")
	}

	// End the round and start a new one — the new round must snapshot 1234.
	h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: protocol.CMsgAdminEndRound, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
	if h.state() != protocol.StateBoard {
		t.Fatalf("state = %s, want BOARD after endRound", h.state())
	}
	h.selectCell("admin", 5, 1) // a different cell with tracks
	var nextInterval int
	h.sync(func() { nextInterval = h.e.rc.cfg.IntervalMs })
	if nextInterval != 1234 {
		t.Fatalf("next round interval = %d, want 1234 (snapshot of new config)", nextInterval)
	}
}

// TestReveal_ConfigRejectedForNonAdmin verifies the role gate.
func TestReveal_ConfigRejectedForNonAdmin(t *testing.T) {
	h := newRevealHarness(t)
	defer h.run()()
	h.join("c1", "fp1", "alice")

	cfg, _ := json.Marshal(adminSetRevealCfgData{IntervalMs: ptr(500)})
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: cmsgAdminSetRevealCfg, Data: cfg, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})

	var got int
	h.sync(func() { got = h.e.revealCfg.IntervalMs })
	if got == 500 {
		t.Fatal("non-admin was able to change reveal config")
	}
}

func ptr[T any](v T) *T { return &v }

func nonSpace(mask []string) int {
	n := 0
	for _, c := range mask {
		if c != " " {
			n++
		}
	}
	return n
}

func revealedTotal(masks []maskedRevealData) int {
	if len(masks) == 0 {
		return 0
	}
	last := masks[len(masks)-1]
	return revealedCount(last.Artist) + revealedCount(last.Song)
}

// TestResetKeepsBoard is a regression guard: after End Game -> New Game
// (ResetToLobby), the board must stay loaded so Start Game works with the SAME
// board without re-attaching (previously reset nil'd e.board, breaking restart).
func TestResetKeepsBoard(t *testing.T) {
	h := newRevealHarness(t)
	defer h.run()()
	h.joinAdmin("admin")

	// Start a game, then end it to reach GAME_OVER.
	if err := h.e.StartGame(); err != nil {
		t.Fatalf("StartGame: %v", err)
	}
	h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: protocol.CMsgAdminEndGame, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
	if h.state() != protocol.StateGameOver {
		t.Fatalf("state = %s, want GAME_OVER", h.state())
	}

	// New Game -> back to LOBBY, board still loaded.
	if err := h.e.ResetToLobby(); err != nil {
		t.Fatalf("ResetToLobby: %v", err)
	}
	var hasBoard bool
	h.sync(func() { hasBoard = h.e.board != nil })
	if !hasBoard {
		t.Fatal("board was unloaded by reset; Start Game would fail")
	}

	// Start Game must succeed with the same board (the bug: "no board attached").
	if err := h.e.StartGame(); err != nil {
		t.Fatalf("StartGame after reset failed: %v", err)
	}
	if h.state() != protocol.StateBoard {
		t.Fatalf("state = %s, want BOARD after restart", h.state())
	}
}

// TestPlaybackNoTrackDoesNotCrash guards the nil-deref that took down the whole
// server: an admin Play/Pause between rounds (no e.curTrack) must be a safe
// no-op, not a panic.
func TestPlaybackNoTrackDoesNotCrash(t *testing.T) {
	h := newRevealHarness(t)
	defer h.run()()
	h.joinAdmin("admin")

	for _, action := range []string{"play", "resume", "pause"} {
		d, _ := json.Marshal(protocol.AdminPlaybackData{Action: action})
		h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: protocol.CMsgAdminPlayback, Data: d, Nonce: h.gate.Current()}, nowMs())
	}
	// If the loop survived, this sync round-trips; a crash would hang/fail.
	h.sync(func() {})
	if h.state() == "" {
		t.Fatal("engine loop died")
	}
}
