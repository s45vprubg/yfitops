package game

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/s45vprubg/yfitops/server/internal/protocol"
)

// fakeDropPointStore is a controllable game.DropPointStore for testing the
// engine's Mark/Jump Drop Point handlers without a real Postgres instance.
// The never-clobber-manual upsert rule itself lives in the real SQL (see
// store/admin_store.go's SetDropPoint and the DB-gated staging test
// alongside it) — this fake just records what it was told, so these tests
// can focus on the engine's own behavior (what it calls, what it broadcasts,
// what it leaves alone).
//
// eng7-1 (QA sweep 7): GetDropPoint/SetDropPoint now run off the engine's Run
// loop in a goroutine (see refreshCurDropPoint/onAdminSetDropPoint), so this
// fake is read/written from more than one goroutine for real — a mutex is no
// longer optional, and getDelay/setErr let tests exercise the async paths
// deliberately (a slow store, a failing write).
type fakeDropPointStore struct {
	mu       sync.Mutex
	points   map[string]int64
	source   map[string]string
	getDelay time.Duration // artificial delay before GetDropPoint returns (eng7-1 repro)
	setErr   error         // if non-nil, SetDropPoint fails instead of writing (eng7-3 repro)
	gets     chan struct{} // if non-nil, GetDropPoint sends after each call completes
	sets     chan struct{} // if non-nil, SetDropPoint sends after each call completes
}

func newFakeDropPointStore() *fakeDropPointStore {
	return &fakeDropPointStore{points: map[string]int64{}, source: map[string]string{}}
}

func (f *fakeDropPointStore) GetDropPoint(_ context.Context, spotifyURI string) (int64, bool, error) {
	if f.getDelay > 0 {
		time.Sleep(f.getDelay)
	}
	f.mu.Lock()
	ms, ok := f.points[spotifyURI]
	f.mu.Unlock()
	if f.gets != nil {
		f.gets <- struct{}{}
	}
	return ms, ok, nil
}

func (f *fakeDropPointStore) SetDropPoint(_ context.Context, spotifyURI string, positionMs int64, source string) error {
	defer func() {
		if f.sets != nil {
			f.sets <- struct{}{}
		}
	}()
	if f.setErr != nil {
		return f.setErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if existing, ok := f.source[spotifyURI]; ok && existing == "manual" && source != "manual" {
		return nil // never-clobber-manual, mirrored here for engine-level tests
	}
	f.points[spotifyURI] = positionMs
	f.source[spotifyURI] = source
	return nil
}

// pointFor is a thread-safe read for tests observing a write that may still
// be in flight on another goroutine (eng7-1's async SetDropPoint).
func (f *fakeDropPointStore) pointFor(spotifyURI string) (int64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ms, ok := f.points[spotifyURI]
	return ms, ok
}

func (f *fakeDropPointStore) sourceFor(spotifyURI string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.source[spotifyURI]
}

// waitDropPointRefresh gives refreshCurDropPoint's goroutine+submit (eng7-1)
// time to land on the Run loop, by polling e.curDropPointMs ON the loop
// itself (via h.sync) until it matches want or the timeout elapses. Polling
// on the loop (rather than sleeping and reading state directly) keeps every
// read properly synchronized with the single-writer goroutine — no data race,
// no fixed sleep to tune.
func (h *harness) waitDropPointRefresh(timeout time.Duration, want *int64) bool {
	deadline := time.Now().Add(timeout)
	for {
		var got *int64
		h.sync(func() { got = h.e.curDropPointMs })
		switch {
		case want == nil && got == nil:
			return true
		case want != nil && got != nil && *got == *want:
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// waitForErrorCode polls h.bcast for a SMsgError frame carrying code, up to
// timeout. Used for errors reported asynchronously after a store call lands
// (eng7-3's dropPointSaveFailed).
func (h *harness) waitForErrorCode(timeout time.Duration, code string) bool {
	deadline := time.Now().Add(timeout)
	for {
		h.bcast.mu.Lock()
		for _, f := range h.bcast.frames {
			if f.env.Type == protocol.SMsgError {
				var ed protocol.ErrorData
				_ = json.Unmarshal(f.env.Data, &ed)
				if ed.Code == code {
					h.bcast.mu.Unlock()
					return true
				}
			}
		}
		h.bcast.mu.Unlock()
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func int64p(v int64) *int64 { return &v }

// TestOnAdminSetDropPoint_MarksCurrentTrack covers the happy path: marking
// writes to the store as "manual" and immediately updates the engine's own
// cache (so a following Jump works without waiting on a broadcast or on the
// (now async, eng7-1) DB write itself).
func TestOnAdminSetDropPoint_MarksCurrentTrack(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	store := newFakeDropPointStore()
	store.sets = make(chan struct{}, 4)
	h.sync(func() { h.e.dropPointStore = store })
	h.joinAdmin("admin")
	h.selectCell("admin", 1, 1)

	const markAt = int64(30000)
	d, _ := json.Marshal(adminSetDropPointData{PositionMs: markAt})
	h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: cmsgAdminSetDropPoint, Data: d, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})

	// e.curDropPointMs is the engine's own optimistic cache: it updates
	// synchronously (the value is the admin's own input, no DB round-trip
	// needed to know it), so this must already be true with no wait.
	h.sync(func() {
		if h.e.curDropPointMs == nil || *h.e.curDropPointMs != markAt {
			t.Errorf("e.curDropPointMs = %v, want %d", h.e.curDropPointMs, markAt)
		}
	})

	// The actual persistence runs off-loop (eng7-1); wait for it to land
	// before inspecting the store.
	select {
	case <-store.sets:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for SetDropPoint to be called")
	}
	if got, ok := store.pointFor("spotify:track:secret"); !ok || got != markAt {
		t.Errorf("store.pointFor = %d, %v; want %d, true", got, ok, markAt)
	}
	if store.sourceFor("spotify:track:secret") != "manual" {
		t.Errorf("source = %q, want manual", store.sourceFor("spotify:track:secret"))
	}
}

// TestOnAdminSetDropPoint_NilStoreNoops covers the optional-dependency case:
// no Postgres (nil dropPointStore) must make the handler a safe no-op, not a
// panic — matching every other optional-port's nil-safety in this codebase.
// eng7-3: it must also now surface a distinct sendError instead of a silent
// no-op.
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
	if !h.waitForErrorCode(500*time.Millisecond, "dropPointDisabled") {
		t.Error("expected a dropPointDisabled error, got none")
	}
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
	if !h.waitDropPointRefresh(time.Second, int64p(markedAt)) {
		t.Fatal("timed out waiting for refreshCurDropPoint's async result to land")
	}

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
	if !h.waitDropPointRefresh(time.Second, int64p(markedAt)) {
		t.Fatal("timed out waiting for refreshCurDropPoint's async result to land")
	}

	h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: cmsgAdminJumpToDropPoint, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})

	if last, ok := h.audio.last(); !ok || last.pos != 0 {
		t.Errorf("audio.last() = %+v, want pos=0 (clamped)", last)
	}
}

// TestOnAdminJumpToDropPoint_FeatureDisabledErrors covers eng7-4: with no
// dropPointStore configured at all, Jump must report a cause distinct from
// "no mark exists" (noDropPoint) or "no track" (dropPointNoTrack) — an
// operator seeing this on-site needs to know the feature itself is off, not
// start hunting for a missing mark.
func TestOnAdminJumpToDropPoint_FeatureDisabledErrors(t *testing.T) {
	h := newHarness(t) // dropPointStore stays nil
	defer h.run()()
	h.joinAdmin("admin")
	h.selectCell("admin", 1, 1)
	h.bcast.reset()

	h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: cmsgAdminJumpToDropPoint, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})

	foundErr := false
	h.bcast.mu.Lock()
	for _, f := range h.bcast.frames {
		if f.env.Type == protocol.SMsgError {
			var ed protocol.ErrorData
			_ = json.Unmarshal(f.env.Data, &ed)
			if ed.Code == "dropPointDisabled" {
				foundErr = true
			}
		}
	}
	h.bcast.mu.Unlock()
	if !foundErr {
		t.Error("expected a dropPointDisabled error, got none")
	}
}

