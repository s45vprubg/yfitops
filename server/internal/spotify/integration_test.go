//go:build spotify_integration

// Live Spotify integration checks — a diagnostic harness for when Spotify
// starts returning surprises (403s, deprecated endpoints, scope changes, etc.).
// It hits the REAL Spotify API, so it is behind the `spotify_integration` build
// tag and NEVER runs in normal `go test ./...` or preflight (which have no
// credentials). Its whole job is to tell you, fast, WHICH capability broke and
// with what status — the thing that took a long manual bisection the first time
// (Feb-2026 the /playlists/{id}/tracks endpoint was deprecated and started
// 403ing for Development-Mode apps; the fix was to move to /playlists/{id}/items).
//
// Run it:
//
//	# creds from deploy/.env; refresh token auto-loaded from certs/ (see below)
//	set -a; . ../../../deploy/.env; set +a
//	go test -tags spotify_integration -v ./internal/spotify/ -run Integration
//
// Credentials (all via env; the test SKIPS rather than fails if creds absent):
//   - SPOTIFY_CLIENT_ID / SPOTIFY_CLIENT_SECRET   (required; from deploy/.env)
//   - a user refresh token, resolved in this order:
//       1. SPOTIFY_REFRESH_TOKEN env var
//       2. SPOTIFY_TOKEN_FILE env var (path to a file holding just the token)
//       3. ../../../certs/spotify_refresh_token  (what the dev server persists)
//     Without a refresh token, only the app-only (client-credentials) checks
//     run — enough to detect an endpoint deprecation, but not user-scoped paths.
//   - SPOTIFY_TEST_PLAYLIST  (optional; a playlist URL/URI/ID to import-test.
//     Defaults to Spotify's "Today's Top Hits" editorial playlist. NOTE:
//     editorial playlists are themselves restricted for Development-Mode apps,
//     so for a meaningful playlist check point this at one you OWN.)
//   - SPOTIFY_TEST_QUERY     (optional; search term. Default "radiohead creep".)
//
// Design: each capability is its own subtest so a failure pinpoints the broken
// call. We surface the HTTP status and body on failure — a bare "403" is what
// made the original bug so slow to place.

package spotify

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/s45vprubg/yfitops/server/internal/config"
)

const defaultTestPlaylist = "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M" // Today's Top Hits

// testClient builds a real Client from env creds. It skips the whole suite if
// client credentials are missing, and seeds a refresh token if one is findable.
// The bool return reports whether a user refresh token was loaded (user-scoped
// subtests skip when false).
func testClient(t *testing.T) (*Client, bool) {
	t.Helper()

	id := os.Getenv("SPOTIFY_CLIENT_ID")
	secret := os.Getenv("SPOTIFY_CLIENT_SECRET")
	if id == "" || secret == "" {
		t.Skip("SPOTIFY_CLIENT_ID/SPOTIFY_CLIENT_SECRET not set — skipping live Spotify integration checks")
	}

	c := New(&config.Config{
		SpotifyClientID:     id,
		SpotifyClientSecret: secret,
		SpotifyRedirectURI:  envOr("SPOTIFY_REDIRECT_URI", "http://127.0.0.1:8777/auth/spotify/callback"),
	})

	rt := loadRefreshToken(t)
	if rt != "" {
		c.RestoreRefreshToken(rt)
	}
	return c, rt != ""
}

// loadRefreshToken resolves a user refresh token from env or the persisted file
// the dev server writes. Returns "" if none is available.
func loadRefreshToken(t *testing.T) string {
	t.Helper()
	if rt := strings.TrimSpace(os.Getenv("SPOTIFY_REFRESH_TOKEN")); rt != "" {
		return rt
	}
	candidates := []string{
		os.Getenv("SPOTIFY_TOKEN_FILE"),
		"../../../certs/spotify_refresh_token",
	}
	for _, p := range candidates {
		if p == "" {
			continue
		}
		if b, err := os.ReadFile(p); err == nil {
			if rt := strings.TrimSpace(string(b)); rt != "" {
				t.Logf("loaded refresh token from %s", p)
				return rt
			}
		}
	}
	return ""
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return c
}

