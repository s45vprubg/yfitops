package game

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/s45vprubg/yfitops/server/internal/protocol"
)

// This file tests the graceful Daily Double fade-out (replacing the old
// instant e.audio.Pause() the moment all ratings land) and the admin volume
// control it's built on top of. See fadeOutAndFinishDailyDouble/
// onAdminSetVolume/onAdminEndDailyDouble in engine.go.

// withFastFade shrinks DailyDoubleFadeDuration for the duration of a test,
// mirroring TestTransition_*'s own override of TransitionDelay — these
// tests should not take 3 real seconds each.
func withFastFade(t *testing.T) {
	t.Helper()
	old := DailyDoubleFadeDuration
	DailyDoubleFadeDuration = 60 * time.Millisecond
	t.Cleanup(func() { DailyDoubleFadeDuration = old })
}

// waitUntil polls cond with a short sleep until it's true or timeout elapses,
// matching the polling style waitForErrorCode/waitDropPointRefresh already
// use elsewhere in this package for off-loop async completions.
func waitUntil(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

func (r *fakeRepo) eventCount(kind string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, k := range r.events {
		if k == kind {
			n++
		}
	}
	return n
}

func (h *harness) rate(connID string, stars int) {
	d, _ := json.Marshal(protocol.RateData{Stars: stars})
	h.e.OnMessage(connID, protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgRate, Data: d, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
}

// setupDDPerformance drives the real accept/choose flow (same sequence as
// TestQARegression_DailyDoubleFreshRoundKey) to get a live Daily Double
// performance in progress, with one additional online mobile player besides
// the performer so ratingPool is non-empty. Returns the performer's ID and
// the rater's connID.
func setupDDPerformance(t *testing.T, h *harness) (performerID, raterConn string) {
	t.Helper()
	h.joinAdmin("admin")
	performerID = h.join("c1", "fp1", "perf")
	raterConn = "c2"
	h.join(raterConn, "fp2", "rater")

	h.selectCell("admin", 1, 2) // 1st pick on the DD cell: a normal round
	n := h.gate.Current()
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: n}, nowMs())
	h.sync(func() {})
	h.grade("admin", protocol.VerdictCorrect)
	h.sync(func() {
		h.e.curTrack = nil
		h.e.curCell = nil
		h.e.state = protocol.StateBoard
	})

	h.selectCell("admin", 1, 2) // 2nd pick: triggers the Daily Double offer
	h.ddDecide("c1", true)
	choices := h.lastDailyDoubleOfferTrackIDs("c1")
	if len(choices) == 0 {
		t.Fatalf("performer received no song choices after accepting")
	}
	h.ddChoose("c1", choices[0])

	if h.state() != protocol.StateDailyDouble {
		t.Fatalf("state = %s, want DAILY_DOUBLE after choosing", h.state())
	}
	return performerID, raterConn
}

