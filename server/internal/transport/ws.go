package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"nhooyr.io/websocket"

	"github.com/s45vprubg/yfitops/server/internal/game"
	"github.com/s45vprubg/yfitops/server/internal/protocol"
)

// WSHandler upgrades plain HTTP connections to WebSocket and bridges them into
// the same Hub used by WebTransport. It is the fallback for phones on a LAN where
// the page is served over plain HTTP (no secure context → WebTransport is
// undefined in the browser).
//
// It is CLEARTEXT: no encryption, no cert pinning. Anyone on the network can
// read, forge and replay frames, including the admin/stage secret carried in the
// hello. main.go therefore mounts it only when BOTH YFI_DEV_WS=1 and
// YFI_INSECURE_TRANSPORT=1 are set (see decideWS), independent of YFI_ENV — a
// prod deployment may opt in, and gets a loud boot warning when it does.
//
// Because it is a SECOND DOOR INTO THE SAME HUB, every transport-layer guarantee
// has to be re-established here rather than inherited: the session limiter, the
// hub-side stream closer, and the idle timeout below are all mirrors of the
// WebTransport path in server.go. Anything added to that path needs a twin here.
type WSHandler struct {
	hub     *Hub
	handler game.InboundHandler
	server  *Server

	// idleTimeout is the per-connection inactivity ceiling; overridden in tests.
	idleTimeout time.Duration
}

// wsIdleTimeout mirrors the WebTransport side's quic.Config.MaxIdleTimeout
// (server.go) so both transports reap dead peers on the same schedule. The
// mobile client heartbeats every 2s (web/mobile/src/lib/useGame.ts
// HEARTBEAT_MS), so 30s is ~15 missed heartbeats — a healthy-but-quiet
// spectator can never trip it, while a vanished phone stops holding a goroutine,
// a hub slot and a socket forever.
const wsIdleTimeout = 30 * time.Second

// NewWSHandler creates a WebSocket handler sharing the same hub and engine as
// the WebTransport server.
func NewWSHandler(hub *Hub, handler game.InboundHandler, srv *Server) *WSHandler {
	return &WSHandler{hub: hub, handler: handler, server: srv, idleTimeout: wsIdleTimeout}
}

func (ws *WSHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Per-IP + global session cap, mirroring handleSession (server.go). /ws and
	// /wt deliberately share ONE limiter instance: the cap is a resource ceiling
	// on the process, not per-transport. Reject BEFORE the upgrade so a flood is
	// turned away cheaply. Acquire pairs with exactly one release on every exit
	// path.
	ip := clientIP(r.RemoteAddr)
	if !ws.server.limiter.acquire(ip) {
		log.Printf("transport/ws: session rejected (limit reached) from %s", ip)
		http.Error(w, "too many connections", http.StatusServiceUnavailable)
		return
	}
	defer ws.server.limiter.release(ip)

	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// "*" disables the library's cross-origin check ON PURPOSE. The default
		// requires the Origin host to EQUAL the request Host, which breaks the
		// exact case this dev-only fallback exists for: a phone loading the PWA
		// from the Vite dev server on one host:port while /ws lives on another.
		// The WebTransport twin is equally open for the same reason
		// (server.go CheckOrigin). Auth is enforced at the Hello layer, not the
		// origin — and this handler is only mounted behind the explicit
		// YFI_DEV_WS=1 + YFI_INSECURE_TRANSPORT=1 opt-in.
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		log.Printf("transport/ws: upgrade failed: %v", err)
		return
	}
	defer c.CloseNow()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// localClose flags a teardown WE initiated (idle reap or a hub-side drop)
	// so the read-loop error branch below can tell "the socket died because we
	// deliberately killed it" from "the socket died and we don't know why" —
	// the former is expected behavior and must not log as a fault, the latter
	// is the only thing worth a loud log line. Set it BEFORE calling CloseNow
	// in both teardown paths so there is no window where the resulting
	// net.ErrClosed-shaped read error could slip through unflagged.
	var localClose atomic.Bool

	// Idle reaper: a network-idle peer (phone in a tunnel, laptop lid closed)
	// never produces a read error, so without this the read loop parks forever.
	// CloseNow is immediate and unblocks the parked Reader below; the timer is
	// reset on every successful frame read and every successful write, so only
	// genuine silence trips it.
	idle := newIdleTimer(ws.idleTimeout, func() {
		log.Printf("transport/ws: idle timeout (%s) from %s; closing", ws.idleTimeout, ip)
		localClose.Store(true)
		_ = c.CloseNow()
		cancel()
	})
	defer idle.stop()

	rw := newWSReadWriter(ctx, c, idle)
	connID := fmt.Sprintf("c%d", ws.server.connSeq.Add(1))

	// Give the hub a closer so a hub-side drop (enqueue overflow -> conn.stop)
	// actually tears this socket down and unblocks the read loop below;
	// otherwise the conn leaves the hub but keeps processing inbound frames as a
	// zombie (a dropped player who can still buzz).
	//
	// CloseNow, never c.Close(code, reason): stop() runs INLINE on the engine's
	// broadcast goroutine, and c.Close performs a close handshake that waits for
	// a peer reply the dropped peer will never send — that would freeze the
	// whole game for the handshake timeout. CloseNow is immediate, idempotent,
	// and composes with the defer above.
	ws.hub.add(connID, rw, closerFunc(func() error {
		localClose.Store(true)
		return c.CloseNow()
	}))
	ws.handler.OnConnect(connID, ip)
	defer func() {
		ws.handler.OnDisconnect(connID)
		ws.hub.remove(connID)
	}()

	fr := newFrameReader(rw)
	for {
		body, err := fr.ReadFrame()
		if err != nil {
			if !errors.Is(err, io.EOF) && !isWSNormalClose(err) && !localClose.Load() {
				log.Printf("transport/ws: conn %s read error: %v", connID, err)
			}
			return
		}
		idle.reset()

		arrivalUnixMs := time.Now().UnixMilli()

		var env protocol.ClientEnvelope
		if err := unmarshalEnvelope(body, &env); err != nil {
			log.Printf("transport/ws: conn %s malformed frame: %v", connID, err)
			continue
		}

		ws.handler.OnMessage(connID, ws.hub.roleOf(connID), env, arrivalUnixMs)
	}
}

