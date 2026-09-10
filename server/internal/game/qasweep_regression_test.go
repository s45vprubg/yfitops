package game

import (
	"encoding/json"
	"fmt"
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
// rater disconnecting should complete the daily double (advance to BOARD),
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

	// 1st pick on the daily-double cell (1,2 in testBoard) is a normal round;
	// the performer buzzes/graded correct so they become lastScorer.
	h.selectCell("admin", 1, 2)
	if h.state() != protocol.StateRoundActive {
		t.Fatalf("state = %s, want ROUND_ACTIVE", h.state())
	}
	h.e.OnMessage("perf", protocol.RoleMobile,
		protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
	h.grade("admin", protocol.VerdictCorrect)
	h.sync(func() {
		h.e.curTrack = nil
		h.e.curCell = nil
		h.e.state = protocol.StateBoard
	})

	// 2nd pick triggers the offer; accept + choose starts the performance so
	// the rating pool ({r1, r2}) actually exists.
	h.selectCell("admin", 1, 2)
	if h.state() != protocol.StateDailyDouble {
		t.Fatalf("state = %s, want DAILY_DOUBLE after 2nd pick", h.state())
	}
	h.ddDecide("perf", true)
	choices := h.lastDailyDoubleOfferTrackIDs("perf")
	if len(choices) == 0 {
		t.Fatalf("performer received no song choices after accepting")
	}
	h.ddChoose("perf", choices[0])
	if h.state() != protocol.StateDailyDouble {
		t.Fatalf("state = %s, want DAILY_DOUBLE once performing", h.state())
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

// ---------------------------------------------------------------------------
// QA sweep 6 (dd-eng-1..6): the redesigned Daily Double's engine.go / model.go
// bugs. All six tests below are written to FAIL if their respective fix is
// reverted.
// ---------------------------------------------------------------------------

// dd-eng-1: startDailyDoublePerformance() must mint a FRESH e.roundKey,
// mirroring startTrack's own pattern. Before the fix, e.roundKey was never
// touched by startDailyDoublePerformance, so it stayed pinned at whatever
// value the PRIOR normal round's startTrack() set — meaning
// fetchAndSendLyrics's staleness guard (`if e.roundKey != rk { return }`,
// captured from that prior round) would incorrectly evaluate false during the
// live Daily Double performance, letting a delayed prior-round lyric fetch
// broadcast its lines over the DD performance's own (already-sent, correct)
// lyrics.
func TestQARegression_DailyDoubleFreshRoundKey(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	performer := h.join("c1", "fp1", "perf")

	// 1st pick: a normal round on the DD cell (1,2) so a real e.roundKey gets
	// minted by startTrack.
	h.selectCell("admin", 1, 2)
	var roundKeyAfterNormalRound string
	h.sync(func() { roundKeyAfterNormalRound = h.e.roundKey })
	if roundKeyAfterNormalRound == "" {
		t.Fatalf("startTrack did not mint a roundKey")
	}
	n := h.gate.Current()
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: n}, nowMs())
	h.sync(func() {})
	h.grade("admin", protocol.VerdictCorrect)
	h.sync(func() {
		h.e.curTrack = nil
		h.e.curCell = nil
		h.e.state = protocol.StateBoard
	})

	// 2nd pick triggers the offer; accept + choose starts the DD performance.
	h.selectCell("admin", 1, 2)
	if h.state() != protocol.StateDailyDouble {
		t.Fatalf("state = %s, want DAILY_DOUBLE", h.state())
	}
	h.ddDecide("c1", true)
	choices := h.lastDailyDoubleOfferTrackIDs("c1")
	if len(choices) == 0 {
		t.Fatalf("performer %q received no song choices after accepting", performer)
	}
	h.ddChoose("c1", choices[0])

	var roundKeyDuringDD string
	h.sync(func() { roundKeyDuringDD = h.e.roundKey })
	if roundKeyDuringDD == "" {
		t.Fatalf("startDailyDoublePerformance did not mint a roundKey at all")
	}
	if roundKeyDuringDD == roundKeyAfterNormalRound {
		t.Fatalf("e.roundKey unchanged across the Daily Double boundary (%q); a still-in-flight prior-round lyric fetch would pass the staleness guard and leak its lyrics into this live DD performance", roundKeyDuringDD)
	}
}

// dd-eng-2: accepting a Daily Double offer must be idempotent. Accepting does
// NOT call transitionTo (state stays DAILY_DOUBLE), so the nonce gate is never
// bumped between accept and the eventual choose — the client's nonce stays
// Validate()-true for a repeat send. Before the fix, each accept call drew and
// permanently removed ANOTHER batch of up to 5 tracks from the no-replacement
// DDBucket and silently discarded the previously-drawn (never-shown)
// candidates. A handful of retries could drain the whole curated bucket
// before the contestant ever saw a real offer.
func TestQARegression_DailyDoubleAcceptIsIdempotent(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	performer := h.join("c1", "fp1", "perf")
	_ = performer

	// Swap in a bigger DDBucket (7 tracks) so a second draw would be
	// observable as a further shrink beyond the first batch of 5.
	bucket := make([]*Track, 7)
	for i := range bucket {
		bucket[i] = &Track{ID: fmt.Sprintf("bucket-%d", i), Playable: true}
	}
	h.sync(func() { h.e.board.DDBucket = bucket })

	h.selectCell("admin", 1, 2) // 1st visit: normal round -> perf becomes lastScorer
	n := h.gate.Current()
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: n}, nowMs())
	h.sync(func() {})
	h.grade("admin", protocol.VerdictCorrect)
	h.sync(func() {
		h.e.curTrack = nil
		h.e.curCell = nil
		h.e.state = protocol.StateBoard
	})

	h.selectCell("admin", 1, 2) // 2nd visit: triggers the offer
	if h.state() != protocol.StateDailyDouble {
		t.Fatalf("state = %s, want DAILY_DOUBLE", h.state())
	}

	h.ddDecide("c1", true)
	first := h.lastDailyDoubleOfferTrackIDs("c1")
	if len(first) == 0 {
		t.Fatalf("first accept produced no candidates")
	}
	var bucketAfterFirst int
	h.sync(func() { bucketAfterFirst = len(h.e.board.DDBucket) })

	// Replay the identical accept decision (double-tap / client retry after a
	// dropped ack / hostile replay — the nonce is still valid).
	h.ddDecide("c1", true)
	second := h.lastDailyDoubleOfferTrackIDs("c1")
	var bucketAfterSecond int
	h.sync(func() { bucketAfterSecond = len(h.e.board.DDBucket) })

	if bucketAfterSecond != bucketAfterFirst {
		t.Fatalf("second accept drew ANOTHER batch from DDBucket: bucket size went %d -> %d; a repeat accept must not draw again", bucketAfterFirst, bucketAfterSecond)
	}
	if len(second) != len(first) {
		t.Fatalf("second accept sent a DIFFERENT batch size (%d) than the first (%d)", len(second), len(first))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("second accept sent DIFFERENT candidates than the first: got %v, want %v", second, first)
		}
	}
}

