package spotify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSearch_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("q") != "never gonna" {
			t.Fatalf("unexpected query: %s", r.URL.Query().Get("q"))
		}
		if r.URL.Query().Get("type") != "track" {
			t.Fatalf("expected type=track, got %s", r.URL.Query().Get("type"))
		}
		if r.URL.Query().Get("limit") != "5" {
			t.Fatalf("expected limit=5, got %s", r.URL.Query().Get("limit"))
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("missing/wrong auth header")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"tracks": {
				"items": [
					{
						"uri": "spotify:track:abc123",
						"name": "Never Gonna Give You Up",
						"duration_ms": 213000,
						"artists": [{"name": "Rick Astley"}],
						"album": {"images": [{"url": "https://example.com/art.jpg"}]}
					}
				]
			}
		}`))
	}))
	defer ts.Close()

	c := &Client{
		HTTPClient:  ts.Client(),
		apiBase:     ts.URL,
		accessToken: "test-token",
	}

	results, err := c.Search(context.Background(), "never gonna", 5)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.URI != "spotify:track:abc123" {
		t.Errorf("URI = %q, want spotify:track:abc123", r.URI)
	}
	if r.Artist != "Rick Astley" {
		t.Errorf("Artist = %q, want Rick Astley", r.Artist)
	}
	if r.Song != "Never Gonna Give You Up" {
		t.Errorf("Song = %q, want Never Gonna Give You Up", r.Song)
	}
	if r.AlbumArt != "https://example.com/art.jpg" {
		t.Errorf("AlbumArt = %q, want https://example.com/art.jpg", r.AlbumArt)
	}
	if r.DurationMs != 213000 {
		t.Errorf("DurationMs = %d, want 213000", r.DurationMs)
	}
}

func TestSearch_RefreshOn401(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/token" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"access_token": "refreshed-token", "refresh_token": "new-rt"}`))
			return
		}
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "Bearer refreshed-token" {
			t.Fatalf("expected refreshed token, got %s", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tracks": {"items": []}}`))
	}))
	defer ts.Close()

	c := &Client{
		HTTPClient:   ts.Client(),
		apiBase:      ts.URL,
		tokenURL:     ts.URL + "/api/token",
		accessToken:  "expired-token",
		refreshToken: "original-rt",
		clientID:     "cid",
		clientSecret: "csec",
	}

	results, err := c.Search(context.Background(), "test", 10)
	if err != nil {
		t.Fatalf("Search after refresh failed: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
	if calls != 2 {
		t.Fatalf("expected 2 search calls (initial 401 + retry), got %d", calls)
	}
}

func TestSearch_LimitClamp(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := r.URL.Query().Get("limit")
		if limit != "50" {
			t.Errorf("expected clamped limit=50, got %s", limit)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tracks": {"items": []}}`))
	}))
	defer ts.Close()

	c := &Client{
		HTTPClient:  ts.Client(),
		apiBase:     ts.URL,
		accessToken: "token",
	}

	_, err := c.Search(context.Background(), "test", 100)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
}

func TestSearch_NoToken(t *testing.T) {
	c := &Client{
		HTTPClient:  http.DefaultClient,
		apiBase:     "http://unused",
		accessToken: "",
	}

	_, err := c.Search(context.Background(), "test", 10)
	if err == nil {
		t.Fatal("expected error when no token set")
	}
}
