package lyrics

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/s45vprubg/yfitops/server/internal/config"
	"github.com/s45vprubg/yfitops/server/internal/protocol"
)

func TestParseLRC(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []protocol.LyricLine
	}{
		{
			name: "single timestamp hundredths",
			in:   "[00:12.40]Break stuff!",
			want: []protocol.LyricLine{{TimeMs: 12400, Text: "Break stuff!"}},
		},
		{
			name: "millisecond precision",
			in:   "[00:12.401]Break stuff!",
			want: []protocol.LyricLine{{TimeMs: 12401, Text: "Break stuff!"}},
		},
		{
			name: "single-digit fraction",
			in:   "[00:12.4]Break stuff!",
			want: []protocol.LyricLine{{TimeMs: 12400, Text: "Break stuff!"}},
		},
		{
			name: "no fraction",
			in:   "[01:05]One minute five",
			want: []protocol.LyricLine{{TimeMs: 65000, Text: "One minute five"}},
		},
		{
			name: "minutes accumulate",
			in:   "[02:03.50]Later line",
			want: []protocol.LyricLine{{TimeMs: 123500, Text: "Later line"}},
		},
		{
			name: "multi-timestamp single line emits each",
			in:   "[00:10.00][00:20.50]Repeated chorus",
			want: []protocol.LyricLine{
				{TimeMs: 10000, Text: "Repeated chorus"},
				{TimeMs: 20500, Text: "Repeated chorus"},
			},
		},
		{
			name: "metadata tags skipped",
			in:   "[ar: Limp Bizkit]\n[ti: Break Stuff]\n[length: 02:46]\n[00:12.40]Break stuff!",
			want: []protocol.LyricLine{{TimeMs: 12400, Text: "Break stuff!"}},
		},
		{
			name: "blank and whitespace lines ignored",
			in:   "\n   \n[00:01.00]First\n\n[00:02.00]Second\n",
			want: []protocol.LyricLine{
				{TimeMs: 1000, Text: "First"},
				{TimeMs: 2000, Text: "Second"},
			},
		},
		{
			name: "timestamp-only line yields empty text cue",
			in:   "[00:05.00]\n[00:06.00]After silence",
			want: []protocol.LyricLine{
				{TimeMs: 5000, Text: ""},
				{TimeMs: 6000, Text: "After silence"},
			},
		},
		{
			name: "malformed lines without timestamp dropped",
			in:   "just some text\n[bad]\n[00:03.00]Real line\nmore junk",
			want: []protocol.LyricLine{{TimeMs: 3000, Text: "Real line"}},
		},
		{
			name: "out-of-order input is sorted",
			in:   "[00:20.00]Second\n[00:10.00]First",
			want: []protocol.LyricLine{
				{TimeMs: 10000, Text: "First"},
				{TimeMs: 20000, Text: "Second"},
			},
		},
		{
			name: "crlf line endings",
			in:   "[00:01.00]First\r\n[00:02.00]Second\r\n",
			want: []protocol.LyricLine{
				{TimeMs: 1000, Text: "First"},
				{TimeMs: 2000, Text: "Second"},
			},
		},
		{
			name: "bracketed text after timestamp is preserved",
			in:   "[00:01.00][verse 1] sing it",
			want: []protocol.LyricLine{{TimeMs: 1000, Text: "[verse 1] sing it"}},
		},
		{
			name: "empty document",
			in:   "",
			want: nil,
		},
		{
			name: "interior timestamp not treated as tag",
			in:   "[00:01.00]meet me at [02:00.00]",
			want: []protocol.LyricLine{{TimeMs: 1000, Text: "meet me at [02:00.00]"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseLRC(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseLRC()\n got: %#v\nwant: %#v", got, tc.want)
			}
		})
	}
}

func newClient(t *testing.T, baseURL string) *LRCLIBClient {
	t.Helper()
	return New(&config.Config{LRCLIBBaseURL: baseURL}).WithHTTPClient(http.DefaultClient)
}