// TestIntegration_TokenRefresh proves the refresh-token grant still works and
// mints a live access token. This is the foundation — everything user-scoped
// depends on it, so if it fails the rest is expected to skip.
func TestIntegration_TokenRefresh(t *testing.T) {
	c, hasUser := testClient(t)
	if !hasUser {
		t.Skip("no refresh token available — skipping user token refresh (re-auth via the dev server to persist one)")
	}
	tok, err := c.ValidToken(ctx(t))
	if err != nil {
		t.Fatalf("ValidToken (refresh grant) failed: %v", err)
	}
	if tok == "" {
		t.Fatal("ValidToken returned empty token")
	}
	t.Logf("OK: refresh grant produced a live access token (len %d)", len(tok))
}

// TestIntegration_Search checks catalog search — the path the Board Builder's
// add-track flow uses, and a canary that stays green even when playlist reads
// are restricted (which is exactly how we first isolated the Feb-2026 change).
func TestIntegration_Search(t *testing.T) {
	c, hasUser := testClient(t)
	if !hasUser {
		t.Skip("no refresh token available — search needs a user token in this app's setup")
	}
	if _, err := c.ValidToken(ctx(t)); err != nil {
		t.Fatalf("could not mint token before search: %v", err)
	}
	q := envOr("SPOTIFY_TEST_QUERY", "radiohead creep")
	results, err := c.Search(ctx(t), q, 5)
	if err != nil {
		t.Fatalf("Search(%q) failed: %v", q, err)
	}
	if len(results) == 0 {
		t.Fatalf("Search(%q) returned no results", q)
	}
	for i, r := range results {
		if r.URI == "" || r.Song == "" {
			t.Errorf("result %d missing URI/Song: %+v", i, r)
		}
	}
	t.Logf("OK: search %q -> %d results (first: %q by %q)", q, len(results), results[0].Song, results[0].Artist)
}

// TestIntegration_PlaylistImport is the regression guard for the Feb-2026
// endpoint migration: GetPlaylistTracks must return real tracks (it now calls
// /playlists/{id}/items with a market, not the deprecated /tracks). A 403 here
// with search passing is the signature of another endpoint deprecation.
//
// Point SPOTIFY_TEST_PLAYLIST at a playlist you OWN for a meaningful result —
// editorial playlists are separately restricted for Development-Mode apps.
func TestIntegration_PlaylistImport(t *testing.T) {
	c, hasUser := testClient(t)
	if !hasUser {
		t.Skip("no refresh token available — playlist import needs a user token")
	}
	if _, err := c.ValidToken(ctx(t)); err != nil {
		t.Fatalf("could not mint token before playlist fetch: %v", err)
	}
	raw := envOr("SPOTIFY_TEST_PLAYLIST", defaultTestPlaylist)
	id := parsePlaylistID(raw)
	if id == "" {
		t.Fatalf("could not extract a playlist id from %q", raw)
	}

	tracks, err := c.GetPlaylistTracks(ctx(t), id)
	if err != nil {
		if strings.Contains(err.Error(), "status 403") {
			t.Fatalf("playlist fetch 403 while search works — likely another endpoint deprecation/restriction. "+
				"Check the current Spotify Web API docs for /playlists/%s/items. Raw err: %v", id, err)
		}
		t.Fatalf("GetPlaylistTracks(%s) failed: %v", id, err)
	}
	if len(tracks) == 0 {
		t.Fatalf("GetPlaylistTracks(%s) returned 0 tracks (playlist empty, or a fields/shape change silently dropped them)", id)
	}
	for i, tr := range tracks {
		if tr.URI == "" || tr.Song == "" {
			t.Errorf("track %d missing URI/Song: %+v", i, tr)
		}
	}
	t.Logf("OK: imported %d tracks from %s (first: %q by %q)", len(tracks), id, tracks[0].Song, tracks[0].Artist)
}

// parsePlaylistID mirrors admin.extractPlaylistID for the three accepted forms,
// kept local so this package has no dependency on the admin package.
func parsePlaylistID(input string) string {
	input = strings.TrimSpace(input)
	if strings.HasPrefix(input, "spotify:playlist:") {
		return strings.TrimPrefix(input, "spotify:playlist:")
	}
	if i := strings.Index(input, "open.spotify.com/playlist/"); i >= 0 {
		id := input[i+len("open.spotify.com/playlist/"):]
		if j := strings.IndexAny(id, "?/"); j >= 0 {
			id = id[:j]
		}
		return id
	}
	return input // assume a bare id
}
