package transport

import (
	"encoding/json"
	"io"
	"log"
	"sync"
	"sync/atomic"

	"github.com/s45vprubg/yfitops/server/internal/protocol"
)

// sendQueueDepth is the per-connection outbound buffer. Frames are produced by
// the engine's command loop and drained by a dedicated writer goroutine; the
// buffer absorbs bursts (e.g. a full state sync) without blocking the engine.
// If a slow client backs this up, we drop the connection rather than stall the
// whole game (see enqueue).
const sendQueueDepth = 64

// conn is the hub's view of a single live connection: its assigned role, the
// stream writer, and a buffered outbound channel feeding the writer goroutine.
type conn struct {
	id   string
	role protocol.Role

	out    chan protocol.ServerEnvelope
	w      io.Writer
	seq    atomic.Uint64 // per-connection ServerEnvelope.Seq (protocol.go §Seq)
	closed chan struct{}
	once   sync.Once
}

// Hub tracks all live connections and implements game.Broadcaster. Role-scoped
// fan-out (Broadcast) is the transport-level enforcement point for the §4A
// sanitization boundary: the engine emits a reveal frame to RoleStage and the
// hub guarantees no mobile connection can receive it.
//
// All map access is guarded by mu; per-connection sends go through a buffered
// channel so a single slow writer never blocks the broadcaster.
type Hub struct {
	mu    sync.RWMutex
	conns map[string]*conn
}

// NewHub returns an empty hub ready to register connections.
func NewHub() *Hub {
	return &Hub{conns: make(map[string]*conn)}
}

// add registers a connection with the given writer and starts its writer
// goroutine. New connections default to RoleMobile (the least-trusted role)
// until a validated Hello promotes them via SetRole.
func (h *Hub) add(id string, w io.Writer) *conn {
	c := &conn{
		id:     id,
		role:   protocol.RoleMobile,
		out:    make(chan protocol.ServerEnvelope, sendQueueDepth),
		w:      w,
		closed: make(chan struct{}),
	}
	h.mu.Lock()
	h.conns[id] = c
	h.mu.Unlock()

	go c.writeLoop()
	return c
}

// remove unregisters a connection and stops its writer goroutine. Safe to call
// more than once.
func (h *Hub) remove(id string) {
	h.mu.Lock()
	c := h.conns[id]
	delete(h.conns, id)
	h.mu.Unlock()
	if c != nil {
		c.stop()
	}
}

// SetRole sets a connection's authenticated role after a validated Hello. The
// engine holds the concrete *Hub and calls this once it trusts the client's
// declared role (mobile is the default until then). No-op if the connection is
// already gone.
func (h *Hub) SetRole(connID string, role protocol.Role) {
	h.mu.Lock()
	if c := h.conns[connID]; c != nil {
		c.role = role
	}
	h.mu.Unlock()
}

// SendTo delivers a frame to a single connection. Implements game.Broadcaster.
func (h *Hub) SendTo(connID string, env protocol.ServerEnvelope) {
	h.mu.RLock()
	c := h.conns[connID]
	h.mu.RUnlock()
	if c != nil {
		c.enqueue(env)
	}
}

// Broadcast delivers a frame to every connection of the given role. This is the
// sanitization gate (§4A): reveal/lyrics/admin payloads go out role-scoped so
// they can never reach mobile. Implements game.Broadcaster.
func (h *Hub) Broadcast(role protocol.Role, env protocol.ServerEnvelope) {
	h.mu.RLock()
	targets := make([]*conn, 0, len(h.conns))
	for _, c := range h.conns {
		if c.role == role {
			targets = append(targets, c)
		}
	}
	h.mu.RUnlock()
	for _, c := range targets {
		c.enqueue(env)
	}
}

// BroadcastAll delivers to every connection regardless of role. Use only for
// already-sanitized payloads (StateData). Implements game.Broadcaster.
func (h *Hub) BroadcastAll(env protocol.ServerEnvelope) {
	h.mu.RLock()
	targets := make([]*conn, 0, len(h.conns))
	for _, c := range h.conns {
		targets = append(targets, c)
	}
	h.mu.RUnlock()
	for _, c := range targets {
		c.enqueue(env)
	}
}

// enqueue hands a frame to the connection's writer goroutine. If the buffer is
// full the client is too slow to keep up with game state, so we drop the
// connection rather than block the engine — a stalled buzzer is worse than a
// dropped laggard.
func (c *conn) enqueue(env protocol.ServerEnvelope) {
	select {
	case <-c.closed:
		return
	case c.out <- env:
	default:
		log.Printf("transport: send queue full for conn %s; dropping connection", c.id)
		c.stop()
	}
}

// writeLoop serializes outbound envelopes, stamps a per-connection sequence
// number, and frames them onto the stream until the connection is stopped.
func (c *conn) writeLoop() {
	for {
		select {
		case <-c.closed:
			return
		case env := <-c.out:
			env.Seq = c.seq.Add(1)
			body, err := json.Marshal(env)
			if err != nil {
				log.Printf("transport: marshal envelope for conn %s: %v", c.id, err)
				continue
			}
			if _, err := c.w.Write(encodeFrame(body)); err != nil {
				// Write failure means the stream is gone; stop and let the read
				// loop drive OnDisconnect.
				c.stop()
				return
			}
		}
	}
}

// stop closes the writer goroutine exactly once.
func (c *conn) stop() {
	c.once.Do(func() { close(c.closed) })
}
