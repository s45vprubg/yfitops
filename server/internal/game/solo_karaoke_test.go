package game

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/s45vprubg/yfitops/server/internal/protocol"
)

// solo_karaoke_test.go exercises the Solo Karaoke mode-lock, auto-advance,
// repeat/shuffle, and queue-exhaustion behavior against the same in-memory
// fakes/harness engine_test.go uses for trivia.

// soloKaraokeMsg sends an admin.soloKaraoke.* action from adminConn and
// drains the Run loop. payload may be nil for actions with no body
// (play/pause/resume/stop/next/prev).
func (h *harness) soloKaraokeMsg(adminConn string, msgType protocol.ClientMsgType, payload any) {
	var data []byte
	if payload != nil {
		data, _ = json.Marshal(payload)
	}
	h.e.OnMessage(adminConn, protocol.RoleAdmin, protocol.ClientEnvelope{Type: msgType, Data: data, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
}

// stagePlayerState drives stage.playerState from stageConn and drains.
func (h *harness) stagePlayerState(stageConn string, posMs int64, paused, trackEnded bool) {
	d, _ := json.Marshal(protocol.StagePlayerStateData{PositionMs: posMs, Paused: paused, TrackEnded: trackEnded})
	h.e.OnMessage(stageConn, protocol.RoleStage, protocol.ClientEnvelope{Type: protocol.CMsgStagePlayerState, Data: d}, nowMs())
	h.sync(func() {})
}

// lastModeLockOwner returns the Owner of the most recent smsgModeLock frame
// broadcast to RoleAdmin, or "" if none was sent (since the last h.bcast.reset()).
func (h *harness) lastModeLockOwner() string {
	owner := ""
	h.bcast.mu.Lock()
	for _, f := range h.bcast.frames {
		if f.env.Type == smsgModeLock && f.role == protocol.RoleAdmin {
			var d modeLockData
			_ = json.Unmarshal(f.env.Data, &d)
			owner = d.Owner
		}
	}
	h.bcast.mu.Unlock()
	return owner
}

func soloKaraokeTestTrack(id, song string) *Track {
	return &Track{
		ID:         id,
		SpotifyURI: "spotify:track:" + id,
		Artist:     "Test Artist",
		Song:       song,
		DurationMs: 200000,
		Playable:   true,
	}
}

// TestSoloKaraoke_ModeLock_RejectsStartGameWhileActive: trivia's StartGame
// must be rejected while Solo Karaoke is active, and must not change state.
func TestSoloKaraoke_ModeLock_RejectsStartGameWhileActive(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.joinStage("stage")

	h.e.ReloadSoloKaraokeQueue([]*Track{soloKaraokeTestTrack("sk1", "Song One")})
	h.sync(func() {})
	h.soloKaraokeMsg("admin", cmsgAdminSoloKaraokePlay, nil)

	var active bool
	h.sync(func() { active = h.e.soloKaraoke.active })
	if !active {
		t.Fatalf("expected solo karaoke active after play")
	}

	if err := h.e.StartGame(); err == nil {
		t.Fatalf("expected StartGame to be rejected while solo karaoke is active")
	}
	if h.state() != protocol.StateLobby {
		t.Fatalf("state changed despite rejected StartGame: %s", h.state())
	}
}

// TestSoloKaraoke_ModeLock_RejectsPlayWhileTriviaActive: admin.soloKaraoke.play
// must be rejected while trivia is active (state not LOBBY/GAME_OVER), and
// must not flip e.soloKaraoke.active.
func TestSoloKaraoke_ModeLock_RejectsPlayWhileTriviaActive(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.joinStage("stage")

	if err := h.e.StartGame(); err != nil {
		t.Fatalf("StartGame: %v", err)
	}
	if h.state() != protocol.StateBoard {
		t.Fatalf("state = %s, want BOARD", h.state())
	}

	h.e.ReloadSoloKaraokeQueue([]*Track{soloKaraokeTestTrack("sk1", "Song One")})
	h.sync(func() {})

	h.bcast.reset()
	h.soloKaraokeMsg("admin", cmsgAdminSoloKaraokePlay, nil)

	var active bool
	h.sync(func() { active = h.e.soloKaraoke.active })
	if active {
		t.Fatalf("expected solo karaoke play to be rejected while trivia is active")
	}

	found := false
	h.bcast.mu.Lock()
	for _, f := range h.bcast.frames {
		if f.connID == "admin" && f.env.Type == protocol.SMsgError {
			found = true
		}
	}
	h.bcast.mu.Unlock()
	if !found {
		t.Fatalf("expected an error frame sent to the admin connection")
	}
}

// TestSoloKaraoke_AutoAdvance_OnTrackEnded: a natural TrackEnded signal
// (no repeat, no shuffle) advances to the next track in queue order.
func TestSoloKaraoke_AutoAdvance_OnTrackEnded(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.joinStage("stage")

	h.e.ReloadSoloKaraokeQueue([]*Track{
		soloKaraokeTestTrack("sk1", "Song One"),
		soloKaraokeTestTrack("sk2", "Song Two"),
	})
	h.sync(func() {})
	h.soloKaraokeMsg("admin", cmsgAdminSoloKaraokePlay, nil)

	var cur string
	h.sync(func() { cur = h.e.soloKaraoke.currentTrack.ID })
	if cur != "sk1" {
		t.Fatalf("expected sk1 playing first, got %s", cur)
	}

	h.stagePlayerState("stage", 200000, false, true)

	h.sync(func() { cur = h.e.soloKaraoke.currentTrack.ID })
	if cur != "sk2" {
		t.Fatalf("expected auto-advance to sk2, got %s", cur)
	}
}

// TestSoloKaraoke_Repeat_One_ReplaysCurrentTrack: repeat="one" replays the
// SAME track on TrackEnded (with a fresh Play call, not a no-op) rather than
// advancing.
func TestSoloKaraoke_Repeat_One_ReplaysCurrentTrack(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.joinStage("stage")

	h.e.ReloadSoloKaraokeQueue([]*Track{
		soloKaraokeTestTrack("sk1", "Song One"),
		soloKaraokeTestTrack("sk2", "Song Two"),
	})
	h.sync(func() {})
	h.soloKaraokeMsg("admin", cmsgAdminSoloKaraokePlay, nil)
	h.soloKaraokeMsg("admin", cmsgAdminSoloKaraokeSetRepeat, soloKaraokeSetRepeatData{Repeat: "one"})

	playsBefore := len(h.audio.ofMethod("play"))
	h.stagePlayerState("stage", 200000, false, true)
	playsAfter := len(h.audio.ofMethod("play"))

	var cur string
	h.sync(func() { cur = h.e.soloKaraoke.currentTrack.ID })
	if cur != "sk1" {
		t.Fatalf("expected repeat=one to replay sk1, got %s", cur)
	}
	if playsAfter <= playsBefore {
		t.Fatalf("expected a fresh audio.Play call on replay (before=%d after=%d)", playsBefore, playsAfter)
	}
}

// TestSoloKaraoke_Shuffle_NeverImmediatelyRepeatsCurrent: with shuffle on, a
// run of TrackEnded signals must never pick the just-playing track again.
func TestSoloKaraoke_Shuffle_NeverImmediatelyRepeatsCurrent(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.joinStage("stage")

	h.e.ReloadSoloKaraokeQueue([]*Track{
		soloKaraokeTestTrack("sk1", "Song One"),
		soloKaraokeTestTrack("sk2", "Song Two"),
		soloKaraokeTestTrack("sk3", "Song Three"),
	})
	h.sync(func() {})
	h.soloKaraokeMsg("admin", cmsgAdminSoloKaraokeSetShuffle, soloKaraokeSetShuffleData{Shuffle: true})
	h.soloKaraokeMsg("admin", cmsgAdminSoloKaraokePlay, nil)

	for i := 0; i < 20; i++ {
		var before string
		h.sync(func() { before = h.e.soloKaraoke.currentTrack.ID })
		h.stagePlayerState("stage", 200000, false, true)
		var after string
		h.sync(func() { after = h.e.soloKaraoke.currentTrack.ID })
		if after == before {
			t.Fatalf("iteration %d: shuffle repeated the current track immediately (%s -> %s)", i, before, after)
		}
	}
}

// TestSoloKaraoke_QueueExhaustion_NoRepeat_DeactivatesAndReleasesLock: a
// single-track queue with no repeat set, on TrackEnded, deactivates Solo
// Karaoke and releases the mode-lock back to "none".
func TestSoloKaraoke_QueueExhaustion_NoRepeat_DeactivatesAndReleasesLock(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.joinStage("stage")

	h.e.ReloadSoloKaraokeQueue([]*Track{soloKaraokeTestTrack("sk1", "Song One")})
	h.sync(func() {})
	h.soloKaraokeMsg("admin", cmsgAdminSoloKaraokePlay, nil)

	h.bcast.reset()
	h.stagePlayerState("stage", 200000, false, true)

	var active bool
	h.sync(func() { active = h.e.soloKaraoke.active })
	if active {
		t.Fatalf("expected solo karaoke to deactivate on queue exhaustion")
	}
	if got := h.lastModeLockOwner(); got != "none" {
		t.Fatalf("expected modeLock owner \"none\" after exhaustion, got %q", got)
	}

	// The gate must actually be released: trivia can now start.
	if err := h.e.StartGame(); err != nil {
		t.Fatalf("expected StartGame to succeed after solo karaoke released the gate: %v", err)
	}
}

// TestSoloKaraoke_ModeLock_BroadcastsOnEveryOwnerChange walks every owner
// transition (none -> trivia -> none -> solo_karaoke -> none) and asserts a
// smsgModeLock frame fires on each one with the correct owner.
func TestSoloKaraoke_ModeLock_BroadcastsOnEveryOwnerChange(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.joinStage("stage")

	h.bcast.reset()
	if err := h.e.StartGame(); err != nil {
		t.Fatalf("StartGame: %v", err)
	}
	if got := h.lastModeLockOwner(); got != "trivia" {
		t.Fatalf("after StartGame: owner = %q, want %q", got, "trivia")
	}

	h.bcast.reset()
	h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: protocol.CMsgAdminEndGame}, nowMs())
	h.sync(func() {})
	if got := h.lastModeLockOwner(); got != "none" {
		t.Fatalf("after admin.endGame: owner = %q, want %q", got, "none")
	}

	h.e.ReloadSoloKaraokeQueue([]*Track{soloKaraokeTestTrack("sk1", "Song One")})
	h.sync(func() {})

	h.bcast.reset()
	h.soloKaraokeMsg("admin", cmsgAdminSoloKaraokePlay, nil)
	if got := h.lastModeLockOwner(); got != "solo_karaoke" {
		t.Fatalf("after solo karaoke play: owner = %q, want %q", got, "solo_karaoke")
	}

	h.bcast.reset()
	h.soloKaraokeMsg("admin", cmsgAdminSoloKaraokeStop, nil)
	if got := h.lastModeLockOwner(); got != "none" {
		t.Fatalf("after solo karaoke stop: owner = %q, want %q", got, "none")
	}
}

