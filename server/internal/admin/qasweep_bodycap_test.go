package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestQARegression_BodySizeCapped pins adminapi-4: admin JSON bodies are capped
// via http.MaxBytesReader so an authenticated caller can't exhaust memory with a
// giant payload. A body well over the 1 MiB cap must be rejected, not decoded.
// Fails-when-reverted: without MaxBytesReader the oversized "name" decodes fine
// and createBoard returns 201.
func TestQARegression_BodySizeCapped(t *testing.T) {
	_, mux := newTestHandler()

	// A single JSON string field larger than the 1 MiB cap.
	huge := strings.Repeat("A", (1<<20)+4096)
	body := strings.NewReader(`{"name":"` + huge + `"}`)
	req := httptest.NewRequest("POST", "/api/boards", body)
	req.Header.Set("Authorization", "Bearer test-secret")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code == http.StatusCreated || w.Code == http.StatusOK {
		t.Fatalf("oversized body was accepted (code %d); MaxBytesReader cap not enforced", w.Code)
	}
	if w.Code < 400 || w.Code >= 500 {
		t.Fatalf("expected a 4xx rejection for oversized body, got %d: %s", w.Code, w.Body.String())
	}
	// Distinguish the CAP path from the after-decode length check: with the cap,
	// MaxBytesReader aborts the read so decodeJSON fails ("invalid JSON"). Without
	// the cap the 1 MiB name decodes and only then trips "name too long" — so a
	// bare 4xx is not enough to prove the cap. Require the decode-failure message.
	if !strings.Contains(w.Body.String(), "invalid JSON") {
		t.Fatalf("body was fully decoded before rejection (%q); MaxBytesReader cap not enforced", strings.TrimSpace(w.Body.String()))
	}
}