// TestDailyDoubleFade_AllRatedDoesNotEndPerformance: owner decision,
// 2026-10-22 — submitting the last rating used to end the performance
// immediately (first an instant e.audio.Pause(), later a graceful fade).
// That felt unfair to a performer still mid-song: the only ways to end a
// performance now are the admin's End Daily Double button or the track
// finishing on its own. This pins that "all rated" alone is a no-op — the
// fade/score/restore behavior itself is covered by
// TestDailyDoubleFade_EndButtonTriggersSameFade, and that the rating is
// still retained (counted in the eventual payout) is confirmed here too.
func TestDailyDoubleFade_AllRatedDoesNotEndPerformance(t *testing.T) {
	withFastFade(t)
	h := newHarness(t)
	defer h.run()()
	performerID, raterConn := setupDDPerformance(t, h)

	var scoreBefore int
	h.sync(func() { scoreBefore = h.e.reg.players[performerID].Score })

	h.rate(raterConn, 5) // the only eligible rater — used to complete the pool immediately

	h.sync(func() {
		if h.e.ddFadingRoundKey != "" {
			t.Fatalf("e.ddFadingRoundKey = %q after the last rating landed, want empty (rating alone must not end the performance)", h.e.ddFadingRoundKey)
		}
		if h.e.state != protocol.StateDailyDouble {
			t.Fatalf("state = %s, want DAILY_DOUBLE still in progress after the last rating", h.e.state)
		}
	})
	if got := len(h.audio.ofMethod("pause")); got != 0 {
		t.Fatalf("got %d pause calls after rating alone, want 0", got)
	}

	// The admin can still end it whenever they decide to — and the 5-star
	// rating submitted above must still be the one counted in the payout,
	// not discarded for having arrived before the end.
	h.endDailyDouble("admin")
	if !waitUntil(t, time.Second, func() bool { return len(h.audio.ofMethod("pause")) > 0 }) {
		t.Fatal("timed out waiting for the admin-ended fade to finish and pause")
	}
	h.sync(func() {
		got := h.e.reg.players[performerID].Score
		if got <= scoreBefore {
			t.Errorf("performer score = %d, want > %d (payout applied)", got, scoreBefore)
		}
		// 5 stars -> DailyDoubleMultiplier(5) = 2.0x -> the richest payout
		// tier; a 1-star-floor payout (the no-ratings-at-all case) would be
		// much lower, so this also confirms the rating wasn't dropped.
		want := scoreBefore + int(float64(MaxPointsForRow(1))*DailyDoubleMultiplier(5))
		if got != want {
			t.Errorf("performer score = %d, want %d (5-star rating counted, not discarded)", got, want)
		}
	})
	if h.repo.eventCount("dailyDouble") != 1 {
		t.Errorf("dailyDouble LogEvent count = %d, want exactly 1", h.repo.eventCount("dailyDouble"))
	}
}

// TestDailyDoubleFade_EndButtonTriggersSameFade confirms the new
// admin.endDailyDouble button goes through the identical fade — ending a
// performance early (before all ratings are in) still fades, awards from
// whatever ratings exist (1-star floor here, since nobody rated), and
// restores volume.
func TestDailyDoubleFade_EndButtonTriggersSameFade(t *testing.T) {
	withFastFade(t)
	h := newHarness(t)
	defer h.run()()
	performerID, _ := setupDDPerformance(t, h)

	var scoreBefore int
	h.sync(func() { scoreBefore = h.e.reg.players[performerID].Score })

	d, _ := json.Marshal(struct{}{})
	h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: cmsgAdminEndDailyDouble, Data: d, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})

	if !waitUntil(t, time.Second, func() bool { return len(h.audio.ofMethod("pause")) > 0 }) {
		t.Fatal("timed out waiting for the fade to finish and pause")
	}
	vols := h.audio.ofMethod("setVolume")
	if len(vols) < dailyDoubleFadeSteps+1 { // fade-down steps + the final restore
		t.Fatalf("got %d setVolume calls, want at least %d", len(vols), dailyDoubleFadeSteps+1)
	}
	if last := vols[len(vols)-1]; last.vol != 100 {
		t.Errorf("final setVolume call = %d, want 100", last.vol)
	}
	h.sync(func() {
		if h.e.state != protocol.StateBoard {
			t.Errorf("state = %s, want BOARD", h.e.state)
		}
		got := h.e.reg.players[performerID].Score
		if got <= scoreBefore {
			t.Errorf("performer score = %d, want > %d (1-star-floor payout still applied)", got, scoreBefore)
		}
	})
}

