package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"

	"github.com/s45vprubg/yfitops/server/internal/config"
	"github.com/s45vprubg/yfitops/server/internal/game"
	"github.com/s45vprubg/yfitops/server/internal/protocol"
)

// wtPath is the WebTransport endpoint clients CONNECT to. client.ts is
// constructed with a URL like https://host:4433/wt, so the path must match.
const wtPath = "/wt"

// controlStreamAcceptTimeout bounds how long a freshly-upgraded session may
// take to open its single control stream before we give up and tear it down
// (transport-2). A well-behaved client opens the stream immediately.
const controlStreamAcceptTimeout = 10 * time.Second

// Server is the WebTransport/HTTP3 edge. It accepts QUIC sessions, opens one
// bidirectional control stream per session, and bridges decoded frames to the
// game engine through the InboundHandler seam while fanning out engine frames
// through the Hub.
type Server struct {
	cfg     *config.Config
	hub     *Hub
	handler game.InboundHandler

	wt      *webtransport.Server
	connSeq atomic.Uint64   // monotonic source of connection IDs
	limiter *sessionLimiter // caps concurrent sessions globally + per IP
}

// NewServer wires the transport to its hub and the engine's inbound handler.
// It loads cfg.CertFile/KeyFile, generating a self-signed pair if either is
// missing so headless test/dev works out of the box.
func NewServer(cfg *config.Config, hub *Hub, handler game.InboundHandler) (*Server, error) {
	if hub == nil {
		return nil, errors.New("transport: nil hub")
	}
	if handler == nil {
		return nil, errors.New("transport: nil handler")
	}

	cert, err := loadOrGenerateCert(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, err
	}

	h3 := &http3.Server{
		Addr:      cfg.ListenAddr,
		TLSConfig: http3.ConfigureTLSConfig(&tls.Config{Certificates: []tls.Certificate{cert}}),
		QUICConfig: &quic.Config{
			EnableDatagrams:                  true,
			EnableStreamResetPartialDelivery: true,
			// Defensive limits (transport-2): reap idle/dead sessions and cap
			// per-session streams. The application opens exactly one
			// bidirectional control stream per session (see handleSession), but
			// at the QUIC layer the HTTP/3 CONNECT request is itself a
			// bidirectional stream, so a legitimate WebTransport session needs 2
			// incoming bidi streams (CONNECT + control). Cap at 2 to reject a
			// client that tries to fan out extra streams while still allowing the
			// normal path. Verified against the E2E WebTransport test.
			MaxIdleTimeout:     30 * time.Second,
			MaxIncomingStreams: 2,
		},
	}
	webtransport.ConfigureHTTP3Server(h3)

	s := &Server{
		cfg:     cfg,
		hub:     hub,
		handler: handler,
		limiter: newSessionLimiterFromEnv(),
	}

	mux := http.NewServeMux()
	h3.Handler = mux
	s.wt = &webtransport.Server{
		H3: h3,
		// Dev/LAN party deployment: clients connect from arbitrary phone
		// browsers, so cross-origin CONNECTs are expected. Auth happens at the
		// Hello layer, not the origin.
		CheckOrigin: func(*http.Request) bool { return true },
	}
	mux.HandleFunc(wtPath, s.handleSession)

	return s, nil
}

// Start runs the server until ctx is cancelled, then shuts it down. It blocks.
func (s *Server) Start(ctx context.Context) error {
	errc := make(chan error, 1)
	go func() { errc <- s.ListenAndServe() }()

	select {
	case <-ctx.Done():
		_ = s.wt.Close()
		<-errc // let ListenAndServe unwind
		return ctx.Err()
	case err := <-errc:
		return err
	}
}

// ListenAndServe serves WebTransport on cfg.ListenAddr until the server is
// closed. The TLS cert is already configured on the embedded http3.Server.
func (s *Server) ListenAndServe() error {
	log.Printf("transport: WebTransport listening on %s%s", s.cfg.ListenAddr, wtPath)
	err := s.wt.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, quic.ErrServerClosed) {
		return nil
	}
	return err
}

// Serve serves WebTransport over a caller-provided UDP socket instead of
// binding cfg.ListenAddr itself. This lets a caller bind an ephemeral port
// (":0") and learn the actual address via the PacketConn — used by the E2E
// integration test, and useful in production for socket-activation / passing a
// pre-tuned UDP buffer. Blocks until the server is closed.
func (s *Server) Serve(conn net.PacketConn) error {
	err := s.wt.Serve(conn)
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, quic.ErrServerClosed) {
		return nil
	}
	return err
}

// Close shuts the server down.
func (s *Server) Close() error { return s.wt.Close() }

