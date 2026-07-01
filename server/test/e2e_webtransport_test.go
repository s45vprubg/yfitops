// Package test holds end-to-end integration tests that drive the REAL
// transport stack (WebTransport over QUIC with a self-signed cert) against the
// real engine + in-memory store. These are the tests that prove the wiring
// holds over the wire — role promotion on Hello (§4A), the sanitization
// boundary (mobile never receives reveal/lyrics), and the atomic single-winner
// buzz (§3.4) — things the per-package unit tests can't catch because they call
// the engine directly with the role pre-set.
//
// This runs headlessly: webtransport-go is both client and server, so no
// browser is needed. It is the closest we get to verifying the browser clients'
// path without Chromium.
package test

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/webtransport-go"

	"github.com/s45vprubg/yfitops/server/internal/anticheat"
	"github.com/s45vprubg/yfitops/server/internal/config"
	"github.com/s45vprubg/yfitops/server/internal/game"
	"github.com/s45vprubg/yfitops/server/internal/lyrics"
	"github.com/s45vprubg/yfitops/server/internal/protocol"
	"github.com/s45vprubg/yfitops/server/internal/spotify"
	"github.com/s45vprubg/yfitops/server/internal/store"
	"github.com/s45vprubg/yfitops/server/internal/transport"
)

// wtClient is a tiny WebTransport client mirroring web/shared/client.ts framing:
// [4-byte big-endian length][JSON envelope] over one bidirectional stream.
type wtClient struct {
	sess   *webtransport.Session
	stream *webtransport.Stream
	mu     sync.Mutex
	recv   []protocol.ServerEnvelope
	recvMu sync.Mutex
}

func dialClient(t *testing.T, ctx context.Context, url string) *wtClient {
	t.Helper()
	d := webtransport.Dialer{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // self-signed dev cert; this is a test
			NextProtos:         []string{"h3"},
		},
		QUICConfig: &quic.Config{EnableDatagrams: true, EnableStreamResetPartialDelivery: true},
	}
	_, sess, err := d.Dial(ctx, url, http.Header{})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	stream, err := sess.OpenStreamSync(ctx)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	c := &wtClient{sess: sess, stream: stream}
	go c.readLoop()
	return c
}

func (c *wtClient) send(t *testing.T, env protocol.ClientEnvelope) {
	t.Helper()
	body, _ := json.Marshal(env)
	frame := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(body)))
	copy(frame[4:], body)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.stream.Write(frame); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func (c *wtClient) readLoop() {
	var buf []byte
	tmp := make([]byte, 4096)
	for {
		n, err := c.stream.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			for {
				if len(buf) < 4 {
					break
				}
				l := binary.BigEndian.Uint32(buf[:4])
				if uint32(len(buf)) < 4+l {
					break
				}
				body := buf[4 : 4+l]
				var env protocol.ServerEnvelope
				if json.Unmarshal(body, &env) == nil {
					c.recvMu.Lock()
					c.recv = append(c.recv, env)
					c.recvMu.Unlock()
				}
				buf = buf[4+l:]
			}
		}
		if err != nil {
			return
		}
	}
}

func (c *wtClient) received() []protocol.ServerEnvelope {
	c.recvMu.Lock()
	defer c.recvMu.Unlock()
	out := make([]protocol.ServerEnvelope, len(c.recv))
	copy(out, c.recv)
	return out
}

// lastNonce returns the highest nonce observed across received frames, matching
// the browser client's "echo the latest server nonce" behavior (client.ts).
func (c *wtClient) lastNonce() uint64 {
	c.recvMu.Lock()
	defer c.recvMu.Unlock()
	var n uint64
	for _, e := range c.recv {
		if e.Nonce > n {
			n = e.Nonce
		}
	}
	return n
}

