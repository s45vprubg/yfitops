package game

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/s45vprubg/yfitops/server/internal/anticheat"
	"github.com/s45vprubg/yfitops/server/internal/protocol"
)

// engine_test.go exercises the full lifecycle (design_doc §3) against in-memory
// fakes for every dependency. The most important test is the sanitization one:
// mobile must NEVER receive track metadata (§4A).

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// fakeLock is an in-memory atomic single-winner buzz lock (mirrors Redis SET NX).
type fakeLock struct {
	mu     sync.Mutex
	owners map[string]string
}

func newFakeLock() *fakeLock { return &fakeLock{owners: map[string]string{}} }

func (l *fakeLock) TryAcquire(_ context.Context, key, playerID string) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, taken := l.owners[key]; taken {
		return false, nil
	}
	l.owners[key] = playerID
	return true, nil
}
func (l *fakeLock) Release(_ context.Context, key string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.owners, key)
	return nil
}
func (l *fakeLock) Holder(_ context.Context, key string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.owners[key], nil
}

type fakeRepo struct {
	mu     sync.Mutex
	scores map[string]int
	events []string
}

func newFakeRepo() *fakeRepo { return &fakeRepo{scores: map[string]int{}} }

func (r *fakeRepo) CreateSession(context.Context, *Session) error { return nil }
func (r *fakeRepo) SaveScore(_ context.Context, _, playerID, _ string, score int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scores[playerID] = score
	return nil
}
func (r *fakeRepo) LogEvent(_ context.Context, _, kind string, _ map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, kind)
	return nil
}
func (r *fakeRepo) LoadBoard(context.Context, string) (*Board, error) { return nil, nil }
func (r *fakeRepo) Leaderboard(context.Context, int) ([]protocol.ScoreEntry, error) {
	return nil, nil
}
func (r *fakeRepo) MarkTrackPlayed(context.Context, string, string) error { return nil }
func (r *fakeRepo) ClearPlayed(context.Context, string) error             { return nil }

type audioCall struct {
	method string
	uri    string
	pos    int64
	vol    int
}

type fakeAudio struct {
	mu           sync.Mutex
	calls        []audioCall
	volumeErr    error // injected failure for SetVolume, to test the restore-is-still-attempted guarantee
	volumeNow    int
	volumeNowSet bool
	// volumeDelay, if set, sleeps for this long on the FIRST SetVolume call
	// only (then clears itself) — simulates a slow network call to prove
	// (s8eng-05) that outbound SetVolume calls are APPLIED in strict
	// enqueue order even when an earlier call is slower to execute than a
	// later one.
	volumeDelay time.Duration
}

func (a *fakeAudio) SetDevice(string) {}
func (a *fakeAudio) Play(_ context.Context, uri string, pos int64) error {
	a.add(audioCall{method: "play", uri: uri, pos: pos})
	return nil
}
func (a *fakeAudio) Pause(context.Context) error  { a.add(audioCall{method: "pause"}); return nil }
func (a *fakeAudio) Resume(context.Context) error { a.add(audioCall{method: "resume"}); return nil }
func (a *fakeAudio) SetVolume(_ context.Context, percent int) error {
	a.mu.Lock()
	delay := a.volumeDelay
	a.volumeDelay = 0 // one-shot: only the first call is slowed
	a.volumeNow = percent
	a.volumeNowSet = true
	err := a.volumeErr
	a.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	a.add(audioCall{method: "setVolume", vol: percent})
	return err
}
func (a *fakeAudio) AuthURL(string) string                  { return "" }
func (a *fakeAudio) Exchange(context.Context, string) error { return nil }
func (a *fakeAudio) add(c audioCall) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, c)
}
func (a *fakeAudio) last() (audioCall, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.calls) == 0 {
		return audioCall{}, false
	}
	return a.calls[len(a.calls)-1], true
}
func (a *fakeAudio) ofMethod(method string) []audioCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []audioCall
	for _, c := range a.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

type fakeLyrics struct{}

func (fakeLyrics) Fetch(context.Context, string, string, int) ([]protocol.LyricLine, error) {
	return []protocol.LyricLine{{TimeMs: 0, Text: "secret lyric line"}}, nil
}

// sentFrame records who a frame went to and the raw payload.
type sentFrame struct {
	role   protocol.Role // role for Broadcast; "" for SendTo/BroadcastAll
	connID string        // for SendTo
	all    bool          // BroadcastAll
	env    protocol.ServerEnvelope
}

// fakeBcast records every emitted frame. connRole lets the sanitization test
// know which role a SendTo connection belongs to.
type fakeBcast struct {
	mu       sync.Mutex
	frames   []sentFrame
	connRole map[string]protocol.Role
}

func newFakeBcast() *fakeBcast {
	return &fakeBcast{connRole: map[string]protocol.Role{}}
}
func (b *fakeBcast) SendTo(connID string, env protocol.ServerEnvelope) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.frames = append(b.frames, sentFrame{connID: connID, env: env})
}
func (b *fakeBcast) Broadcast(role protocol.Role, env protocol.ServerEnvelope) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.frames = append(b.frames, sentFrame{role: role, env: env})
}
func (b *fakeBcast) BroadcastAll(env protocol.ServerEnvelope) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.frames = append(b.frames, sentFrame{all: true, env: env})
}

// mobileFrames returns every frame a mobile client could possibly receive:
// BroadcastAll, Broadcast(mobile), and SendTo a mobile-role connection.
func (b *fakeBcast) mobileFrames() []sentFrame {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := []sentFrame{}
	for _, f := range b.frames {
		switch {
		case f.all:
			out = append(out, f)
		case f.role == protocol.RoleMobile:
			out = append(out, f)
		case f.connID != "" && b.connRole[f.connID] == protocol.RoleMobile:
			out = append(out, f)
		}
	}
	return out
}

func (b *fakeBcast) reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.frames = nil
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

type harness struct {
	e     *Engine
	bcast *fakeBcast
	lock  *fakeLock
	repo  *fakeRepo
	audio *fakeAudio
	gate  *anticheat.NonceGate
	t     *testing.T
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	bc := newFakeBcast()
	lk := newFakeLock()
	repo := newFakeRepo()
	audio := &fakeAudio{}
	gate := anticheat.NewNonceGate([]byte("test-secret"))
	e := NewEngine(repo, lk, audio, fakeLyrics{}, nil, nil, bc, gate, Config{
		SessionID:        "s1",
		SkipThresholdPct: 50,
		// Resolve buzzes synchronously (negative sentinel) so existing
		// deterministic tests see the old serial first-buzz-wins behavior — the
		// first buzz opens and immediately resolves a 1-contender window. The
		// async fairness window (BuzzWindowMs > 0) is exercised separately in
		// TestBuzz_FairestArrivalWinsWithinWindow.
		BuzzWindowMs: -1,
		Rand:         rand.New(rand.NewSource(1)),
	})
	e.SetBoard(testBoard())
	return &harness{e: e, bcast: bc, lock: lk, repo: repo, audio: audio, gate: gate, t: t}
}

func testBoard() *Board {
	cell := func(row, col int, dd bool, n int) *Cell {
		c := &Cell{Row: row, Col: col, Category: "Nu Metal", DailyDouble: dd}
		if dd {
			// Manually-flagged test cells bypass assignDailyDoubles(), which is
			// what normally sets this; the trigger fires on the cell's 2nd visit.
			c.DDTriggerVisit = 2
		}
		for i := 0; i < n; i++ {
			c.Tracks = append(c.Tracks, &Track{
				ID:         strings.Join([]string{"t", string(rune('0' + row)), string(rune('0' + col)), string(rune('a' + i))}, ""),
				SpotifyURI: "spotify:track:secret",
				Artist:     "Limp Bizkit",
				Song:       "Rollin",
				AlbumArt:   "art.jpg",
				DurationMs: 200000,
				Playable:   true,
			})
		}
		return c
	}
	return &Board{
		Rows: 2, Cols: 2,
		Cells: [][]*Cell{
			// (1,2) is the daily-double cell: 2 tracks so its 1st pick plays a
			// normal round and its 2nd pick triggers the Daily Double offer.
			{cell(1, 1, false, 2), cell(1, 2, true, 2)},
			{cell(5, 1, false, 3), cell(5, 2, false, 1)},
		},
		DailyDoubleCount: 1,
		DDBucket: []*Track{
			{ID: "dd-bucket-1", SpotifyURI: "spotify:track:ddsecret1", Artist: "Daily Double Artist", Song: "Daily Double Song", DurationMs: 180000, Playable: true},
		},
	}
}

// run starts the engine loop and returns a stop func.
func (h *harness) run() func() {
	ctx, cancel := context.WithCancel(context.Background())
	go h.e.Run(ctx)
	// Wait until the loop is live by round-tripping a sync command.
	h.e.submitSync(func() {})
	return cancel
}

// sync runs fn on the loop and blocks.
func (h *harness) sync(fn func()) { h.e.submitSync(fn) }

// join registers a mobile player and returns its connID + playerID.
func (h *harness) join(connID, fp, handle string) string {
	h.bcast.mu.Lock()
	h.bcast.connRole[connID] = protocol.RoleMobile
	h.bcast.mu.Unlock()
	h.e.OnConnect(connID, "10.0.0."+connID)
	hello, _ := json.Marshal(protocol.HelloData{Role: protocol.RoleMobile, Handle: handle, DeviceFP: fp})
	h.e.OnMessage(connID, protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgHello, Data: hello}, nowMs())
	var pid string
	h.sync(func() { pid = h.e.reg.playerForConn(connID).ID })
	return pid
}

