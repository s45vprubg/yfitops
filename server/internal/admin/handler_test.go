package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/s45vprubg/yfitops/server/internal/game"
)

// mockStore is a minimal AdminStore for handler tests.
type mockStore struct {
	boards  []Board
	tracks  []Track
	layout  *Layout
	created []string
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
func (m *mockStore) AddTrack(_ context.Context, t *Track) error {
	m.tracks = append(m.tracks, *t)
	return nil
}
func (m *mockStore) ListTracks(_ context.Context, _ string) ([]Track, error) { return m.tracks, nil }
func (m *mockStore) UnplacedTracks(_ context.Context, _ string) ([]Track, error) {
	return m.tracks, nil
}
func (m *mockStore) DeleteTrack(_ context.Context, _ string) error         { return nil }
func (m *mockStore) AddColumn(_ context.Context, _ string, _ int, _ string) error { return nil }
func (m *mockStore) RemoveColumn(_ context.Context, _ string, _ int) error { return nil }
func (m *mockStore) RenameCategory(_ context.Context, _ string, _ int, _ string) error { return nil }
func (m *mockStore) PlaceTrack(_ context.Context, _ string, _, _ int, _ string, _ int) error {
	return nil
}
func (m *mockStore) UnplaceTrack(_ context.Context, _ string, _, _ int, _ string) error { return nil }
func (m *mockStore) GetLayout(_ context.Context, _ string) (*Layout, error) { return m.layout, nil }
func (m *mockStore) LoadBoardByID(_ context.Context, _ string) (*game.Board, error) {
	return &game.Board{Rows: 5, Cols: 1}, nil
}
func (m *mockStore) AttachBoard(_ context.Context, _, _ string) error { return nil }

type mockEngine struct{ reloaded bool }

func (m *mockEngine) ReloadBoard(_ *game.Board) { m.reloaded = true }
func (m *mockEngine) StartGame() error           { return nil }

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
	if !strings.Contains(w.Body.String(), "live-token") {
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

func TestSpotifyToken_NotConfigured(t *testing.T) {
	mux := http.NewServeMux()
	RegisterSpotifyToken(mux, nil, "test-secret") // nil spotify

	req := httptest.NewRequest("GET", "/api/spotify/token", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}
