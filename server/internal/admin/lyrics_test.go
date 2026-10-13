package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeProber is a controllable LyricsProber for handler-level tests. probe
// maps "artist|song" -> a canned (text, found, err) result; calls records
// every invocation so tests can assert which songs were (not) probed.
type fakeProber struct {
	probe map[string]proberResult
	calls []string
}

type proberResult struct {
	text  string
	found bool
	err   error
}

func (f *fakeProber) Probe(_ context.Context, artist, song string, _ int) (string, bool, error) {
	key := artist + "|" + song
	f.calls = append(f.calls, key)
	if r, ok := f.probe[key]; ok {
		return r.text, r.found, r.err
	}
	return "", false, nil
}

func newLyricsTestHandler(prober *fakeProber, store *mockStore) (*Handler, *http.ServeMux) {
	h := NewHandler(store, nil, &mockEngine{}, "test-secret")
	h.SetLyricsProber(prober)
	mux := http.NewServeMux()
	h.Register(mux)
	return h, mux
}

func doReq(mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	var r *strings.Reader
	if body != "" {
		r = strings.NewReader(body)
	} else {
		r = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, r)
	req.Header.Set("Authorization", "Bearer test-secret")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// TestRescanLyrics_OnlyProbesNeverAttempted is the regression test for the
// bug this whole feature exists to fix: a bulk "Check lyrics" rescan must
// never re-probe (and so can never overwrite) a song that already has a
// resolved or not_found entry in the lyrics cache — only songs with no
// track_lyrics row at all should ever be probed by the bulk endpoint.
func TestRescanLyrics_OnlyProbesNeverAttempted(t *testing.T) {
	store := &mockStore{
		boards: []Board{{ID: "brd_test", Name: "Test", Cols: 3}},
		tracks: []Track{
			{ID: "t_resolved", BoardID: "brd_test", SpotifyURI: "spotify:track:resolved", Artist: "A1", Song: "S1"},
			{ID: "t_notfound", BoardID: "brd_test", SpotifyURI: "spotify:track:notfound", Artist: "A2", Song: "S2"},
			{ID: "t_new", BoardID: "brd_test", SpotifyURI: "spotify:track:new", Artist: "A3", Song: "S3"},
		},
		lyrics: map[string]LyricsRecord{
			"spotify:track:resolved": {SpotifyURI: "spotify:track:resolved", Status: LyricsStatusResolved, Source: "lrclib"},
			"spotify:track:notfound": {SpotifyURI: "spotify:track:notfound", Status: LyricsStatusNotFound},
		},
	}
	prober := &fakeProber{probe: map[string]proberResult{
		"A3|S3": {text: "[00:00.00]new song lyrics", found: true},
	}}
	_, mux := newLyricsTestHandler(prober, store)

	w := doReq(mux, "POST", "/api/boards/brd_test/rescan-lyrics", "")
	if w.Code != http.StatusOK {
		t.Fatalf("rescan: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if len(prober.calls) != 1 || prober.calls[0] != "A3|S3" {
		t.Fatalf("probe calls = %v, want exactly [A3|S3] (resolved/not_found songs must not be re-probed)", prober.calls)
	}
	if store.lyrics["spotify:track:resolved"].Status != LyricsStatusResolved {
		t.Error("previously-resolved song's status was touched by rescan")
	}
	if store.lyrics["spotify:track:notfound"].Status != LyricsStatusNotFound {
		t.Error("previously-not_found song's status was touched by rescan")
	}
	if store.lyrics["spotify:track:new"].Status != LyricsStatusResolved {
		t.Errorf("never-attempted song should now be resolved, got %+v", store.lyrics["spotify:track:new"])
	}

	var summary map[string]int
	if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if summary["attempted"] != 1 || summary["resolved"] != 1 {
		t.Errorf("summary = %+v, want attempted=1 resolved=1", summary)
	}
}

// TestRescanLyrics_TransientErrorDoesNotOverwriteResolved is the direct
// regression test for the original bug: a transient probe error during a
// bulk rescan must never flip a previously-resolved song back to
// not-playable. Since resolved songs are now skipped entirely by rescan
// (never re-probed), this is true by construction — this test pins that
// behavior down explicitly rather than relying on it being incidental.
func TestRescanLyrics_TransientErrorDoesNotOverwriteResolved(t *testing.T) {
	store := &mockStore{
		boards: []Board{{ID: "brd_test", Name: "Test", Cols: 3}},
		tracks: []Track{
			{ID: "t_resolved", BoardID: "brd_test", SpotifyURI: "spotify:track:resolved", Artist: "A1", Song: "S1"},
		},
		lyrics: map[string]LyricsRecord{
			"spotify:track:resolved": {SpotifyURI: "spotify:track:resolved", Status: LyricsStatusResolved, Source: "lrclib"},
		},
	}
	// Even if the prober were to error on every call, it must never be
	// invoked for an already-resolved song.
	prober := &fakeProber{probe: map[string]proberResult{
		"A1|S1": {err: errors.New("simulated LRCLIB timeout")},
	}}
	_, mux := newLyricsTestHandler(prober, store)

	w := doReq(mux, "POST", "/api/boards/brd_test/rescan-lyrics", "")
	if w.Code != http.StatusOK {
		t.Fatalf("rescan: expected 200, got %d", w.Code)
	}
	if len(prober.calls) != 0 {
		t.Fatalf("probe calls = %v, want none — resolved songs must never be re-probed by bulk rescan", prober.calls)
	}
	if store.lyrics["spotify:track:resolved"].Status != LyricsStatusResolved {
		t.Errorf("resolved song's status changed to %q", store.lyrics["spotify:track:resolved"].Status)
	}
}

// TestRequeryLyrics_RetriesRegardlessOfStatus covers the explicit per-track
// retry path for a song the bulk rescan would otherwise never touch again.
func TestRequeryLyrics_RetriesRegardlessOfStatus(t *testing.T) {
	store := &mockStore{
		boards: []Board{{ID: "brd_test", Name: "Test", Cols: 3}},
		tracks: []Track{
			{ID: "t1", BoardID: "brd_test", SpotifyURI: "spotify:track:x", Artist: "Artist", Song: "Song"},
		},
		lyrics: map[string]LyricsRecord{
			"spotify:track:x": {SpotifyURI: "spotify:track:x", Status: LyricsStatusNotFound},
		},
	}
	prober := &fakeProber{probe: map[string]proberResult{
		"Artist|Song": {text: "[00:00.00]found it now", found: true},
	}}
	_, mux := newLyricsTestHandler(prober, store)

	w := doReq(mux, "POST", "/api/boards/brd_test/tracks/t1/requery-lyrics", "")
	if w.Code != http.StatusOK {
		t.Fatalf("requery: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(prober.calls) != 1 {
		t.Fatalf("probe calls = %v, want exactly one requery attempt", prober.calls)
	}
	if store.lyrics["spotify:track:x"].Status != LyricsStatusResolved {
		t.Errorf("status = %q, want resolved after a successful requery", store.lyrics["spotify:track:x"].Status)
	}
}

// TestSetLyricsManual_StoresAndValidates covers manual LRC entry: a valid
// paste resolves the song, and unparseable text is rejected (not silently
// stored as "resolved" with zero usable lines).
func TestSetLyricsManual_StoresAndValidates(t *testing.T) {
	store := &mockStore{
		boards: []Board{{ID: "brd_test", Name: "Test", Cols: 3}},
		tracks: []Track{
			{ID: "t1", BoardID: "brd_test", SpotifyURI: "spotify:track:manual", Artist: "Artist", Song: "Song"},
		},
	}
	_, mux := newLyricsTestHandler(&fakeProber{}, store)

	bad := doReq(mux, "PUT", "/api/boards/brd_test/tracks/t1/lyrics", `{"lrcText":"not lrc at all, no timestamps"}`)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("unparseable lrcText: expected 400, got %d: %s", bad.Code, bad.Body.String())
	}
	if _, ok := store.lyrics["spotify:track:manual"]; ok {
		t.Error("unparseable text must not be stored")
	}

	good := doReq(mux, "PUT", "/api/boards/brd_test/tracks/t1/lyrics", `{"lrcText":"[00:01.00]hand typed lyric"}`)
	if good.Code != http.StatusOK {
		t.Fatalf("valid lrcText: expected 200, got %d: %s", good.Code, good.Body.String())
	}
	rec, ok := store.lyrics["spotify:track:manual"]
	if !ok || rec.Status != LyricsStatusResolved || rec.Source != "manual" {
		t.Errorf("stored record = %+v, want resolved/manual", rec)
	}
}

// TestAddDailyDoubleTrack_RequiresResolvedLyrics covers the hard requirement:
// Daily Double has no override, so a song must already be resolved in the
// global lyrics cache before it can be added at all.
func TestAddDailyDoubleTrack_RequiresResolvedLyrics(t *testing.T) {
	store := &mockStore{
		boards: []Board{{ID: "brd_test", Name: "Test", Cols: 3}},
		lyrics: map[string]LyricsRecord{
			"spotify:track:good": {SpotifyURI: "spotify:track:good", Status: LyricsStatusResolved, Source: "lrclib"},
			"spotify:track:bad":  {SpotifyURI: "spotify:track:bad", Status: LyricsStatusNotFound},
		},
	}
	h := NewHandler(store, nil, &mockEngine{}, "test-secret")
	mux := http.NewServeMux()
	h.Register(mux)

	blocked := doReq(mux, "POST", "/api/boards/brd_test/daily-double-tracks",
		`{"spotifyUri":"spotify:track:bad","artist":"A","song":"S","durationMs":180000}`)
	if blocked.Code != http.StatusConflict {
		t.Fatalf("not_found song: expected 409, got %d: %s", blocked.Code, blocked.Body.String())
	}
	if len(store.ddTracks) != 0 {
		t.Fatalf("ddTracks = %v, want none added for an unresolved song", store.ddTracks)
	}

	blockedUnknown := doReq(mux, "POST", "/api/boards/brd_test/daily-double-tracks",
		`{"spotifyUri":"spotify:track:nevercheckedxyz","artist":"A","song":"S","durationMs":180000}`)
	if blockedUnknown.Code != http.StatusConflict {
		t.Fatalf("never-checked song: expected 409, got %d: %s", blockedUnknown.Code, blockedUnknown.Body.String())
	}

	allowed := doReq(mux, "POST", "/api/boards/brd_test/daily-double-tracks",
		`{"spotifyUri":"spotify:track:good","artist":"A","song":"S","durationMs":180000}`)
	if allowed.Code != http.StatusCreated {
		t.Fatalf("resolved song: expected 201, got %d: %s", allowed.Code, allowed.Body.String())
	}
	if len(store.ddTracks) != 1 || store.ddTracks[0].LyricsStatus != LyricsStatusResolved {
		t.Fatalf("ddTracks = %+v, want one resolved entry", store.ddTracks)
	}
}