// TestOnAdminJumpToDropPoint_NoTrackErrors covers eng7-4's third cause: a
// store is configured but no track is currently playing (e.g. still on the
// board, nothing selected yet) — distinct from both "feature off" and "no
// mark on this track".
func TestOnAdminJumpToDropPoint_NoTrackErrors(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	store := newFakeDropPointStore()
	h.sync(func() { h.e.dropPointStore = store })
	h.joinAdmin("admin")
	// Deliberately no selectCell: engine is on the board, e.curTrack is nil.
	h.bcast.reset()

	h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: cmsgAdminJumpToDropPoint, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})

	foundErr := false
	h.bcast.mu.Lock()
	for _, f := range h.bcast.frames {
		if f.env.Type == protocol.SMsgError {
			var ed protocol.ErrorData
			_ = json.Unmarshal(f.env.Data, &ed)
			if ed.Code == "dropPointNoTrack" {
				foundErr = true
			}
		}
	}
	h.bcast.mu.Unlock()
	if !foundErr {
		t.Error("expected a dropPointNoTrack error, got none")
	}
}

// TestOnAdminSetDropPoint_NoTrackErrors covers eng7-3's second branch: a
// store is configured but nothing is currently playing.
func TestOnAdminSetDropPoint_NoTrackErrors(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	store := newFakeDropPointStore()
	h.sync(func() { h.e.dropPointStore = store })
	h.joinAdmin("admin")
	// Deliberately no selectCell: e.curTrack is nil.

	d, _ := json.Marshal(adminSetDropPointData{PositionMs: 1000})
	h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: cmsgAdminSetDropPoint, Data: d, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})

	if !h.waitForErrorCode(500*time.Millisecond, "dropPointNoTrack") {
		t.Error("expected a dropPointNoTrack error, got none")
	}
}

