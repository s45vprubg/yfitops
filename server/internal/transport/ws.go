package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"nhooyr.io/websocket"

	"github.com/s45vprubg/yfitops/server/internal/game"
	"github.com/s45vprubg/yfitops/server/internal/protocol"
)

// WSHandler upgrades plain HTTP connections to WebSocket and bridges them into
// the same Hub used by WebTransport. This is a DEV-ONLY fallback for phones on
// a LAN where the page is served over plain HTTP (no secure context →
// WebTransport is undefined in the browser). It must NEVER be registered in
// production — the gameserver only mounts it when YFI_DEV_WS=1.
type WSHandler struct {
	hub     *Hub
	handler game.InboundHandler
	server  *Server
}

// NewWSHandler creates a WebSocket handler sharing the same hub and engine as
// the WebTransport server.
func NewWSHandler(hub *Hub, handler game.InboundHandler, srv *Server) *WSHandler {
	return &WSHandler{hub: hub, handler: handler, server: srv}
}

func (ws *WSHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		log.Printf("transport/ws: upgrade failed: %v", err)
		return
	}
	defer c.CloseNow()

	ctx := r.Context()
	rw := newWSReadWriter(ctx, c)
	connID := fmt.Sprintf("c%d", ws.server.connSeq.Add(1))

	ws.hub.add(connID, rw)
	ws.handler.OnConnect(connID, clientIP(r.RemoteAddr))
	defer func() {
		ws.handler.OnDisconnect(connID)
		ws.hub.remove(connID)
	}()

	fr := newFrameReader(rw)
	for {
		body, err := fr.ReadFrame()
		if err != nil {
			if !errors.Is(err, io.EOF) && !isWSNormalClose(err) {
				log.Printf("transport/ws: conn %s read error: %v", connID, err)
			}
			return
		}

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
}

func newWSReadWriter(ctx context.Context, c *websocket.Conn) *wsReadWriter {
	return &wsReadWriter{ctx: ctx, conn: c}
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
	return len(p), nil
}

func isWSNormalClose(err error) bool {
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		return ce.Code == websocket.StatusNormalClosure || ce.Code == websocket.StatusGoingAway
	}
	return false
}

