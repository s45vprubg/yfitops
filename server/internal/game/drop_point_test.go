package game

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/s45vprubg/yfitops/server/internal/protocol"
)

// fakeDropPointStore is a controllable game.DropPointStore for testing the
// engine's Mark/Jump Drop Point handlers without a real Postgres instance.
// The never-clobber-manual upsert rule itself lives in the real SQL (see
// store/admin_store.go's SetDropPoint and the DB-gated staging test
// alongside it) — this fake just records what it was told, so these tests
// can focus on the engine's own behavior (what it calls, what it broadcasts,
// what it leaves alone).
type fakeDropPointStore struct {
	points map[string]int64 // spotifyURI -> positionMs
	source map[string]string
}

func newFakeDropPointStore() *fakeDropPointStore {
	return &fakeDropPointStore{points: map[string]int64{}, source: map[string]string{}}
}

func (f *fakeDropPointStore) GetDropPoint(_ context.Context, spotifyURI string) (int64, bool, error) {
	ms, ok := f.points[spotifyURI]
	return ms, ok, nil
}

func (f *fakeDropPointStore) SetDropPoint(_ context.Context, spotifyURI string, positionMs int64, source string) error {
	if existing, ok := f.source[spotifyURI]; ok && existing == "manual" && source != "manual" {
		return nil // never-clobber-manual, mirrored here for engine-level tests
	}
	f.points[spotifyURI] = positionMs
	f.source[spotifyURI] = source
	return nil
}

// TestOnAdminSetDropPoint_MarksCurrentTrack covers the happy path: marking
// writes to the store as "manual" and immediately updates the engine's own
// cache (so a following Jump works without waiting on a broadcast).
func TestOnAdminSetDropPoint_MarksCurrentTrack(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	store := newFakeDropPointStore()
	h.sync(func() { h.e.dropPointStore = store })
	h.joinAdmin("admin")
	h.selectCell("admin", 1, 1)

	const markAt = int64(30000)
	d, _ := json.Marshal(adminSetDropPointData{PositionMs: markAt})
	h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: cmsgAdminSetDropPoint, Data: d, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})

	if got, ok, err := store.GetDropPoint(context.Background(), "spotify:track:secret"); err != nil || !ok || got != markAt {
		t.Errorf("store.GetDropPoint = %d, %v, %v; want %d, true, nil", got, ok, err, markAt)
	}
	if store.source["spotify:track:secret"] != "manual" {
		t.Errorf("source = %q, want manual", store.source["spotify:track:secret"])
	}
	h.sync(func() {
		if h.e.curDropPointMs == nil || *h.e.curDropPointMs != markAt {
			t.Errorf("e.curDropPointMs = %v, want %d", h.e.curDropPointMs, markAt)
		}
	})
}

// TestOnAdminSetDropPoint_NilStoreNoops covers the optional-dependency case:
// no Postgres (nil dropPointStore) must make the handler a safe no-op, not a
// panic — matching every other optional-port's nil-safety in this codebase.
func TestOnAdminSetDropPoint_NilStoreNoops(t *testing.T) {
	h := newHarness(t) // dropPointStore is nil by default
	defer h.run()()
	h.joinAdmin("admin")
	h.selectCell("admin", 1, 1)

	d, _ := json.Marshal(adminSetDropPointData{PositionMs: 1000})
	h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: cmsgAdminSetDropPoint, Data: d, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {
		if h.e.curDropPointMs != nil {
			t.Errorf("e.curDropPointMs = %v, want nil with no store", h.e.curDropPointMs)
		}
	})
}

// TestOnAdminJumpToDropPoint_NoDropPointErrors covers the UI's disabled-
// button-didn't-stop-the-click case: jumping with nothing marked must
// surface a specific "noDropPoint" error, not silently do nothing or seek
// to a garbage position.
func TestOnAdminJumpToDropPoint_NoDropPointErrors(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	store := newFakeDropPointStore()
	h.sync(func() { h.e.dropPointStore = store })
	h.joinAdmin("admin")
	h.selectCell("admin", 1, 1) // starts the round's own track — audio.Play fires once, legitimately

	var callsBefore int
	h.sync(func() { callsBefore = len(h.audio.calls) })
	h.bcast.reset()

	h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: cmsgAdminJumpToDropPoint, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})

	foundErr := false
	h.bcast.mu.Lock()
	for _, f := range h.bcast.frames {
		if f.env.Type == protocol.SMsgError {
			var ed protocol.ErrorData
			_ = json.Unmarshal(f.env.Data, &ed)
			if ed.Code == "noDropPoint" {
				foundErr = true
			}
		}
	}
	h.bcast.mu.Unlock()
	if !foundErr {
		t.Error("expected a noDropPoint error, got none")
	}
	var callsAfter int
	h.sync(func() { callsAfter = len(h.audio.calls) })
	if callsAfter != callsBefore {
		t.Errorf("audio.Play was called by the jump attempt despite no drop point existing (calls %d -> %d)", callsBefore, callsAfter)
	}
}