func (h *harness) joinAdmin(connID string) {
	h.bcast.mu.Lock()
	h.bcast.connRole[connID] = protocol.RoleAdmin
	h.bcast.mu.Unlock()
	h.e.OnConnect(connID, "")
	hello, _ := json.Marshal(protocol.HelloData{Role: protocol.RoleAdmin})
	h.e.OnMessage(connID, protocol.RoleAdmin, protocol.ClientEnvelope{Type: protocol.CMsgHello, Data: hello}, nowMs())
	h.sync(func() {})
}

func (h *harness) joinStage(connID string) {
	h.bcast.mu.Lock()
	h.bcast.connRole[connID] = protocol.RoleStage
	h.bcast.mu.Unlock()
	h.e.OnConnect(connID, "")
	hello, _ := json.Marshal(protocol.HelloData{Role: protocol.RoleStage})
	h.e.OnMessage(connID, protocol.RoleStage, protocol.ClientEnvelope{Type: protocol.CMsgHello, Data: hello}, nowMs())
	h.sync(func() {})
}

func (h *harness) selectCell(adminConn string, row, col int) {
	d, _ := json.Marshal(protocol.AdminSelectData{Row: row, Col: col})
	h.e.OnMessage(adminConn, protocol.RoleAdmin, protocol.ClientEnvelope{Type: protocol.CMsgAdminSelect, Data: d, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
}

// playerSelectCell drives the new player-picker action (cmsgPlayerSelectCell).
func (h *harness) playerSelectCell(connID string, row, col int) {
	d, _ := json.Marshal(playerSelectCellData{Row: row, Col: col})
	h.e.OnMessage(connID, protocol.RoleMobile, protocol.ClientEnvelope{Type: cmsgPlayerSelectCell, Data: d, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
}

func (h *harness) grade(adminConn string, v protocol.GradeVerdict) {
	d, _ := json.Marshal(protocol.AdminGradeData{Verdict: v})
	h.e.OnMessage(adminConn, protocol.RoleAdmin, protocol.ClientEnvelope{Type: protocol.CMsgAdminGrade, Data: d, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
}

// kick drives admin.kick (ban or a plain non-banning kick) against playerID.
func (h *harness) kick(adminConn, playerID string, ban bool) {
	d, _ := json.Marshal(protocol.AdminKickData{PlayerID: playerID, Ban: ban})
	h.e.OnMessage(adminConn, protocol.RoleAdmin, protocol.ClientEnvelope{Type: protocol.CMsgAdminKick, Data: d, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
}

func (h *harness) ddDecide(connID string, accept bool) {
	d, _ := json.Marshal(dailyDoubleDecisionData{Accept: accept})
	h.e.OnMessage(connID, protocol.RoleMobile, protocol.ClientEnvelope{Type: cmsgDailyDoubleDecision, Data: d, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
}

func (h *harness) ddChoose(connID, trackID string) {
	d, _ := json.Marshal(dailyDoubleChooseData{TrackID: trackID})
	h.e.OnMessage(connID, protocol.RoleMobile, protocol.ClientEnvelope{Type: cmsgDailyDoubleChoose, Data: d, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
}

// lastDailyDoubleOfferTrackIDs returns the track IDs from the most recent
// smsgDailyDoubleOffer frame sent to connID (test helper for driving choose).
func (h *harness) lastDailyDoubleOfferTrackIDs(connID string) []string {
	h.bcast.mu.Lock()
	defer h.bcast.mu.Unlock()
	var out []string
	for i := len(h.bcast.frames) - 1; i >= 0; i-- {
		f := h.bcast.frames[i]
		if f.connID == connID && f.env.Type == smsgDailyDoubleOffer {
			var d dailyDoubleOfferData
			_ = json.Unmarshal(f.env.Data, &d)
			for _, s := range d.Songs {
				out = append(out, s.ID)
			}
			return out
		}
	}
	return out
}

func (h *harness) state() protocol.GameState {
	var s protocol.GameState
	h.sync(func() { s = h.e.state })
	return s
}

func (h *harness) score(pid string) int {
	var s int
	h.sync(func() { s = h.e.reg.players[pid].Score })
	return s
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestBuzz_SingleWinner: concurrent buzzes yield exactly one winner and audio
// pauses on the win (§3.4, §9).
func TestBuzz_SingleWinner(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.joinStage("stage")
	p1 := h.join("c1", "fp1", "alice")
	p2 := h.join("c2", "fp2", "bob")
	p3 := h.join("c3", "fp3", "carol")
	_ = p1
	_ = p2
	_ = p3

	h.selectCell("admin", 1, 1)
	if h.state() != protocol.StateRoundActive {
		t.Fatalf("state = %s, want ROUND_ACTIVE", h.state())
	}
	nonce := h.gate.Current()

	// Fire concurrent buzzes from many goroutines.
	var wg sync.WaitGroup
	for _, c := range []string{"c1", "c2", "c3"} {
		wg.Add(1)
		go func(conn string) {
			defer wg.Done()
			h.e.OnMessage(conn, protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: nonce}, nowMs())
		}(c)
	}
	wg.Wait()
	h.sync(func() {}) // drain

	// Count buzzResult{won:true}.
	wins := 0
	var winner string
	h.bcast.mu.Lock()
	for _, f := range h.bcast.frames {
		if f.env.Type == protocol.SMsgBuzzResult {
			var br protocol.BuzzResultData
			_ = json.Unmarshal(f.env.Data, &br)
			if br.Won {
				wins++
				winner = f.connID
			}
		}
	}
	h.bcast.mu.Unlock()
	if wins != 1 {
		t.Fatalf("got %d winners, want exactly 1", wins)
	}
	if winner == "" {
		t.Fatalf("no winning connection recorded")
	}

	// Audio must have paused on the win.
	last, ok := h.audio.last()
	_ = last
	_ = ok
	foundPause := false
	h.bcast.mu.Lock()
	for _, f := range h.bcast.frames {
		if f.env.Type == protocol.SMsgAudio {
			var ad protocol.AudioData
			_ = json.Unmarshal(f.env.Data, &ad)
			if ad.Action == "pause" {
				foundPause = true
			}
		}
	}
	h.bcast.mu.Unlock()
	if !foundPause {
		t.Errorf("expected an audio pause frame after the winning buzz")
	}
	if h.state() != protocol.StateAdjudicate {
		t.Errorf("state = %s, want ADJUDICATE", h.state())
	}
}

// TestBuzz_EarliestArrivalWinsAndForgedRTTBuysNothing proves the buzz-ordering
// contract after QA sweep 3 (s3-fe): within the collection window the winner is
// the contender with the EARLIEST server-stamped arrival, and the client's
// self-reported RTT is IGNORED. The later-arriving player forges a huge RTT
// (which the old §4C path would have turned into an earlier "effective" time and
// a win) and must STILL LOSE to the earlier real arrival — a forged RTT buys no
// advantage. Also pins that the enqueue order does not decide the winner.
func TestBuzz_EarliestArrivalWinsAndForgedRTTBuysNothing(t *testing.T) {
	h := newHarness(t)
	// A very large window so the resolution AfterFunc never fires during the test;
	// we resolve deterministically via submitSync below.
	h.e.buzzWindowMs = 60_000
	defer h.run()()
	h.joinAdmin("admin")
	h.joinStage("stage")
	early := h.join("c1", "fp1", "alice") // earliest ARRIVAL, honest low RTT
	late := h.join("c2", "fp2", "bob")    // later arrival, forges a huge RTT

	h.selectCell("admin", 1, 1)
	if h.state() != protocol.StateRoundActive {
		t.Fatalf("state = %s, want ROUND_ACTIVE", h.state())
	}

	// bob forges RTT 100 (the max-impact value: old code subtracted the 50ms cap
	// from his arrival, handing him the win). alice reports an honest RTT 0. RTT
	// must now be irrelevant to ordering.
	h.sync(func() {
		h.e.reg.players[early].RTTMs = 0
		h.e.reg.players[late].RTTMs = 100
	})

	nonce := h.gate.Current()
	arrivalEarly := nowMs()
	arrivalLate := arrivalEarly + 5 // bob genuinely arrives 5ms LATER
	var rk string
	h.sync(func() { rk = h.e.roundKey })

	// Enqueue bob's buzz FIRST (later arrival, forged RTT), then alice's (earlier
	// arrival). Neither enqueue order nor the forged RTT may decide it — only the
	// server arrival stamp. Both land in the open window; no result sent yet.
	h.e.OnMessage("c2", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: nonce}, arrivalLate)
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: nonce}, arrivalEarly)
	h.sync(func() {})

	// Window is open with two contenders and no winner yet.
	var open bool
	var n int
	h.sync(func() { open, n = h.e.buzzWindowOpen, len(h.e.buzzContenders) })
	if !open || n != 2 {
		t.Fatalf("window state: open=%v contenders=%d, want open with 2", open, n)
	}
	if h.state() != protocol.StateRoundActive {
		t.Fatalf("state advanced before resolution: %s", h.state())
	}

	// Resolve deterministically.
	h.sync(func() { h.e.resolveBuzzWindow(rk) })

	// alice (earliest arrival) must win, despite bob's forged high RTT and despite
	// bob buzzing first. The winning conn asserted below is alice's c1.
	var buzzWinner string
	h.sync(func() { buzzWinner = h.e.buzzWinner })
	if buzzWinner != early {
		t.Fatalf("buzzWinner = %q, want alice (%q) — earliest server arrival must win; a forged RTT must buy nothing", buzzWinner, early)
	}

	// Exactly one Won:true, and it goes to bob's conn; alice gets Won:false.
	wins := map[string]bool{}
	wonCount := 0
	h.bcast.mu.Lock()
	for _, f := range h.bcast.frames {
		if f.env.Type != protocol.SMsgBuzzResult {
			continue
		}
		var br protocol.BuzzResultData
		_ = json.Unmarshal(f.env.Data, &br)
		if br.Won {
			wonCount++
			wins[f.connID] = true
		}
	}
	h.bcast.mu.Unlock()
	if wonCount != 1 {
		t.Fatalf("got %d Won:true frames, want exactly 1", wonCount)
	}
	if !wins["c1"] {
		t.Fatalf("Won:true did not go to alice's conn c1 (earliest arrival); wins=%v", wins)
	}
	if h.state() != protocol.StateAdjudicate {
		t.Fatalf("state = %s, want ADJUDICATE after resolution", h.state())
	}
	_ = late
}

// TestBuzz_StaleNonceDropped: a buzz with a stale nonce is ignored (§4D).
func TestBuzz_StaleNonceDropped(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.join("c1", "fp1", "alice")
	h.selectCell("admin", 1, 1)

	staleNonce := h.gate.Current() - 1 // one behind current
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: staleNonce}, nowMs())
	h.sync(func() {})

	if h.state() != protocol.StateRoundActive {
		t.Fatalf("stale buzz changed state to %s; want ROUND_ACTIVE", h.state())
	}
	// No winner should have been recorded.
	holder, _ := h.lock.Holder(context.Background(), h.e.roundKey)
	if holder != "" {
		t.Errorf("stale buzz acquired lock for %q", holder)
	}
}

// TestGrade_CorrectAwardsDecayedPoints: a correct grade awards CurrentPoints
// for the elapsed time and moves to karaoke (§3.6, §7).
func TestGrade_CorrectAwardsDecayedPoints(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	p1 := h.join("c1", "fp1", "alice")
	h.selectCell("admin", 5, 2) // row 5, max 200
	nonce := h.gate.Current()

	// Force a known elapsed time within the hold window so points == max 200.
	h.sync(func() { h.e.trackStartMs = nowMs() })
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: nonce}, nowMs())
	h.sync(func() {})
	h.grade("admin", protocol.VerdictCorrect)

	if got := h.score(p1); got != 200 {
		t.Errorf("score = %d, want 200 (row5 max within hold window)", got)
	}
	if h.state() != protocol.StateKaraoke {
		t.Errorf("state = %s, want KARAOKE", h.state())
	}
}

// TestGrade_PartialLeavesRemainingPool: first partial takes 50, second partial-
// half claims the remainder (§7).
func TestGrade_PartialLeavesRemainingPool(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	p1 := h.join("c1", "fp1", "alice")
	p2 := h.join("c2", "fp2", "bob")
	h.selectCell("admin", 5, 1) // row 5 max 200
	h.sync(func() { h.e.trackStartMs = nowMs() })

	// Player 1 buzzes, graded partial -> +50, remaining 150 alive.
	n := h.gate.Current()
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: n}, nowMs())
	h.sync(func() {})
	h.grade("admin", protocol.VerdictPartial)
	if got := h.score(p1); got != 50 {
		t.Fatalf("p1 partial score = %d, want 50", got)
	}
	var rem int
	h.sync(func() { rem = h.e.partial.remaining })
	if rem != 150 {
		t.Fatalf("remaining pool = %d, want 150", rem)
	}
	if h.state() != protocol.StateRoundActive {
		t.Fatalf("state after partial = %s, want ROUND_ACTIVE", h.state())
	}

	// Player 1 cannot buzz again (already guessed this track, §3.4).
	h.sync(func() { h.e.trackStartMs = nowMs() })
	n = h.gate.Current()
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: n}, nowMs())
	h.sync(func() {})
	if got := h.lockHolderFor(h.e.roundKey); got == p1 {
		t.Errorf("p1 won a second buzz after guessing; should be locked out")
	}

	// Player 2 buzzes and is graded partial (the remaining half) -> claims rest.
	h.e.OnMessage("c2", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: n}, nowMs())
	h.sync(func() {})
	h.grade("admin", protocol.VerdictPartial)
	if got := h.score(p2); got != 50 {
		t.Errorf("p2 second-partial score = %d, want 50", got)
	}
	h.sync(func() { rem = h.e.partial.remaining })
	if rem != 100 {
		t.Errorf("remaining after second partial = %d, want 100", rem)
	}
}

