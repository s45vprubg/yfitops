package spotify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// refreshrace_test.go pins spotify-1: concurrent callers hitting an expired
// token must NOT each fire a refresh_token grant (which risks clobbering a
// rotated refresh token). refreshMu serializes refresh so exactly ONE grant
// runs and late callers reuse the freshly-minted token.
func TestValidToken_ConcurrentRefreshSingleFlight(t *testing.T) {
	var grants int64
	var inFlight int64
	var maxConcurrent int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		if form.Get("grant_type") != "refresh_token" {
			t.Errorf("unexpected grant_type %q", form.Get("grant_type"))
			return
		}
		// Track peak concurrency inside the grant handler: with correct
		// serialization this must never exceed 1.
		n := atomic.AddInt64(&inFlight, 1)
		for {
			m := atomic.LoadInt64(&maxConcurrent)
			if n <= m || atomic.CompareAndSwapInt64(&maxConcurrent, m, n) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond) // widen the race window
		atomic.AddInt64(&grants, 1)
		atomic.AddInt64(&inFlight, -1)
		// Rotate the refresh token each grant, like Spotify may: if two grants
		// ran, the second would overwrite the first's token — the exact bug.
		json.NewEncoder(w).Encode(tokenResponse{
			AccessToken:  "fresh-acc",
			RefreshToken: "rotated-ref",
			ExpiresIn:    3600,
		})
	}))
	defer srv.Close()

	now := time.Unix(2_000_000, 0)
	c := New(testConfig())
	c.HTTPClient = &http.Client{Transport: rewriteTransport{base: srv.URL}}
	c.now = func() time.Time { return now }

	// Expired token + a refresh token available: every caller sees "needs refresh".
	c.accessToken = "stale-acc"
	c.refreshToken = "ref-1"
	c.expiresAt = now.Add(-time.Minute)

	const callers = 20
	var wg sync.WaitGroup
	errs := make([]error, callers)
	toks := make([]string, callers)
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release all at once for a real stampede
			toks[i], errs[i] = c.ValidToken(context.Background())
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d ValidToken: %v", i, err)
		}
		if toks[i] != "fresh-acc" {
			t.Fatalf("caller %d got %q, want fresh-acc", i, toks[i])
		}
	}
	if g := atomic.LoadInt64(&grants); g != 1 {
		t.Fatalf("SINGLE-FLIGHT VIOLATED: %d concurrent callers triggered %d refresh grants, want exactly 1 (duplicate grants can clobber a rotated refresh token)", callers, g)
	}
	if m := atomic.LoadInt64(&maxConcurrent); m > 1 {
		t.Fatalf("SERIALIZATION VIOLATED: %d refresh grants ran concurrently, want max 1", m)
	}
	// The rotated refresh token from the single grant must be stored intact.
	if c.RefreshToken() != "rotated-ref" {
		t.Fatalf("refresh token = %q, want rotated-ref (the single grant's rotation)", c.RefreshToken())
	}
}

// TestPlay401ForcesRefreshOnLocallyLiveToken pins s2-store-001: when the server
// 401s a token that is still locally "live" (expiresAt far in the future,
// well outside the skew window), the 401 retry MUST force a new grant. The
// sweep-1 skew short-circuit at the top of refresh() would otherwise reuse the
// same dead token and the retry would 401 again — defeating the whole
// refresh-on-401 path. The forced entry bypasses the skew check.
func TestPlay401ForcesRefreshOnLocallyLiveToken(t *testing.T) {
	var playCalls, refreshCalls int32
	var retryAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/token":
			atomic.AddInt32(&refreshCalls, 1)
			json.NewEncoder(w).Encode(tokenResponse{AccessToken: "fresh-token", ExpiresIn: 3600})
		case "/v1/me/player/play":
			if atomic.AddInt32(&playCalls, 1) == 1 {
				w.WriteHeader(http.StatusUnauthorized) // server revoked it early
				return
			}
			retryAuth = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	now := time.Unix(3_000_000, 0)
	c := New(testConfig())
	c.HTTPClient = &http.Client{Transport: rewriteTransport{base: srv.URL}}
	c.now = func() time.Time { return now }
	c.accessToken = "revoked-but-live"
	c.refreshToken = "ref-1"
	c.expiresAt = now.Add(time.Hour) // locally live, well outside the skew window

	if err := c.Play(context.Background(), "spotify:track:z", 0); err != nil {
		t.Fatalf("Play: %v", err)
	}
	if got := atomic.LoadInt32(&refreshCalls); got != 1 {
		t.Fatalf("refresh grants = %d, want 1 (forced path must POST despite locally-live token)", got)
	}
	if got := atomic.LoadInt32(&playCalls); got != 2 {
		t.Fatalf("play attempts = %d, want 2 (initial 401 + retry)", got)
	}
	if retryAuth != "Bearer fresh-token" {
		t.Fatalf("retry authorization = %q, want Bearer fresh-token (dead token must not be reused)", retryAuth)
	}
}