// TestOnAdminJumpToDropPoint_SeeksWithBufferWithoutTouchingDecayClock is the
// Jump equivalent of the scrubber's own decay-independence regression test:
// jumping must land at (markedPosition - dropPointSeekBufferMs) via the same
// shared seekTo path, and must NOT re-anchor trackStartMs/pausedAtMs — the
// same deliberate decision as manual seeking, for the same reason (jumping
// into a song can land well past scoring.go's 60s decay window).
func TestOnAdminJumpToDropPoint_SeeksWithBufferWithoutTouchingDecayClock(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	store := newFakeDropPointStore()
	h.sync(func() { h.e.dropPointStore = store })
	h.joinAdmin("admin")
	h.joinStage("stage")
	h.selectCell("admin", 1, 1)

	const markedAt = int64(90000)
	_ = store.SetDropPoint(context.Background(), "spotify:track:secret", markedAt, "manual")
	h.sync(func() { h.e.refreshCurDropPoint() })

	var trackStartBefore, pausedAtBefore int64
	h.sync(func() {
		trackStartBefore = h.e.trackStartMs
		pausedAtBefore = h.e.pausedAtMs
	})
	h.bcast.reset()

	h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: cmsgAdminJumpToDropPoint, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})

	want := markedAt - dropPointSeekBufferMs
	if last, ok := h.audio.last(); !ok || last.method != "play" || last.pos != want {
		t.Errorf("audio.last() = %+v, want play @ %d", last, want)
	}

	foundSeekFrame := false
	h.bcast.mu.Lock()
	for _, f := range h.bcast.frames {
		if f.env.Type == protocol.SMsgAudio {
			var ad protocol.AudioData
			_ = json.Unmarshal(f.env.Data, &ad)
			if ad.Action == "play" && ad.PositionMs == want {
				foundSeekFrame = true
			}
		}
	}
	h.bcast.mu.Unlock()
	if !foundSeekFrame {
		t.Errorf("expected an SMsgAudio{action:play, positionMs:%d} frame after jump", want)
	}

	var trackStartAfter, pausedAtAfter int64
	h.sync(func() {
		trackStartAfter = h.e.trackStartMs
		pausedAtAfter = h.e.pausedAtMs
	})
	if trackStartAfter != trackStartBefore || pausedAtAfter != pausedAtBefore {
		t.Errorf("trackStartMs/pausedAtMs changed on jump: before (%d,%d) after (%d,%d) — decay clock must stay independent of a drop-point jump",
			trackStartBefore, pausedAtBefore, trackStartAfter, pausedAtAfter)
	}
}

// TestOnAdminJumpToDropPoint_ClampsToZero covers a mark near the very start
// of a track: the buffer must never push the seek target negative.
func TestOnAdminJumpToDropPoint_ClampsToZero(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	store := newFakeDropPointStore()
	h.sync(func() { h.e.dropPointStore = store })
	h.joinAdmin("admin")
	h.selectCell("admin", 1, 1)

	const markedAt = int64(1000) // less than dropPointSeekBufferMs
	_ = store.SetDropPoint(context.Background(), "spotify:track:secret", markedAt, "manual")
	h.sync(func() { h.e.refreshCurDropPoint() })

	h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: cmsgAdminJumpToDropPoint, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})

	if last, ok := h.audio.last(); !ok || last.pos != 0 {
		t.Errorf("audio.last() = %+v, want pos=0 (clamped)", last)
	}
}

// TestBroadcastPlaybackPosition_IncludesDropPoint covers the admin scrubber
// feed: once a drop point is cached for the current track, every position
// broadcast must carry it, so the UI can render the marker and enable "Jump
// to Drop" without a separate round-trip.
func TestBroadcastPlaybackPosition_IncludesDropPoint(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	store := newFakeDropPointStore()
	h.sync(func() { h.e.dropPointStore = store })
	h.joinAdmin("admin")
	h.joinStage("stage")
	h.selectCell("admin", 1, 1)

	const markedAt = int64(55000)
	_ = store.SetDropPoint(context.Background(), "spotify:track:secret", markedAt, "manual")
	h.sync(func() { h.e.refreshCurDropPoint() })
	h.bcast.reset()

	d, _ := json.Marshal(protocol.StagePlayerStateData{PositionMs: 5000})
	h.e.OnMessage("stage", protocol.RoleStage, protocol.ClientEnvelope{Type: protocol.CMsgStagePlayerState, Data: d, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})

	var got playbackPositionData
	found := false
	h.bcast.mu.Lock()
	for _, f := range h.bcast.frames {
		if f.env.Type == smsgPlaybackPosition && f.role == protocol.RoleAdmin {
			_ = json.Unmarshal(f.env.Data, &got)
			found = true
		}
	}
	h.bcast.mu.Unlock()
	if !found {
		t.Fatal("expected a playbackPosition frame to RoleAdmin, got none")
	}
	if !got.HasDropPoint || got.DropPointMs != markedAt {
		t.Errorf("playbackPositionData = %+v, want HasDropPoint=true DropPointMs=%d", got, markedAt)
	}
}