// TestSoloKaraoke_SeekWhilePaused_StaysPaused is the regression test for
// s10eng-01: seeking while paused must leave the device genuinely paused at
// the new position, not resume real playback while e.soloKaraoke.paused
// still claims paused — the pre-fix desync left Pause dead for the rest of
// the track and made Resume audibly jump backward.
func TestSoloKaraoke_SeekWhilePaused_StaysPaused(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.joinStage("stage")

	h.e.ReloadSoloKaraokeQueue([]*Track{soloKaraokeTestTrack("sk1", "Song One")})
	h.sync(func() {})
	h.soloKaraokeMsg("admin", cmsgAdminSoloKaraokePlay, nil)
	h.soloKaraokeMsg("admin", cmsgAdminSoloKaraokePause, nil)

	var paused bool
	h.sync(func() { paused = h.e.soloKaraoke.paused })
	if !paused {
		t.Fatalf("expected paused=true after pause")
	}

	h.soloKaraokeMsg("admin", cmsgAdminSoloKaraokeSeek, soloKaraokeSeekData{PositionMs: 50000})

	h.sync(func() { paused = h.e.soloKaraoke.paused })
	if !paused {
		t.Fatalf("expected paused=true to survive a seek while paused (s10eng-01 regression)")
	}
	// The device must have actually been told to pause again after the seek
	// (loadAndPlayTrack resumes it), not just the in-memory flag.
	if last, ok := h.audio.last(); !ok || last.method != "pause" {
		t.Fatalf("expected the last audio call after seek-while-paused to be pause, got %+v (ok=%v)", last, ok)
	}

	// Pause must now be a genuine no-op (already paused), not dead — and
	// Resume must work from the NEW seeked position, not jump back.
	playsBefore := len(h.audio.ofMethod("resume"))
	h.soloKaraokeMsg("admin", cmsgAdminSoloKaraokeResume, nil)
	if len(h.audio.ofMethod("resume")) <= playsBefore {
		t.Fatalf("expected Resume to actually resume after a seek-while-paused")
	}
	var pausedAtMs int64
	h.sync(func() { pausedAtMs = h.e.soloKaraoke.pausedAtMs })
	if pausedAtMs != 50000 {
		t.Fatalf("pausedAtMs = %d, want 50000 (the seeked position, not a stale pre-seek one)", pausedAtMs)
	}
}