func (h *harness) lockHolderFor(key string) string {
	var k string
	h.sync(func() { k = key })
	holder, _ := h.lock.Holder(context.Background(), k)
	return holder
}

// TestSanitization_MobileNeverGetsTrackMetadata is the MANDATORY §4A test: no
// frame reaching a mobile client may contain artist/song/lyrics/URI.
func TestSanitization_MobileNeverGetsTrackMetadata(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.joinStage("stage")
	p1 := h.join("c1", "fp1", "alice")
	p2 := h.join("c2", "fp2", "bob")
	_ = p1
	_ = p2

	// Drive a full round including reveal + lyrics so trusted payloads exist.
	h.selectCell("admin", 1, 1)
	n := h.gate.Current()
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: n}, nowMs())
	h.sync(func() {})
	h.grade("admin", protocol.VerdictCorrect) // -> reveal + lyrics to stage/admin

	// Inspect every frame a mobile client could receive.
	banned := []string{"Limp Bizkit", "Rollin", "secret lyric line", "spotify:track:secret", "art.jpg"}
	for _, f := range h.bcast.mobileFrames() {
		raw := string(f.env.Data)
		for _, bad := range banned {
			if strings.Contains(raw, bad) {
				t.Errorf("mobile frame %s leaked %q: %s", f.env.Type, bad, raw)
			}
		}
		// Reveal/lyrics/adminView/board/trackStart must never target mobile.
		switch f.env.Type {
		case protocol.SMsgReveal, protocol.SMsgLyrics, protocol.SMsgAdminView, protocol.SMsgTrackStart, protocol.SMsgBoard:
			t.Errorf("mobile received trusted frame type %s", f.env.Type)
		}
	}
}

// TestSkipVote_ThresholdAndDynamicRecalc covers the §3.8 vote math including
// dynamic recalculation when an active user disconnects.
func TestSkipVote_ThresholdAndDynamicRecalc(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	p1 := h.join("c1", "fp1", "a")
	p2 := h.join("c2", "fp2", "b")
	p3 := h.join("c3", "fp3", "c")
	_ = p1
	_ = p2
	_ = p3

	// Solve a track to enter karaoke; pool = 3 active users, 50% => need 2.
	h.selectCell("admin", 1, 1)
	n := h.gate.Current()
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: n}, nowMs())
	h.sync(func() {})
	h.grade("admin", protocol.VerdictCorrect)
	if h.state() != protocol.StateKaraoke {
		t.Fatalf("state = %s, want KARAOKE", h.state())
	}
	var need int
	h.sync(func() { need = h.e.needVotes() })
	if need != 2 { // floor(3*50/100)+1 = 2
		t.Fatalf("need = %d, want 2 for 3 active @50%%", need)
	}

	// One vote: not enough.
	kn := h.gate.Current()
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgVote, Nonce: kn}, nowMs())
	h.sync(func() {})
	if h.state() != protocol.StateKaraoke {
		t.Fatalf("transitioned with 1/2 votes")
	}

	// p3 disconnects -> pool shrinks to 2 active, need = floor(2*50/100)+1 = 2.
	// p1's single vote is still 1, so still short. (Recalc happened.)
	h.e.OnDisconnect("c3")
	h.sync(func() {})
	h.sync(func() { need = h.e.needVotes() })
	if need != 2 {
		t.Fatalf("need after disconnect = %d, want 2", need)
	}
	if h.state() != protocol.StateKaraoke {
		t.Fatalf("transitioned prematurely after disconnect")
	}

	// p2 votes -> 2/2 met. Player-driven cell picking (AND-gate): the vote
	// threshold alone is no longer enough while p1 (the winner, now the
	// designated picker) hasn't chosen the next cell yet.
	h.e.OnMessage("c2", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgVote, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
	if h.state() != protocol.StateKaraoke {
		t.Fatalf("state = %s, want KARAOKE (vote passed but picker hasn't picked)", h.state())
	}

	// p1 (the picker) picks the next cell -> both AND-gate halves now
	// satisfied -> TRANSITION.
	h.playerSelectCell("c1", 5, 1)
	if h.state() != protocol.StateTransition {
		t.Fatalf("state = %s, want TRANSITION after threshold met AND picker picked", h.state())
	}
}