// wsReadWriter adapts a nhooyr websocket.Conn to io.ReadWriter so the Hub's
// frame-writing and the frameReader work without changes.
type wsReadWriter struct {
	ctx    context.Context
	conn   *websocket.Conn
	reader io.Reader
	idle   *idleTimer // nil-safe; reset on every successful write
}

func newWSReadWriter(ctx context.Context, c *websocket.Conn, idle *idleTimer) *wsReadWriter {
	return &wsReadWriter{ctx: ctx, conn: c, idle: idle}
}

func (rw *wsReadWriter) Read(p []byte) (int, error) {
	for {
		if rw.reader != nil {
			n, err := rw.reader.Read(p)
			if errors.Is(err, io.EOF) {
				rw.reader = nil
				if n > 0 {
					return n, nil
				}
				continue
			}
			return n, err
		}
		_, reader, err := rw.conn.Reader(rw.ctx)
		if err != nil {
			return 0, err
		}
		rw.reader = reader
	}
}

func (rw *wsReadWriter) Write(p []byte) (int, error) {
	err := rw.conn.Write(rw.ctx, websocket.MessageBinary, p)
	if err != nil {
		return 0, err
	}
	rw.idle.reset()
	return len(p), nil
}

// idleTimer closes a connection after a period with no traffic. reset() is
// called from BOTH the read loop and the hub's writer goroutine, so the
// underlying time.Timer is mutex-guarded; once stop() has run (handler exit)
// further resets are no-ops so the timer cannot be re-armed after teardown.
//
// time.Timer.Reset does NOT retract an AfterFunc callback that has already
// begun running — a reset() arriving microseconds after expiry does not save
// the connection under a naive single-Timer implementation. gen guards
// against that: each reset() stops the current timer, bumps gen, and arms a
// FRESH timer whose callback closes over the new generation. A callback for
// a superseded timer that is already in flight will, once it acquires mu,
// see i.gen no longer matches the generation it was armed with and veto
// itself instead of firing onExpire.
type idleTimer struct {
	mu       sync.Mutex
	t        *time.Timer
	d        time.Duration
	stopped  bool
	gen      uint64
	onExpire func()
}

func newIdleTimer(d time.Duration, onExpire func()) *idleTimer {
	i := &idleTimer{d: d, onExpire: onExpire}
	i.t = time.AfterFunc(d, i.fireFunc(0))
	return i
}

// fireFunc returns a callback bound to generation gen. It decides under mu
// whether this firing is still current, then — critically — calls onExpire
// AFTER releasing the lock, so onExpire (which may CloseNow/cancel) can never
// be blocked behind, or itself block, a concurrent reset()/stop() call.
func (i *idleTimer) fireFunc(gen uint64) func() {
	return func() {
		i.mu.Lock()
		expired := !i.stopped && i.gen == gen
		i.mu.Unlock()
		if expired {
			i.onExpire()
		}
	}
}

func (i *idleTimer) reset() {
	if i == nil {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.stopped {
		return
	}
	i.gen++
	i.t.Stop()
	i.t = time.AfterFunc(i.d, i.fireFunc(i.gen))
}

func (i *idleTimer) stop() {
	if i == nil {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.stopped = true
	i.t.Stop()
}

func isWSNormalClose(err error) bool {
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		return ce.Code == websocket.StatusNormalClosure || ce.Code == websocket.StatusGoingAway
	}
	return false
}

