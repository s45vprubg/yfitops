package test

import (
	"context"
	"crypto/tls"
	"net/http"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/webtransport-go"
)

// e2e_sessioncap_test.go proves the per-IP session cap (QA sweep 3) end-to-end
// over real WebTransport: with the per-IP cap set small, the Nth+1 concurrent
// session from the same IP is REJECTED at CONNECT, and freeing one admits a new
// one. This exercises the actual handleSession wiring (acquire before Upgrade +
// defer release), not just the limiter unit.
//
// It sets YFI_MAX_SESSIONS_PER_IP via t.Setenv BEFORE startServer builds the
// server (NewServer reads it), so it must not run in parallel.
func TestE2E_PerIPSessionCapRejectsExcess(t *testing.T) {
	const perIP = 3
	t.Setenv("YFI_MAX_SESSIONS_PER_IP", "3")
	t.Setenv("YFI_MAX_SESSIONS", "0") // isolate the per-IP dimension

	url, teardown := startServer(t)
	defer teardown()

	dial := func(ctx context.Context) (*http.Response, *webtransport.Session, error) {
		d := webtransport.Dialer{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h3"}},
			QUICConfig:      &quic.Config{EnableDatagrams: true, EnableStreamResetPartialDelivery: true},
		}
		return d.Dial(ctx, url, http.Header{})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Open `perIP` sessions from this host (all 127.0.0.1) and open their
	// control streams so each is a fully-established live session.
	var live []*webtransport.Session
	for i := 0; i < perIP; i++ {
		_, sess, err := dial(ctx)
		if err != nil {
			t.Fatalf("session %d should be admitted (under cap): %v", i, err)
		}
		if _, err := sess.OpenStreamSync(ctx); err != nil {
			t.Fatalf("session %d open control stream: %v", i, err)
		}
		live = append(live, sess)
	}
	// Let the server register all `perIP` control streams (handleSession runs
	// per session; acquire happens before Upgrade so the counts settle fast).
	time.Sleep(300 * time.Millisecond)

	// The next session from the same IP must be REJECTED (503 at CONNECT).
	dctx, dcancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer dcancel()
	resp, _, err := dial(dctx)
	if err == nil {
		t.Fatalf("session %d should have been REJECTED by the per-IP cap (%d), but dial succeeded", perIP, perIP)
	}
	if resp != nil && resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("rejected session: status = %d, want 503", resp.StatusCode)
	}

	// Free one slot; a new session must now be admitted.
	live[0].CloseWithError(0, "test: free a slot")
	time.Sleep(500 * time.Millisecond) // let handleSession's defer release run

	rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer rcancel()
	_, sess, err := dial(rctx)
	if err != nil {
		t.Fatalf("after freeing a slot, a new session should be admitted: %v", err)
	}
	if _, err := sess.OpenStreamSync(rctx); err != nil {
		t.Fatalf("recovered session open control stream: %v", err)
	}
	sess.CloseWithError(0, "done")
	for _, s := range live[1:] {
		s.CloseWithError(0, "done")
	}
}
