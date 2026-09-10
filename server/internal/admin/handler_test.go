package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/s45vprubg/yfitops/server/internal/game"
)

// mockStore is a minimal AdminStore for handler tests.
type mockStore struct {
	boards   []Board
	tracks   []Track
	ddTracks []DailyDoubleTrack
	layout   *Layout
	created  []string
}

func (m *mockStore) CreateBoard(_ context.Context, id, name string) error {
	m.created = append(m.created, id)
	m.boards = append(m.boards, Board{ID: id, Name: name, Cols: 1})
	return nil
}
func (m *mockStore) ListBoards(_ context.Context) ([]Board, error)   { return m.boards, nil }
func (m *mockStore) GetBoard(_ context.Context, id string) (*Board, error) {
	for i := range m.boards {
		if m.boards[i].ID == id {
			return &m.boards[i], nil
		}
	}
	return nil, nil
}
func (m *mockStore) RenameBoard(_ context.Context, _, _ string) error  { return nil }
func (m *mockStore) DeleteBoard(_ context.Context, _ string) error     { return nil }
func (m *mockStore) UpdateBoardCols(_ context.Context, _ string, _ int) error { return nil }
func (m *mockStore) SetDailyDoubleCount(_ context.Context, _ string, _ int) error { return nil }
// AddDailyDoubleTrack mirrors the fixed Postgres behavior: a duplicate
// (boardID, spotifyUri) pair is a no-op that reports ErrDailyDoubleTrackExists
// instead of silently appending a second, unpersisted-looking row (dd-api-1).
func (m *mockStore) AddDailyDoubleTrack(_ context.Context, t *DailyDoubleTrack) error {
	for _, existing := range m.ddTracks {
		if existing.BoardID == t.BoardID && existing.SpotifyURI == t.SpotifyURI {
			return ErrDailyDoubleTrackExists
		}
	}
	m.ddTracks = append(m.ddTracks, *t)
	return nil
}
func (m *mockStore) ListDailyDoubleTracks(_ context.Context, boardID string) ([]DailyDoubleTrack, error) {
	var out []DailyDoubleTrack
	for _, t := range m.ddTracks {
		if t.BoardID == boardID {
			out = append(out, t)
		}
	}
	return out, nil
}

// RemoveDailyDoubleTrack mirrors the fixed Postgres behavior: the delete is
// scoped to boardID, returning ErrDailyDoubleTrackNotFound (not a silent
// success) if trackID doesn't exist or belongs to a different board (dd-api-2).
func (m *mockStore) RemoveDailyDoubleTrack(_ context.Context, boardID, trackID string) error {
	for i, t := range m.ddTracks {
		if t.ID == trackID {
			if t.BoardID != boardID {
				return ErrDailyDoubleTrackNotFound
			}
			m.ddTracks = append(m.ddTracks[:i], m.ddTracks[i+1:]...)
			return nil
		}
	}
	return ErrDailyDoubleTrackNotFound
}
func (m *mockStore) AddTrack(_ context.Context, t *Track) error {
	m.tracks = append(m.tracks, *t)
	return nil
}
func (m *mockStore) ListTracks(_ context.Context, _ string) ([]Track, error) { return m.tracks, nil }
func (m *mockStore) UnplacedTracks(_ context.Context, _ string) ([]Track, error) {
	return m.tracks, nil
}
func (m *mockStore) DeleteTrack(_ context.Context, _ string) error         { return nil }
func (m *mockStore) SetTrackLyrics(_ context.Context, _ string, _ *bool, _ *bool) error { return nil }
func (m *mockStore) AddColumn(_ context.Context, _ string, _ int, _ string) error { return nil }
func (m *mockStore) RemoveColumn(_ context.Context, _ string, _ int) error { return nil }
func (m *mockStore) RenameCategory(_ context.Context, _ string, _ int, _ string) error { return nil }
func (m *mockStore) PlaceTrack(_ context.Context, _ string, _, _ int, _ string, _ int) error {
	return nil
}
func (m *mockStore) UnplaceTrack(_ context.Context, _ string, _, _ int, _ string) error { return nil }
func (m *mockStore) GetLayout(_ context.Context, _ string) (*Layout, error) { return m.layout, nil }
func (m *mockStore) RebuildLayout(_ context.Context, _ string, _ int, _ []LayoutColumn) error {
	return nil
}
func (m *mockStore) LoadBoardByID(_ context.Context, _ string) (*game.Board, error) {
	return &game.Board{Rows: 5, Cols: 1}, nil
}
func (m *mockStore) AttachBoard(_ context.Context, _, _ string) error { return nil }