// TestSkipVote_HundredPercentRequiresAll: maxed slider needs EQUAL 100% (§3.8).
func TestSkipVote_HundredPercentRequiresAll(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.join("c1", "fp1", "a")
	h.join("c2", "fp2", "b")

	// Set threshold to 100 before karaoke.
	d, _ := json.Marshal(protocol.AdminSetThreshData{Percent: 100})
	h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: protocol.CMsgAdminSetThresh, Data: d, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})

	h.selectCell("admin", 1, 1)
	n := h.gate.Current()
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: n}, nowMs())
	h.sync(func() {})
	h.grade("admin", protocol.VerdictCorrect)

	var need int
	h.sync(func() { need = h.e.needVotes() })
	if need != 2 {
		t.Fatalf("need at 100%% = %d, want 2 (all active)", need)
	}
	// Only one votes: not enough.
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgVote, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
	if h.state() != protocol.StateKaraoke {
		t.Fatalf("transitioned at 1/2 with 100%% threshold")
	}
	// Second votes: now everyone has — but the AND-gate still needs the
	// picker (c1, this round's winner) to also pick before transitioning.
	h.e.OnMessage("c2", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgVote, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
	if h.state() != protocol.StateKaraoke {
		t.Fatalf("state = %s, want KARAOKE at 2/2 votes but no pick yet", h.state())
	}
	h.playerSelectCell("c1", 5, 1)
	if h.state() != protocol.StateTransition {
		t.Fatalf("state = %s, want TRANSITION at 2/2 votes AND a pick", h.state())
	}
}

// TestGrade_SecondConcurrentGradeIgnored: first-packet-wins (§9). The second
// grade arrives after the state has left ADJUDICATE and is a no-op.
func TestGrade_SecondConcurrentGradeIgnored(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin1")
	h.joinAdmin("admin2")
	p1 := h.join("c1", "fp1", "alice")
	h.selectCell("admin1", 5, 1) // row5 max 200
	h.sync(func() { h.e.trackStartMs = nowMs() })
	n := h.gate.Current()
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: n}, nowMs())
	h.sync(func() {})

	// Two admins grade "simultaneously": correct then partial. Enqueue both
	// before draining so they are sequential on the loop. The first (correct)
	// awards 200 and leaves ADJUDICATE; the second (partial) must be ignored.
	gn := h.gate.Current()
	dc, _ := json.Marshal(protocol.AdminGradeData{Verdict: protocol.VerdictCorrect})
	dp, _ := json.Marshal(protocol.AdminGradeData{Verdict: protocol.VerdictPartial})
	h.e.OnMessage("admin1", protocol.RoleAdmin, protocol.ClientEnvelope{Type: protocol.CMsgAdminGrade, Data: dc, Nonce: gn}, nowMs())
	h.e.OnMessage("admin2", protocol.RoleAdmin, protocol.ClientEnvelope{Type: protocol.CMsgAdminGrade, Data: dp, Nonce: gn}, nowMs())
	h.sync(func() {})

	if got := h.score(p1); got != 200 {
		t.Errorf("score = %d, want 200 (only the first correct grade applied)", got)
	}
	if h.state() != protocol.StateKaraoke {
		t.Errorf("state = %s, want KARAOKE", h.state())
	}
}

// TestDailyDouble_BonusApplied: a correct guess on a daily-double cell enters
// DAILY_DOUBLE, collects ratings, and applies the multiplier bonus (§7).
// TestDailyDouble_BonusApplied drives the full redesigned lifecycle: a normal
// round on the Daily Double cell's 1st pick (which also makes the winner
// lastScorer / the contestant), the 2nd pick triggering the offer instead of a
// track, accept, song choice, crowd rating, and the row-value x multiplier
// payout — then straight back to BOARD (no karaoke).
func TestDailyDouble_BonusApplied(t *testing.T) {
	withFastFade(t) // the payout now lands after a graceful fade, not synchronously
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	performer := h.join("c1", "fp1", "perf")
	rater := h.join("c2", "fp2", "rater")
	_ = rater

	// 1st pick: a normal round on the DD cell so the winner becomes lastScorer.
	h.selectCell("admin", 1, 2)
	if h.state() != protocol.StateRoundActive {
		t.Fatalf("state after 1st pick = %s, want ROUND_ACTIVE", h.state())
	}
	h.sync(func() { h.e.trackStartMs = nowMs() })
	n := h.gate.Current()
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: n}, nowMs())
	h.sync(func() {})
	h.grade("admin", protocol.VerdictCorrect)
	if h.state() != protocol.StateKaraoke {
		t.Fatalf("state after 1st-pick grade = %s, want KARAOKE", h.state())
	}
	if got := h.score(performer); got != 100 {
		t.Fatalf("performer after 1st pick = %d, want 100 (row1 max within hold)", got)
	}
	// Return to board directly (test-only shortcut for the real skip-vote ->
	// transition-countdown -> board flow, which isn't what's under test here).
	h.sync(func() {
		h.e.curTrack = nil
		h.e.curCell = nil
		h.e.state = protocol.StateBoard
	})

	// 2nd pick: no track plays; the offer goes to the contestant (lastScorer).
	h.selectCell("admin", 1, 2)
	if h.state() != protocol.StateDailyDouble {
		t.Fatalf("state after 2nd pick = %s, want DAILY_DOUBLE", h.state())
	}
	if got := h.score(performer); got != 100 {
		t.Fatalf("score changed just from the offer starting: %d, want still 100", got)
	}

	h.ddDecide("c1", true)
	choices := h.lastDailyDoubleOfferTrackIDs("c1")
	if len(choices) == 0 {
		t.Fatalf("contestant received no song choices after accepting")
	}
	h.ddChoose("c1", choices[0])

	// The single rater gives 5 stars -> 2.0x multiplier on the full row1 max(100).
	rd, _ := json.Marshal(protocol.RateData{Stars: 5})
	h.e.OnMessage("c2", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgRate, Data: rd, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})

	// The payout now lands after a graceful fade (fadeOutAndFinishDailyDouble),
	// not synchronously the instant the last rating arrives — wait for it.
	if !waitUntil(t, time.Second, func() bool { return len(h.audio.ofMethod("pause")) > 0 }) {
		t.Fatal("timed out waiting for the Daily Double fade to finish and pause")
	}

	if got := h.score(performer); got != 300 {
		t.Errorf("performer after DD payout = %d, want 300 (100 from round 1 + 200 DD payout)", got)
	}
	if h.state() != protocol.StateBoard {
		t.Errorf("state = %s, want BOARD after DD resolves (no karaoke)", h.state())
	}
}

// TestIncorrect_LocksOutGuesserResumesAudio (§3.6).
func TestIncorrect_LocksOutGuesserResumesAudio(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	p1 := h.join("c1", "fp1", "alice")
	h.selectCell("admin", 1, 1)
	n := h.gate.Current()
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: n}, nowMs())
	h.sync(func() {})
	h.grade("admin", protocol.VerdictIncorrect)

	if got := h.score(p1); got != 0 {
		t.Errorf("incorrect awarded points: %d", got)
	}
	if h.state() != protocol.StateRoundActive {
		t.Errorf("state = %s, want ROUND_ACTIVE after incorrect", h.state())
	}
	var guessed bool
	h.sync(func() { guessed = h.e.reg.players[p1].GuessedThisTrack })
	if !guessed {
		t.Errorf("incorrect guesser not locked out for this track")
	}
	// Audio resume should have been issued.
	if c, _ := h.audio.last(); c.method != "resume" {
		t.Errorf("last audio call = %q, want resume", c.method)
	}
}

// TestEphemeralResume: reconnecting with the same fingerprint resumes score (§3.2).
func TestEphemeralResume(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	p1 := h.join("c1", "fp1", "alice")
	h.sync(func() { h.e.reg.players[p1].Score = 175 })

	h.e.OnDisconnect("c1")
	h.sync(func() {})
	// Same fingerprint, new connection.
	p1b := h.join("c1b", "fp1", "alice")
	if p1b != p1 {
		t.Fatalf("resume created new player %q != %q", p1b, p1)
	}
	if got := h.score(p1b); got != 175 {
		t.Errorf("resumed score = %d, want 175", got)
	}
}

// TestTransitionReturnsToBoard verifies the countdown returns to BOARD (§3.9).
// TestTransition_PickedCellStartsAfterCountdown: player-driven cell picking
// changed what the transition countdown lands on. Previously (no picker
// feature) it always returned to BOARD and waited for the admin; now, once
// both AND-gate halves are satisfied (vote passed AND the picker picked),
// the countdown starts the PICKED cell directly — BOARD is only a
// momentary pass-through, not the resting state.
func TestTransition_PickedCellStartsAfterCountdown(t *testing.T) {
	old := TransitionDelay
	TransitionDelay = 10 * time.Millisecond
	defer func() { TransitionDelay = old }()

	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.join("c1", "fp1", "a")
	h.selectCell("admin", 1, 1)
	n := h.gate.Current()
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: n}, nowMs())
	h.sync(func() {})
	h.grade("admin", protocol.VerdictCorrect)
	// 1 active @50% needs 1 vote.
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgVote, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
	if h.state() != protocol.StateKaraoke {
		t.Fatalf("state = %s, want KARAOKE (vote passed but picker hasn't picked)", h.state())
	}
	h.playerSelectCell("c1", 5, 2)
	if h.state() != protocol.StateTransition {
		t.Fatalf("state = %s, want TRANSITION", h.state())
	}
	time.Sleep(40 * time.Millisecond)
	if h.state() != protocol.StateRoundActive {
		t.Errorf("state = %s, want ROUND_ACTIVE (the picked cell started directly)", h.state())
	}
	var row int
	h.sync(func() { row = h.e.curRow })
	if row != 5 {
		t.Errorf("curRow = %d, want 5 (the picked cell, not an admin/random pick)", row)
	}
}

