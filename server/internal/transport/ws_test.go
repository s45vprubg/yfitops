package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"nhooyr.io/websocket"

	"github.com/s45vprubg/yfitops/server/internal/protocol"
)

// ws_test.go exercises the REAL WSHandler over a real TCP WebSocket (httptest +
// websocket.Dial). drop_test.go pins the anti-zombie guarantee against a
// hand-built hub.add(id, w, closer) call, which cannot notice a real call site
// that forgets the closer — these tests close that gap, and cover the /ws
// session cap and the idle reaper (QA sweep 4: s4-ws-001/002/003).

var _ http.Handler = (*WSHandler)(nil)

// wsStubHandler records the game.InboundHandler lifecycle.
type wsStubHandler struct {
	mu          sync.Mutex
	connects    int
	disconnects int
	types       []protocol.ClientMsgType
}

func (s *wsStubHandler) OnConnect(connID, remoteIP string) {
	s.mu.Lock()
	s.connects++
	s.mu.Unlock()
}

func (s *wsStubHandler) OnMessage(connID string, role protocol.Role, env protocol.ClientEnvelope, arrivalUnixMs int64) {
	s.mu.Lock()
	s.types = append(s.types, env.Type)
	s.mu.Unlock()
}

func (s *wsStubHandler) OnDisconnect(connID string) {
	s.mu.Lock()
	s.disconnects++
	s.mu.Unlock()
}

func (s *wsStubHandler) gotTypes() []protocol.ClientMsgType {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]protocol.ClientMsgType(nil), s.types...)
}

func (s *wsStubHandler) disconnected() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.disconnects
}

// newWSTestServer wires a real WSHandler (generous limiter unless overridden)
// onto an httptest server and returns its ws:// URL.
func newWSTestServer(t *testing.T, hub *Hub, h *wsStubHandler, srv *Server) (string, *httptest.Server, *WSHandler) {
	t.Helper()
	wsh := NewWSHandler(hub, h, srv)
	ts := httptest.NewServer(wsh)
	t.Cleanup(ts.Close)
	return "ws" + strings.TrimPrefix(ts.URL, "http"), ts, wsh
}

func wsDial(t *testing.T, url string, ts *httptest.Server) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: ts.Client()})
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func hubSize(h *Hub) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.conns)
}

func anyConn(h *Hub) *conn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, c := range h.conns {
		return c
	}
	return nil
}

// TestWSHandlerFramingRoundTrip: client frames reach OnMessage decoded, and a
// hub broadcast comes back out framed and parseable — through the real handler.
func TestWSHandlerFramingRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		env  protocol.ClientEnvelope
	}{
		{"heartbeat", protocol.ClientEnvelope{Type: protocol.CMsgHeartbeat}},
		{"buzz", protocol.ClientEnvelope{Type: protocol.CMsgBuzz}},
		{"resync", protocol.ClientEnvelope{Type: protocol.CMsgResync}},
		{"hello", protocol.ClientEnvelope{Type: protocol.CMsgHello, Data: json.RawMessage(`{"role":"mobile"}`)}},
	}

	hub := NewHub()
	h := &wsStubHandler{}
	url, ts, _ := newWSTestServer(t, hub, h, &Server{limiter: newSessionLimiter(0, 0)})

	c, _, err := wsDial(t, url, ts)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()
	waitFor(t, "hub registration", func() bool { return hubSize(hub) == 1 })

	want := make([]protocol.ClientMsgType, 0, len(cases))
	for _, tc := range cases {
		body, err := json.Marshal(tc.env)
		if err != nil {
			t.Fatalf("%s: marshal: %v", tc.name, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if err := c.Write(ctx, websocket.MessageBinary, encodeFrame(body)); err != nil {
			cancel()
			t.Fatalf("%s: write: %v", tc.name, err)
		}
		cancel()
		want = append(want, tc.env.Type)
	}

	waitFor(t, "all frames delivered to OnMessage", func() bool { return len(h.gotTypes()) == len(want) })
	got := h.gotTypes()
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("frame %d type = %q, want %q", i, got[i], want[i])
		}
	}

	// Server -> client direction, through the hub's writer goroutine.
	hub.BroadcastAll(protocol.ServerEnvelope{Type: protocol.SMsgState})
	readCtx, readCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer readCancel()
	_, data, err := c.Read(readCtx)
	if err != nil {
		t.Fatalf("read broadcast: %v", err)
	}
	frame, err := newFrameReader(strings.NewReader(string(data))).ReadFrame()
	if err != nil {
		t.Fatalf("decode broadcast frame: %v", err)
	}
	var out protocol.ServerEnvelope
	if err := json.Unmarshal(frame, &out); err != nil {
		t.Fatalf("unmarshal broadcast: %v", err)
	}
	if out.Type != protocol.SMsgState || out.Seq != 1 {
		t.Fatalf("broadcast = %+v, want type=%q seq=1", out, protocol.SMsgState)
	}
}

