package spotify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/s45vprubg/yfitops/server/internal/config"
)

// These tests make NO real network calls — every Spotify host is replaced by an
// httptest.Server via the injected HTTPClient. They verify request shaping and
// the refresh/retry control flow, not live playback (see the package doc's
// runtime-limitation note: live behavior needs a real Premium account + creds).

func testConfig() *config.Config {
	return &config.Config{
		SpotifyClientID:     "test-client-id",
		SpotifyClientSecret: "test-client-secret",
		SpotifyRedirectURI:  "http://localhost:8080/auth/spotify/callback",
	}
}

// rewriteTransport routes every outbound request to the test server,
// preserving the original path+query so handlers can assert on them. This lets
// the client keep its real https://api.spotify.com URLs while tests intercept.
type rewriteTransport struct {
	base string // httptest server base, e.g. http://127.0.0.1:NNN
}

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	u, _ := url.Parse(rt.base)
	req.URL.Scheme = u.Scheme
	req.URL.Host = u.Host
	return http.DefaultTransport.RoundTrip(req)
}

func TestAuthURL(t *testing.T) {
	c := New(testConfig())
	raw := c.AuthURL("xyz-state")

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("AuthURL not parseable: %v", err)
	}
	if got, want := u.Scheme+"://"+u.Host+u.Path, authorizeURL; got != want {
		t.Errorf("authorize base = %q, want %q", got, want)
	}
	q := u.Query()
	if q.Get("client_id") != "test-client-id" {
		t.Errorf("client_id = %q", q.Get("client_id"))
	}
	if q.Get("response_type") != "code" {
		t.Errorf("response_type = %q", q.Get("response_type"))
	}
	if q.Get("redirect_uri") != "http://localhost:8080/auth/spotify/callback" {
		t.Errorf("redirect_uri = %q", q.Get("redirect_uri"))
	}
	if q.Get("state") != "xyz-state" {
		t.Errorf("state = %q", q.Get("state"))
	}
	// All three legacy scopes present (server.js:1207).
	gotScopes := q.Get("scope")
	for _, s := range []string{"user-read-playback-state", "user-modify-playback-state", "user-read-currently-playing"} {
		if !strings.Contains(gotScopes, s) {
			t.Errorf("scope %q missing from %q", s, gotScopes)
		}
	}
}

func TestExchange(t *testing.T) {
	var gotForm url.Values
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/token" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotForm, _ = url.ParseQuery(string(body))
		json.NewEncoder(w).Encode(tokenResponse{AccessToken: "acc-1", RefreshToken: "ref-1"})
	}))
	defer srv.Close()

	c := New(testConfig())
	c.HTTPClient = &http.Client{Transport: rewriteTransport{base: srv.URL}}

	if err := c.Exchange(context.Background(), "the-code"); err != nil {
		t.Fatalf("Exchange: %v", err)
	}

	if gotForm.Get("grant_type") != "authorization_code" {
		t.Errorf("grant_type = %q", gotForm.Get("grant_type"))
	}
	if gotForm.Get("code") != "the-code" {
		t.Errorf("code = %q", gotForm.Get("code"))
	}
	// Basic auth must carry the client credentials.
	if !strings.HasPrefix(gotAuth, "Basic ") {
		t.Errorf("token auth not Basic: %q", gotAuth)
	}
	if c.accessToken != "acc-1" || c.refreshToken != "ref-1" {
		t.Errorf("tokens not stored: acc=%q ref=%q", c.accessToken, c.refreshToken)
	}
}

func TestPlayRoutesToDeviceWithBearer(t *testing.T) {
	var gotPath, gotDevice, gotAuth, gotMethod string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotDevice = r.URL.Query().Get("device_id")
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := New(testConfig())
	c.HTTPClient = &http.Client{Transport: rewriteTransport{base: srv.URL}}
	c.accessToken = "live-token"
	c.SetDevice("stage-virtual-device")

	if err := c.Play(context.Background(), "spotify:track:abc", 12345); err != nil {
		t.Fatalf("Play: %v", err)
	}

	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotPath != "/v1/me/player/play" {
		t.Errorf("path = %q, want /v1/me/player/play", gotPath)
	}
	if gotDevice != "stage-virtual-device" {
		t.Errorf("device_id = %q", gotDevice)
	}
	if gotAuth != "Bearer live-token" {
		t.Errorf("authorization = %q", gotAuth)
	}
	uris, _ := gotBody["uris"].([]any)
	if len(uris) != 1 || uris[0] != "spotify:track:abc" {
		t.Errorf("uris = %v", gotBody["uris"])
	}
	if gotBody["position_ms"] != float64(12345) {
		t.Errorf("position_ms = %v", gotBody["position_ms"])
	}
}