// dd-eng-3: beginDailyDoubleOffer's lastScorer fast path must reject a banned
// contestant, mirroring the fallback path's explicit !p.Banned filter.
// registry.online() is purely connection-count based and unrelated to Banned;
// onAdminKick sets Banned=true but never force-disconnects the transport, so
// a banned player who ignores the kick error frame stays online()==true.
// Before the fix, a banned lastScorer would be selected as the Daily Double
// contestant, bypassing the ban entirely.
func TestQARegression_DailyDoubleBannedLastScorerExcluded(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	performer := h.join("c1", "fp1", "perf")
	bystander := h.join("c2", "fp2", "bystander")

	// 1st pick: perf wins a normal round on the DD cell -> becomes lastScorer.
	h.selectCell("admin", 1, 2)
	n := h.gate.Current()
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: n}, nowMs())
	h.sync(func() {})
	h.grade("admin", protocol.VerdictCorrect)
	h.sync(func() {
		h.e.curTrack = nil
		h.e.curCell = nil
		h.e.state = protocol.StateBoard
	})

	// Admin bans perf WITHOUT their socket closing (the standard kick/ban
	// flow: onAdminKick sets Banned but never force-disconnects), so perf
	// remains registry.online()==true and still e.lastScorer.
	kd, _ := json.Marshal(protocol.AdminKickData{PlayerID: performer, Ban: true})
	h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: protocol.CMsgAdminKick, Data: kd, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})

	// 2nd pick triggers the Daily Double offer. The banned player must NOT be
	// selected as the contestant via the lastScorer fast path.
	h.selectCell("admin", 1, 2)
	if h.state() != protocol.StateDailyDouble {
		t.Fatalf("state = %s, want DAILY_DOUBLE", h.state())
	}
	var contestantID string
	h.sync(func() {
		if h.e.ddOffer != nil {
			contestantID = h.e.ddOffer.contestantID
		}
	})
	if contestantID == performer {
		t.Fatalf("banned player %q (still lastScorer, still online) was selected as the Daily Double contestant", performer)
	}
	if contestantID != bystander {
		t.Fatalf("contestant = %q, want fallback to the only other eligible online player %q", contestantID, bystander)
	}
}