// TestWSHandlerDropTearsDownSocket is the regression test for s4-ws-002: the
// real handler must hand the hub a stream closer, so a slow-client drop
// (enqueue overflow) unblocks the parked read loop and runs OnDisconnect.
// Without the closer this hangs as a zombie that keeps processing frames.
func TestWSHandlerDropTearsDownSocket(t *testing.T) {
	hub := NewHub()
	h := &wsStubHandler{}
	url, ts, _ := newWSTestServer(t, hub, h, &Server{limiter: newSessionLimiter(0, 0)})

	c, _, err := wsDial(t, url, ts)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()
	waitFor(t, "hub registration", func() bool { return hubSize(hub) == 1 })
	victim := anyConn(hub)
	if victim == nil {
		t.Fatal("conn vanished from hub")
	}
	if victim.stream == nil {
		t.Fatal("ZOMBIE: the real WSHandler registered the conn with NO stream closer; a hub-side drop can never tear the socket down")
	}

	// The client never reads, so a burst of 64 KiB frames fills the kernel
	// buffers, parks the hub writeLoop on Write, fills the out channel and trips
	// enqueue's overflow -> stop() path.
	blob, err := json.Marshal(strings.Repeat("x", 64<<10))
	if err != nil {
		t.Fatalf("marshal blob: %v", err)
	}
	big := protocol.ServerEnvelope{Type: protocol.SMsgState, Data: blob}

	deadline := time.Now().Add(20 * time.Second)
	sent := 0
	for time.Now().Before(deadline) {
		select {
		case <-victim.closed:
		default:
			// stop() runs INLINE here; if it blocked, the engine's broadcast
			// goroutine would freeze and this loop would never finish.
			hub.BroadcastAll(big)
			sent++
			continue
		}
		break
	}
	select {
	case <-victim.closed:
		t.Logf("hub dropped conn %s after %d frames", victim.id, sent)
	default:
		t.Fatalf("could not trigger the enqueue overflow drop after %d frames", sent)
	}

	waitFor(t, "OnDisconnect after drop", func() bool { return h.disconnected() == 1 })
	waitFor(t, "hub to drain", func() bool { return hubSize(hub) == 0 })
}

// TestWSHandlerHonorsSessionLimiter is the regression test for s4-ws-001: /ws
// must consult the same per-IP/global cap handleSession enforces, rejecting with
// 503 and re-admitting once a slot frees.
func TestWSHandlerHonorsSessionLimiter(t *testing.T) {
	hub := NewHub()
	h := &wsStubHandler{}
	srv := &Server{limiter: newSessionLimiter(1, 1)} // global 1, per-IP 1
	url, ts, _ := newWSTestServer(t, hub, h, srv)

	first, _, err := wsDial(t, url, ts)
	if err != nil {
		t.Fatalf("first dial should be admitted: %v", err)
	}
	waitFor(t, "first conn in hub", func() bool { return hubSize(hub) == 1 })

	second, resp, err := wsDial(t, url, ts)
	if err == nil {
		second.CloseNow()
		first.CloseNow()
		t.Fatal("LIMITER BYPASS: a 2nd /ws connection from the same IP was admitted with maxPerIP=1")
	}
	if resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		t.Fatalf("2nd dial rejected with status %d, want %d (%v)", code, http.StatusServiceUnavailable, err)
	}

	// Free the slot; the release runs in ServeHTTP's defer, so poll.
	first.CloseNow()
	waitFor(t, "hub to drain after close", func() bool { return hubSize(hub) == 0 })
	waitFor(t, "limiter slot to be released", func() bool {
		third, _, err := wsDial(t, url, ts)
		if err != nil {
			return false
		}
		third.CloseNow()
		return true
	})
}