func TestPauseRoutesToDevice(t *testing.T) {
	var gotPath, gotDevice string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotDevice = r.URL.Query().Get("device_id")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := New(testConfig())
	c.HTTPClient = &http.Client{Transport: rewriteTransport{base: srv.URL}}
	c.accessToken = "live-token"
	c.SetDevice("dev-9")

	if err := c.Pause(context.Background()); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if gotPath != "/v1/me/player/pause" {
		t.Errorf("path = %q", gotPath)
	}
	if gotDevice != "dev-9" {
		t.Errorf("device_id = %q", gotDevice)
	}
}

// TestPlayRefreshesOn401 is the key control-flow test: a 401 from the player
// endpoint must trigger one token refresh then one retry, mirroring the legacy
// refreshSpotifyToken/retry logic (server.js:1300-1311).
func TestPlayRefreshesOn401(t *testing.T) {
	var playCalls, refreshCalls int32
	var refreshGrant string
	var secondToken string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/token":
			atomic.AddInt32(&refreshCalls, 1)
			b, _ := io.ReadAll(r.Body)
			form, _ := url.ParseQuery(string(b))
			refreshGrant = form.Get("grant_type")
			json.NewEncoder(w).Encode(tokenResponse{AccessToken: "fresh-token"})
		case "/v1/me/player/play":
			n := atomic.AddInt32(&playCalls, 1)
			if n == 1 {
				// First attempt: stale token -> 401.
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			// Retry should carry the refreshed token.
			secondToken = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := New(testConfig())
	c.HTTPClient = &http.Client{Transport: rewriteTransport{base: srv.URL}}
	c.accessToken = "stale-token"
	c.refreshToken = "ref-token"
	c.SetDevice("dev-1")

	if err := c.Play(context.Background(), "spotify:track:z", 0); err != nil {
		t.Fatalf("Play: %v", err)
	}

	if got := atomic.LoadInt32(&playCalls); got != 2 {
		t.Errorf("play attempts = %d, want 2 (initial + retry)", got)
	}
	if got := atomic.LoadInt32(&refreshCalls); got != 1 {
		t.Errorf("refresh calls = %d, want 1", got)
	}
	if refreshGrant != "refresh_token" {
		t.Errorf("refresh grant_type = %q", refreshGrant)
	}
	if secondToken != "Bearer fresh-token" {
		t.Errorf("retry authorization = %q, want Bearer fresh-token", secondToken)
	}
	if c.accessToken != "fresh-token" {
		t.Errorf("stored access token = %q, want fresh-token", c.accessToken)
	}
}

// TestPlayDoesNotRetryTwice: a persistent 401 should refresh+retry exactly once
// and then surface the error (no infinite loop).
func TestPlayDoesNotRetryTwice(t *testing.T) {
	var playCalls, refreshCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/token":
			atomic.AddInt32(&refreshCalls, 1)
			json.NewEncoder(w).Encode(tokenResponse{AccessToken: "fresh"})
		case "/v1/me/player/play":
			atomic.AddInt32(&playCalls, 1)
			w.WriteHeader(http.StatusUnauthorized) // always 401
		}
	}))
	defer srv.Close()

	c := New(testConfig())
	c.HTTPClient = &http.Client{Transport: rewriteTransport{base: srv.URL}}
	c.accessToken = "stale"
	c.refreshToken = "ref"

	if err := c.Play(context.Background(), "spotify:track:z", 0); err == nil {
		t.Fatal("expected error on persistent 401, got nil")
	}
	if got := atomic.LoadInt32(&playCalls); got != 2 {
		t.Errorf("play attempts = %d, want 2", got)
	}
	if got := atomic.LoadInt32(&refreshCalls); got != 1 {
		t.Errorf("refresh calls = %d, want 1", got)
	}
}

func TestPlayWithoutTokenFails(t *testing.T) {
	c := New(testConfig())
	if err := c.Play(context.Background(), "spotify:track:z", 0); err == nil {
		t.Fatal("expected error without access token, got nil")
	}
}

func TestRefreshWithoutTokenFails(t *testing.T) {
	c := New(testConfig())
	c.accessToken = "x" // present but expired path: 401 with no refresh token
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	c.HTTPClient = &http.Client{Transport: rewriteTransport{base: srv.URL}}

	if err := c.Resume(context.Background()); err == nil {
		t.Fatal("expected error refreshing without refresh token, got nil")
	}
}