// TestTransition_NoValidPickFallsBackToBoard: the defensive fallback in
// beginTransition's countdown — if the picked cell somehow isn't valid
// anymore by the time the countdown fires (not reachable in normal play
// today, but guarded rather than assumed), land on BOARD and clear the
// stale picker state instead of leaving the game stuck.
func TestTransition_NoValidPickFallsBackToBoard(t *testing.T) {
	old := TransitionDelay
	TransitionDelay = 10 * time.Millisecond
	defer func() { TransitionDelay = old }()

	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.join("c1", "fp1", "a")
	h.selectCell("admin", 1, 1)
	n := h.gate.Current()
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: n}, nowMs())
	h.sync(func() {})
	h.grade("admin", protocol.VerdictCorrect)
	h.playerSelectCell("c1", 5, 2) // valid pick, recorded but karaokeReady isn't true yet
	if h.state() != protocol.StateKaraoke {
		t.Fatalf("state = %s, want KARAOKE (picked but vote hasn't passed)", h.state())
	}
	// Manually force into TRANSITION with a now-invalid pending pick, to
	// exercise the defensive fallback directly (this exact sequence isn't
	// reachable through the public AND-gate today, by design).
	h.sync(func() {
		h.e.pendingPick.Row, h.e.pendingPick.Col = 99, 99 // no such cell
		h.e.beginTransition()
	})
	if h.state() != protocol.StateTransition {
		t.Fatalf("state = %s, want TRANSITION", h.state())
	}
	time.Sleep(40 * time.Millisecond)
	if h.state() != protocol.StateBoard {
		t.Errorf("state = %s, want BOARD (invalid pick, fell back)", h.state())
	}
	var picker string
	h.sync(func() { picker = h.e.cellPicker })
	if picker != "" {
		t.Errorf("cellPicker = %q, want cleared after the invalid-pick fallback", picker)
	}
}

// TestAdminRoleRequired: a non-admin admin.* message is rejected (§9 auth).
func TestAdminRoleRequired(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.join("c1", "fp1", "alice")
	d, _ := json.Marshal(protocol.AdminAwardData{PlayerID: "fp1", Delta: 9999})
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgAdminAward, Data: d}, nowMs())
	h.sync(func() {})
	if got := h.score("fp1"); got != 0 {
		t.Errorf("mobile-issued admin award applied: score=%d", got)
	}
}

// ---------------------------------------------------------------------------
// Daily Double: assignment, decline reroll, admin skip, sanitization.
// ---------------------------------------------------------------------------

// ddCell builds a Cell with n playable tracks in the given category, for
// assignDailyDoubles/decline test boards (not testBoard()'s shared shape).
func ddCell(row, col int, category string, n int) *Cell {
	c := &Cell{Row: row, Col: col, Category: category}
	for i := 0; i < n; i++ {
		c.Tracks = append(c.Tracks, &Track{
			ID:       fmt.Sprintf("t-%d-%d-%d", row, col, i),
			Playable: true,
		})
	}
	return c
}

// TestAssignDailyDoubles_BalancedAcrossCategories: requesting more Daily
// Doubles than categories must not let any category end up more than 1 ahead
// of any other (the balanced round-robin invariant).
func TestAssignDailyDoubles_BalancedAcrossCategories(t *testing.T) {
	h := newHarness(t)
	defer h.run()()

	board := &Board{
		Rows: 3, Cols: 3,
		Cells: [][]*Cell{
			{ddCell(1, 1, "A", 2), ddCell(1, 2, "B", 2), ddCell(1, 3, "C", 2)},
			{ddCell(2, 1, "A", 2), ddCell(2, 2, "B", 2), ddCell(2, 3, "C", 2)},
			{ddCell(3, 1, "A", 2), ddCell(3, 2, "B", 2), ddCell(3, 3, "C", 2)},
		},
		DailyDoubleCount: 5, // 5 over 3 categories: at least one gets a 2nd
	}
	h.sync(func() {
		h.e.board = board
		h.e.assignDailyDoubles()
	})

	counts := map[string]int{}
	total := 0
	for _, row := range board.Cells {
		for _, c := range row {
			if c.DailyDouble {
				if c.DDTriggerVisit != 2 {
					t.Errorf("cell (%d,%d) flagged but DDTriggerVisit = %d, want 2", c.Row, c.Col, c.DDTriggerVisit)
				}
				counts[c.Category]++
				total++
			}
		}
	}
	if total != 5 {
		t.Fatalf("placed %d daily doubles, want 5", total)
	}
	min, max := -1, -1
	for _, n := range counts {
		if min == -1 || n < min {
			min = n
		}
		if max == -1 || n > max {
			max = n
		}
	}
	if max-min > 1 {
		t.Errorf("category counts %v span more than 1 (min=%d max=%d)", counts, min, max)
	}
}

// TestAssignDailyDoubles_DegradesGracefully: fewer eligible cells than
// requested must place as many as possible, not crash or hang.
func TestAssignDailyDoubles_DegradesGracefully(t *testing.T) {
	h := newHarness(t)
	defer h.run()()

	board := &Board{
		Rows: 1, Cols: 2,
		Cells: [][]*Cell{
			{ddCell(1, 1, "A", 2), ddCell(1, 2, "B", 1)}, // (1,2) has only 1 track: ineligible
		},
		DailyDoubleCount: 5,
	}
	h.sync(func() {
		h.e.board = board
		h.e.assignDailyDoubles()
	})

	total := 0
	for _, row := range board.Cells {
		for _, c := range row {
			if c.DailyDouble {
				total++
			}
		}
	}
	if total != 1 {
		t.Fatalf("placed %d daily doubles, want 1 (only one eligible cell exists)", total)
	}
}

// TestSelectCell_DailyDoubleTriggersOnSecondVisitOnly: the 1st pick of a
// flagged cell plays a normal round; only the 2nd pick triggers the offer.
func TestSelectCell_DailyDoubleTriggersOnSecondVisitOnly(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.join("c1", "fp1", "alice")

	h.selectCell("admin", 1, 2)
	if h.state() != protocol.StateRoundActive {
		t.Fatalf("1st pick state = %s, want ROUND_ACTIVE (normal round)", h.state())
	}
	h.sync(func() {
		h.e.curTrack = nil
		h.e.curCell = nil
		h.e.state = protocol.StateBoard
	})

	h.selectCell("admin", 1, 2)
	if h.state() != protocol.StateDailyDouble {
		t.Fatalf("2nd pick state = %s, want DAILY_DOUBLE", h.state())
	}
}

// TestDailyDouble_DeclineRerollsToNewCell: declining clears the original
// cell's flag permanently and arms a different eligible cell to fire on its
// very next selection.
func TestDailyDouble_DeclineRerollsToNewCell(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.join("c1", "fp1", "alice")

	board := &Board{
		Rows: 1, Cols: 2,
		Cells: [][]*Cell{
			{ddCell(1, 1, "A", 2), ddCell(1, 2, "B", 2)},
		},
	}
	board.Cells[0][1].DailyDouble = true
	board.Cells[0][1].DDTriggerVisit = 2
	h.sync(func() { h.e.board = board })

	// Arm it: 1st normal pick, then the 2nd pick triggers the offer.
	h.selectCell("admin", 1, 2)
	h.sync(func() {
		h.e.curTrack = nil
		h.e.curCell = nil
		h.e.state = protocol.StateBoard
	})
	h.selectCell("admin", 1, 2)
	if h.state() != protocol.StateDailyDouble {
		t.Fatalf("state = %s, want DAILY_DOUBLE", h.state())
	}

	h.ddDecide("c1", false) // decline
	if h.state() != protocol.StateBoard {
		t.Fatalf("state after decline = %s, want BOARD", h.state())
	}
	if board.Cells[0][1].DailyDouble {
		t.Errorf("declined cell (1,2) still flagged DailyDouble")
	}
	other := board.Cells[0][0]
	if !other.DailyDouble || other.DDTriggerVisit != 1 {
		t.Errorf("reroll target (1,1): DailyDouble=%v DDTriggerVisit=%d, want true/1 (fires on its very next pick)",
			other.DailyDouble, other.DDTriggerVisit)
	}

	// Confirm it actually fires on the very next (1st) pick of the reroll cell.
	h.selectCell("admin", 1, 1)
	if h.state() != protocol.StateDailyDouble {
		t.Errorf("reroll cell's next pick state = %s, want DAILY_DOUBLE", h.state())
	}
}

// TestDailyDouble_DeclineCancelsWhenNoEligibleCell: with no other eligible
// cell anywhere, a decline cancels outright rather than stalling.
func TestDailyDouble_DeclineCancelsWhenNoEligibleCell(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.join("c1", "fp1", "alice")

	board := &Board{
		Rows: 1, Cols: 1,
		Cells: [][]*Cell{{ddCell(1, 1, "A", 2)}},
	}
	board.Cells[0][0].DailyDouble = true
	board.Cells[0][0].DDTriggerVisit = 2
	h.sync(func() { h.e.board = board })

	h.selectCell("admin", 1, 1)
	h.sync(func() {
		h.e.curTrack = nil
		h.e.curCell = nil
		h.e.state = protocol.StateBoard
	})
	h.selectCell("admin", 1, 1)
	if h.state() != protocol.StateDailyDouble {
		t.Fatalf("state = %s, want DAILY_DOUBLE", h.state())
	}

	h.ddDecide("c1", false)
	if h.state() != protocol.StateBoard {
		t.Fatalf("state after decline = %s, want BOARD", h.state())
	}
	if board.Cells[0][0].DailyDouble {
		t.Errorf("cell still flagged after cancel-with-no-replacement")
	}
}

