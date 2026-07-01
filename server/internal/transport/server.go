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

// Server is the WebTransport/HTTP3 edge. It accepts QUIC sessions, opens one
// bidirectional control stream per session, and bridges decoded frames to the
// game engine through the InboundHandler seam while fanning out engine frames
// through the Hub.
type Server struct {
	cfg     *config.Config
	hub     *Hub
	handler game.InboundHandler

	wt      *webtransport.Server
	connSeq atomic.Uint64 // monotonic source of connection IDs
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
		},
	}
	webtransport.ConfigureHTTP3Server(h3)

	s := &Server{
		cfg:     cfg,
		hub:     hub,
		handler: handler,
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
	sess, err := s.wt.Upgrade(w, r)
	if err != nil {
		log.Printf("transport: upgrade failed: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// The client opens exactly one bidirectional control stream after the
	// session is established (client.ts: createBidirectionalStream).
	stream, err := sess.AcceptStream(sess.Context())
	if err != nil {
		log.Printf("transport: accept control stream: %v", err)
		sess.CloseWithError(0, "no control stream")
		return
	}

	s.serveStream(sess, stream, clientIP(r.RemoteAddr))
}

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

	s.hub.add(connID, stream)
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