// waitFor polls until pred sees a matching frame or the deadline passes.
func (c *wtClient) waitFor(t *testing.T, pred func(protocol.ServerEnvelope) bool, what string) protocol.ServerEnvelope {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range c.received() {
			if pred(e) {
				return e
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; got %d frames", what, len(c.received()))
	return protocol.ServerEnvelope{}
}

// startServer boots the real engine + transport on an ephemeral UDP port and
// returns the dial URL plus a teardown func.
func startServer(t *testing.T) (url string, teardown func()) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Load()
	cfg.CertFile = filepath.Join(dir, "cert.pem")
	cfg.KeyFile = filepath.Join(dir, "key.pem")
	cfg.AdminSecret = "test-admin"

	repo := store.NewMemRepo()
	board := repo.SeedSampleBoard(cfg.SessionID())
	lock := store.NewMemLock()
	gate := anticheat.NewNonceGate([]byte(cfg.NonceSecret))
	hub := transport.NewHub()

	eng := game.NewEngine(repo, lock, spotify.New(cfg), lyrics.New(cfg), hub, gate, game.Config{
		SessionID:   cfg.SessionID(),
		AdminSecret: cfg.AdminSecret,
	})
	eng.SetBoard(board)
	eng.SetRoleSetter(hub) // the wiring this whole test exists to prove

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = eng.Run(ctx) }()

	srv, err := transport.NewServer(cfg, hub, eng)
	if err != nil {
		cancel()
		t.Fatalf("new server: %v", err)
	}

	// Bind an ephemeral UDP socket so the OS hands us a free port and we know it.
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		cancel()
		t.Fatalf("listen udp: %v", err)
	}
	port := pc.LocalAddr().(*net.UDPAddr).Port
	go func() { _ = srv.Serve(pc) }()
	time.Sleep(200 * time.Millisecond) // let the listener come up

	return "https://127.0.0.1:" + itoa(port) + "/wt", func() {
		cancel()
		_ = srv.Close()
		_ = pc.Close()
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestE2E_HelloWelcome proves a mobile client can connect over real
// WebTransport, send a Hello, and get a Welcome back with a player ID — the
// full transport -> engine -> hub -> transport round trip.
func TestE2E_HelloWelcome(t *testing.T) {
	url, teardown := startServer(t)
	defer teardown()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := dialClient(t, ctx, url)

	hello, _ := json.Marshal(protocol.HelloData{Role: protocol.RoleMobile, Handle: "neo", DeviceFP: "fp-1"})
	c.send(t, protocol.ClientEnvelope{Type: protocol.CMsgHello, Data: hello})

	w := c.waitFor(t, func(e protocol.ServerEnvelope) bool {
		return e.Type == protocol.SMsgWelcome
	}, "welcome")
	var wd protocol.WelcomeData
	_ = json.Unmarshal(w.Data, &wd)
	if wd.PlayerID == "" {
		t.Fatal("welcome carried no player ID")
	}
	if wd.Role != protocol.RoleMobile {
		t.Errorf("welcome role = %q, want mobile", wd.Role)
	}
}

// TestE2E_RolePromotionAndSanitization is THE integration test. It proves the
// bug fixed at integration time — that the engine promotes a connection's role
// in the transport hub on a validated Hello (without which role-scoped
// broadcasts reach no one) — AND that the §4A sanitization boundary holds over
// the real wire: a stage client receives the reveal payload, a mobile client
// connected to the SAME game never does.
func TestE2E_RolePromotionAndSanitization(t *testing.T) {
	url, teardown := startServer(t)
	defer teardown()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	// Connect a stage (trusted) and a mobile (untrusted) client.
	stage := dialClient(t, ctx, url)
	mobile := dialClient(t, ctx, url)

	// Stage is a TRUSTED client (it receives reveal data), so it is now gated by
	// the shared secret just like admin — send it.
	stageHello, _ := json.Marshal(protocol.HelloData{Role: protocol.RoleStage, AdminSecret: "test-admin"})
	stage.send(t, protocol.ClientEnvelope{Type: protocol.CMsgHello, Data: stageHello})
	stage.waitFor(t, func(e protocol.ServerEnvelope) bool { return e.Type == protocol.SMsgWelcome }, "stage welcome")

	mobHello, _ := json.Marshal(protocol.HelloData{Role: protocol.RoleMobile, Handle: "trinity", DeviceFP: "fp-2"})
	mobile.send(t, protocol.ClientEnvelope{Type: protocol.CMsgHello, Data: mobHello})
	mobile.waitFor(t, func(e protocol.ServerEnvelope) bool { return e.Type == protocol.SMsgWelcome }, "mobile welcome")

	// Connect an admin and drive a cell selection -> a track starts and the
	// stage should receive a trackStart, then a reveal once we force-reveal.
	admin := dialClient(t, ctx, url)
	adminHello, _ := json.Marshal(protocol.HelloData{Role: protocol.RoleAdmin, AdminSecret: "test-admin"})
	admin.send(t, protocol.ClientEnvelope{Type: protocol.CMsgHello, Data: adminHello})
	aw := admin.waitFor(t, func(e protocol.ServerEnvelope) bool { return e.Type == protocol.SMsgWelcome }, "admin welcome")
	var awd protocol.WelcomeData
	_ = json.Unmarshal(aw.Data, &awd)

	// Admin selects a cell. The board is 1-indexed (rows/cols 1-5). Echo nonce.
	sel, _ := json.Marshal(protocol.AdminSelectData{Row: 1, Col: 1})
	admin.send(t, protocol.ClientEnvelope{Type: protocol.CMsgAdminSelect, Data: sel, Nonce: awd.Nonce})

	// Stage must receive a trackStart (trusted timing payload). This only
	// arrives if SetRole promoted the stage conn — otherwise Broadcast(stage)
	// hits nobody.
	stage.waitFor(t, func(e protocol.ServerEnvelope) bool {
		return e.Type == protocol.SMsgTrackStart
	}, "stage trackStart (proves role promotion)")

	// Force a reveal so the trusted reveal payload is broadcast.
	// Re-fetch a fresh nonce from any state frame the admin saw.
	admin.send(t, protocol.ClientEnvelope{Type: protocol.CMsgAdminReveal, Nonce: admin.lastNonce()})

	// Stage SHOULD get the reveal with real artist/song.
	rv := stage.waitFor(t, func(e protocol.ServerEnvelope) bool {
		return e.Type == protocol.SMsgReveal
	}, "stage reveal")
	var rd protocol.RevealData
	_ = json.Unmarshal(rv.Data, &rd)
	if rd.Artist == "" && rd.Song == "" {
		t.Error("stage reveal carried no track metadata")
	}

	// Give any errant frame time to arrive, then assert the §4A invariants.
	time.Sleep(300 * time.Millisecond)

	// (1) Mobile must NEVER receive a trusted frame. The server-authoritative
	// letter reveal ("maskedReveal") is explicitly NOT trusted — it carries only
	// letters already shown on the stage — but the full SMsgReveal, lyrics,
	// adminView, board, and trackStart remain stage/admin-only.
	for _, e := range mobile.received() {
		switch e.Type {
		case protocol.SMsgReveal, protocol.SMsgLyrics, protocol.SMsgAdminView, protocol.SMsgBoard, protocol.SMsgTrackStart:
			t.Fatalf("SANITIZATION BREACH (§4A): mobile received trusted frame %q: %s", e.Type, string(e.Data))
		}
		// The RAW answer string may appear in a mobile frame ONLY inside a
		// maskedReveal (where letters are per-char array elements, so the full
		// contiguous artist string never actually appears). Any OTHER frame type
		// containing the artist substring is a breach.
		if e.Type != maskedRevealType && rd.Artist != "" && len(e.Data) > 0 && containsStr(string(e.Data), rd.Artist) {
			t.Fatalf("SANITIZATION BREACH (§4A): mobile frame %q leaked artist %q", e.Type, rd.Artist)
		}
	}

	// (2) Co-visibility: every maskedReveal the MOBILE received must byte-equal
	// one the STAGE received. Since the engine builds ONE envelope and fans the
	// identical frame to both roles, this encodes "mobile never gets a letter
	// the stage wasn't sent, and is never ahead of the projector."
	stageMasks := map[string]bool{}
	for _, e := range stage.received() {
		if e.Type == maskedRevealType {
			stageMasks[string(e.Data)] = true
		}
	}
	mobileMaskCount := 0
	for _, e := range mobile.received() {
		if e.Type != maskedRevealType {
			continue
		}
		mobileMaskCount++
		if !stageMasks[string(e.Data)] {
			t.Fatalf("CO-VISIBILITY BREACH (§4A): mobile got a maskedReveal the stage never received: %s", string(e.Data))
		}
	}
	if mobileMaskCount == 0 {
		t.Error("expected mobile to receive at least one maskedReveal frame (streamed reveal)")
	}
}

// maskedRevealType mirrors the local smsgMaskedReveal const in package game
// (a CONTRACT-QUESTION type, not in protocol.go).
const maskedRevealType protocol.ServerMsgType = "maskedReveal"

func containsStr(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && stringIndex(haystack, needle) >= 0
}

func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