// TestDailyDoubleFade_DoubleFireGuarded: if somehow both the automatic
// all-rated completion and a manual admin.endDailyDouble click land for the
// same performance (e.g. a rating and a button click racing), the second
// must be a synchronous no-op (e.ddFadingRoundKey's top-of-function guard),
// not a second award.
func TestDailyDoubleFade_DoubleFireGuarded(t *testing.T) {
	withFastFade(t)
	h := newHarness(t)
	defer h.run()()
	_, raterConn := setupDDPerformance(t, h)

	h.rate(raterConn, 5) // completes the pool, starts the fade (ddFadingRoundKey=e.roundKey)

	// Fire the button WHILE the fade is still in flight (volume mid-step,
	// well inside the 60ms fast-fade window) — must be a no-op, not a
	// second finishDailyDouble.
	d, _ := json.Marshal(struct{}{})
	h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: cmsgAdminEndDailyDouble, Data: d, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})

	if !waitUntil(t, time.Second, func() bool { return len(h.audio.ofMethod("pause")) > 0 }) {
		t.Fatal("timed out waiting for the fade to finish and pause")
	}
	// Give any (incorrect) second fade a moment to also land, if the guard
	// were broken, before asserting.
	time.Sleep(100 * time.Millisecond)
	if n := len(h.audio.ofMethod("pause")); n != 1 {
		t.Errorf("pause call count = %d, want exactly 1 (double-fire guard failed)", n)
	}
	if n := h.repo.eventCount("dailyDouble"); n != 1 {
		t.Errorf("dailyDouble LogEvent count = %d, want exactly 1 (double award)", n)
	}
	h.sync(func() {
		if h.e.ddFadingRoundKey != "" {
			t.Errorf("e.ddFadingRoundKey = %q, want empty", h.e.ddFadingRoundKey)
		}
	})
}

// TestDailyDoubleFade_ForceEndRoundWinsRaceAndVolumeStillRestores is the
// hardest edge case the plan calls out: Force End Round (admin.skipDailyDouble
// via endRound) stays INSTANT and untouched by this feature. If it fires
// while a graceful fade is already mid-flight, it must win immediately
// (round ends right away, exactly once), and the fade's own delayed
// continuation — landing afterward — must detect the state has moved on and
// bail WITHOUT double-awarding, while its deferred restore still fires so
// the volume is never left faded down.
func TestDailyDoubleFade_ForceEndRoundWinsRaceAndVolumeStillRestores(t *testing.T) {
	withFastFade(t)
	h := newHarness(t)
	defer h.run()()
	performerID, raterConn := setupDDPerformance(t, h)
	_ = raterConn

	d, _ := json.Marshal(struct{}{})
	h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: cmsgAdminEndDailyDouble, Data: d, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {}) // ddFadingRoundKey is now set to e.roundKey; the fade goroutine is sleeping through its 60ms window

	// Force End Round, synchronously on the loop, while the fade is still
	// in flight. endRound -> skipDailyDouble -> finishDailyDouble directly
	// (curTrack != nil), bypassing the fade entirely — this must win.
	h.sync(func() { h.e.endRound() })

	var scoreAfterForceEnd int
	h.sync(func() {
		if h.e.state != protocol.StateBoard {
			t.Fatalf("state = %s, want BOARD immediately after Force End Round (it must not wait on the fade)", h.e.state)
		}
		scoreAfterForceEnd = h.e.reg.players[performerID].Score
	})
	if h.repo.eventCount("dailyDouble") != 1 {
		t.Fatalf("dailyDouble LogEvent count = %d after Force End Round, want exactly 1", h.repo.eventCount("dailyDouble"))
	}

	// Now let the fade's own continuation land (its 60ms sleep finishes on
	// its own clock, independent of the state change above). It must NOT
	// award a second time, but it MUST still restore the volume — this is
	// the guaranteed-restore defer firing on the early-bail path, not just
	// the happy path.
	if !waitUntil(t, time.Second, func() bool {
		var ending string
		h.sync(func() { ending = h.e.ddFadingRoundKey })
		return ending == ""
	}) {
		t.Fatal("timed out waiting for the fade's continuation to land and clear e.ddFadingRoundKey")
	}
	if h.repo.eventCount("dailyDouble") != 1 {
		t.Errorf("dailyDouble LogEvent count = %d after the fade's continuation also landed, want still exactly 1 (no double award)", h.repo.eventCount("dailyDouble"))
	}
	h.sync(func() {
		if got := h.e.reg.players[performerID].Score; got != scoreAfterForceEnd {
			t.Errorf("performer score changed from %d to %d after the fade's continuation landed — it must have been a no-op", scoreAfterForceEnd, got)
		}
	})
	vols := h.audio.ofMethod("setVolume")
	if len(vols) == 0 {
		t.Fatal("no setVolume calls at all — the guaranteed restore never fired")
	}
	if last := vols[len(vols)-1]; last.vol != 100 {
		t.Errorf("final setVolume call = %d, want 100 — volume must be restored even on the early-bail path", last.vol)
	}
}

