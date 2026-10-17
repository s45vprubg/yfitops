package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/s45vprubg/yfitops/server/internal/game"
)

// solokaraoke_test.go covers the Solo Karaoke Library/Playlist REST routes,
// mirroring handler_test.go's Daily Double regression tests' style
// (mockStore + httptest, status-code-driven assertions).

// mockSoloKaraokeReloader records every ReloadSoloKaraokeQueue call so tests
// can assert the engine is kept in sync after playlist mutations.
type mockSoloKaraokeReloader struct {
	lastTracks []*game.Track
	calls      int
}

func (m *mockSoloKaraokeReloader) ReloadSoloKaraokeQueue(tracks []*game.Track) {
	m.calls++
	m.lastTracks = tracks
}

func newSoloKaraokeTestHandler(store *mockStore, reloader *mockSoloKaraokeReloader) (*Handler, *http.ServeMux) {
	h := NewHandler(store, nil, &mockEngine{}, "test-secret")
	if reloader != nil {
		h.SetSoloKaraokeReloader(reloader)
	}
	mux := http.NewServeMux()
	h.Register(mux)
	return h, mux
}

func authedRequest(method, target string, body string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Authorization", "Bearer test-secret")
	return r
}

// TestSoloKaraokeLibrary_Add_SoftLyricsGate_NeverBlocks: unlike Daily
// Double's hard 412 reject, adding a track with no resolved lyrics must
// still succeed (201) with lyricsStatus reported as unknown/empty.
func TestSoloKaraokeLibrary_Add_SoftLyricsGate_NeverBlocks(t *testing.T) {
	store := &mockStore{}
	_, mux := newSoloKaraokeTestHandler(store, nil)

	req := authedRequest("POST", "/api/solo-karaoke/library",
		`{"spotifyUri":"spotify:track:sk1","artist":"A","song":"S","durationMs":180000}`)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 even with no resolved lyrics (soft gate), got %d: %s", w.Code, w.Body.String())
	}
	if len(store.skLibrary) != 1 {
		t.Fatalf("expected 1 library track stored, got %d", len(store.skLibrary))
	}
}

// TestSoloKaraokeLibrary_Add_DuplicateSpotifyURI_Returns409 mirrors
// TestQARegression_AddDailyDoubleTrack_DuplicateSpotifyURI_Returns409: a
// second add with the same spotifyUri must not be treated as a new track,
// and must not echo the unsaved second payload's metadata.
func TestSoloKaraokeLibrary_Add_DuplicateSpotifyURI_Returns409(t *testing.T) {
	store := &mockStore{}
	_, mux := newSoloKaraokeTestHandler(store, nil)

	first := authedRequest("POST", "/api/solo-karaoke/library",
		`{"spotifyUri":"spotify:track:dup1","artist":"A","song":"S","durationMs":180000}`)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, first)
	if w.Code != http.StatusCreated {
		t.Fatalf("first add: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	second := authedRequest("POST", "/api/solo-karaoke/library",
		`{"spotifyUri":"spotify:track:dup1","artist":"DIFFERENT","song":"DIFFERENT","durationMs":180000}`)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, second)
	if w2.Code != http.StatusConflict {
		t.Fatalf("duplicate add: expected 409, got %d: %s", w2.Code, w2.Body.String())
	}
	if strings.Contains(w2.Body.String(), "DIFFERENT") {
		t.Error("409 response must not echo back the unsaved payload's metadata")
	}
}

// TestSoloKaraokeLibrary_Delete_NotFound_Returns404.
func TestSoloKaraokeLibrary_Delete_NotFound_Returns404(t *testing.T) {
	store := &mockStore{}
	_, mux := newSoloKaraokeTestHandler(store, nil)

	req := authedRequest("DELETE", "/api/solo-karaoke/library/does-not-exist", "")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestSoloKaraokePlaylist_Add_AssignsIncrementingPosition_AndReloadsEngine:
// each add to the Playlist appends at the end (position = len), and the
// wired SoloKaraokeReloader is pushed the full ordered queue afterward.
func TestSoloKaraokePlaylist_Add_AssignsIncrementingPosition_AndReloadsEngine(t *testing.T) {
	store := &mockStore{}
	reloader := &mockSoloKaraokeReloader{}
	_, mux := newSoloKaraokeTestHandler(store, reloader)

	for i, uri := range []string{"spotify:track:p1", "spotify:track:p2"} {
		req := authedRequest("POST", "/api/solo-karaoke/playlist",
			`{"spotifyUri":"`+uri+`","artist":"A","song":"S","durationMs":180000}`)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("add %d: expected 201, got %d: %s", i, w.Code, w.Body.String())
		}
	}

	if len(store.skPlaylist) != 2 {
		t.Fatalf("expected 2 playlist tracks, got %d", len(store.skPlaylist))
	}
	if store.skPlaylist[0].Position != 0 || store.skPlaylist[1].Position != 1 {
		t.Fatalf("expected incrementing positions 0,1 — got %d,%d", store.skPlaylist[0].Position, store.skPlaylist[1].Position)
	}

	if reloader.calls != 2 {
		t.Fatalf("expected the engine reloader to be called once per playlist mutation, got %d calls", reloader.calls)
	}
	if len(reloader.lastTracks) != 2 {
		t.Fatalf("expected the last reload to carry both tracks, got %d", len(reloader.lastTracks))
	}
}