// TestWSHandlerIdleTimeout is the regression test for s4-ws-003: a silent peer
// must be reaped (mirrors quic.Config.MaxIdleTimeout), while a peer sending its
// normal heartbeat traffic must survive.
func TestWSHandlerIdleTimeout(t *testing.T) {
	t.Run("silent conn is reaped", func(t *testing.T) {
		hub := NewHub()
		h := &wsStubHandler{}
		url, ts, wsh := newWSTestServer(t, hub, h, &Server{limiter: newSessionLimiter(0, 0)})
		wsh.idleTimeout = 200 * time.Millisecond

		c, _, err := wsDial(t, url, ts)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer c.CloseNow()
		waitFor(t, "hub registration", func() bool { return hubSize(hub) == 1 })

		// Never write anything. The reaper must close us.
		waitFor(t, "idle conn to be reaped", func() bool { return hubSize(hub) == 0 && h.disconnected() == 1 })
	})

	t.Run("heartbeat traffic keeps it alive", func(t *testing.T) {
		hub := NewHub()
		h := &wsStubHandler{}
		url, ts, wsh := newWSTestServer(t, hub, h, &Server{limiter: newSessionLimiter(0, 0)})
		// Ratio mirrors production: 30s timeout vs a 2s client heartbeat
		// (web/mobile/src/lib/useGame.ts HEARTBEAT_MS).
		wsh.idleTimeout = 300 * time.Millisecond
		beat := 20 * time.Millisecond

		c, _, err := wsDial(t, url, ts)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer c.CloseNow()
		waitFor(t, "hub registration", func() bool { return hubSize(hub) == 1 })

		body, err := json.Marshal(protocol.ClientEnvelope{Type: protocol.CMsgHeartbeat})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		for i := 0; i < 30; i++ { // 600ms of traffic, 2x the idle timeout
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			err := c.Write(ctx, websocket.MessageBinary, encodeFrame(body))
			cancel()
			if err != nil {
				t.Fatalf("heartbeat %d failed — idle reaper killed a healthy conn: %v", i, err)
			}
			time.Sleep(beat)
		}
		if hubSize(hub) != 1 || h.disconnected() != 0 {
			t.Fatalf("healthy heartbeating conn was reaped: hub=%d disconnects=%d", hubSize(hub), h.disconnected())
		}
	})
}

// captureLog redirects the shared log.Writer() to a buffer for the duration
// of a test and restores it on cleanup.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(orig) })
	return &buf
}

