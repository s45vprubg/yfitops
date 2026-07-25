package transport

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/s45vprubg/yfitops/server/internal/protocol"
)

// drop_test.go pins the transport-1 fix: when a connection's send queue
// overflows (a client that stopped reading its stream), enqueue drops the whole
// connection — and stop() must close the underlying stream, not just the
// writer. Closing the stream is what unblocks serveStream's parked ReadFrame so
// OnDisconnect can run. Before the fix, stop() only closed c.closed (the writer)
// and the stream/read loop leaked as a zombie.

// blockingWriter never drains — Write blocks until the conn is stopped, so the
// writeLoop parks on it and the out channel fills, exactly like a peer that has
// stopped reading its WebTransport stream.
type blockingWriter struct {
	release chan struct{}
}

func (b *blockingWriter) Write(p []byte) (int, error) {
	<-b.release // never returns until closed
	return len(p), nil
}

// recordingCloser stands in for the streamCloser wired in serveStream (which
// runs CancelRead + Close on the real WebTransport stream). We assert it is
// invoked when the connection is dropped.
type recordingCloser struct {
	mu     sync.Mutex
	closed bool
}

func (r *recordingCloser) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return nil
}
func (r *recordingCloser) wasClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// TestConnDropClosesStreamOnOverflow: overflow the send queue and assert the
// stream closer fired (whole-connection teardown), not just the writer stop.
func TestConnDropClosesStreamOnOverflow(t *testing.T) {
	h := NewHub()
	bw := &blockingWriter{release: make(chan struct{})}
	rc := &recordingCloser{}
	defer close(bw.release)

	c := h.add("victim", bw, rc)

	// Fill the writeLoop (parked on the blocking Write) + the entire out buffer.
	// The first enqueue is consumed by writeLoop and parks on Write; the next
	// sendQueueDepth enqueues fill the channel; the one after overflows -> drop.
	for i := 0; i < sendQueueDepth+8; i++ {
		c.enqueue(protocol.ServerEnvelope{Type: protocol.SMsgState})
	}

	// stop() must have closed the stream closer (idempotent via sync.Once).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if rc.wasClosed() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !rc.wasClosed() {
		t.Fatal("ZOMBIE: send-queue overflow dropped the conn but never closed the stream — the read loop would stay parked and OnDisconnect never fire (transport-1 regression)")
	}

	// And the closed channel must be closed (writer torn down too).
	select {
	case <-c.closed:
	default:
		t.Fatal("conn.closed not closed after drop")
	}
}

// TestConnStopIsIdempotent: stop() may be called more than once (enqueue drop +
// serveStream defer). The sync.Once must make repeated stops safe.
func TestConnStopIsIdempotent(t *testing.T) {
	h := NewHub()
	bw := &blockingWriter{release: make(chan struct{})}
	rc := &countingCloser{}
	defer close(bw.release)

	c := h.add("v2", bw, rc)
	c.stop()
	c.stop()
	c.stop()

	if got := rc.count(); got != 1 {
		t.Fatalf("stream closer called %d times, want exactly 1 (sync.Once)", got)
	}
}

type countingCloser struct {
	mu sync.Mutex
	n  int
}

func (c *countingCloser) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return nil
}
func (c *countingCloser) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

var _ = errors.New // reserved for future error-path assertions