// TestAdminSkipDailyDouble_TreatedAsDecline: the dedicated admin action during
// the offer/pick sub-phase behaves exactly like a contestant decline.
func TestAdminSkipDailyDouble_TreatedAsDecline(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.join("c1", "fp1", "alice")
	h.selectCell("admin", 1, 2)
	h.sync(func() {
		h.e.curTrack = nil
		h.e.curCell = nil
		h.e.state = protocol.StateBoard
	})
	h.selectCell("admin", 1, 2)
	if h.state() != protocol.StateDailyDouble {
		t.Fatalf("state = %s, want DAILY_DOUBLE", h.state())
	}

	h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: cmsgAdminSkipDailyDouble, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
	if h.state() != protocol.StateBoard {
		t.Fatalf("state after admin skip = %s, want BOARD", h.state())
	}
	var dd bool
	h.sync(func() { dd = h.e.board.Cells[0][1].DailyDouble })
	if dd {
		t.Errorf("cell still flagged DailyDouble after admin skip")
	}
}

// TestSanitization_DailyDoubleOfferNeverReachesOtherMobile: the §4A-exception
// offer (title/artist) must go ONLY to the contestant's own connection, never
// broadcast and never to any other mobile connection.
func TestSanitization_DailyDoubleOfferNeverReachesOtherMobile(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	performer := h.join("c1", "fp1", "perf")
	h.join("c2", "fp2", "bystander")
	_ = performer

	h.selectCell("admin", 1, 2)
	h.sync(func() {
		h.e.curTrack = nil
		h.e.curCell = nil
		h.e.state = protocol.StateBoard
	})
	h.selectCell("admin", 1, 2)
	h.ddDecide("c1", true)

	h.bcast.mu.Lock()
	defer h.bcast.mu.Unlock()
	for _, f := range h.bcast.frames {
		if f.env.Type != smsgDailyDoubleOffer {
			continue
		}
		if f.role != "" || f.all {
			t.Fatalf("smsgDailyDoubleOffer was broadcast (role=%q all=%v), must be SendTo only", f.role, f.all)
		}
		if f.connID != "c1" {
			t.Errorf("smsgDailyDoubleOffer sent to connID %q, want only the contestant's c1", f.connID)
		}
	}
}

// TestDailyDouble_PerformancePlaysWithNoOtherRatersOnline pins a real bug: with
// nobody else online to rate, the performance used to call audio.Play and then
// IMMEDIATELY call audio.Pause (finishDailyDouble short-circuiting on an empty
// ratingPool) — the song visibly never started. An empty pool must let the
// song play out normally; only "everyone who could rate already did" may
// finish early.
func TestDailyDouble_PerformancePlaysWithNoOtherRatersOnline(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.joinStage("stage")
	h.join("c1", "fp1", "perf") // the ONLY mobile player — also the contestant

	h.selectCell("admin", 1, 2)
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
	h.ddChoose("c1", choices[0])

	last, ok := h.audio.last()
	if !ok || last.method != "play" {
		t.Fatalf("last audio call = %+v (ok=%v), want a play call that STICKS (no immediate pause)", last, ok)
	}
	if h.state() != protocol.StateDailyDouble {
		t.Fatalf("state = %s, want still DAILY_DOUBLE (performance must not be cut short)", h.state())
	}

	// The admin's Force End Round still works to finish it (1★ floor, nobody rated).
	h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: protocol.CMsgAdminEndRound, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
	if h.state() != protocol.StateBoard {
		t.Errorf("state after Force End Round = %s, want BOARD", h.state())
	}
}

// TestMobilePlayerCount covers the public landing page's "N jacked in"
// counter: it must count only mobile (buzzer) players, never admin/stage
// connections, and must track joins live without any extra bookkeeping to
// keep in sync (it reads the same registry the scoreboard does).
func TestMobilePlayerCount(t *testing.T) {
	h := newHarness(t)
	stop := h.run()
	defer stop()

	if n := h.e.MobilePlayerCount(); n != 0 {
		t.Fatalf("count with no players = %d, want 0", n)
	}

	h.joinAdmin("admin")
	h.joinStage("stage")
	if n := h.e.MobilePlayerCount(); n != 0 {
		t.Fatalf("count after admin+stage join = %d, want 0 (neither is mobile)", n)
	}

	h.join("c1", "fp1", "alice")
	if n := h.e.MobilePlayerCount(); n != 1 {
		t.Fatalf("count after 1 mobile join = %d, want 1", n)
	}

	h.join("c2", "fp2", "bob")
	h.join("c3", "fp3", "carol")
	if n := h.e.MobilePlayerCount(); n != 3 {
		t.Fatalf("count after 3 mobile joins = %d, want 3", n)
	}
}

// TestAdminSeek_SeeksWithoutTouchingDecayClock covers the control-room
// scrubber: it must command Spotify to the chosen position and broadcast the
// same to stage, but must NOT re-anchor trackStartMs/pausedAtMs the way
// resumeAudio does on pause/resume — a seek can jump the song anywhere
// (including past scoring.go's 60s decay window), and letting decay follow
// would zero out the bonus just because the admin cued to a later part of
// the song. This is a deliberate design decision, not an oversight; this
// test is the regression guard for it.
func TestAdminSeek_SeeksWithoutTouchingDecayClock(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.joinStage("stage")
	h.selectCell("admin", 1, 1)

	var trackStartBefore, pausedAtBefore int64
	h.sync(func() {
		trackStartBefore = h.e.trackStartMs
		pausedAtBefore = h.e.pausedAtMs
	})

	const seekTo = int64(42000)
	d, _ := json.Marshal(adminSeekData{PositionMs: seekTo})
	h.e.OnMessage("admin", protocol.RoleAdmin, protocol.ClientEnvelope{Type: cmsgAdminSeek, Data: d, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})

	if last, ok := h.audio.last(); !ok || last.method != "play" || last.uri != "spotify:track:secret" || last.pos != seekTo {
		t.Errorf("audio.last() = %+v, want play spotify:track:secret @ %d", last, seekTo)
	}

	foundSeekFrame := false
	h.bcast.mu.Lock()
	for _, f := range h.bcast.frames {
		if f.env.Type == protocol.SMsgAudio {
			var ad protocol.AudioData
			_ = json.Unmarshal(f.env.Data, &ad)
			if ad.Action == "play" && ad.PositionMs == seekTo && ad.TrackURI == "spotify:track:secret" {
				foundSeekFrame = true
			}
		}
	}
	h.bcast.mu.Unlock()
	if !foundSeekFrame {
		t.Errorf("expected an SMsgAudio{action:play, positionMs:%d} frame after seek", seekTo)
	}

	var trackStartAfter, pausedAtAfter int64
	h.sync(func() {
		trackStartAfter = h.e.trackStartMs
		pausedAtAfter = h.e.pausedAtMs
	})
	if trackStartAfter != trackStartBefore {
		t.Errorf("trackStartMs changed by seek: %d -> %d, want unchanged", trackStartBefore, trackStartAfter)
	}
	if pausedAtAfter != pausedAtBefore {
		t.Errorf("pausedAtMs changed by seek: %d -> %d, want unchanged", pausedAtBefore, pausedAtAfter)
	}
}

// TestBroadcastPlaybackPosition_AdminOnly covers the server->admin relay of
// stage's live position reports (previously received and silently discarded
// except for the TrackEnded field). Must reach admin only — never mobile or
// stage — and must carry the current track's known duration.
func TestBroadcastPlaybackPosition_AdminOnly(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.joinStage("stage")
	h.selectCell("admin", 1, 1)
	h.bcast.reset()

	d, _ := json.Marshal(protocol.StagePlayerStateData{PositionMs: 5000, Paused: false, TrackEnded: false})
	h.e.OnMessage("stage", protocol.RoleStage, protocol.ClientEnvelope{Type: protocol.CMsgStagePlayerState, Data: d, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})

	var toAdmin, toOther int
	var got playbackPositionData
	h.bcast.mu.Lock()
	for _, f := range h.bcast.frames {
		if f.env.Type != smsgPlaybackPosition {
			continue
		}
		if f.role == protocol.RoleAdmin {
			toAdmin++
			_ = json.Unmarshal(f.env.Data, &got)
		} else {
			toOther++
		}
	}
	h.bcast.mu.Unlock()

	if toAdmin == 0 {
		t.Fatal("expected a playbackPosition frame to RoleAdmin, got none")
	}
	if toOther != 0 {
		t.Errorf("playbackPosition leaked to a non-admin role: %d frame(s)", toOther)
	}
	if got.PositionMs != 5000 || got.DurationMs != 200000 || got.Paused {
		t.Errorf("playbackPositionData = %+v, want {5000 200000 false}", got)
	}
}

// ---------------------------------------------------------------------------
// Player-driven cell picking: whoever most recently scored (e.cellPicker)
// can pick the next board cell from their own phone. During KARAOKE this is
// one half of an AND-gate with the skip vote / track-end (see
// karaokeConditionMet); after a Daily Double (which goes straight to BOARD,
// no karaoke phase) it's an immediate pick, same as admin.select.
// ---------------------------------------------------------------------------

// TestPlayerPicker_PickThenVotePasses: the AND-gate's OTHER ordering (the
// skip-vote tests above cover vote-passes-then-pick) — the picker picks
// FIRST, and the transition only fires once the vote threshold is also met.
func TestPlayerPicker_PickThenVotePasses(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
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

	// Picker picks first — vote hasn't passed yet, must stay in KARAOKE.
	h.playerSelectCell("c1", 5, 1)
	if h.state() != protocol.StateKaraoke {
		t.Fatalf("state = %s, want KARAOKE (picked but vote hasn't passed)", h.state())
	}
	var hasPicked bool
	h.sync(func() { hasPicked = h.e.pendingPick != nil })
	if !hasPicked {
		t.Fatalf("pendingPick not recorded after a valid pick")
	}

	// 1 active @50% needs 1 vote — now both halves are satisfied.
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgVote, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
	if h.state() != protocol.StateTransition {
		t.Fatalf("state = %s, want TRANSITION after the vote also passed", h.state())
	}
}

