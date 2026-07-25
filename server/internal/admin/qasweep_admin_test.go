package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// posCapturingStore records the pos passed to PlaceTrack so tests can assert
// that a chunked/empty body is handled correctly (s2-admin-1).
type posCapturingStore struct {
	mockStore
	lastPos int
}

func (s *posCapturingStore) PlaceTrack(_ context.Context, _ string, _, _ int, _ string, pos int) error {
	s.lastPos = pos
	return nil
}

func newPosTestHandler(store *posCapturingStore) (*Handler, *http.ServeMux) {
	store.boards = []Board{{ID: "brd_test", Name: "Test Board", Cols: 3}}
	h := NewHandler(store, nil, &mockEngine{}, "test-secret")
	mux := http.NewServeMux()
	h.Register(mux)
	return h, mux
}

// TestAuth_Accepts_RightToken guards the constant-time digest compare change
// (s2-admin-2): the correct secret must still authenticate.
func TestAuth_Accepts_RightToken(t *testing.T) {
	_, mux := newTestHandler()

	req := httptest.NewRequest("GET", "/api/boards", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 with correct secret, got %d", w.Code)
	}
}

// TestAuth_Rejects_TokenPrefix ensures a right-length prefix (a proper subset
// of the secret) is rejected — the digest compare must not leak via length.
func TestAuth_Rejects_TokenPrefix(t *testing.T) {
	_, mux := newTestHandler()

	req := httptest.NewRequest("GET", "/api/boards", nil)
	req.Header.Set("Authorization", "Bearer test-secre") // one char short
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for short token, got %d", w.Code)
	}
}

// TestPlaceTrack_ChunkedBodyHonorsPos verifies s2-admin-1: a chunked request
// (ContentLength == -1) carrying a real pos must be decoded, not dropped to 0.
func TestPlaceTrack_ChunkedBodyHonorsPos(t *testing.T) {
	store := &posCapturingStore{}
	_, mux := newPosTestHandler(store)

	req := httptest.NewRequest("PUT", "/api/boards/brd_test/cells/1/1/tracks/trk_x",
		strings.NewReader(`{"pos":3}`))
	req.Header.Set("Authorization", "Bearer test-secret")
	req.ContentLength = -1 // simulate chunked transfer encoding
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}
	if store.lastPos != 3 {
		t.Fatalf("expected pos 3 honored from chunked body, got %d", store.lastPos)
	}
}

// TestPlaceTrack_EmptyBodyDefaultsPos verifies an empty body (io.EOF) is
// tolerated and pos defaults to 0 rather than 400.
func TestPlaceTrack_EmptyBodyDefaultsPos(t *testing.T) {
	store := &posCapturingStore{}
	_, mux := newPosTestHandler(store)

	req := httptest.NewRequest("PUT", "/api/boards/brd_test/cells/1/1/tracks/trk_x", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for empty body, got %d", w.Code)
	}
	if store.lastPos != 0 {
		t.Fatalf("expected default pos 0, got %d", store.lastPos)
	}
}