// NOTE on s10eng-02 (duplicate TrackEnded double-advancing the queue): fixed
// client-side in web/stage/src/audio/spotify.ts (ingest() now edge-triggers
// trackEnded instead of reporting it as a level), not here — see
// soloKaraokeAdvance's doc comment for why a server-side guard can't
// actually distinguish the two cases. No Go regression test for this one;
// this project has no frontend test runner (standing gap, same as every
// other UI-layer fix across prior sweeps), verified via tsc + hand-trace.

// TestSoloKaraoke_DeleteCurrentTrack_RepeatOne_FallsBackInsteadOfEnding is
// the regression test for s10eng-03: deleting the currently-playing track
// from the Playlist mid-song while repeat="one" must fall back to normal
// step-forward behavior on the next TrackEnded, not end the whole session.
func TestSoloKaraoke_DeleteCurrentTrack_RepeatOne_FallsBackInsteadOfEnding(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.joinStage("stage")

	h.e.ReloadSoloKaraokeQueue([]*Track{
		soloKaraokeTestTrack("sk1", "Song One"),
		soloKaraokeTestTrack("sk2", "Song Two"),
	})
	h.sync(func() {})
	h.soloKaraokeMsg("admin", cmsgAdminSoloKaraokePlay, nil)
	h.soloKaraokeMsg("admin", cmsgAdminSoloKaraokeSetRepeat, soloKaraokeSetRepeatData{Repeat: "one"})

	// Admin deletes the currently-playing track (sk1) from the Playlist bucket
	// mid-song — ReloadSoloKaraokeQueue can't find it anymore, index -> -1.
	h.e.ReloadSoloKaraokeQueue([]*Track{soloKaraokeTestTrack("sk2", "Song Two")})
	h.sync(func() {})

	h.stagePlayerState("stage", 200000, false, true)

	var active bool
	var cur string
	h.sync(func() {
		active = h.e.soloKaraoke.active
		if h.e.soloKaraoke.currentTrack != nil {
			cur = h.e.soloKaraoke.currentTrack.ID
		}
	})
	if !active {
		t.Fatalf("expected solo karaoke to fall back to sk2 instead of ending the session (s10eng-03 regression)")
	}
	if cur != "sk2" {
		t.Fatalf("expected fallback to play sk2, got %q", cur)
	}
}