// TestPlayerPicker_TrackEndedTriggersAndGate: the "ready" half of the
// AND-gate can also come from the song ending naturally (onStagePlayerState's
// TrackEnded), not just a passed skip vote.
func TestPlayerPicker_TrackEndedTriggersAndGate(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.joinStage("stage")
	h.join("c1", "fp1", "a")
	h.selectCell("admin", 1, 1)
	n := h.gate.Current()
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: n}, nowMs())
	h.sync(func() {})
	h.grade("admin", protocol.VerdictCorrect)

	h.playerSelectCell("c1", 5, 1)
	if h.state() != protocol.StateKaraoke {
		t.Fatalf("state = %s, want KARAOKE (picked, song still playing)", h.state())
	}

	ps, _ := json.Marshal(protocol.StagePlayerStateData{TrackEnded: true})
	h.e.OnMessage("stage", protocol.RoleStage, protocol.ClientEnvelope{Type: protocol.CMsgStagePlayerState, Data: ps, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
	if h.state() != protocol.StateTransition {
		t.Fatalf("state = %s, want TRANSITION after the track ended with a pick already in hand", h.state())
	}
}

// TestPlayerPicker_NonPickerRejected: only the connection bound to
// e.cellPicker may pick — anyone else's player.selectCell is silently
// ignored, mirroring onDailyDoubleChoose's single-connection scoping.
func TestPlayerPicker_NonPickerRejected(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.join("c1", "fp1", "winner")
	h.join("c2", "fp2", "bystander")
	h.selectCell("admin", 1, 1)
	n := h.gate.Current()
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: n}, nowMs())
	h.sync(func() {})
	h.grade("admin", protocol.VerdictCorrect)

	h.playerSelectCell("c2", 5, 1) // c2 is not the picker (c1 is)
	var pending *cellCoord
	h.sync(func() { pending = h.e.pendingPick })
	if pending != nil {
		t.Fatalf("non-picker's pick was honored: %+v", pending)
	}
	if h.state() != protocol.StateKaraoke {
		t.Fatalf("state = %s, want KARAOKE (rejected pick must not advance anything)", h.state())
	}
}

// TestPlayerPicker_AdminOverrideClearsPending: admin's manual pick always
// wins immediately, even with a player's pick already pending, and leaves
// no stale state that could misfire against a later karaoke phase.
func TestPlayerPicker_AdminOverrideClearsPending(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.join("c1", "fp1", "a")
	h.selectCell("admin", 1, 1)
	n := h.gate.Current()
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: n}, nowMs())
	h.sync(func() {})
	h.grade("admin", protocol.VerdictCorrect)
	h.playerSelectCell("c1", 5, 1) // pending pick recorded, vote hasn't passed

	h.selectCell("admin", 5, 2) // admin overrides mid-karaoke with a DIFFERENT cell
	if h.state() != protocol.StateRoundActive {
		t.Fatalf("state = %s, want ROUND_ACTIVE (admin override starts immediately)", h.state())
	}
	var row, col int
	var picker string
	var pending *cellCoord
	h.sync(func() {
		row, col = h.e.curRow, h.e.curCell.Col
		picker = h.e.cellPicker
		pending = h.e.pendingPick
	})
	if row != 5 || col != 2 {
		t.Errorf("curCell = (%d,%d), want (5,2) — admin's own choice, not the pending pick", row, col)
	}
	if picker != "" {
		t.Errorf("cellPicker = %q, want cleared by the admin override", picker)
	}
	if pending != nil {
		t.Errorf("pendingPick = %+v, want cleared by the admin override", pending)
	}
}

// TestPlayerPicker_NoScorerYetAdminOnly: before anyone has scored,
// e.cellPicker is "" and the mechanism simply doesn't engage — a mobile
// player's pick is ignored and admin.select works exactly as before this
// feature existed. This is the explicitly-chosen fallback, not a bug.
func TestPlayerPicker_NoScorerYetAdminOnly(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	h.join("c1", "fp1", "a")

	var picker string
	h.sync(func() { picker = h.e.cellPicker })
	if picker != "" {
		t.Fatalf("cellPicker = %q before any round has resolved, want \"\"", picker)
	}

	stateBefore := h.state()
	h.playerSelectCell("c1", 1, 1) // nobody is the designated picker yet
	if h.state() != stateBefore {
		t.Fatalf("state changed from %s to %s — an unsolicited pick must be a no-op", stateBefore, h.state())
	}

	h.selectCell("admin", 1, 1) // admin picks exactly as before
	if h.state() != protocol.StateRoundActive {
		t.Fatalf("state = %s, want ROUND_ACTIVE (admin.select unaffected)", h.state())
	}
}

// TestPlayerPicker_BannedPickerRejected (s8eng-01): onPlayerSelectCell must
// reject a banned picker's selection even if e.cellPicker/e.pendingPick were
// never cleared (e.g. a merely-banned-not-kicked edge case, or to prove the
// guard itself works independent of onAdminKick's own cleanup). A hostile
// client that keeps sending player.selectCell on a banned player's stale
// connection must never have it processed.
func TestPlayerPicker_BannedPickerRejected(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	p1 := h.join("c1", "fp1", "a")
	h.selectCell("admin", 1, 1)
	n := h.gate.Current()
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: n}, nowMs())
	h.sync(func() {})
	h.grade("admin", protocol.VerdictCorrect)
	if h.state() != protocol.StateKaraoke {
		t.Fatalf("state = %s, want KARAOKE", h.state())
	}
	var picker string
	h.sync(func() { picker = h.e.cellPicker })
	if picker != p1 {
		t.Fatalf("cellPicker = %q, want %q", picker, p1)
	}

	// Ban the picker directly (bypassing onAdminKick) so e.cellPicker stays
	// set to p1 — isolates onPlayerSelectCell's own p.Banned guard.
	h.sync(func() { h.e.reg.players[p1].Banned = true })

	h.playerSelectCell("c1", 5, 1)
	var pending *cellCoord
	h.sync(func() { pending = h.e.pendingPick })
	if pending != nil {
		t.Fatalf("banned picker's selection was honored: %+v", pending)
	}
	if h.state() != protocol.StateKaraoke {
		t.Fatalf("state = %s, want KARAOKE (a banned picker's selection must not advance anything)", h.state())
	}
}

// TestOnAdminKick_ClearsCellPicker (s8eng-01): kicking the current
// cell-picker must clear e.cellPicker/e.pendingPick/e.karaokeReady and
// broadcast the cleared picker status, instead of leaving the kicked
// player's picking rights intact.
func TestOnAdminKick_ClearsCellPicker(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	p1 := h.join("c1", "fp1", "a")
	h.selectCell("admin", 1, 1)
	n := h.gate.Current()
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: n}, nowMs())
	h.sync(func() {})
	h.grade("admin", protocol.VerdictCorrect)
	if h.state() != protocol.StateKaraoke {
		t.Fatalf("state = %s, want KARAOKE", h.state())
	}
	var picker string
	h.sync(func() { picker = h.e.cellPicker })
	if picker != p1 {
		t.Fatalf("cellPicker = %q, want %q", picker, p1)
	}
	h.bcast.reset()

	h.kick("admin", p1, true)

	var cellPicker string
	var pending *cellCoord
	var ready bool
	var banned bool
	h.sync(func() {
		cellPicker = h.e.cellPicker
		pending = h.e.pendingPick
		ready = h.e.karaokeReady
		banned = h.e.reg.players[p1].Banned
	})
	if !banned {
		t.Fatalf("p1.Banned = false after admin.kick(ban:true)")
	}
	if cellPicker != "" {
		t.Errorf("cellPicker = %q after kicking the picker, want cleared", cellPicker)
	}
	if pending != nil {
		t.Errorf("pendingPick = %+v after kicking the picker, want cleared", pending)
	}
	if ready {
		t.Error("karaokeReady = true after kicking the picker, want cleared")
	}

	// A pickerStatus frame reflecting the clear must have gone out.
	foundCleared := false
	h.bcast.mu.Lock()
	for _, f := range h.bcast.frames {
		if f.env.Type == smsgPickerStatus {
			var d pickerStatusData
			_ = json.Unmarshal(f.env.Data, &d)
			if d.PickerID == "" {
				foundCleared = true
			}
		}
	}
	h.bcast.mu.Unlock()
	if !foundCleared {
		t.Error("no pickerStatus broadcast reflecting the cleared picker after kick")
	}

	// Belt-and-suspenders: the kicked player's hostile client still can't
	// act even if it kept trying (cellPicker is now "" so the pick is
	// rejected regardless of which guard catches it).
	h.playerSelectCell("c1", 5, 1)
	h.sync(func() { pending = h.e.pendingPick })
	if pending != nil {
		t.Errorf("kicked picker's post-kick selection was honored: %+v", pending)
	}
}

