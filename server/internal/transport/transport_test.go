package transport

import (
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/s45vprubg/yfitops/server/internal/protocol"
)

// --- Framing codec ---------------------------------------------------------

func TestEncodeFrameRoundTrip(t *testing.T) {
	body := []byte(`{"t":"hello","d":{"role":"mobile"}}`)
	frame := encodeFrame(body)

	if got := binary.BigEndian.Uint32(frame[:4]); got != uint32(len(body)) {
		t.Fatalf("length prefix = %d, want %d", got, len(body))
	}
	if !bytes.Equal(frame[4:], body) {
		t.Fatalf("body mismatch: got %q want %q", frame[4:], body)
	}

	// Read it back through the frameReader.
	fr := newFrameReader(bytes.NewReader(frame))
	got, err := fr.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("round-trip body = %q, want %q", got, body)
	}
}

func TestFrameReaderMultipleFramesInOneBuffer(t *testing.T) {
	bodies := [][]byte{
		[]byte(`{"t":"buzz"}`),
		[]byte(`{"t":"vote"}`),
		[]byte(`{"t":"heartbeat","d":{"clientTime":42}}`),
	}
	var stream bytes.Buffer
	for _, b := range bodies {
		stream.Write(encodeFrame(b))
	}

	fr := newFrameReader(&stream)
	for i, want := range bodies {
		got, err := fr.ReadFrame()
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("frame %d = %q, want %q", i, got, want)
		}
	}
	if _, err := fr.ReadFrame(); err != io.EOF {
		t.Fatalf("after draining, err = %v, want EOF", err)
	}
}

// chunkReader feeds bytes in fixed-size chunks to exercise partial-frame
// reassembly across multiple Read calls.
type chunkReader struct {
	data  []byte
	chunk int
	pos   int
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if c.pos >= len(c.data) {
		return 0, io.EOF
	}
	end := c.pos + c.chunk
	if end > len(c.data) {
		end = len(c.data)
	}
	n := copy(p, c.data[c.pos:end])
	c.pos += n
	return n, nil
}

func TestFrameReaderPartialReassembly(t *testing.T) {
	bodies := [][]byte{
		[]byte(`{"t":"hello","d":{"role":"stage","handle":"dj"}}`),
		[]byte(`{"t":"admin.grade","d":{"verdict":"correct"}}`),
	}
	var stream bytes.Buffer
	for _, b := range bodies {
		stream.Write(encodeFrame(b))
	}

	// Feed one byte at a time: the prefix, then the body, must reassemble.
	fr := newFrameReader(&chunkReader{data: stream.Bytes(), chunk: 1})
	for i, want := range bodies {
		got, err := fr.ReadFrame()
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("frame %d = %q, want %q", i, got, want)
		}
	}
}

func TestFrameReaderTruncatedFrameIsUnexpectedEOF(t *testing.T) {
	body := []byte(`{"t":"buzz"}`)
	frame := encodeFrame(body)
	// Drop the last byte of the body.
	fr := newFrameReader(bytes.NewReader(frame[:len(frame)-1]))
	if _, err := fr.ReadFrame(); err != io.ErrUnexpectedEOF {
		t.Fatalf("err = %v, want ErrUnexpectedEOF", err)
	}
}

func TestFrameReaderRejectsOversizeFrame(t *testing.T) {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], maxFrameLen+1)
	fr := newFrameReader(bytes.NewReader(hdr[:]))
	if _, err := fr.ReadFrame(); err == nil {
		t.Fatal("expected error for oversize frame length, got nil")
	}
}

// --- Hub -------------------------------------------------------------------

// captureWriter records every framed write, decoding it back into a
// ServerEnvelope so tests can assert on what each connection received.
type captureWriter struct {
	mu  sync.Mutex
	got []protocol.ServerEnvelope
}

func (c *captureWriter) Write(p []byte) (int, error) {
	// p is exactly one frame (encodeFrame output) per writeLoop iteration.
	body := p[frameHeaderLen:]
	var env protocol.ServerEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return 0, err
	}
	c.mu.Lock()
	c.got = append(c.got, env)
	c.mu.Unlock()
	return len(p), nil
}

func (c *captureWriter) types() []protocol.ServerMsgType {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]protocol.ServerMsgType, len(c.got))
	for i, e := range c.got {
		out[i] = e.Type
	}
	return out
}

// waitFor polls until the writer has received n frames or the deadline passes.
func (c *captureWriter) waitFor(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		got := len(c.got)
		c.mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	c.mu.Lock()
	got := len(c.got)
	c.mu.Unlock()
	t.Fatalf("timed out waiting for %d frames; got %d", n, got)
}

