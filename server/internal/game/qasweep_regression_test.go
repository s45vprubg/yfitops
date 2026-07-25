package game

import (
	"encoding/json"
	"testing"

	"github.com/s45vprubg/yfitops/server/internal/protocol"
)

// qasweep_regression_test.go pins the QA-sweep fixes for engine-1 (stage.* authz
// gate) and engine-3 (daily-double completion on rater departure). Both tests
// are written to FAIL if the fix is reverted.

// errorFramesTo returns the ErrorData codes sent directly to a connID.
func (h *harness) errorCodesTo(connID string) []string {
	var out []string
	h.bcast.mu.Lock()
	defer h.bcast.mu.Unlock()
	for _, f := range h.bcast.frames {
		if f.connID == connID && f.env.Type == protocol.SMsgError {
			var d protocol.ErrorData
			_ = json.Unmarshal(f.env.Data, &d)
			out = append(out, d.Code)
		}
	}
	return out
}

// engine-1: a hostile mobile conn must NOT be able to drive stage.* actions.
// It should receive a "forbidden" error and the action must not run; a real
// stage conn sending the same frame must be accepted (no forbidden error).
func TestQARegression_StageMessagesRequireStageRole(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinStage("stage")
	h.join("m1", "fp1", "mallory")

	// Hostile mobile tries to hijack the Spotify playback device.
	dr, _ := json.Marshal(protocol.StageDeviceReadyData{SpotifyDeviceID: "attacker-device"})
	h.e.OnMessage("m1", protocol.RoleMobile,
		protocol.ClientEnvelope{Type: protocol.CMsgStageDeviceReady, Data: dr, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})

	if got := h.errorCodesTo("m1"); len(got) == 0 || got[len(got)-1] != "forbidden" {
		t.Fatalf("mobile stage.deviceReady: want a 'forbidden' error, got codes %v", got)
	}

	// A hostile mobile trying to force a round transition via trackEnded.
	ps, _ := json.Marshal(protocol.StagePlayerStateData{TrackEnded: true})
	h.e.OnMessage("m1", protocol.RoleMobile,
		protocol.ClientEnvelope{Type: protocol.CMsgStagePlayerState, Data: ps, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
	if got := h.errorCodesTo("m1"); got[len(got)-1] != "forbidden" {
		t.Fatalf("mobile stage.playerState: want 'forbidden', got codes %v", got)
	}

	// The legitimate stage conn must be accepted (no forbidden error to it).
	h.e.OnMessage("stage", protocol.RoleStage,
		protocol.ClientEnvelope{Type: protocol.CMsgStageDeviceReady, Data: dr, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
	for _, code := range h.errorCodesTo("stage") {
		if code == "forbidden" {
			t.Fatalf("stage conn was wrongly rejected with 'forbidden'")
		}
	}
}

// engine-3: once the daily double is entered, a rater disconnecting must not
// deadlock the phase. With a two-person rating pool, one rating + the other
// rater disconnecting should complete the daily double (advance to KARAOKE),
// not hang in DAILY_DOUBLE forever.
func TestQARegression_DailyDoubleCompletesWhenRaterLeaves(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.joinStage("stage")
	performer := h.join("perf", "fpp", "performer")
	h.join("r1", "fpr1", "rater1")
	h.join("r2", "fpr2", "rater2")
	_ = performer

	// Select the daily-double cell (1,2 in testBoard), performer buzzes and is
	// graded correct -> enterDailyDouble(performer). Pool = {r1, r2}.
	h.selectCell("admin", 1, 2)
	if h.state() != protocol.StateRoundActive {
		t.Fatalf("state = %s, want ROUND_ACTIVE", h.state())
	}
	h.e.OnMessage("perf", protocol.RoleMobile,
		protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
	h.grade("admin", protocol.VerdictCorrect)

	if h.state() != protocol.StateDailyDouble {
		t.Fatalf("state = %s, want DAILY_DOUBLE after correct grade on DD cell", h.state())
	}

	// One rater rates; the other disconnects. Completion must fire.
	rate, _ := json.Marshal(protocol.RateData{Stars: 4})
	h.e.OnMessage("r1", protocol.RoleMobile,
		protocol.ClientEnvelope{Type: protocol.CMsgRate, Data: rate, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
	h.e.OnDisconnect("r2")
	h.sync(func() {})

	if got := h.state(); got == protocol.StateDailyDouble {
		t.Fatalf("daily double deadlocked: still DAILY_DOUBLE after all remaining raters resolved")
	}
}

// TestQARegression_SanitizeHandle pins the handle-sanitizer contract across
// sweeps: bidi/zero-width format runes are stripped (s2-engine-1) so a hostile
// handle can't scramble the scoreboard render, BUT U+200D ZERO WIDTH JOINER is
// preserved (s3-sanitize) so legitimate emoji ZWJ sequences survive. Fails if
// either half regresses.
func TestQARegression_SanitizeHandle(t *testing.T) {
	const zwj = "‍"
	const rtlOverride = "‮"
	const zeroWidthSpace = "​"

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain ascii", "alice", "alice"},
		{"accented", "José", "José"},
		{"cjk", "日本語", "日本語"},
		{"strips RTL override (bidi injection)", "gnp" + rtlOverride, "gnp"},
		{"strips zero-width space", "a" + zeroWidthSpace + "b", "ab"},
		// A ZWJ emoji sequence (man+ZWJ+laptop) must survive intact.
		{"preserves emoji ZWJ sequence", "👨" + zwj + "💻", "👨" + zwj + "💻"},
		{"empty after strip -> empty", rtlOverride + zeroWidthSpace, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeHandle(tc.in); got != tc.want {
				t.Fatalf("sanitizeHandle(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	// 24-rune cap holds.
	long := ""
	for i := 0; i < 50; i++ {
		long += "x"
	}
	if got := sanitizeHandle(long); len([]rune(got)) != 24 {
		t.Fatalf("cap: got %d runes, want 24", len([]rune(got)))
	}
}