// TestWSHandlerTeardownLogging is the regression test for s5-ws-001: an
// intentional local teardown (idle reap, or a hub-side drop via conn.stop())
// must not log a "read error" — that is expected, deliberate behavior, not a
// fault. A genuine unexpected disconnect must still log loudly; otherwise this
// gate would also pass if someone deleted the logging entirely.
func TestWSHandlerTeardownLogging(t *testing.T) {
	t.Run("idle reap is silent", func(t *testing.T) {
		buf := captureLog(t)
		hub := NewHub()
		h := &wsStubHandler{}
		url, ts, wsh := newWSTestServer(t, hub, h, &Server{limiter: newSessionLimiter(0, 0)})
		wsh.idleTimeout = 100 * time.Millisecond

		c, _, err := wsDial(t, url, ts)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer c.CloseNow()
		waitFor(t, "hub registration", func() bool { return hubSize(hub) == 1 })
		waitFor(t, "idle conn to be reaped", func() bool { return hubSize(hub) == 0 && h.disconnected() == 1 })

		if strings.Contains(buf.String(), "read error") {
			t.Fatalf("idle reap logged a read error, want a silent intentional teardown:\n%s", buf.String())
		}
	})

	t.Run("hub drop is silent", func(t *testing.T) {
		buf := captureLog(t)
		hub := NewHub()
		h := &wsStubHandler{}
		url, ts, _ := newWSTestServer(t, hub, h, &Server{limiter: newSessionLimiter(0, 0)})

		c, _, err := wsDial(t, url, ts)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer c.CloseNow()
		waitFor(t, "hub registration", func() bool { return hubSize(hub) == 1 })
		victim := anyConn(hub)
		if victim == nil {
			t.Fatal("conn vanished from hub")
		}

		blob, err := json.Marshal(strings.Repeat("x", 64<<10))
		if err != nil {
			t.Fatalf("marshal blob: %v", err)
		}
		big := protocol.ServerEnvelope{Type: protocol.SMsgState, Data: blob}

		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			select {
			case <-victim.closed:
			default:
				hub.BroadcastAll(big)
				continue
			}
			break
		}
		select {
		case <-victim.closed:
		default:
			t.Fatal("could not trigger the enqueue overflow drop")
		}
		waitFor(t, "OnDisconnect after drop", func() bool { return h.disconnected() == 1 })

		if strings.Contains(buf.String(), "read error") {
			t.Fatalf("hub drop logged a read error, want a silent intentional teardown:\n%s", buf.String())
		}
	})

	t.Run("genuine abrupt disconnect still logs", func(t *testing.T) {
		buf := captureLog(t)
		hub := NewHub()
		h := &wsStubHandler{}
		url, ts, _ := newWSTestServer(t, hub, h, &Server{limiter: newSessionLimiter(0, 0)})

		c, _, err := wsDial(t, url, ts)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		waitFor(t, "hub registration", func() bool { return hubSize(hub) == 1 })

		// A genuinely abnormal peer-initiated close (protocol error, not the
		// Normal/GoingAway codes isWSNormalClose treats as expected). The
		// server never flagged this as its own doing, so it must still log.
		_ = c.Close(websocket.StatusProtocolError, "boom")

		waitFor(t, "server notices the dirty disconnect", func() bool { return h.disconnected() == 1 })

		if !strings.Contains(buf.String(), "read error") {
			t.Fatalf("genuine unexpected disconnect did not log a read error — logging was over-suppressed:\n%s", buf.String())
		}
	})
}

// TestIdleTimerResetVetoesStaleGeneration is the regression test for
// s5-ws-003: time.Timer.Reset cannot retract an AfterFunc callback that has
// already begun running, so a naive single-Timer idleTimer would fire
// onExpire even after a reset() logically superseded it. This exercises the
// generation-guard directly (deterministic — no sleep-based timer race)
// rather than trying to win a sub-millisecond scheduling race against the
// real time.AfterFunc, which would be flaky by construction.
func TestIdleTimerResetVetoesStaleGeneration(t *testing.T) {
	fired := make(chan struct{}, 1)
	i := newIdleTimer(time.Hour, func() { fired <- struct{}{} })
	defer i.stop()

	// Bind a callback to generation 0 — standing in for the real AfterFunc
	// callback of a timer that already fired but had not yet acquired mu when
	// a reset() arrived microseconds later and superseded it.
	stale := i.fireFunc(0)

	// A real reset() bumps the generation and arms a fresh timer, exactly as
	// it would if traffic arrived right after the stale timer expired.
	i.reset()
	i.reset()

	stale()
	select {
	case <-fired:
		t.Fatal("stale generation's callback fired onExpire; reset() failed to veto it")
	case <-time.After(50 * time.Millisecond):
	}

	i.mu.Lock()
	gen := i.gen
	i.mu.Unlock()
	current := i.fireFunc(gen)
	current()
	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("current generation's callback did not fire onExpire")
	}
}
