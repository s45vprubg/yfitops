package game

import (
	"context"
	"encoding/json"
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

type audioCall struct {
	method string
	uri    string
	pos    int64
}

type fakeAudio struct {
	mu    sync.Mutex
	calls []audioCall
}

func (a *fakeAudio) SetDevice(string) {}
func (a *fakeAudio) Play(_ context.Context, uri string, pos int64) error {
	a.add(audioCall{"play", uri, pos})
	return nil
}
func (a *fakeAudio) Pause(context.Context) error            { a.add(audioCall{method: "pause"}); return nil }
func (a *fakeAudio) Resume(context.Context) error           { a.add(audioCall{method: "resume"}); return nil }
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
	e := NewEngine(repo, lk, audio, fakeLyrics{}, bc, gate, Config{
		SessionID:        "s1",
		SkipThresholdPct: 50,
		Rand:             rand.New(rand.NewSource(1)),
	})
	e.SetBoard(testBoard())
	return &harness{e: e, bcast: bc, lock: lk, repo: repo, audio: audio, gate: gate, t: t}
}

func testBoard() *Board {
	cell := func(row, col int, dd bool, n int) *Cell {
		c := &Cell{Row: row, Col: col, Category: "Nu Metal", DailyDouble: dd}
		for i := 0; i < n; i++ {
			c.Tracks = append(c.Tracks, &Track{
				ID:         strings.Join([]string{"t", string(rune('0' + row)), string(rune('0' + col)), string(rune('a' + i))}, ""),
				SpotifyURI: "spotify:track:secret",
				Artist:     "Limp Bizkit",
				Song:       "Rollin",
				AlbumArt:   "art.jpg",
				DurationMs: 200000,
			})
		}
		return c
	}
	return &Board{
		Rows: 2, Cols: 2,
		Cells: [][]*Cell{
			{cell(1, 1, false, 2), cell(1, 2, true, 1)},
			{cell(5, 1, false, 3), cell(5, 2, false, 1)},
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
	h.e.OnConnect(connID)
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
	h.e.OnConnect(connID)
	hello, _ := json.Marshal(protocol.HelloData{Role: protocol.RoleAdmin})
	h.e.OnMessage(connID, protocol.RoleAdmin, protocol.ClientEnvelope{Type: protocol.CMsgHello, Data: hello}, nowMs())
	h.sync(func() {})
}

func (h *harness) joinStage(connID string) {
	h.bcast.mu.Lock()
	h.bcast.connRole[connID] = protocol.RoleStage
	h.bcast.mu.Unlock()
	h.e.OnConnect(connID)
	hello, _ := json.Marshal(protocol.HelloData{Role: protocol.RoleStage})
	h.e.OnMessage(connID, protocol.RoleStage, protocol.ClientEnvelope{Type: protocol.CMsgHello, Data: hello}, nowMs())
	h.sync(func() {})
}

func (h *harness) selectCell(adminConn string, row, col int) {
	d, _ := json.Marshal(protocol.AdminSelectData{Row: row, Col: col})
	h.e.OnMessage(adminConn, protocol.RoleAdmin, protocol.ClientEnvelope{Type: protocol.CMsgAdminSelect, Data: d, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
}

func (h *harness) grade(adminConn string, v protocol.GradeVerdict) {
	d, _ := json.Marshal(protocol.AdminGradeData{Verdict: v})
	h.e.OnMessage(adminConn, protocol.RoleAdmin, protocol.ClientEnvelope{Type: protocol.CMsgAdminGrade, Data: d, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
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

	// p2 votes -> 2/2 met -> TRANSITION.
	h.e.OnMessage("c2", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgVote, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
	if h.state() != protocol.StateTransition {
		t.Fatalf("state = %s, want TRANSITION after threshold met", h.state())
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
	// Second votes: now everyone has -> transition.
	h.e.OnMessage("c2", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgVote, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})
	if h.state() != protocol.StateTransition {
		t.Fatalf("state = %s, want TRANSITION at 2/2", h.state())
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
func TestDailyDouble_BonusApplied(t *testing.T) {
	h := newHarness(t)
	defer h.run()()
	h.joinAdmin("admin")
	performer := h.join("c1", "fp1", "perf")
	rater := h.join("c2", "fp2", "rater")
	_ = rater

	h.selectCell("admin", 1, 2) // the daily-double cell, row 1 max 100
	h.sync(func() { h.e.trackStartMs = nowMs() })
	n := h.gate.Current()
	h.e.OnMessage("c1", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgBuzz, Nonce: n}, nowMs())
	h.sync(func() {})
	h.grade("admin", protocol.VerdictCorrect)
	if h.state() != protocol.StateDailyDouble {
		t.Fatalf("state = %s, want DAILY_DOUBLE", h.state())
	}
	// Base correct points = 100 (row1 within hold).
	if got := h.score(performer); got != 100 {
		t.Fatalf("performer base = %d, want 100", got)
	}

	// The single rater gives 5 stars -> 2.0x -> +100 bonus on max(100).
	rd, _ := json.Marshal(protocol.RateData{Stars: 5})
	h.e.OnMessage("c2", protocol.RoleMobile, protocol.ClientEnvelope{Type: protocol.CMsgRate, Data: rd, Nonce: h.gate.Current()}, nowMs())
	h.sync(func() {})

	if got := h.score(performer); got != 200 {
		t.Errorf("performer after DD bonus = %d, want 200 (100 base + 100 bonus)", got)
	}
	if h.state() != protocol.StateKaraoke {
		t.Errorf("state = %s, want KARAOKE after DD resolves", h.state())
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
func TestTransitionReturnsToBoard(t *testing.T) {
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
	if h.state() != protocol.StateTransition {
		t.Fatalf("state = %s, want TRANSITION", h.state())
	}
	time.Sleep(40 * time.Millisecond)
	if h.state() != protocol.StateBoard {
		t.Errorf("state = %s, want BOARD after countdown", h.state())
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