// TestSoloKaraokeLibrary_Add_DoesNotReloadEngine: a Library mutation (unlike
// a Playlist mutation) must NOT push a queue reload — the Library isn't the
// live queue the engine plays from.
func TestSoloKaraokeLibrary_Add_DoesNotReloadEngine(t *testing.T) {
	store := &mockStore{}
	reloader := &mockSoloKaraokeReloader{}
	_, mux := newSoloKaraokeTestHandler(store, reloader)

	req := authedRequest("POST", "/api/solo-karaoke/library",
		`{"spotifyUri":"spotify:track:lib1","artist":"A","song":"S","durationMs":180000}`)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if reloader.calls != 0 {
		t.Fatalf("expected a library-only mutation to NOT reload the engine queue, got %d calls", reloader.calls)
	}
}

// TestSoloKaraokePlaylist_Reorder_RewritesPositionsAndReloadsEngine.
func TestSoloKaraokePlaylist_Reorder_RewritesPositionsAndReloadsEngine(t *testing.T) {
	store := &mockStore{
		skPlaylist: []SoloKaraokeTrack{
			{ID: "p1", SpotifyURI: "spotify:track:p1", Artist: "A", Song: "S1", Position: 0},
			{ID: "p2", SpotifyURI: "spotify:track:p2", Artist: "A", Song: "S2", Position: 1},
		},
	}
	reloader := &mockSoloKaraokeReloader{}
	_, mux := newSoloKaraokeTestHandler(store, reloader)

	req := authedRequest("PUT", "/api/solo-karaoke/playlist/reorder", `{"orderedIds":["p2","p1"]}`)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	byID := map[string]int{}
	for _, t := range store.skPlaylist {
		byID[t.ID] = t.Position
	}
	if byID["p2"] != 0 || byID["p1"] != 1 {
		t.Fatalf("expected p2=0, p1=1 after reorder — got p1=%d p2=%d", byID["p1"], byID["p2"])
	}
	if reloader.calls != 1 {
		t.Fatalf("expected reorder to reload the engine queue, got %d calls", reloader.calls)
	}
}

// TestSoloKaraokeTrack_RequeryLyrics_ResolvesAcrossBothBuckets: GetSoloKaraokeTrack
// must find a track whether it lives in the Library or the Playlist.
func TestSoloKaraokeTrack_RequeryLyrics_ResolvesAcrossBothBuckets(t *testing.T) {
	store := &mockStore{
		skLibrary:  []SoloKaraokeTrack{{ID: "lib1", SpotifyURI: "spotify:track:lib1", Artist: "A", Song: "S1"}},
		skPlaylist: []SoloKaraokeTrack{{ID: "pl1", SpotifyURI: "spotify:track:pl1", Artist: "B", Song: "S2"}},
	}
	h, mux := newSoloKaraokeTestHandler(store, nil)
	h.SetLyricsProber(stubLyricsProber{found: true, lrc: "[00:00.00]la la la"})

	for _, trackID := range []string{"lib1", "pl1"} {
		req := authedRequest("POST", "/api/solo-karaoke/tracks/"+trackID+"/requery-lyrics", "")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("requery %s: expected 200, got %d: %s", trackID, w.Code, w.Body.String())
		}
	}

	req := authedRequest("POST", "/api/solo-karaoke/tracks/does-not-exist/requery-lyrics", "")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("requery unknown id: expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// stubLyricsProber is a minimal LyricsProber for the requery test above.
type stubLyricsProber struct {
	found bool
	lrc   string
}

func (s stubLyricsProber) Probe(_ context.Context, _, _ string, _ int) (string, bool, error) {
	return s.lrc, s.found, nil
}