type mockEngine struct{ reloaded bool }

func (m *mockEngine) ReloadBoard(_ *game.Board) { m.reloaded = true }
func (m *mockEngine) StartGame() error           { return nil }
func (m *mockEngine) ResetToLobby() error        { return nil }

func newTestHandler() (*Handler, *http.ServeMux) {
	store := &mockStore{
		boards: []Board{{ID: "brd_test", Name: "Test Board", Cols: 3}},
	}
	h := NewHandler(store, nil, &mockEngine{}, "test-secret")
	mux := http.NewServeMux()
	h.Register(mux)
	return h, mux
}

func TestAuth_Rejects_BadToken(t *testing.T) {
	_, mux := newTestHandler()

	req := httptest.NewRequest("GET", "/api/boards", nil)
	req.Header.Set("Authorization", "Bearer wrong-secret")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestAuth_Rejects_Missing(t *testing.T) {
	_, mux := newTestHandler()

	req := httptest.NewRequest("GET", "/api/boards", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestListBoards(t *testing.T) {
	_, mux := newTestHandler()

	req := httptest.NewRequest("GET", "/api/boards", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Test Board") {
		t.Fatalf("expected board in response: %s", w.Body.String())
	}
}

func TestCreateBoard(t *testing.T) {
	_, mux := newTestHandler()

	body := strings.NewReader(`{"name":"My New Board"}`)
	req := httptest.NewRequest("POST", "/api/boards", body)
	req.Header.Set("Authorization", "Bearer test-secret")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "My New Board") {
		t.Fatalf("expected board name in response: %s", w.Body.String())
	}
}

func TestCreateBoard_EmptyName(t *testing.T) {
	_, mux := newTestHandler()

	body := strings.NewReader(`{"name":""}`)
	req := httptest.NewRequest("POST", "/api/boards", body)
	req.Header.Set("Authorization", "Bearer test-secret")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestAddTrack_InvalidURI(t *testing.T) {
	_, mux := newTestHandler()

	body := strings.NewReader(`{"spotifyUri":"not-valid","artist":"A","song":"S","durationMs":180000}`)
	req := httptest.NewRequest("POST", "/api/boards/brd_test/tracks", body)
	req.Header.Set("Authorization", "Bearer test-secret")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAddColumn_Max8(t *testing.T) {
	store := &mockStore{
		boards: []Board{{ID: "brd_full", Name: "Full", Cols: 8}},
	}
	h := NewHandler(store, nil, &mockEngine{}, "test-secret")
	mux := http.NewServeMux()
	h.Register(mux)

	body := strings.NewReader(`{"category":"Too Many"}`)
	req := httptest.NewRequest("POST", "/api/boards/brd_full/columns", body)
	req.Header.Set("Authorization", "Bearer test-secret")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 (max columns), got %d: %s", w.Code, w.Body.String())
	}
}

func TestCORS_Headers(t *testing.T) {
	_, mux := newTestHandler()
	handler := CORSHandler(mux)

	req := httptest.NewRequest("OPTIONS", "/api/boards", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for OPTIONS preflight, got %d", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal("missing CORS Allow-Origin header")
	}
	if w.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Fatal("missing CORS Allow-Methods header")
	}
}

// mockSpotify implements SpotifySearcher for token-endpoint tests.
type mockSpotify struct {
	token    string
	tokenErr error
}

func (m *mockSpotify) Search(_ context.Context, _ string, _ int) ([]SpotifyResult, error) {
	return nil, nil
}
func (m *mockSpotify) GetPlaylistTracks(_ context.Context, _ string) ([]SpotifyResult, error) {
	return nil, nil
}
func (m *mockSpotify) ValidToken(_ context.Context) (string, error) {
	return m.token, m.tokenErr
}

func TestSpotifyToken_Serves(t *testing.T) {
	mux := http.NewServeMux()
	RegisterSpotifyToken(mux, &mockSpotify{token: "live-token"}, "test-secret")

	req := httptest.NewRequest("GET", "/api/spotify/token", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
	}
	got := decodeTokenResp(t, w.Body.String())
	if !got.Connected {
		t.Error("connected must be true when a live token is served")
	}
	if got.Token == nil || *got.Token != "live-token" {
		t.Errorf("body missing token: %s", w.Body.String())
	}
}

func TestSpotifyToken_RequiresAuth(t *testing.T) {
	mux := http.NewServeMux()
	RegisterSpotifyToken(mux, &mockSpotify{token: "live-token"}, "test-secret")

	// No Bearer -> must be rejected; the token must never leak unauthenticated.
	req := httptest.NewRequest("GET", "/api/spotify/token", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "live-token") {
		t.Error("token leaked to unauthenticated request")
	}
}

// tokenResp mirrors the GET /api/spotify/token response contract. Token is a
// pointer so an explicit JSON null is distinguishable from an empty string.
type tokenResp struct {
	Token     *string `json:"token"`
	Connected bool    `json:"connected"`
}

func decodeTokenResp(t *testing.T, body string) tokenResp {
	t.Helper()
	var got tokenResp
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("response is not valid JSON (%v): %s", err, body)
	}
	return got
}

// CONTRACT: GET /api/spotify/token always answers 200 and reports readiness in
// the body ({token, connected}) rather than via a status code. It previously
// returned 503 when Spotify was unconfigured and 409 when OAuth had not been
// completed. Both collapse into connected:false.
//
// Why: the admin UI and stage both poll this endpoint for connection state, and
// a non-2xx status is indistinguishable from a network/auth failure at the
// fetch layer — so "Spotify isn't set up" looked identical to "the request
// died". An explicit flag separates them. See docs/CHANGELOG.md.
func TestSpotifyToken_NotConfigured(t *testing.T) {
	mux := http.NewServeMux()
	RegisterSpotifyToken(mux, nil, "test-secret") // nil spotify

	req := httptest.NewRequest("GET", "/api/spotify/token", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
	}
	got := decodeTokenResp(t, w.Body.String())
	if got.Connected {
		t.Error("connected must be false when Spotify is not configured")
	}
	if got.Token != nil {
		t.Errorf("token must be null when Spotify is not configured, got %q", *got.Token)
	}
}

// The path that used to answer 409: Spotify is wired up, but ValidToken fails
// (OAuth never completed, or the refresh was rejected). Same shape as the
// unconfigured case — and the upstream error text must not reach the client.
func TestSpotifyToken_RefreshFailed(t *testing.T) {
	mux := http.NewServeMux()
	RegisterSpotifyToken(mux, &mockSpotify{tokenErr: context.DeadlineExceeded}, "test-secret")

	req := httptest.NewRequest("GET", "/api/spotify/token", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
	}
	got := decodeTokenResp(t, w.Body.String())
	if got.Connected {
		t.Error("connected must be false when ValidToken fails")
	}
	if got.Token != nil {
		t.Errorf("token must be null when ValidToken fails, got %q", *got.Token)
	}
	if strings.Contains(w.Body.String(), context.DeadlineExceeded.Error()) {
		t.Error("upstream error text leaked to the client")
	}
}

// TestQARegression_AddDailyDoubleTrack_DuplicateSpotifyURI_Returns409 covers
// dd-api-1: AddDailyDoubleTrack's INSERT is ON CONFLICT (board_id,
// spotify_uri) DO NOTHING, so a duplicate add previously still returned 201
// with a fabricated track object (a fresh generated id, the caller's new
// artist/song) that was never actually written to Postgres. Against the
// pre-fix code, mockStore.AddDailyDoubleTrack unconditionally appended and
// returned nil, and the handler unconditionally wrote 201 regardless of
// outcome — so this second POST would have come back 201 (with a bogus new
// id) instead of 409, and this test would have failed.
func TestQARegression_AddDailyDoubleTrack_DuplicateSpotifyURI_Returns409(t *testing.T) {
	_, mux := newTestHandler()

	first := strings.NewReader(`{"spotifyUri":"spotify:track:qatest123","artist":"QA Artist","song":"QA Song","durationMs":180000}`)
	req := httptest.NewRequest("POST", "/api/boards/brd_test/daily-double-tracks", first)
	req.Header.Set("Authorization", "Bearer test-secret")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("first add: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// Same spotifyUri, different artist/song — must NOT be treated as a new track.
	second := strings.NewReader(`{"spotifyUri":"spotify:track:qatest123","artist":"DIFFERENT ARTIST","song":"DIFFERENT SONG","durationMs":180000}`)
	req2 := httptest.NewRequest("POST", "/api/boards/brd_test/daily-double-tracks", second)
	req2.Header.Set("Authorization", "Bearer test-secret")
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)

	if w2.Code != http.StatusConflict {
		t.Fatalf("duplicate add: expected 409, got %d: %s", w2.Code, w2.Body.String())
	}
	if strings.Contains(w2.Body.String(), "DIFFERENT ARTIST") {
		t.Error("409 response must not echo back the unsaved payload's metadata")
	}
}

// TestQARegression_DeleteDailyDoubleTrack_WrongBoard_Returns404 covers
// dd-api-2: the DELETE route is /api/boards/{id}/daily-double-tracks/{trackId}
// but the pre-fix handler never read the {id} board segment, and
// RemoveDailyDoubleTrack deleted by track id alone — so a track belonging to
// board A could be deleted through board B's URL. Against the pre-fix
// handler/mock (deleteDailyDoubleTrack ignoring boardID, and a 1-arg
// RemoveDailyDoubleTrack that succeeds unconditionally), this DELETE against
// the wrong board would have returned 204 and actually removed the track —
// this test would have failed on both assertions below.
func TestQARegression_DeleteDailyDoubleTrack_WrongBoard_Returns404(t *testing.T) {
	store := &mockStore{
		boards: []Board{
			{ID: "brd_a", Name: "Board A", Cols: 1},
			{ID: "brd_b", Name: "Board B", Cols: 1},
		},
		ddTracks: []DailyDoubleTrack{
			{ID: "ddt_owned_by_a", BoardID: "brd_a", SpotifyURI: "spotify:track:abc", Artist: "A", Song: "S"},
		},
	}
	h := NewHandler(store, nil, &mockEngine{}, "test-secret")
	mux := http.NewServeMux()
	h.Register(mux)

	// Address the DELETE at brd_b, even though the track belongs to brd_a.
	req := httptest.NewRequest("DELETE", "/api/boards/brd_b/daily-double-tracks/ddt_owned_by_a", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 (wrong board), got %d: %s", w.Code, w.Body.String())
	}

	found := false
	for _, tr := range store.ddTracks {
		if tr.ID == "ddt_owned_by_a" {
			found = true
		}
	}
	if !found {
		t.Error("track owned by brd_a must survive a delete addressed to brd_b")
	}
}

// TestQARegression_RenameBoard_DailyDoubleCount_RejectsOversized covers
// dd-api-3: renameBoard's dailyDoubleCount validation only rejected negative
// values, so an admin could PATCH an arbitrarily large count (e.g. 999999)
// and have it persist even though the board can never have that many Daily
// Double cells. Against the pre-fix code (only `< 0` checked, no GetBoard/
// upper-bound check), this PATCH would have returned 204 instead of 400, and
// this test would have failed.
func TestQARegression_RenameBoard_DailyDoubleCount_RejectsOversized(t *testing.T) {
	store := &mockStore{
		boards: []Board{{ID: "brd_small", Name: "Small", Cols: 3}}, // max = 5*3 = 15
	}
	h := NewHandler(store, nil, &mockEngine{}, "test-secret")
	mux := http.NewServeMux()
	h.Register(mux)

	body := strings.NewReader(`{"dailyDoubleCount":999999}`)
	req := httptest.NewRequest("PATCH", "/api/boards/brd_small", body)
	req.Header.Set("Authorization", "Bearer test-secret")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for oversized dailyDoubleCount, got %d: %s", w.Code, w.Body.String())
	}

	// A value within bounds must still be accepted.
	body2 := strings.NewReader(`{"dailyDoubleCount":10}`)
	req2 := httptest.NewRequest("PATCH", "/api/boards/brd_small", body2)
	req2.Header.Set("Authorization", "Bearer test-secret")
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)

	if w2.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for in-bounds dailyDoubleCount, got %d: %s", w2.Code, w2.Body.String())
	}
}