func TestHubBroadcastIsRoleScoped(t *testing.T) {
	h := NewHub()

	stageW, mobileW, adminW := &captureWriter{}, &captureWriter{}, &captureWriter{}
	h.add("stage1", stageW)
	h.add("mobile1", mobileW)
	h.add("admin1", adminW)
	h.SetRole("stage1", protocol.RoleStage)
	h.SetRole("admin1", protocol.RoleAdmin)
	// mobile1 stays at the default RoleMobile.

	// A reveal frame must reach ONLY stage connections (the §4A boundary).
	h.Broadcast(protocol.RoleStage, protocol.ServerEnvelope{Type: protocol.SMsgReveal})

	stageW.waitFor(t, 1)
	if got := stageW.types(); len(got) != 1 || got[0] != protocol.SMsgReveal {
		t.Fatalf("stage got %v, want [reveal]", got)
	}
	// Give the others a chance to (wrongly) receive anything.
	time.Sleep(20 * time.Millisecond)
	if got := mobileW.types(); len(got) != 0 {
		t.Fatalf("mobile received role-scoped reveal: %v", got)
	}
	if got := adminW.types(); len(got) != 0 {
		t.Fatalf("admin received stage-scoped reveal: %v", got)
	}
}

func TestHubSendToReachesOneConn(t *testing.T) {
	h := NewHub()
	aW, bW := &captureWriter{}, &captureWriter{}
	h.add("a", aW)
	h.add("b", bW)

	h.SendTo("a", protocol.ServerEnvelope{Type: protocol.SMsgBuzzResult})

	aW.waitFor(t, 1)
	time.Sleep(20 * time.Millisecond)
	if got := bW.types(); len(got) != 0 {
		t.Fatalf("SendTo leaked to other conn: %v", got)
	}
	if got := aW.types(); got[0] != protocol.SMsgBuzzResult {
		t.Fatalf("conn a got %v, want buzzResult", got)
	}
}

func TestHubBroadcastAllReachesEveryone(t *testing.T) {
	h := NewHub()
	writers := map[string]*captureWriter{
		"stage1":  {},
		"mobile1": {},
		"admin1":  {},
	}
	for id, w := range writers {
		h.add(id, w)
	}
	h.SetRole("stage1", protocol.RoleStage)
	h.SetRole("admin1", protocol.RoleAdmin)

	h.BroadcastAll(protocol.ServerEnvelope{Type: protocol.SMsgState})

	for id, w := range writers {
		w.waitFor(t, 1)
		if got := w.types(); got[0] != protocol.SMsgState {
			t.Fatalf("conn %s got %v, want state", id, got)
		}
	}
}

func TestHubStampsMonotonicSeq(t *testing.T) {
	h := NewHub()
	w := &captureWriter{}
	h.add("a", w)

	for i := 0; i < 3; i++ {
		h.SendTo("a", protocol.ServerEnvelope{Type: protocol.SMsgState})
	}
	w.waitFor(t, 3)

	w.mu.Lock()
	defer w.mu.Unlock()
	for i, e := range w.got {
		if e.Seq != uint64(i+1) {
			t.Fatalf("frame %d seq = %d, want %d", i, e.Seq, i+1)
		}
	}
}

func TestHubRemoveStopsDelivery(t *testing.T) {
	h := NewHub()
	w := &captureWriter{}
	h.add("a", w)
	h.remove("a")

	// Sending after removal must not panic or deliver.
	h.SendTo("a", protocol.ServerEnvelope{Type: protocol.SMsgState})
	time.Sleep(20 * time.Millisecond)
	if got := w.types(); len(got) != 0 {
		t.Fatalf("removed conn still received: %v", got)
	}
}

// --- Cert generation -------------------------------------------------------

func TestGenerateSelfSignedLoadable(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	if err := GenerateSelfSigned(certPath, keyPath); err != nil {
		t.Fatalf("GenerateSelfSigned: %v", err)
	}

	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("LoadX509KeyPair: %v", err)
	}
	if len(cert.Certificate) == 0 {
		t.Fatal("loaded cert has no certificate chain")
	}

	raw, b64, err := CertSHA256(certPath)
	if err != nil {
		t.Fatalf("CertSHA256: %v", err)
	}
	if b64 == "" {
		t.Fatal("empty base64 cert hash")
	}
	if raw == [32]byte{} {
		t.Fatal("zero cert hash")
	}
}