// TestDailyDoubleFade_RestoreStillAttemptedWhenSetVolumeErrors is the
// literal "force an error partway through and confirm the restore still
// happens" case the plan calls for: SetVolume itself fails on every call
// (simulating a Spotify API hiccup mid-fade), and the guaranteed-restore
// defer must still ATTEMPT the final restore call regardless — this proves
// the restore is structurally unskippable (go func(){...}() + defer), not
// merely conditioned on every prior step succeeding.
func TestDailyDoubleFade_RestoreStillAttemptedWhenSetVolumeErrors(t *testing.T) {
	withFastFade(t)
	h := newHarness(t)
	defer h.run()()
	_, raterConn := setupDDPerformance(t, h)
	h.audio.volumeErr = errors.New("simulated Spotify API failure")

	h.rate(raterConn, 5) // completes the pool; admin still has to end it (2026-10-22)
	h.endDailyDouble("admin")

	if !waitUntil(t, time.Second, func() bool { return len(h.audio.ofMethod("pause")) > 0 }) {
		t.Fatal("timed out waiting for the fade to finish and pause, despite every SetVolume call erroring")
	}
	// The scoring/transition logic must still have run to completion — a
	// failing SetVolume must not abort finishDailyDouble itself.
	h.sync(func() {
		if h.e.state != protocol.StateBoard {
			t.Errorf("state = %s, want BOARD even though every SetVolume call errored", h.e.state)
		}
		if h.e.ddFadingRoundKey != "" {
			t.Errorf("e.ddFadingRoundKey = %q, want empty", h.e.ddFadingRoundKey)
		}
	})
	if h.repo.eventCount("dailyDouble") != 1 {
		t.Errorf("dailyDouble LogEvent count = %d, want exactly 1", h.repo.eventCount("dailyDouble"))
	}
	// The restore call itself was still ATTEMPTED (its error is discarded,
	// same as every other audio call in this codebase) — confirm the final
	// setVolume call in the sequence targeted the restore level, not that
	// it "succeeded" (fakeAudio has no notion of partial success/failure
	// beyond the error it returns, which the engine already discards by
	// design for all AudioDevice calls, matching Pause/Resume's existing
	// `_ = e.audio.Pause(...)` convention elsewhere in this file).
	vols := h.audio.ofMethod("setVolume")
	if len(vols) == 0 {
		t.Fatal("no setVolume calls at all — the restore was never even attempted")
	}
	if last := vols[len(vols)-1]; last.vol != 100 {
		t.Errorf("final setVolume call = %d, want 100 (the restore attempt), even though it errored", last.vol)
	}
}

// TestOnAdminEndDailyDouble_RejectsOutsidePerformance covers the UI's
// disabled-button-didn't-stop-the-click case, mirroring the drop-point
// handlers' own such tests: the button only renders during an actual
// performance, but the server enforces the same condition independently.
func TestOnAdminEndDailyDouble_RejectsOutsidePerformance(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")

	d, _ := json.Marshal(struct{}{})
	h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: cmsgAdminEndDailyDouble, Data: d, Nonce: h.gate.Current()}, nowMs())
	if !h.waitForErrorCode(500*time.Millisecond, "noDailyDoublePerformance") {
		t.Error("expected a noDailyDoublePerformance error, got none")
	}
	if n := len(h.audio.ofMethod("pause")); n != 0 {
		t.Errorf("pause call count = %d, want 0 (no performance to end)", n)
	}
}