func TestFetch_HitsRightURLAndParses(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": 1,
			"trackName": "Break Stuff",
			"artistName": "Limp Bizkit",
			"syncedLyrics": "[ar: Limp Bizkit]\n[00:12.40]Break stuff!\n[00:14.00]It's just one of those days"
		}`))
	}))
	defer srv.Close()

	c := newClient(t, srv.URL)
	lines, err := c.Fetch(context.Background(), "Limp Bizkit", "Break Stuff", 166)
	if err != nil {
		t.Fatalf("Fetch error: %v", err)
	}

	if gotPath != "/api/get" {
		t.Errorf("path = %q, want /api/get", gotPath)
	}
	for _, want := range []string{"artist_name=Limp+Bizkit", "track_name=Break+Stuff", "duration=166"} {
		if !containsParam(gotQuery, want) {
			t.Errorf("query %q missing %q", gotQuery, want)
		}
	}

	want := []protocol.LyricLine{
		{TimeMs: 12400, Text: "Break stuff!"},
		{TimeMs: 14000, Text: "It's just one of those days"},
	}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("lines\n got: %#v\nwant: %#v", lines, want)
	}
}

func TestFetch_404ReturnsNilNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"code":404,"name":"TrackNotFound"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	c := newClient(t, srv.URL)
	lines, err := c.Fetch(context.Background(), "Nobody", "Nothing", 100)
	if err != nil {
		t.Fatalf("Fetch error: %v", err)
	}
	if lines != nil {
		t.Fatalf("lines = %#v, want nil", lines)
	}
}

func TestFetch_EmptySyncedLyricsReturnsNilNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Match found, but only plain (unsynced) lyrics present.
		_, _ = w.Write([]byte(`{"plainLyrics":"Break stuff","syncedLyrics":""}`))
	}))
	defer srv.Close()

	c := newClient(t, srv.URL)
	lines, err := c.Fetch(context.Background(), "Limp Bizkit", "Break Stuff", 166)
	if err != nil {
		t.Fatalf("Fetch error: %v", err)
	}
	if lines != nil {
		t.Fatalf("lines = %#v, want nil", lines)
	}
}

func TestFetch_ServerErrorIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := newClient(t, srv.URL)
	if _, err := c.Fetch(context.Background(), "a", "b", 1); err == nil {
		t.Fatal("expected error on 500, got nil")
	}
}

func TestFetch_TrimsBaseURLSlash(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"syncedLyrics":"[00:00.00]hi"}`))
	}))
	defer srv.Close()

	// Trailing slash on base must not produce //api/get.
	c := newClient(t, srv.URL+"/")
	if _, err := c.Fetch(context.Background(), "a", "b", 1); err != nil {
		t.Fatalf("Fetch error: %v", err)
	}
	if gotPath != "/api/get" {
		t.Errorf("path = %q, want /api/get", gotPath)
	}
}

func TestFetch_SetsUserAgent(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`{"syncedLyrics":"[00:00.00]hi"}`))
	}))
	defer srv.Close()

	c := newClient(t, srv.URL)
	if _, err := c.Fetch(context.Background(), "a", "b", 1); err != nil {
		t.Fatalf("Fetch error: %v", err)
	}
	if gotUA == "" || gotUA == "Go-http-client/1.1" {
		t.Fatalf("User-Agent = %q, want a descriptive client identifier per LRCLIB's docs", gotUA)
	}
}

func TestProbe_Classification(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		retryAfter string
		wantFound  bool
		wantErr    bool
		wantRLErr  bool
	}{
		{name: "200 with synced lyrics", status: http.StatusOK, body: `{"syncedLyrics":"[00:00.00]hi"}`, wantFound: true},
		{name: "200 empty synced lyrics is not-found", status: http.StatusOK, body: `{"syncedLyrics":""}`, wantFound: false},
		{name: "404 is confirmed not-found, not an error", status: http.StatusNotFound, wantFound: false, wantErr: false},
		{name: "500 is a plain error", status: http.StatusInternalServerError, wantErr: true},
		{name: "503 is a rate-limit error", status: http.StatusServiceUnavailable, retryAfter: "0", wantErr: true, wantRLErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}
				w.WriteHeader(tc.status)
				if tc.body != "" {
					_, _ = w.Write([]byte(tc.body))
				}
			}))
			defer srv.Close()

			c := newClient(t, srv.URL)
			_, found, err := c.probeOnce(context.Background(), "a", "b", 1)
			if found != tc.wantFound {
				t.Errorf("found = %v, want %v", found, tc.wantFound)
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tc.wantErr)
			}
			var rle *RateLimitError
			if errors.As(err, &rle) != tc.wantRLErr {
				t.Errorf("errors.As(err, *RateLimitError) = %v, want %v (err=%v)", errors.As(err, &rle), tc.wantRLErr, err)
			}
		})
	}
}