// TestOnAdminSetDropPoint_SaveFailureErrors covers eng7-3's third branch: the
// DB write itself fails. eng7-1 moved this call off the Run loop, so the
// error now arrives asynchronously via e.submit — this test proves it still
// surfaces, not that it's silently dropped along with the goroutine.
func TestOnAdminSetDropPoint_SaveFailureErrors(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	store := newFakeDropPointStore()
	store.setErr = errors.New("db unavailable")
	h.sync(func() { h.e.dropPointStore = store })
	h.joinAdmin("admin")
	h.selectCell("admin", 1, 1)

	d, _ := json.Marshal(adminSetDropPointData{PositionMs: 1000})
	h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: cmsgAdminSetDropPoint, Data: d, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})

	if !h.waitForErrorCode(500*time.Millisecond, "dropPointSaveFailed") {
		t.Error("expected a dropPointSaveFailed error, got none")
	}
}

// TestOnAdminSetDropPoint_RejectsOutOfRange covers eng7-2: a positionMs
// outside [0, DurationMs] must be rejected outright (sendError), not clamped
// and not forwarded to the store — this value is persisted and reused across
// every future placement of the song, so a silently "corrected" value is as
// bad as an unchecked one.
func TestOnAdminSetDropPoint_RejectsOutOfRange(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	store := newFakeDropPointStore()
	store.sets = make(chan struct{}, 4)
	h.sync(func() { h.e.dropPointStore = store })
	h.joinAdmin("admin")
	h.selectCell("admin", 1, 1) // testBoard's (1,1) track has DurationMs: 200000

	for _, bad := range []int64{-1, 200001} {
		d, _ := json.Marshal(adminSetDropPointData{PositionMs: bad})
		h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: cmsgAdminSetDropPoint, Data: d, Nonce: h.gate.Current()}, nowMs())
		h.sync(func() {})
	}
	if !h.waitForErrorCode(500*time.Millisecond, "dropPointOutOfRange") {
		t.Error("expected a dropPointOutOfRange error, got none")
	}
	h.sync(func() {
		if h.e.curDropPointMs != nil {
			t.Errorf("e.curDropPointMs = %v, want nil — out-of-range input must never reach the cache", h.e.curDropPointMs)
		}
	})
	select {
	case <-store.sets:
		t.Error("SetDropPoint was called with out-of-range input; it must be rejected before reaching the store")
	case <-time.After(50 * time.Millisecond):
		// expected: never called
	}
}

// TestOnAdminSeek_ClampsOutOfRangePosition covers eng7-2's other caller:
// onAdminSeek must clamp (not forward verbatim) a positionMs outside the
// track's bounds, since seekTo's clamp is shared by both callers.
func TestOnAdminSeek_ClampsOutOfRangePosition(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.selectCell("admin", 1, 1) // DurationMs: 200000

	cases := []struct {
		name string
		in   int64
		want int64
	}{
		{"negative clamps to zero", -5000, 0},
		{"past track end clamps to duration", 999999, 200000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, _ := json.Marshal(adminSeekData{PositionMs: c.in})
			h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: cmsgAdminSeek, Data: d, Nonce: h.gate.Current()}, nowMs())
			h.sync(func() {})
			if last, ok := h.audio.last(); !ok || last.pos != c.want {
				t.Errorf("audio.last() = %+v, want pos=%d (input %d)", last, c.want, c.in)
			}
		})
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
	if !h.waitDropPointRefresh(time.Second, int64p(markedAt)) {
		t.Fatal("timed out waiting for refreshCurDropPoint's async result to land")
	}
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

// TestRefreshCurDropPoint_SlowStoreDoesNotBlockRunLoop is the eng7-1 repro,
// adapted from the QA sweep 7 validator's throwaway test: a slow
// DropPointStore.GetDropPoint must NOT block the Run loop. Before the fix,
// refreshCurDropPoint called GetDropPoint inline on the loop, so a slow
// store blocked every subsequent command for the full store latency. After
// the fix, the slow call runs in a goroutine and a trivial command queued
// right behind it returns almost immediately regardless.
func TestRefreshCurDropPoint_SlowStoreDoesNotBlockRunLoop(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	store := newFakeDropPointStore()
	store.getDelay = 300 * time.Millisecond
	h.sync(func() { h.e.dropPointStore = store })
	h.joinAdmin("admin")

	// selectCell triggers startTrack -> refreshCurDropPoint -> the slow
	// GetDropPoint. Measure how long a TRIVIAL command queued immediately
	// after takes to complete: if the loop were blocked on the store call,
	// this would take >= getDelay.
	d, _ := json.Marshal(protocol.AdminSelectData{Row: 1, Col: 1})
	h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: protocol.CMsgAdminSelect, Data: d, Nonce: h.gate.Current()}, nowMs())

	start := time.Now()
	h.sync(func() {})
	elapsed := time.Since(start)

	if elapsed >= store.getDelay {
		t.Errorf("a trivial command took %v to complete after selectCell (store getDelay %v) — the Run loop was blocked on the slow drop-point store", elapsed, store.getDelay)
	}
	if elapsed > 50*time.Millisecond {
		t.Errorf("a trivial command took %v to complete; expected it to return almost immediately (microseconds), not wait on the store at all", elapsed)
	}
}