// TestOnAdminSetVolume_ClampsAndAppliesImmediately covers the admin volume
// slider: out-of-range input is clamped (not rejected, since this is just a
// UI slider with no persisted/sticky consequence like drop-point marking
// has), and the change is applied to the live device right away, not only
// cached for the next fade.
func TestOnAdminSetVolume_ClampsAndAppliesImmediately(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")

	for _, tc := range []struct {
		send, want int
	}{
		{50, 50},
		{-10, 0},
		{150, 100},
	} {
		d, _ := json.Marshal(adminSetVolumeData{Percent: tc.send})
		h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: cmsgAdminSetVolume, Data: d, Nonce: h.gate.Current()}, nowMs())
		h.sync(func() {
			if h.e.volumePct != tc.want {
				t.Errorf("send %d: e.volumePct = %d, want %d", tc.send, h.e.volumePct, tc.want)
			}
		})
		if !waitUntil(t, time.Second, func() bool {
			last, ok := h.audio.last()
			return ok && last.method == "setVolume" && last.vol == tc.want
		}) {
			t.Errorf("send %d: timed out waiting for SetVolume(%d) on the live device", tc.send, tc.want)
		}
	}
}

// TestSendFullSync_IncludesVolumeStatus confirms a (re)connecting admin
// learns the current operating volume rather than defaulting its slider UI
// to an assumed value.
func TestSendFullSync_IncludesVolumeStatus(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.sync(func() { h.e.volumePct = 42 })
	h.joinAdmin("admin") // onHello -> sendFullSync

	h.bcast.mu.Lock()
	defer h.bcast.mu.Unlock()
	found := false
	for _, f := range h.bcast.frames {
		if f.connID == "admin" && f.env.Type == smsgVolumeStatus {
			var d volumeStatusData
			_ = json.Unmarshal(f.env.Data, &d)
			if d.Percent != 42 {
				t.Errorf("volumeStatus.Percent = %d, want 42", d.Percent)
			}
			found = true
		}
	}
	if !found {
		t.Error("admin never received a volumeStatus frame on connect")
	}
}

// TestDailyDoubleFade_FirstStepIsFullVolumeNoNotch (s8eng-07): the fade's
// very first SetVolume call must reaffirm the current operating level
// itself, not an already-stepped-down value — the old `dailyDoubleFadeSteps
// - 1` start produced an immediate ~17-point drop (100 -> 83 at the default
// 6 steps) before any time had passed, an audible "notch" at the start of
// what's supposed to read as a smooth fade.
func TestDailyDoubleFade_FirstStepIsFullVolumeNoNotch(t *testing.T) {
	withFastFade(t)
	h := newHarness(t)
	defer h.run()()
	_, raterConn := setupDDPerformance(t, h)

	h.rate(raterConn, 5) // completes the pool; admin still has to end it (2026-10-22)
	h.endDailyDouble("admin")

	if !waitUntil(t, time.Second, func() bool { return len(h.audio.ofMethod("pause")) > 0 }) {
		t.Fatal("timed out waiting for the fade to finish and pause")
	}
	vols := h.audio.ofMethod("setVolume")
	if len(vols) == 0 {
		t.Fatal("no setVolume calls at all")
	}
	if vols[0].vol != 100 {
		t.Errorf("first setVolume call = %d, want 100 (the current operating level, no notch)", vols[0].vol)
	}
}

