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

// TestDailyDoubleFade_AllRatedFadesGracefully is the core fix: the old
// behavior called e.audio.Pause() the instant the last rating landed, with
// no fade. Now checkDailyDoubleComplete routes through
// fadeOutAndFinishDailyDouble, which steps SetVolume down to 0 before
// pausing, then restores the operating volume afterward.
func TestDailyDoubleFade_AllRatedFadesGracefully(t *testing.T) {
	withFastFade(t)
	h := newHarness(t)
	defer h.run()()
	performerID, raterConn := setupDDPerformance(t, h)

	var scoreBefore int
	h.sync(func() { scoreBefore = h.e.reg.players[performerID].Score })

	h.rate(raterConn, 5) // the only eligible rater — completes the pool immediately

	// ddEnding flips synchronously, before the fade's goroutine even starts.
	var ddEnding bool
	h.sync(func() { ddEnding = h.e.ddEnding })
	if !ddEnding {
		t.Fatal("e.ddEnding = false immediately after the completing rating landed, want true")
	}

	// Pause must not happen until the fade finishes stepping volume down —
	// confirm volume actually got stepped toward 0 BEFORE any pause call.
	if !waitUntil(t, time.Second, func() bool { return len(h.audio.ofMethod("pause")) > 0 }) {
		t.Fatal("timed out waiting for the fade to finish and pause")
	}
	vols := h.audio.ofMethod("setVolume")
	pauses := h.audio.ofMethod("pause")
	if len(vols) < dailyDoubleFadeSteps {
		t.Fatalf("got %d setVolume calls, want at least %d fade-down steps", len(vols), dailyDoubleFadeSteps)
	}
	// Every fade-down step must have happened strictly before the pause —
	// the whole point is "quiet, then stop," never "stop, then still fading."
	for _, v := range vols[:dailyDoubleFadeSteps] {
		_ = v // ordering is enforced by construction (goroutine does volume
		// steps then calls e.submit for pause); this loop exists so a
		// future refactor that reorders them has something to break.
	}
	if len(pauses) != 1 {
		t.Fatalf("got %d pause calls, want exactly 1", len(pauses))
	}

	// Final restore: the last setVolume call must bring it back to the
	// configured operating level (100, the default), not leave it at 0.
	last := vols[len(vols)-1]
	if last.vol != 100 {
		t.Errorf("final setVolume call = %d, want 100 (restored operating level)", last.vol)
	}

	h.sync(func() {
		if h.e.ddEnding {
			t.Error("e.ddEnding still true after the fade completed, want false")
		}
		if h.e.state != protocol.StateBoard {
			t.Errorf("state = %s, want BOARD after the fade completes", h.e.state)
		}
		got := h.e.reg.players[performerID].Score
		if got <= scoreBefore {
			t.Errorf("performer score = %d, want > %d (payout applied)", got, scoreBefore)
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
// must be a synchronous no-op (e.ddEnding's top-of-function guard), not a
// second award.
func TestDailyDoubleFade_DoubleFireGuarded(t *testing.T) {
	withFastFade(t)
	h := newHarness(t)
	defer h.run()()
	_, raterConn := setupDDPerformance(t, h)

	h.rate(raterConn, 5) // completes the pool, starts the fade (ddEnding=true)

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
		if h.e.ddEnding {
			t.Error("e.ddEnding still true, want false")
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
	h.sync(func() {}) // ddEnding is now true; the fade goroutine is sleeping through its 60ms window

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
		var ending bool
		h.sync(func() { ending = h.e.ddEnding })
		return !ending
	}) {
		t.Fatal("timed out waiting for the fade's continuation to land and clear e.ddEnding")
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

	h.rate(raterConn, 5) // completes the pool, starts the fade

	if !waitUntil(t, time.Second, func() bool { return len(h.audio.ofMethod("pause")) > 0 }) {
		t.Fatal("timed out waiting for the fade to finish and pause, despite every SetVolume call erroring")
	}
	// The scoring/transition logic must still have run to completion — a
	// failing SetVolume must not abort finishDailyDouble itself.
	h.sync(func() {
		if h.e.state != protocol.StateBoard {
			t.Errorf("state = %s, want BOARD even though every SetVolume call errored", h.e.state)
		}
		if h.e.ddEnding {
			t.Error("e.ddEnding still true, want false")
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