// dd-eng-4: startDailyDoublePerformance() must hard-cancel (not proceed with
// a stale e.curRow) when the offer's cell no longer resolves on the current
// board — the case when an admin reloads the board (ReloadBoard has no state
// check at all) while the offer/song-pick sub-phase was still outstanding.
func TestQARegression_DailyDoubleAbortsWhenCellVanishes(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.join("c1", "fp1", "perf")

	h.selectCell("admin", 1, 2) // 1st visit: normal round -> lastScorer
	n := h.gate.Current()
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: n}, nowMs())
	h.sync(func() {})
	h.grade("admin", protocol.VerdictCorrect)
	h.sync(func() {
		h.e.curTrack = nil
		h.e.curCell = nil
		h.e.state = protocol.StateBoard
	})

	h.selectCell("admin", 1, 2) // 2nd visit: triggers the offer
	if h.state() != protocol.StateDailyDouble {
		t.Fatalf("state = %s, want DAILY_DOUBLE", h.state())
	}
	h.ddDecide("c1", true)
	choices := h.lastDailyDoubleOfferTrackIDs("c1")
	if len(choices) == 0 {
		t.Fatalf("no choices offered")
	}

	// Admin reloads the board mid-offer with a board that has NO cell at
	// (1,2) — simulating a re-attach to a different board shape while the
	// contestant is still deciding which song to sing.
	newBoard := &Board{
		Rows: 1, Cols: 1,
		Cells: [][]*Cell{{ddCell(9, 9, "Other", 1)}},
	}
	h.e.ReloadBoard(newBoard)
	h.sync(func() {})

	// The contestant now picks their song against a board where their
	// offer's cell no longer resolves.
	h.ddChoose("c1", choices[0])

	h.sync(func() {
		if h.e.curTrack != nil {
			t.Errorf("curTrack set despite the offer's cell no longer resolving on the current board")
		}
	})
	if h.state() != protocol.StateBoard {
		t.Fatalf("state after vanished-cell choose = %s, want BOARD (aborted like a decline)", h.state())
	}
}

// dd-eng-5: the dedicated admin.skipDailyDouble action must actually do
// something during a LIVE Daily Double performance, not silently no-op.
// Before the fix, dispatchAdmin routed cmsgAdminSkipDailyDouble straight to
// declineDailyDouble(), whose first line bails out once e.ddOffer is nil
// (exactly the state a live performance leaves things in) — contradicting
// endRound's own doc comment that the two admin controls "do the same thing."
func TestQARegression_DailyDoubleSkipDuringLivePerformance(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	performer := h.join("c1", "fp1", "perf")

	h.selectCell("admin", 1, 2)
	n := h.gate.Current()
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: n}, nowMs())
	h.sync(func() {})
	h.grade("admin", protocol.VerdictCorrect)
	h.sync(func() {
		h.e.curTrack = nil
		h.e.curCell = nil
		h.e.state = protocol.StateBoard
	})

	h.selectCell("admin", 1, 2)
	h.ddDecide("c1", true)
	choices := h.lastDailyDoubleOfferTrackIDs("c1")
	if len(choices) == 0 {
		t.Fatalf("no choices offered")
	}
	h.ddChoose("c1", choices[0]) // performance now live: curTrack set, ddOffer nil

	var hasCurTrack bool
	h.sync(func() { hasCurTrack = h.e.curTrack != nil })
	if !hasCurTrack {
		t.Fatalf("setup failed: performance did not start")
	}

	// The dedicated Skip Daily Double admin action, clicked while the
	// performance is LIVE, must finish the DD (mirroring endRound's own
	// branch) instead of silently no-op'ing.
	h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: cmsgAdminSkipDailyDouble, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})

	if h.state() != protocol.StateBoard {
		t.Fatalf("state after admin.skipDailyDouble during a live performance = %s, want BOARD", h.state())
	}
	if got := h.score(performer); got != 200 {
		t.Fatalf("performer score after admin.skipDailyDouble during live performance = %d, want 200 (100 from round 1 + 100 DD payout at the 1x nobody-rated floor)", got)
	}
}

// dd-eng-6: ResetToLobby ("New Game, same board") must restore e.board.DDBucket
// to its board-load-time snapshot. DDBucket is board-level state, separate
// from the per-cell Played flags ResetToLobby already resets, and it drains
// via a no-replacement draw in onDailyDoubleDecision's accept path with
// nothing else ever refilling it — before the fix, several New-Game cycles in
// a row on the same attached board would permanently exhaust it.
func TestQARegression_ResetToLobbyRestoresDDBucket(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")

	var original []*Track
	h.sync(func() { original = append([]*Track{}, h.e.board.DDBucket...) })
	if len(original) == 0 {
		t.Fatalf("test board must ship with a non-empty DDBucket")
	}

	// Drain the bucket to empty, simulating a prior game's no-replacement draws.
	h.sync(func() { h.e.board.DDBucket = nil })

	// Drive to GAME_OVER and reset — the same "New Game, same board" cycle a
	// real admin uses between games.
	h.sync(func() { h.e.state = protocol.StateGameOver })
	if err := h.e.ResetToLobby(); err != nil {
		t.Fatalf("ResetToLobby: %v", err)
	}

	var restored []*Track
	h.sync(func() { restored = h.e.board.DDBucket })
	if len(restored) != len(original) {
		t.Fatalf("DDBucket after ResetToLobby has %d tracks, want %d (restored to the board-load-time snapshot)", len(restored), len(original))
	}
	for i, tr := range restored {
		if tr.ID != original[i].ID {
			t.Fatalf("DDBucket restored with wrong tracks: got %v, want %v", restored, original)
		}
	}
}