// TestDailyDoubleFade_MidFadeAdminSetVolumeWinsRestore (s8eng-04): an
// admin.setVolume issued while a fade is in flight must win the eventual
// guaranteed restore — the restore must re-read the LIVE e.volumePct at the
// moment it fires, not reapply the stale value captured when the fade
// started, otherwise the device silently reverts to the pre-fade level
// while e.volumePct (and the admin UI driven by it) disagrees.
func TestDailyDoubleFade_MidFadeAdminSetVolumeWinsRestore(t *testing.T) {
	withFastFade(t)
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	_, raterConn := setupDDPerformance(t, h)

	h.rate(raterConn, 5) // completes the pool; admin still has to end it (2026-10-22)
	h.endDailyDouble("admin") // starts the fade (restoreTo snapshot = 100)

	// Mid-fade: the operator drags the slider to 40. This must be the value
	// both the device and e.volumePct end up at once the fade's guaranteed
	// restore fires, not the stale 100 captured at fade-start.
	d, _ := json.Marshal(adminSetVolumeData{Percent: 40})
	h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: cmsgAdminSetVolume, Data: d, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})

	if !waitUntil(t, time.Second, func() bool { return len(h.audio.ofMethod("pause")) > 0 }) {
		t.Fatal("timed out waiting for the fade to finish and pause")
	}
	if !waitUntil(t, time.Second, func() bool {
		last, ok := h.audio.last()
		return ok && last.method == "setVolume" && last.vol == 40
	}) {
		last, _ := h.audio.last()
		t.Fatalf("final setVolume call = %+v, want the restore to land on 40 (the operator's mid-fade change), not the stale pre-fade 100", last)
	}
	h.sync(func() {
		if h.e.volumePct != 40 {
			t.Errorf("e.volumePct = %d, want 40", h.e.volumePct)
		}
	})
}

// TestDailyDoubleFade_StaleFadeCannotBlockOrMisfireAgainstNewPerformance
// (s8eng-03): e.ddFadingRoundKey is a per-performance identity (the
// performance's own e.roundKey), not a bare bool. A Force End Round that
// instantly finishes a performance while its OWN graceful fade is still
// mid-flight must not leave a stale marker that (a) blocks a later,
// completely unrelated performance's own fade from ever starting, or (b)
// lets the stale continuation, once it does land, misfire finishDailyDouble
// against whatever performance happens to be live by then.
func TestDailyDoubleFade_StaleFadeCannotBlockOrMisfireAgainstNewPerformance(t *testing.T) {
	withFastFade(t)
	h := newHarness(t)
	defer h.run()()
	performerID, raterConn := setupDDPerformance(t, h)
	var raterID string
	h.sync(func() { raterID = h.e.reg.playerForConn(raterConn).ID })

	// Start fade #1 for performance #1 (ddFadingRoundKey = rk1), then Force
	// End Round immediately — this stays instant and bypasses the fade
	// entirely (awarding performance #1 right away), but fade #1's own
	// goroutine is still sleeping through its ~60ms stepped window with a
	// now-stale ddFadingRoundKey == rk1.
	h.rate(raterConn, 5)
	var rk1 string
	h.sync(func() { rk1 = h.e.roundKey })
	if rk1 == "" {
		t.Fatal("roundKey empty for performance #1")
	}
	h.sync(func() { h.e.endRound() })
	if h.state() != protocol.StateBoard {
		t.Fatalf("state = %s after Force End Round, want BOARD", h.state())
	}
	if n := h.repo.eventCount("dailyDouble"); n != 1 {
		t.Fatalf("dailyDouble LogEvent count = %d after Force End Round, want 1", n)
	}

	// Start a brand-new, UNRELATED Daily Double performance #2 while fade
	// #1's stale continuation is still sleeping. Driven via direct state
	// (a test-only shortcut, mirroring this file's own setupDDPerformance/
	// other tests' pattern) on a DIFFERENT cell than performance #1's,
	// since the test board's one DD cell only triggers on its exact
	// DDTriggerVisit and is already spent.
	rk2 := rk1 + ":perf2"
	h.sync(func() {
		h.e.state = protocol.StateDailyDouble
		h.e.curCell = h.e.board.Cells[1][1] // (5,2) — unrelated to perf #1's (1,2) cell
		h.e.curTrack = h.e.curCell.Tracks[0]
		h.e.curRow = 5
		h.e.cellPicker = performerID
		h.e.trackStartMs = nowMs()
		h.e.roundKey = rk2
		h.e.ratingPool = map[string]bool{raterID: true}
		h.e.ratings = map[string]int{}
	})

	// (a) Performance #2's own fade must NOT be blocked by fade #1's stale
	// marker (still == rk1 at this point).
	h.sync(func() { h.e.fadeOutAndFinishDailyDouble() })
	var fadingKey2 string
	h.sync(func() { fadingKey2 = h.e.ddFadingRoundKey })
	if fadingKey2 != rk2 {
		t.Fatalf("ddFadingRoundKey = %q right after starting fade #2, want %q — performance #2's own fade must not be blocked by the stale marker (%q) left behind by the force-ended performance #1", fadingKey2, rk2, rk1)
	}

	// (b) Let both continuations land: fade #1's stale one (sleeping since
	// before Force End Round, so it lands first) must bail without
	// touching performance #2, and fade #2's own one must finish exactly
	// performance #2, exactly once.
	if !waitUntil(t, 2*time.Second, func() bool { return len(h.audio.ofMethod("pause")) >= 2 }) {
		t.Fatalf("timed out waiting for both performances' pause calls (got %d)", len(h.audio.ofMethod("pause")))
	}
	// Give any (incorrect) extra misfire a moment to also land before
	// asserting a final, settled count.
	time.Sleep(100 * time.Millisecond)
	if n := len(h.audio.ofMethod("pause")); n != 2 {
		t.Errorf("pause call count = %d, want exactly 2 (1 from Force End Round's instant finish, 1 from performance #2's own graceful fade) — a stale fade #1 continuation must not have misfired a 3rd", n)
	}
	if n := h.repo.eventCount("dailyDouble"); n != 2 {
		t.Errorf("dailyDouble LogEvent count = %d, want exactly 2 — a stale continuation from the force-ended performance #1 must not double-award or misfire against performance #2", n)
	}
	h.sync(func() {
		if h.e.state != protocol.StateBoard {
			t.Errorf("state = %s, want BOARD after performance #2's own fade completes", h.e.state)
		}
		if h.e.ddFadingRoundKey != "" {
			t.Errorf("ddFadingRoundKey = %q after both continuations landed, want empty", h.e.ddFadingRoundKey)
		}
	})
}

