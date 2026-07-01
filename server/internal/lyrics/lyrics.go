// Package lyrics implements the LRCLIB synced-lyrics client (design_doc §6).
//
// It satisfies game.LyricsProvider: given an artist/title/duration it queries
// LRCLIB's GET /api/get endpoint, which matches on artist + track + duration
// and returns LRC-format synced lyrics. The LRC text is parsed into absolute
// time-coded protocol.LyricLine values for the stage's karaoke overlay.
//
// Karaoke is non-essential: when LRCLIB has no synced lyrics (404 or an empty
// syncedLyrics field) Fetch returns (nil, nil) so the game simply shows none.
package lyrics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/s45vprubg/yfitops/server/internal/config"
	"github.com/s45vprubg/yfitops/server/internal/protocol"
)

// HTTPClient is the minimal slice of *http.Client the client needs, kept as an
// interface so tests can inject an httptest-backed transport without the
// network.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// LRCLIBClient fetches synced lyrics from an LRCLIB-compatible API.
type LRCLIBClient struct {
	baseURL string
	http    HTTPClient
}

// New builds an LRCLIBClient from config (design_doc §6). The base URL comes
// from cfg.LRCLIBBaseURL; the HTTP client is the stdlib default and can be
// swapped via WithHTTPClient for tests.
func New(cfg *config.Config) *LRCLIBClient {
	return &LRCLIBClient{
		baseURL: strings.TrimRight(cfg.LRCLIBBaseURL, "/"),
		// A bounded timeout so a slow/hung LRCLIB call can't stall a karaoke
		// lyric fetch or wedge a bulk lyrics rescan worker indefinitely.
		http: &http.Client{Timeout: 8 * time.Second},
	}
}

// WithHTTPClient overrides the HTTP client (used by tests). Returns the
// receiver for chaining.
func (c *LRCLIBClient) WithHTTPClient(h HTTPClient) *LRCLIBClient {
	c.http = h
	return c
}

// lrclibResponse is the subset of the LRCLIB /api/get payload we care about.
type lrclibResponse struct {
	SyncedLyrics string `json:"syncedLyrics"`
}

// Fetch implements game.LyricsProvider. It returns time-coded lyric lines for
// the track, or (nil, nil) when no synced lyrics are available.
func (c *LRCLIBClient) Fetch(ctx context.Context, artist, title string, durationSec int) ([]protocol.LyricLine, error) {
	q := url.Values{}
	q.Set("artist_name", artist)
	q.Set("track_name", title)
	q.Set("duration", strconv.Itoa(durationSec))
	endpoint := c.baseURL + "/api/get?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("lyrics: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("lyrics: request: %w", err)
	}
	defer resp.Body.Close()

	// No match (or other client miss) -> karaoke just shows nothing.
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lyrics: unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("lyrics: read body: %w", err)
	}

	var lr lrclibResponse
	if err := json.Unmarshal(body, &lr); err != nil {
		return nil, fmt.Errorf("lyrics: decode body: %w", err)
	}
	if strings.TrimSpace(lr.SyncedLyrics) == "" {
		return nil, nil
	}

	return ParseLRC(lr.SyncedLyrics), nil
}

// HasSyncedLyrics reports whether LRCLIB has time-coded lyrics for a track. Used
// by the admin board builder to grey out karaoke-incompatible tracks. A fetch
// error is treated as "no lyrics" (best-effort; a re-scan can retry).
func (c *LRCLIBClient) HasSyncedLyrics(ctx context.Context, artist, song string, durationSec int) bool {
	lines, err := c.Fetch(ctx, artist, song, durationSec)
	return err == nil && len(lines) > 0
}

// tsRe matches a single LRC timestamp tag: [mm:ss.xx] or [mm:ss.xxx], with the
// fractional part optional ([mm:ss]). Minutes/seconds may be one or more
// digits to tolerate sloppy producers.
var tsRe = regexp.MustCompile(`\[(\d+):(\d{1,2})(?:[.:](\d{1,3}))?\]`)

// ParseLRC converts an LRC document into absolute-time lyric lines.
//
// It handles:
//   - one or many leading timestamps per line (the same text emitted at each),
//   - [mm:ss.xx] hundredths and [mm:ss.xxx] millisecond precision,
//   - blank lines and timestamp-only lines (emitted as empty text, useful as
//     karaoke "clear" cues),
//   - metadata tags like [ar:], [ti:], [length:] which carry no timestamp and
//     are skipped.
//
// Lines without any timestamp tag are dropped. The result is sorted by TimeMs.
func ParseLRC(doc string) []protocol.LyricLine {
	var out []protocol.LyricLine

	for _, raw := range strings.Split(doc, "\n") {
		line := strings.TrimRight(raw, "\r")

		// Collect all timestamp tags at the start of the line. LRC puts every
		// timestamp before the text; we strip them off the front in order.
		var times []int64
		rest := line
		for {
			loc := tsRe.FindStringSubmatchIndex(rest)
			// Only consume tags anchored at the current head of the string;
			// once we hit text, the remainder is the lyric.
			if loc == nil || loc[0] != 0 {
				break
			}
			groups := tsRe.FindStringSubmatch(rest[loc[0]:loc[1]])
			times = append(times, lrcTimeMs(groups[1], groups[2], groups[3]))
			rest = rest[loc[1]:]
		}

		if len(times) == 0 {
			// No timestamp: metadata tag ([ar:]/[ti:]/...) or plain text. Skip.
			continue
		}

		text := strings.TrimSpace(rest)
		for _, t := range times {
			out = append(out, protocol.LyricLine{TimeMs: t, Text: text})
		}
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].TimeMs < out[j].TimeMs })
	return out
}

// lrcTimeMs converts the captured minute/second/fraction groups into absolute
// milliseconds. frac is the raw digits after the separator (1-3 of them):
// "4" -> 400ms, "40" -> 400ms, "400" -> 400ms. Inputs are pre-validated by the
// regexp so the Atoi calls cannot fail.
func lrcTimeMs(minStr, secStr, frac string) int64 {
	min, _ := strconv.Atoi(minStr)
	sec, _ := strconv.Atoi(secStr)

	ms := 0
	switch len(frac) {
	case 0:
		ms = 0
	case 1:
		ms, _ = strconv.Atoi(frac)
		ms *= 100
	case 2:
		ms, _ = strconv.Atoi(frac)
		ms *= 10
	case 3:
		ms, _ = strconv.Atoi(frac)
	}

	return int64(min)*60_000 + int64(sec)*1_000 + int64(ms)
}