// TestOnDisconnect_PickerClearsStaleState (s8eng-02): a cell-picker who
// fully disconnects before the OTHER AND-gate half has fired must have
// e.cellPicker/e.pendingPick/e.karaokeReady cleared, instead of leaving a
// "waiting on {handle}" status that can never resolve.
func TestOnDisconnect_PickerClearsStaleState(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	p1 := h.join("c1", "fp1", "a")
	h.selectCell("admin", 1, 1)
	n := h.gate.Current()
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: n}, nowMs())
	h.sync(func() {})
	h.grade("admin", protocol.VerdictCorrect)
	if h.state() != protocol.StateKaraoke {
		t.Fatalf("state = %s, want KARAOKE", h.state())
	}
	var picker string
	h.sync(func() { picker = h.e.cellPicker })
	if picker != p1 {
		t.Fatalf("cellPicker = %q, want %q", picker, p1)
	}

	h.e.OnDisconnect("c1")
	h.sync(func() {})

	var cellPicker string
	var pending *cellCoord
	var ready bool
	h.sync(func() {
		cellPicker = h.e.cellPicker
		pending = h.e.pendingPick
		ready = h.e.karaokeReady
	})
	if cellPicker != "" {
		t.Errorf("cellPicker = %q after the picker disconnected, want cleared", cellPicker)
	}
	if pending != nil {
		t.Errorf("pendingPick = %+v after the picker disconnected, want cleared", pending)
	}
	if ready {
		t.Error("karaokeReady = true after the picker disconnected, want cleared")
	}
	// karaokeReady was false (the other AND-gate half hadn't fired), so this
	// must be a simple clear — no transition should have been forced.
	if h.state() != protocol.StateKaraoke {
		t.Errorf("state = %s, want still KARAOKE (no AND-gate half had fired yet)", h.state())
	}
}

// TestOnDisconnect_PickerDisconnectWithReadyFallsThroughToBoard (s8eng-02):
// if the OTHER AND-gate half (skip-vote threshold / track-ended) already
// fired before the picker disconnects, nothing will ever supply a
// pendingPick again — OnDisconnect must fall through to the same
// no-valid-pick continuation beginTransition's own stalled-pick handling
// uses, instead of leaving the round stuck in KARAOKE forever.
func TestOnDisconnect_PickerDisconnectWithReadyFallsThroughToBoard(t *testing.T) {
	old := TransitionDelay
	TransitionDelay = 20 * time.Millisecond
	t.Cleanup(func() { TransitionDelay = old })

	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	p1 := h.join("c1", "fp1", "a")
	h.selectCell("admin", 1, 1)
	n := h.gate.Current()
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: n}, nowMs())
	h.sync(func() {})
	h.grade("admin", protocol.VerdictCorrect)
	if h.state() != protocol.StateKaraoke {
		t.Fatalf("state = %s, want KARAOKE", h.state())
	}

	// Simulate the OTHER AND-gate half already having fired (e.g. the skip
	// vote threshold was reached) before the picker ever picked.
	h.sync(func() {
		if h.e.cellPicker != p1 {
			t.Fatalf("cellPicker = %q, want %q", h.e.cellPicker, p1)
		}
		h.e.karaokeReady = true
	})

	h.e.OnDisconnect("c1")
	h.sync(func() {})

	// Must have fallen through to beginTransition immediately — state moves
	// to TRANSITION right away, picker state cleared right away too.
	var cellPicker string
	var pending *cellCoord
	var ready bool
	var state protocol.GameState
	h.sync(func() {
		cellPicker = h.e.cellPicker
		pending = h.e.pendingPick
		ready = h.e.karaokeReady
		state = h.e.state
	})
	if state != protocol.StateTransition {
		t.Fatalf("state = %s immediately after disconnect, want TRANSITION (fell through to beginTransition)", state)
	}
	if cellPicker != "" || pending != nil || ready {
		t.Errorf("picker state not cleared immediately: cellPicker=%q pending=%+v karaokeReady=%v", cellPicker, pending, ready)
	}

	// And it must not get stuck there — beginTransition's own no-valid-pick
	// continuation lands after TransitionDelay and returns to BOARD.
	if !waitUntil(t, time.Second, func() bool { return h.state() == protocol.StateBoard }) {
		t.Fatalf("state = %s after waiting out TransitionDelay, want BOARD (must not get stuck in TRANSITION/KARAOKE forever)", h.state())
	}
}

// TestDailyDouble_WinnerBecomesPicker: a Daily Double payout updates
// e.cellPicker the same as a normal correct guess (both go through the
// shared award() path) — the performer can pick the next cell directly from
// BOARD, with no karaoke/vote phase to wait on.
func TestDailyDouble_WinnerBecomesPicker(t *testing.T) {
	withFastFade(t) // the DD resolution now lands after a graceful fade, not synchronously
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	performer := h.join("c1", "fp1", "perf")
	rater := h.join("c2", "fp2", "rater")
	_ = rater

	h.selectCell("admin", 1, 2)
	h.sync(func() { h.e.trackStartMs = nowMs() })
	n := h.gate.Current()
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: n}, nowMs())
	h.sync(func() {})
	h.grade("admin", protocol.VerdictCorrect)
	h.sync(func() {
		h.e.curTrack = nil
		h.e.curCell = nil
		h.e.state = protocol.StateBoard
	})

	h.selectCell("admin", 1, 2) // 2nd pick on the DD cell -> offer
	if h.state() != protocol.StateDailyDouble {
		t.Fatalf("state after 2nd pick = %s, want DAILY_DOUBLE", h.state())
	}
	h.ddDecide("c1", true)
	choices := h.lastDailyDoubleOfferTrackIDs("c1")
	if len(choices) == 0 {
		t.Fatalf("contestant received no song choices after accepting")
	}
	h.ddChoose("c1", choices[0])

	rd, _ := json.Marshal(protocol.RateData{Stars: 5})
	h.e.OnMessage("c2", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgRate, Data: rd, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
	// The DD resolution now lands after a graceful fade, not synchronously.
	if !waitUntil(t, time.Second, func() bool { return len(h.audio.ofMethod("pause")) > 0 }) {
		t.Fatal("timed out waiting for the Daily Double fade to finish and pause")
	}
	if h.state() != protocol.StateBoard {
		t.Fatalf("state = %s, want BOARD after DD resolves", h.state())
	}
	var picker string
	h.sync(func() { picker = h.e.cellPicker })
	if picker != performer {
		t.Fatalf("cellPicker = %q, want the DD performer %q", picker, performer)
	}

	h.playerSelectCell("c1", 5, 1) // the DD winner picks the next cell directly
	if h.state() != protocol.StateRoundActive {
		t.Fatalf("state = %s, want ROUND_ACTIVE (DD winner's pick started it)", h.state())
	}
	var row int
	h.sync(func() { row = h.e.curRow })
	if row != 5 {
		t.Errorf("curRow = %d, want 5 (the DD winner's own pick)", row)
	}
}

// TestJoinBurst_CoalescesScoreboardBoardTelemetry is the regression gate for
// the stress-test finding: onHello used to call broadcastScoreboard/
// broadcastBoard/broadcastTelemetry synchronously and unconditionally on
// EVERY join — the identical O(P^2)-floodable pattern already fixed for
// onHeartbeat (telemetryDirty + a coalescing ticker), just never applied to
// joins. A burst of N joins (as a scripted load test produces) used to cost
// N full population-wide broadcasts back-to-back on the single Run-loop
// goroutine that also services the admin connection, worsening as player
// count climbed toward the ~500 the user actually tested with.
func TestJoinBurst_CoalescesScoreboardBoardTelemetry(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin1")
	h.bcast.mu.Lock()
	h.bcast.frames = nil // drop admin's own join broadcasts; only count what the burst below produces
	h.bcast.mu.Unlock()

	const burst = 25
	for i := 0; i < burst; i++ {
		h.join(fmt.Sprintf("c%d", i), fmt.Sprintf("fp%d", i), fmt.Sprintf("player%d", i))
	}

	h.bcast.mu.Lock()
	var scoreboardFrames, boardFrames, telemetryFrames int
	for _, f := range h.bcast.frames {
		// Only count actual Broadcast(role, ...) calls (f.role != "") — the
		// O(P) fan-out this fix targets. sendFullSync's per-joiner SendTo of
		// their OWN scoreboard copy (f.connID != "", f.role == "") is a
		// single-recipient send, not the bug, and is expected once per join.
		if f.role == "" {
			continue
		}
		switch f.env.Type {
		case protocol.SMsgScoreboard:
			scoreboardFrames++
		case protocol.SMsgBoard:
			boardFrames++
		case protocol.SMsgTelemetry:
			telemetryFrames++
		}
	}
	h.bcast.mu.Unlock()

	if scoreboardFrames != 0 {
		t.Errorf("scoreboard frames emitted synchronously during the burst = %d, want 0 (should be coalesced via scoreboardDirty, not broadcast inline per join)", scoreboardFrames)
	}
	if boardFrames != 0 {
		t.Errorf("board frames emitted synchronously during the burst = %d, want 0 (should be coalesced via boardDirty)", boardFrames)
	}
	if telemetryFrames != 0 {
		t.Errorf("telemetry frames emitted synchronously during the burst = %d, want 0 (should be coalesced via telemetryDirty)", telemetryFrames)
	}

	h.sync(func() {
		if !h.e.scoreboardDirty {
			t.Error("scoreboardDirty = false after a join burst, want true (so the ticker actually flushes a real update)")
		}
		if !h.e.boardDirty {
			t.Error("boardDirty = false after a join burst, want true")
		}
		if !h.e.telemetryDirty {
			t.Error("telemetryDirty = false after a join burst, want true")
		}
	})
}