// TestVolumeSerialization_AppliesInStrictEnqueueOrderDespiteSlowDevice
// (s8eng-05): every outbound SetVolume call — admin.setVolume's live-apply,
// the fade's stepped ramp, and the fade's restore — must be APPLIED to the
// device in the same order it was issued, even if an earlier call happens
// to be slower to execute than a later one (simulating real network
// latency/reordering risk). Before this fix, each call site fired its own
// ad hoc, independently-scheduled goroutine straight at the device with no
// ordering guarantee between them.
func TestVolumeSerialization_AppliesInStrictEnqueueOrderDespiteSlowDevice(t *testing.T) {
	h := newHarness(t)
	defer h.run()()

	// The FIRST call is artificially slow (simulating network latency); a
	// second, faster call is enqueued right after it. Without a single
	// serializing writer, a bare `go e.audio.SetVolume(...)` per call could
	// let the second (fast) call's goroutine win the race and get recorded
	// before the first (slow) one — exactly the s8eng-05 race.
	h.audio.volumeDelay = 80 * time.Millisecond

	h.sync(func() {
		h.e.sendVolume(100) // enqueued first — e.g. the fade's own step
		h.e.sendVolume(40)  // enqueued second — e.g. a concurrent admin.setVolume
	})

	if !waitUntil(t, time.Second, func() bool { return len(h.audio.ofMethod("setVolume")) >= 2 }) {
		t.Fatal("timed out waiting for both setVolume calls to be applied")
	}
	vols := h.audio.ofMethod("setVolume")
	if len(vols) != 2 {
		t.Fatalf("got %d setVolume calls, want exactly 2", len(vols))
	}
	if vols[0].vol != 100 || vols[1].vol != 40 {
		t.Errorf("applied order = [%d, %d], want [100, 40] — a slower first-enqueued call must not let a faster second-enqueued call overtake it", vols[0].vol, vols[1].vol)
	}
}
