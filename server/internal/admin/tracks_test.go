package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// cancelingProber simulates a mid-rescan client disconnect: its first Probe
// call cancels the request's own context (as if the client just hung up)
// and still returns a normal successful result for that in-flight call —
// exactly like a real LRCLIB round trip that was already on the wire when
// the client disconnected. Every call after that mirrors what a real
// LRCLIBClient.probeOnce does once ctx is cancelled (lyrics.go's c.http.Do
// fails with the context's error): it returns ctx.Err() as the probe error.
type cancelingProber struct {
	cancel context.CancelFunc
	calls  int
}

func (p *cancelingProber) Probe(ctx context.Context, _, _ string, _ int) (string, bool, error) {
	p.calls++
	if p.calls == 1 {
		p.cancel()
		return "[00:00.00]ok", true, nil
	}
	return "", false, ctx.Err()
}

// TestRescanLyrics_CancelledContext_LeavesUnreachedTracksRowless is the
// regression test for api7-001: once the request context is cancelled
// (client disconnect) mid-rescan, the loop must stop immediately and every
// track it never reached must be left with NO track_lyrics row at all —
// not a bogus "pending" row carrying a "context canceled" error message.
// Reuses the validator's repro approach: a fake prober that cancels the
// context on its first call.
func TestRescanLyrics_CancelledContext_LeavesUnreachedTracksRowless(t *testing.T) {
	store := &mockStore{
		boards: []Board{{ID: "brd_test", Name: "Test", Cols: 3}},
		tracks: []Track{
			{ID: "t1", BoardID: "brd_test", SpotifyURI: "spotify:track:one", Artist: "A1", Song: "S1"},
			{ID: "t2", BoardID: "brd_test", SpotifyURI: "spotify:track:two", Artist: "A2", Song: "S2"},
			{ID: "t3", BoardID: "brd_test", SpotifyURI: "spotify:track:three", Artist: "A3", Song: "S3"},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prober := &cancelingProber{cancel: cancel}

	h := NewHandler(store, nil, &mockEngine{}, "test-secret")
	h.SetLyricsProber(prober)
	mux := http.NewServeMux()
	h.Register(mux)

	req := httptest.NewRequest("POST", "/api/boards/brd_test/rescan-lyrics", strings.NewReader("")).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer test-secret")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if prober.calls != 1 {
		t.Fatalf("prober.calls = %d, want exactly 1 — the loop must stop at the top of the next iteration once the context is cancelled, never probing t2/t3", prober.calls)
	}
	if rec, ok := store.lyrics["spotify:track:two"]; ok {
		t.Errorf("t2 (never reached after cancel) got a track_lyrics row: %+v — must stay rowless, not pending/error", rec)
	}
	if rec, ok := store.lyrics["spotify:track:three"]; ok {
		t.Errorf("t3 (never reached after cancel) got a track_lyrics row: %+v — must stay rowless, not pending/error", rec)
	}
	if rec, ok := store.lyrics["spotify:track:one"]; !ok || rec.Status != LyricsStatusResolved {
		t.Errorf("t1 (reached before cancel fired) = %+v, want resolved", rec)
	}
}

// TestRescanLyrics_CapStopsProcessingAndSignalsTruncation is the regression
// test for api7-002: a board with more never-attempted tracks than
// maxRescanTracksPerRequest must only have the cap's worth probed, and the
// response must say so (Truncated: true) rather than silently stopping with
// no indication a re-run is needed.
func TestRescanLyrics_CapStopsProcessingAndSignalsTruncation(t *testing.T) {
	origDelay := lyricsProbeDelay
	lyricsProbeDelay = time.Millisecond // avoid a real multi-second sleep across ~100 iterations
	defer func() { lyricsProbeDelay = origDelay }()

	numTracks := maxRescanTracksPerRequest + 5
	tracks := make([]Track, numTracks)
	for i := range tracks {
		tracks[i] = Track{
			ID:         fmt.Sprintf("t%d", i),
			BoardID:    "brd_test",
			SpotifyURI: fmt.Sprintf("spotify:track:%d", i),
			Artist:     fmt.Sprintf("Artist%d", i),
			Song:       fmt.Sprintf("Song%d", i),
		}
	}
	store := &mockStore{
		boards: []Board{{ID: "brd_test", Name: "Test", Cols: 3}},
		tracks: tracks,
	}
	prober := &fakeProber{probe: map[string]proberResult{}}

	h := NewHandler(store, nil, &mockEngine{}, "test-secret")
	h.SetLyricsProber(prober)
	mux := http.NewServeMux()
	h.Register(mux)

	w := doReq(mux, "POST", "/api/boards/brd_test/rescan-lyrics", "")
	if w.Code != http.StatusOK {
		t.Fatalf("rescan: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(prober.calls) != maxRescanTracksPerRequest {
		t.Fatalf("prober.calls = %d, want exactly the cap (%d)", len(prober.calls), maxRescanTracksPerRequest)
	}

	var summary rescanLyricsResult
	if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if summary.Attempted != maxRescanTracksPerRequest {
		t.Errorf("summary.Attempted = %d, want %d", summary.Attempted, maxRescanTracksPerRequest)
	}
	if !summary.Truncated {
		t.Error("summary.Truncated = false, want true — tracks remained unprocessed past the cap")
	}
}

// TestRequeryLyrics_And_SetLyricsManual_404OnBoardMismatch is the regression
// test for api7-003: a trackId that belongs to a different board than the
// one in the URL path must 404, mirroring dailydouble.go's board-scoped
// pattern (dd-api-2), instead of silently acting on the track anyway.
func TestRequeryLyrics_And_SetLyricsManual_404OnBoardMismatch(t *testing.T) {
	store := &mockStore{
		boards: []Board{
			{ID: "brd_a", Name: "A", Cols: 3},
			{ID: "brd_b", Name: "B", Cols: 3},
		},
		tracks: []Track{
			{ID: "t1", BoardID: "brd_a", SpotifyURI: "spotify:track:x", Artist: "Artist", Song: "Song"},
		},
	}
	prober := &fakeProber{probe: map[string]proberResult{
		"Artist|Song": {text: "[00:00.00]x", found: true},
	}}
	h := NewHandler(store, nil, &mockEngine{}, "test-secret")
	h.SetLyricsProber(prober)
	mux := http.NewServeMux()
	h.Register(mux)

	// t1 belongs to brd_a; requesting it through brd_b's URL must 404 and
	// must not touch the lyrics cache or spend a probe call.
	badRequery := doReq(mux, "POST", "/api/boards/brd_b/tracks/t1/requery-lyrics", "")
	if badRequery.Code != http.StatusNotFound {
		t.Fatalf("requery via wrong board: expected 404, got %d: %s", badRequery.Code, badRequery.Body.String())
	}
	if len(prober.calls) != 0 {
		t.Fatalf("probe calls = %v, want none — board mismatch must short-circuit before probing", prober.calls)
	}
	if _, ok := store.lyrics["spotify:track:x"]; ok {
		t.Error("board-mismatched requery must not write to the lyrics cache")
	}

	goodRequery := doReq(mux, "POST", "/api/boards/brd_a/tracks/t1/requery-lyrics", "")
	if goodRequery.Code != http.StatusOK {
		t.Fatalf("requery via correct board: expected 200, got %d: %s", goodRequery.Code, goodRequery.Body.String())
	}
	if len(prober.calls) != 1 {
		t.Fatalf("probe calls = %v, want exactly one after the correct-board requery", prober.calls)
	}

	badManual := doReq(mux, "PUT", "/api/boards/brd_b/tracks/t1/lyrics", `{"lrcText":"[00:01.00]line"}`)
	if badManual.Code != http.StatusNotFound {
		t.Fatalf("setLyricsManual via wrong board: expected 404, got %d: %s", badManual.Code, badManual.Body.String())
	}

	goodManual := doReq(mux, "PUT", "/api/boards/brd_a/tracks/t1/lyrics", `{"lrcText":"[00:01.00]line"}`)
	if goodManual.Code != http.StatusOK {
		t.Fatalf("setLyricsManual via correct board: expected 200, got %d: %s", goodManual.Code, goodManual.Body.String())
	}
}