// TestSoloKaraoke_PlayFailure_SurfacesError is the regression test for
// s10eng-07: a failed audio.Play must broadcast smsgSoloKaraokeError to
// admin instead of only being logged server-side with no client-visible signal.
func TestSoloKaraoke_PlayFailure_SurfacesError(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.joinStage("stage")

	h.e.ReloadSoloKaraokeQueue([]*Track{soloKaraokeTestTrack("sk1", "Song One")})
	h.sync(func() {})

	h.audio.mu.Lock()
	h.audio.playErr = fmt.Errorf("injected spotify failure")
	h.audio.mu.Unlock()

	h.bcast.reset()
	h.soloKaraokeMsg("admin", cmsgAdminSoloKaraokePlay, nil)

	found := false
	h.bcast.mu.Lock()
	for _, f := range h.bcast.frames {
		if f.role == protocol.RoleAdmin && f.env.Type == smsgSoloKaraokeError {
			found = true
		}
	}
	h.bcast.mu.Unlock()
	if !found {
		t.Fatalf("expected a smsgSoloKaraokeError frame broadcast to admin after a failed Play")
	}
}

// TestSoloKaraoke_NowPlaying_IncludesPaused is the regression test for
// s10eng-05: the full-metadata now-playing payload must carry the current
// paused state so a (re)connecting admin tab's Play/Pause toggle has an
// authoritative signal from this one frame, not just a hardcoded false.
func TestSoloKaraoke_NowPlaying_IncludesPaused(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.joinStage("stage")

	h.e.ReloadSoloKaraokeQueue([]*Track{soloKaraokeTestTrack("sk1", "Song One")})
	h.sync(func() {})
	h.soloKaraokeMsg("admin", cmsgAdminSoloKaraokePlay, nil)
	h.soloKaraokeMsg("admin", cmsgAdminSoloKaraokePause, nil)

	var d soloKaraokeNowPlayingData
	h.sync(func() { d = h.e.soloKaraokeNowPlayingData() })
	if !d.Paused {
		t.Fatalf("expected soloKaraokeNowPlayingData().Paused = true after pause, got false")
	}
}