func TestProbe_RetriesOnceAfterRateLimit(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"syncedLyrics":"[00:00.00]hi"}`))
	}))
	defer srv.Close()

	c := newClient(t, srv.URL)
	text, found, err := c.Probe(context.Background(), "a", "b", 1)
	if err != nil {
		t.Fatalf("Probe error: %v", err)
	}
	if !found || text == "" {
		t.Fatalf("found=%v text=%q, want a resolved result after the retry", found, text)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (one 429 + one retry)", calls)
	}
}

func TestProbe_GivesUpAfterSecondRateLimit(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := newClient(t, srv.URL)
	_, _, err := c.Probe(context.Background(), "a", "b", 1)
	var rle *RateLimitError
	if !errors.As(err, &rle) {
		t.Fatalf("err = %v, want *RateLimitError after exhausting the single retry", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (no unbounded retry loop)", calls)
	}
}

// TestProbe_FallsBackToSearchWithinTolerance covers the real-world case this
// fallback exists for: LRCLIB has a duplicate submission of the same
// recording (duration reported ~1s off) that DOES carry synced lyrics, while
// the one /api/get happens to return does not.
func TestProbe_FallsBackToSearchWithinTolerance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/get":
			_, _ = w.Write([]byte(`{"plainLyrics":"text only","syncedLyrics":""}`))
		case "/api/search":
			_, _ = w.Write([]byte(`[
				{"duration":245,"syncedLyrics":""},
				{"duration":279,"syncedLyrics":"[00:00.00]near-duplicate match"}
			]`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := newClient(t, srv.URL)
	text, found, err := c.Probe(context.Background(), "a", "b", 278)
	if err != nil {
		t.Fatalf("Probe error: %v", err)
	}
	if !found {
		t.Fatal("found = false, want true via the search fallback")
	}
	if text != "[00:00.00]near-duplicate match" {
		t.Fatalf("text = %q, want the near-duplicate's synced lyrics", text)
	}
}

// TestProbe_FallbackRejectsDistantDuration proves the fallback does NOT
// widen matching to a genuinely different edit/remix — only to near-duplicate
// submissions of the same recording. A candidate well outside the tolerance
// (here 40s off, same shape as a real shortened radio/compilation edit) must
// be ignored even though it has synced lyrics.
func TestProbe_FallbackRejectsDistantDuration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/get":
			_, _ = w.Write([]byte(`{"syncedLyrics":""}`))
		case "/api/search":
			_, _ = w.Write([]byte(`[{"duration":239,"syncedLyrics":"[00:00.00]different edit, do not use"}]`))
		}
	}))
	defer srv.Close()

	c := newClient(t, srv.URL)
	_, found, err := c.Probe(context.Background(), "a", "b", 278)
	if err != nil {
		t.Fatalf("Probe error: %v", err)
	}
	if found {
		t.Fatal("found = true, want false — the only candidate is 39s off, well outside tolerance")
	}
}

// TestProbe_SkipsFallbackWhenPrimaryAlreadyFound proves the fallback never
// fires (and so never spends a second request) once /api/get already
// resolved synced lyrics directly — it only ever supplements a clean
// not-found, never second-guesses a hit.
func TestProbe_SkipsFallbackWhenPrimaryAlreadyFound(t *testing.T) {
	searchCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/search" {
			searchCalls++
		}
		_, _ = w.Write([]byte(`{"syncedLyrics":"[00:00.00]primary hit"}`))
	}))
	defer srv.Close()

	c := newClient(t, srv.URL)
	text, found, err := c.Probe(context.Background(), "a", "b", 278)
	if err != nil || !found || text != "[00:00.00]primary hit" {
		t.Fatalf("text=%q found=%v err=%v, want the primary result unchanged", text, found, err)
	}
	if searchCalls != 0 {
		t.Fatalf("search fallback called %d times, want 0", searchCalls)
	}
}

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want time.Duration
	}{
		{"normal small value", "2", 2 * time.Second},
		{"missing header", "", 1 * time.Second},
		{"non-numeric", "not-a-number", 1 * time.Second},
		{"negative input", "-1", 1 * time.Second},
		{"zero", "0", 0},
		{"at the max boundary", "10", 10 * time.Second},
		{"just over the max boundary", "11", 1 * time.Second},
		// A ~10-digit value is well within int range for strconv.Atoi but,
		// pre-fix, overflowed the int64 multiply by time.Second and wrapped
		// to a negative duration (see lyr7-001). Must clamp to the safe
		// fallback, and must never be negative.
		{"absurd ~10-digit value", "9999999999", 1 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseRetryAfter(tc.in)
			if got != tc.want {
				t.Errorf("parseRetryAfter(%q) = %v, want %v", tc.in, got, tc.want)
			}
			if got < 0 {
				t.Errorf("parseRetryAfter(%q) = %v, must never be negative", tc.in, got)
			}
		})
	}
}

func containsParam(query, param string) bool {
	for _, p := range splitAmp(query) {
		if p == param {
			return true
		}
	}
	return false
}

func splitAmp(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '&' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