// handleSession upgrades an HTTP/3 CONNECT into a WebTransport session, then
// services its single control stream.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	// Per-IP + global session cap (QA sweep 3). Reject BEFORE the WebTransport
	// upgrade so a flooding source is turned away cheaply, without spending a
	// handshake. Acquire pairs with exactly one release on every exit path.
	ip := clientIP(r.RemoteAddr)
	if !s.limiter.acquire(ip) {
		log.Printf("transport: session rejected (limit reached) from %s", ip)
		http.Error(w, "too many connections", http.StatusServiceUnavailable)
		return
	}
	defer s.limiter.release(ip)

	sess, err := s.wt.Upgrade(w, r)
	if err != nil {
		log.Printf("transport: upgrade failed: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// The client opens exactly one bidirectional control stream after the
	// session is established (client.ts: createBidirectionalStream). Bound the
	// wait (transport-2): a session that upgrades but never opens its control
	// stream would otherwise park AcceptStream forever, tying up resources.
	acceptCtx, cancel := context.WithTimeout(sess.Context(), controlStreamAcceptTimeout)
	defer cancel()
	stream, err := sess.AcceptStream(acceptCtx)
	if err != nil {
		log.Printf("transport: accept control stream: %v", err)
		sess.CloseWithError(0, "no control stream")
		return
	}

	s.serveStream(sess, stream, clientIP(r.RemoteAddr))
}

// readCanceler is implemented by streams that can abort their receive side
// (webtransport.Stream.CancelRead). Closing a WebTransport stream only shuts
// the send direction, so we need CancelRead to unblock a parked Read.
type readCanceler interface {
	CancelRead(webtransport.StreamErrorCode)
}

// streamCloser returns an io.Closer that tears the stream fully down: it closes
// the send side and, if supported, cancels the receive side so a Read parked in
// serveStream's loop unblocks and the connection's teardown runs. Idempotency is
// guaranteed by the caller (conn.stop uses sync.Once).
func streamCloser(stream io.ReadWriteCloser) io.Closer {
	return closerFunc(func() error {
		if rc, ok := stream.(readCanceler); ok {
			rc.CancelRead(0)
		}
		return stream.Close()
	})
}

type closerFunc func() error

func (f closerFunc) Close() error { return f() }

// clientIP extracts the host portion of a RemoteAddr ("ip:port" -> "ip").
func clientIP(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}

// serveStream registers the connection, drives the read loop, and guarantees a
// single OnDisconnect on exit.
func (s *Server) serveStream(sess *webtransport.Session, stream io.ReadWriteCloser, remoteIP string) {
	connID := fmt.Sprintf("c%d", s.connSeq.Add(1))

	// Give the hub a closer that fully tears the stream down. A slow client can
	// be dropped by the hub (enqueue -> stop), and the drop must unblock the
	// read loop below. *webtransport.Stream.Close() only closes the SEND side,
	// leaving a parked ReadFrame blocked forever; CancelRead is what actually
	// unblocks it, after which serveStream's defer runs OnDisconnect+remove.
	s.hub.add(connID, stream, streamCloser(stream))
	s.handler.OnConnect(connID, remoteIP)
	defer func() {
		s.handler.OnDisconnect(connID)
		s.hub.remove(connID)
		_ = stream.Close()
	}()

	fr := newFrameReader(stream)
	for {
		body, err := fr.ReadFrame()
		if err != nil {
			if !errors.Is(err, io.EOF) && !isSessionClosed(err) {
				log.Printf("transport: conn %s read error: %v", connID, err)
			}
			return
		}

		// §4B Server arrival authority: stamp the instant the full frame clears
		// the network edge, BEFORE any decode/dispatch work, so buzz ordering
		// uses a clean server clock. Client timestamps are never trusted here.
		arrivalUnixMs := time.Now().UnixMilli()

		var env protocol.ClientEnvelope
		if err := unmarshalEnvelope(body, &env); err != nil {
			// Malformed frame: skip it, matching the browser client's
			// tolerant "skip malformed frame" behavior. A bad frame must not
			// tear down an otherwise healthy connection.
			log.Printf("transport: conn %s malformed frame: %v", connID, err)
			continue
		}

		// Forward with the connection's CURRENT role. New connections are
		// RoleMobile until the engine validates a Hello and calls Hub.SetRole;
		// the engine re-reads the role on the next message.
		s.handler.OnMessage(connID, s.hub.roleOf(connID), env, arrivalUnixMs)
	}
}

// roleOf returns a connection's current role, or RoleMobile if it is unknown
// (already removed). RoleMobile is the safe default: the least-trusted role.
func (h *Hub) roleOf(connID string) protocol.Role {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if c := h.conns[connID]; c != nil {
		return c.role
	}
	return protocol.RoleMobile
}

// isSessionClosed reports whether err is a normal WebTransport session
// teardown rather than a real read fault, to keep logs quiet on clean exits.
func isSessionClosed(err error) bool {
	var serr *webtransport.SessionError
	return errors.As(err, &serr)
}
