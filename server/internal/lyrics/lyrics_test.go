package lyrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

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
